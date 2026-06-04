package strategy

import (
	"context"
	"log/slog"
	"time"

	"github.com/johnny1110/friday/internal/binance"
)

// Recalibrator periodically re-runs the startup confidence calibration on FRESH
// candles and updates the live calibration store (PRD-020 §5). backtest.Calibrate
// runs ONCE at startup; but regimes shift — a strategy that crushed a trending
// week becomes a loser in chop — so static confidences go stale. Every
// FRIDAY_RECALIBRATE_HOURS this re-fetches 5m×1500 candles per symbol, recomputes
// the win-rate→confidence map, and installs it via SetDefaultCalibration (the
// same store ConsensusFor reads each round).
//
// The sweep deliberately runs on 5m — the timeframe the strategies are tuned for
// and where the 5m-led MTF vote carries the most weight (5m×2.0). Calibrating on
// 4h disabled strategies on their best (5m) timeframe because they score poorly
// on 4h, which silenced the whole MTF vote (see PRD note); 5m fixes that.
//
// The calibrate step is injected (CalibrateFn) rather than imported so the
// strategy package does not depend on backtest (which depends on strategy) —
// avoiding an import cycle. bootstrap wires backtest.Calibrate in.
type Recalibrator struct {
	Symbols     []string
	Strategies  []Strategy
	Interval    time.Duration
	Fetch       func(ctx context.Context, symbol, interval string, limit int) ([]binance.Kline, error)
	CalibrateFn func(strategies []Strategy, candlesBySymbol map[string][]binance.Kline) map[string]map[string]float64
	Logger      *slog.Logger
}

// recalibrateCandles is the timeframe/limit each run re-fetches — matching the
// startup sweep (5m × 1500 candles, ~5.2 days, Binance's per-request cap).
const (
	recalibrateInterval = "5m"
	recalibrateLimit    = 1500
)

// Run blocks, recalibrating every Interval until ctx is cancelled. An Interval
// of 0 (or no symbols / no fetch / no calibrate func) disables it — Run returns
// immediately. A failed cycle logs a warning and keeps the existing confidences
// (never clears them).
func (r *Recalibrator) Run(ctx context.Context) {
	if r.Interval <= 0 || len(r.Symbols) == 0 || r.Fetch == nil || r.CalibrateFn == nil {
		return
	}
	log := r.Logger
	if log == nil {
		log = slog.Default()
	}
	ticker := time.NewTicker(r.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.runOnce(ctx, log)
		}
	}
}

// runOnce fetches fresh candles for every symbol and updates the store if the
// sweep produced any calibrated confidences. Partial fetch failures are
// tolerated (the symbols that did load are still calibrated).
func (r *Recalibrator) runOnce(ctx context.Context, log *slog.Logger) {
	candles := make(map[string][]binance.Kline, len(r.Symbols))
	for _, sym := range r.Symbols {
		ks, err := r.Fetch(ctx, sym, recalibrateInterval, recalibrateLimit)
		if err != nil {
			log.Warn("recalibrate.fetch_failed", "symbol", sym, "err", err)
			continue
		}
		candles[sym] = ks
	}
	if len(candles) == 0 {
		log.Warn("recalibrate.no_candles", "msg", "keeping existing confidences")
		return
	}
	cal := r.CalibrateFn(r.Strategies, candles)
	if len(cal) == 0 {
		log.Warn("recalibrate.empty_result", "msg", "keeping existing confidences")
		return
	}
	SetDefaultCalibration(cal)
	n := 0
	for _, m := range cal {
		n += len(m)
	}
	log.Info("recalibrate.updated", "symbols", len(candles), "confidences", n)
}
