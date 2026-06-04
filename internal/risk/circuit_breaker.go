package risk

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// BreakerState is the session-level health of the trading system.
type BreakerState int

const (
	StateNormal  BreakerState = iota // all clear
	StateWarning                     // (reserved) approaching limits
	StatePaused                      // new entries blocked; closes allowed; cooling down
	StateHalted                      // emergency: only WAIT until a manual reset
)

func (s BreakerState) String() string {
	switch s {
	case StateNormal:
		return "NORMAL"
	case StateWarning:
		return "WARNING"
	case StatePaused:
		return "PAUSED"
	case StateHalted:
		return "HALTED"
	default:
		return "UNKNOWN"
	}
}

// CircuitBreaker tracks session-level metrics and blocks new entries when
// the system is bleeding (PRD-005). It complements the per-trade
// MarginCapValidator: that caps one order's size; this stops a losing
// session from compounding.
//
// Feeding (in the multi-agent architecture):
//   - RecordTrade(pnl)  — called by the log_trade tool on every close.
//   - Observe(balance)  — called by the orchestrator each round with the
//     Risk Manager's live wallet balance (captures the starting balance,
//     evaluates the daily-loss and drawdown thresholds).
//   - Tick()            — called by the orchestrator once per round to
//     count down a cooldown.
//   - Check()           — called by binance_order before an OPENING order.
//
// All methods are safe for concurrent use.
type CircuitBreaker struct {
	mu sync.Mutex

	// Config (set at construction).
	dailyLossLimitPct    float64 // e.g. 0.10 → pause at -10% of starting balance
	maxConsecutiveLosses int     // e.g. 5 → pause
	drawdownHaltPct      float64 // e.g. 0.20 → halt at -20% of starting balance
	cooldownCycles       int     // rounds to remain paused

	// Live state.
	startingBalance    float64
	sessionRealizedPnL float64
	consecutiveLosses  int
	state              BreakerState
	pausedRemaining    int
	reason             string

	// Persistence (optional): when persistPath is set the breaker survives a
	// restart WITHIN the same UTC day, so frequent restarts can't reset the
	// consecutive-loss / daily-loss protection (the live failure mode). day is
	// the UTC date the current metrics belong to; a restart on a new day starts
	// fresh (a HALT is latched across the day boundary as a safety backstop).
	persistPath string
	day         string
}

// persistedBreaker is the on-disk snapshot of the breaker's live state.
type persistedBreaker struct {
	Day                string  `json:"day"`
	StartingBalance    float64 `json:"starting_balance"`
	SessionRealizedPnL float64 `json:"session_realized_pnl"`
	ConsecutiveLosses  int     `json:"consecutive_losses"`
	State              int     `json:"state"`
	PausedRemaining    int     `json:"paused_remaining"`
	Reason             string  `json:"reason"`
}

// NewCircuitBreaker builds a breaker. Non-positive config values fall back
// to the documented defaults.
func NewCircuitBreaker(dailyLossPct float64, maxConsec int, drawdownHaltPct float64, cooldownCycles int) *CircuitBreaker {
	if dailyLossPct <= 0 {
		dailyLossPct = 0.10
	}
	if maxConsec <= 0 {
		maxConsec = 5
	}
	if drawdownHaltPct <= 0 {
		drawdownHaltPct = 0.20
	}
	if cooldownCycles <= 0 {
		cooldownCycles = 20
	}
	return &CircuitBreaker{
		dailyLossLimitPct:    dailyLossPct,
		maxConsecutiveLosses: maxConsec,
		drawdownHaltPct:      drawdownHaltPct,
		cooldownCycles:       cooldownCycles,
		state:                StateNormal,
	}
}

// EnablePersistence points the breaker at a JSON file and loads any same-day
// snapshot from it, so state survives a restart within the trading day. Call
// once right after construction, before the breaker is used.
func (cb *CircuitBreaker) EnablePersistence(path string) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.persistPath = path
	cb.load()
}

// utcDay is the current UTC calendar date — the key that decides whether a
// persisted snapshot still applies (same day) or is stale (new day → reset).
func utcDay() string { return time.Now().UTC().Format("2006-01-02") }

// load restores a same-day snapshot from persistPath. A snapshot from an earlier
// day is discarded (daily metrics reset) EXCEPT a HALT, which latches across the
// boundary so an emergency stop isn't silently cleared by a date change. Caller
// holds the lock.
func (cb *CircuitBreaker) load() {
	today := utcDay()
	cb.day = today
	data, err := os.ReadFile(cb.persistPath)
	if err != nil {
		return
	}
	var p persistedBreaker
	if json.Unmarshal(data, &p) != nil {
		return
	}
	if p.Day != today {
		if BreakerState(p.State) == StateHalted {
			cb.state = StateHalted
			cb.reason = p.Reason
		}
		return
	}
	cb.startingBalance = p.StartingBalance
	cb.sessionRealizedPnL = p.SessionRealizedPnL
	cb.consecutiveLosses = p.ConsecutiveLosses
	cb.state = BreakerState(p.State)
	cb.pausedRemaining = p.PausedRemaining
	cb.reason = p.Reason
}

// save writes the current state to persistPath (best-effort — a write error must
// never break trading). Caller holds the lock.
func (cb *CircuitBreaker) save() {
	if cb.persistPath == "" {
		return
	}
	if cb.day == "" {
		cb.day = utcDay()
	}
	b, err := json.Marshal(persistedBreaker{
		Day:                cb.day,
		StartingBalance:    cb.startingBalance,
		SessionRealizedPnL: cb.sessionRealizedPnL,
		ConsecutiveLosses:  cb.consecutiveLosses,
		State:              int(cb.state),
		PausedRemaining:    cb.pausedRemaining,
		Reason:             cb.reason,
	})
	if err != nil {
		return
	}
	_ = os.WriteFile(cb.persistPath, b, 0o644)
}

// pause moves to StatePaused with a cooldown (unless already Halted, which
// outranks Paused). Caller holds the lock.
func (cb *CircuitBreaker) pause(reason string) {
	if cb.state == StateHalted {
		return
	}
	cb.state = StatePaused
	cb.pausedRemaining = cb.cooldownCycles
	cb.reason = reason
}

// RecordTrade folds one closed trade's realised PnL into the session. A
// loss increments the consecutive-loss counter (a win resets it); enough
// consecutive losses pauses trading. Daily-loss and drawdown thresholds
// need the balance and are evaluated in Observe.
func (cb *CircuitBreaker) RecordTrade(pnl float64) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.sessionRealizedPnL += pnl
	switch {
	case pnl < 0:
		cb.consecutiveLosses++
	case pnl > 0:
		cb.consecutiveLosses = 0
	}
	if cb.consecutiveLosses >= cb.maxConsecutiveLosses {
		cb.pause(fmt.Sprintf("%d consecutive losses", cb.consecutiveLosses))
	}
	cb.save()
}

// Observe captures the starting balance (first non-zero observation) and
// evaluates the balance-relative thresholds: daily realised loss → pause,
// total drawdown vs starting balance → halt.
func (cb *CircuitBreaker) Observe(balance float64) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.startingBalance == 0 && balance > 0 {
		cb.startingBalance = balance
	}
	if cb.startingBalance == 0 {
		return
	}

	if cb.sessionRealizedPnL <= -cb.dailyLossLimitPct*cb.startingBalance {
		cb.pause(fmt.Sprintf("daily loss %.2f (≥ %.0f%% of starting $%.2f)",
			cb.sessionRealizedPnL, cb.dailyLossLimitPct*100, cb.startingBalance))
	}

	// Drawdown vs starting balance (wallet balance reflects realised PnL).
	if drawdownPct := (balance - cb.startingBalance) / cb.startingBalance; drawdownPct <= -cb.drawdownHaltPct {
		cb.state = StateHalted
		cb.reason = fmt.Sprintf("drawdown %.1f%% (≥ %.0f%% halt) — balance $%.2f vs start $%.2f",
			drawdownPct*100, cb.drawdownHaltPct*100, balance, cb.startingBalance)
	}
	cb.save()
}

// Tick advances the cooldown by one round; an expired cooldown returns a
// Paused breaker to Normal. Halted never auto-clears (needs Reset).
func (cb *CircuitBreaker) Tick() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.state != StatePaused {
		return
	}
	if cb.pausedRemaining > 0 {
		cb.pausedRemaining--
	}
	if cb.pausedRemaining <= 0 {
		cb.state = StateNormal
		cb.reason = ""
		cb.consecutiveLosses = 0
	}
	cb.save()
}

// Check is called before an OPENING order. It returns a non-nil, actionable
// error when new entries are blocked (Paused or Halted). Reduce-only closes
// must bypass this at the call site.
func (cb *CircuitBreaker) Check() error {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case StateHalted:
		return fmt.Errorf("CIRCUIT BREAKER HALTED: %s. New entries are blocked; only closes/WAIT until a manual reset", cb.reason)
	case StatePaused:
		return fmt.Errorf("CIRCUIT BREAKER PAUSED: %s. New entries blocked for %d more cycle(s); close existing positions or WAIT", cb.reason, cb.pausedRemaining)
	default:
		return nil
	}
}

// State returns the current breaker state (for tests / callers that branch
// on it).
func (cb *CircuitBreaker) State() BreakerState {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.state
}

// Reset clears a Halted/Paused breaker back to Normal (manual intervention).
func (cb *CircuitBreaker) Reset() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.state = StateNormal
	cb.reason = ""
	cb.pausedRemaining = 0
	cb.consecutiveLosses = 0
	cb.save()
}

// Status is a one-line natural-language summary for the agents' prompts.
func (cb *CircuitBreaker) Status() string {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case StatePaused:
		return fmt.Sprintf("PAUSED (%s; %d cycles remain; session PnL %+.2f) — CLOSE/WAIT only",
			cb.reason, cb.pausedRemaining, cb.sessionRealizedPnL)
	case StateHalted:
		return fmt.Sprintf("HALTED (%s) — WAIT only until reset", cb.reason)
	default:
		return fmt.Sprintf("NORMAL (session PnL %+.2f, %d consecutive losses)",
			cb.sessionRealizedPnL, cb.consecutiveLosses)
	}
}
