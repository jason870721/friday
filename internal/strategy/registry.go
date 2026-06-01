package strategy

import (
	"sync"

	"github.com/johnny1110/friday/internal/binance"
)

// Registry holds the active single-symbol strategies and, optionally, a
// per-symbol calibration of their confidences (PRD-015). The calibration is
// FLAT (strategy name → confidence) because a registry is always evaluated for
// one symbol at a time; ConsensusFor installs the right symbol's slice from the
// process-wide store before each read.
type Registry struct {
	strategies  []Strategy
	calibration map[string]float64 // strategy name → calibrated base confidence; nil = use hardcoded
}

// NewRegistry builds a registry from the given strategies.
func NewRegistry(strategies ...Strategy) *Registry {
	return &Registry{strategies: strategies}
}

// defaultStrategies is friday's standard single-symbol strategy set, shared by
// DefaultRegistry and DefaultStrategies (calibration backtests the same set).
func defaultStrategies() []Strategy {
	return []Strategy{
		Momentum{},
		Breakout{},
		MeanReversion{},
		EMACross{},  // PRD-013: fourth single-symbol trend vote
		Bollinger{}, // PRD-020 §7: volatility-adaptive mean-reversion + band-walk
	}
}

// DefaultStrategies returns the standard strategy set — used by the calibration
// pipeline (PRD-015) so it backtests exactly the strategies the registry runs.
func DefaultStrategies() []Strategy { return defaultStrategies() }

// DefaultRegistry is friday's standard single-symbol strategy set. (The
// cross-symbol divergence strategy is applied separately — see
// DivergenceSignals — because it needs all symbols at once.)
func DefaultRegistry() *Registry {
	return NewRegistry(defaultStrategies()...)
}

// SetCalibration installs the calibrated confidences for the symbol this
// registry is about to evaluate (strategy name → confidence). nil clears it.
func (r *Registry) SetCalibration(m map[string]float64) { r.calibration = m }

// AnalyzeAll runs every strategy against the symbol's candles and returns
// their signals. When a calibrated confidence exists for a directional signal's
// strategy (PRD-015), it REPLACES the hardcoded base; the ADX boost is then
// re-applied additively on top, preserving "a strong trend adds conviction".
func (r *Registry) AnalyzeAll(symbol string, candles []binance.Kline) []Signal {
	out := make([]Signal, 0, len(r.strategies))
	for _, s := range r.strategies {
		// PRD-016 R6: a strategy calibrated to 0 confidence on this symbol (no
		// historical edge, ≥5 trades) is auto-disabled — it doesn't even vote.
		if r.calibration != nil {
			if base, ok := r.calibration[s.Name()]; ok && base <= 0 {
				continue
			}
		}
		sig := s.Analyze(symbol, candles)
		if sig.Direction != Neutral && r.calibration != nil {
			if base, ok := r.calibration[sig.Strategy]; ok {
				sig.Confidence = clamp01(base + adxBoost(candles))
			}
		}
		out = append(out, sig)
	}
	return out
}

// Consensus runs all strategies and aggregates them into a single
// recommendation for the symbol.
func (r *Registry) Consensus(symbol string, candles []binance.Kline) Consensus {
	return Aggregate(symbol, r.AnalyzeAll(symbol, candles))
}

// --- process-wide calibration store (PRD-015) ---
//
// The trading tools call the package-level ConsensusFor, which builds a fresh
// DefaultRegistry each round; so the calibration produced once at startup is
// kept here and injected per-symbol on each read. bootstrap installs it via
// SetDefaultCalibration after the startup backtest sweep.
var (
	calMu            sync.RWMutex
	calibrationStore map[string]map[string]float64 // symbol → strategy → confidence
)

// SetDefaultCalibration installs the process-wide per-symbol calibration that
// ConsensusFor applies. A nil/empty map disables calibration (hardcoded
// confidences stand).
func SetDefaultCalibration(m map[string]map[string]float64) {
	calMu.Lock()
	defer calMu.Unlock()
	calibrationStore = m
}

// calibrationFor returns the calibrated confidences for one symbol, or nil.
func calibrationFor(symbol string) map[string]float64 {
	calMu.RLock()
	defer calMu.RUnlock()
	return calibrationStore[symbol]
}

// ConsensusFor is a convenience using the default registry — what
// binance_klines calls to annotate its Summary. It applies any startup
// calibration for the symbol (PRD-015).
func ConsensusFor(symbol string, candles []binance.Kline) Consensus {
	r := DefaultRegistry()
	r.SetCalibration(calibrationFor(symbol))
	return r.Consensus(symbol, candles)
}
