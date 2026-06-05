package risk

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// PRD-009: a fast-reaction stop-loss / take-profit safety net. A goroutine
// polls mark price ~every second and fires a reduce-only market close the
// instant a registered level is crossed — independent of (and far faster than)
// the agents' 15s round loop, so an open position is never left unprotected
// between cycles.
//
// Limitations (documented, see PRD-009 §6): levels are in-memory only (no
// persistence across restarts) and are enforced by friday's own polling, not
// exchange-native STOP_MARKET orders.

// DefaultStopPollInterval is how often the monitor samples price.
const DefaultStopPollInterval = time.Second

// StopLevels is the protection registered for one symbol's open position.
type StopLevels struct {
	StopPrice    float64 // mark price that triggers the stop-loss (0 = none)
	TakeProfit   float64 // mark price that triggers the take-profit (0 = none)
	PositionQty  float64 // base-asset size to close on breach
	PositionSide string  // DirLong / DirShort
	EntryPrice   float64 // entry price of the position (for PnL estimation; 0 = unknown)
	Leverage     float64 // position leverage (for ROE on a triggered close; 0 = unknown)
}

// active reports whether these levels are worth monitoring.
func (l StopLevels) active() bool {
	return l.PositionQty > 0 &&
		(l.StopPrice > 0 || l.TakeProfit > 0) &&
		(l.PositionSide == DirLong || l.PositionSide == DirShort)
}

// StopBroker is the minimal exchange access the monitor needs. Kept narrow and
// primitive-typed so it mocks cleanly in tests (no binance import here).
type StopBroker interface {
	MarkPrice(ctx context.Context, symbol string) (float64, error)
	CloseReduceOnly(ctx context.Context, symbol string, qty float64, positionSide string) error
}

// StopCloseEvent describes a position that the StopMonitor closed.
type StopCloseEvent struct {
	Symbol       string
	PositionQty  float64
	PositionSide string  // DirLong or DirShort
	EntryPrice   float64 // entry price (0 = unknown)
	MarkPrice    float64
	Leverage     float64 // position leverage (for ROE; 0 = unknown)
	Reason       string  // "stop-loss" or "take-profit"
}

// StopCloseCallback is invoked after the StopMonitor closes a position. It
// receives the close details so the caller can log the trade, fire
// notifications, and feed the circuit breaker — the same path a round-based
// close takes through log_trade. A nil callback disables this.
type StopCloseCallback func(event StopCloseEvent)

// peakState tracks a position's best favourable uPnL (USDT) since its levels
// were armed, sampled from the real mark price each poll — so the trailing-stop
// decision keys off a Go-verified peak instead of the agent's free-text carry
// estimate. `entry` ties the peak to a specific position: a different entry means
// a new position, so the peak resets.
type peakState struct {
	entry float64 // entry the peak is measured against
	peak  float64 // best uPnL (USDT) seen
	seen  bool    // whether peak has been set from a real observation
}

// StopMonitor watches registered levels and flattens on breach.
type StopMonitor struct {
	broker   StopBroker
	interval time.Duration
	logger   *slog.Logger
	onClose  StopCloseCallback

	mu     sync.Mutex
	levels map[string]StopLevels
	peaks  map[string]peakState
}

// NewStopMonitor builds a monitor. interval ≤ 0 uses DefaultStopPollInterval;
// a nil logger falls back to slog.Default(). onClose is called after each
// successful stop-loss or take-profit close (nil = no callback).
func NewStopMonitor(broker StopBroker, interval time.Duration, logger *slog.Logger, onClose StopCloseCallback) *StopMonitor {
	if interval <= 0 {
		interval = DefaultStopPollInterval
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &StopMonitor{
		broker:   broker,
		interval: interval,
		logger:   logger,
		onClose:  onClose,
		levels:   make(map[string]StopLevels),
		peaks:    make(map[string]peakState),
	}
}

// SetLevels registers protection for symbol. An INACTIVE level (zero quantity,
// or neither a stop nor a TP) clears any existing entry — so the same call both
// arms and disarms the monitor.
func (m *StopMonitor) SetLevels(symbol string, l StopLevels) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !l.active() {
		delete(m.levels, symbol)
		delete(m.peaks, symbol)
		return
	}
	m.levels[symbol] = l
	// Reset the peak only when this is a NEW position (no prior peak, or the entry
	// changed); keep it across re-arms of the same open position so the peak
	// accumulates over the position's life.
	if ps, ok := m.peaks[symbol]; !ok || ps.entry != l.EntryPrice {
		m.peaks[symbol] = peakState{entry: l.EntryPrice}
	}
}

// PeakPnL returns the best favourable uPnL (USDT) the monitor has observed for
// symbol's current position since its levels were armed, and whether any peak has
// been recorded. Authoritative — sampled from the real mark price each poll, so
// it does not drift like the agent's carry estimate.
func (m *StopMonitor) PeakPnL(symbol string) (float64, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ps, ok := m.peaks[symbol]
	if !ok || !ps.seen {
		return 0, false
	}
	return ps.peak, true
}

// updatePeak folds one mark-price observation into symbol's peak favourable uPnL.
func (m *StopMonitor) updatePeak(symbol string, l StopLevels, mark float64) {
	if l.EntryPrice <= 0 || l.PositionQty <= 0 {
		return
	}
	var upnl float64
	switch l.PositionSide {
	case DirLong:
		upnl = (mark - l.EntryPrice) * l.PositionQty
	case DirShort:
		upnl = (l.EntryPrice - mark) * l.PositionQty
	default:
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ps, ok := m.peaks[symbol]
	if !ok || ps.entry != l.EntryPrice {
		ps = peakState{entry: l.EntryPrice}
	}
	if !ps.seen || upnl > ps.peak {
		ps.peak, ps.seen = upnl, true
	}
	m.peaks[symbol] = ps
}

// Active reports how many symbols currently have levels (diagnostics/tests).
func (m *StopMonitor) Active() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.levels)
}

// Start polls until ctx is cancelled. Safe to run in a goroutine.
func (m *StopMonitor) Start(ctx context.Context) {
	t := time.NewTicker(m.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.check(ctx)
		}
	}
}

// snapshot copies the active levels so the (network) price/close calls happen
// without holding the lock.
func (m *StopMonitor) snapshot() map[string]StopLevels {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.levels) == 0 {
		return nil
	}
	out := make(map[string]StopLevels, len(m.levels))
	for k, v := range m.levels {
		out[k] = v
	}
	return out
}

// check runs one polling pass over the registered levels.
func (m *StopMonitor) check(ctx context.Context) {
	for symbol, l := range m.snapshot() {
		mark, err := m.broker.MarkPrice(ctx, symbol)
		if err != nil {
			m.logger.Debug("stop_monitor.price_failed", "symbol", symbol, "err", err)
			continue
		}
		m.updatePeak(symbol, l, mark)
		reason := breachReason(l, mark)
		if reason == "" {
			continue
		}
		m.logger.Info("stop_monitor.fired", "symbol", symbol, "reason", reason,
			"mark", mark, "qty", l.PositionQty, "side", l.PositionSide)
		if err := m.broker.CloseReduceOnly(ctx, symbol, l.PositionQty, l.PositionSide); err != nil {
			m.logger.Error("stop_monitor.close_failed", "symbol", symbol, "err", err)
		} else {
			m.logger.Info("stop_monitor.closed", "symbol", symbol, "reason", reason, "mark", mark)
			if m.onClose != nil {
				m.onClose(StopCloseEvent{
					Symbol:       symbol,
					PositionQty:  l.PositionQty,
					PositionSide: l.PositionSide,
					EntryPrice:   l.EntryPrice,
					MarkPrice:    mark,
					Leverage:     l.Leverage,
					Reason:       reason,
				})
			}
		}
		// One-shot: clear whether or not the close succeeded, so a persistent
		// failure (e.g. position already gone) can't spin; the next round's
		// risk checks reconcile any remainder.
		m.SetLevels(symbol, StopLevels{})
	}
}

// breachReason returns "" (in range), "stop-loss", or "take-profit" for a
// position given the current mark price.
func breachReason(l StopLevels, mark float64) string {
	switch l.PositionSide {
	case DirLong:
		if l.StopPrice > 0 && mark <= l.StopPrice {
			return "stop-loss"
		}
		if l.TakeProfit > 0 && mark >= l.TakeProfit {
			return "take-profit"
		}
	case DirShort:
		if l.StopPrice > 0 && mark >= l.StopPrice {
			return "stop-loss"
		}
		if l.TakeProfit > 0 && mark <= l.TakeProfit {
			return "take-profit"
		}
	}
	return ""
}
