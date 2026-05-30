package strategy

import "github.com/johnny1110/friday/internal/binance"

// Registry holds the active single-symbol strategies.
type Registry struct {
	strategies []Strategy
}

// NewRegistry builds a registry from the given strategies.
func NewRegistry(strategies ...Strategy) *Registry {
	return &Registry{strategies: strategies}
}

// DefaultRegistry is friday's standard single-symbol strategy set. (The
// cross-symbol divergence strategy is applied separately — see
// DivergenceSignals — because it needs all symbols at once.)
func DefaultRegistry() *Registry {
	return NewRegistry(
		Momentum{},
		Breakout{},
		MeanReversion{},
	)
}

// AnalyzeAll runs every strategy against the symbol's candles and returns
// their signals.
func (r *Registry) AnalyzeAll(symbol string, candles []binance.Kline) []Signal {
	out := make([]Signal, 0, len(r.strategies))
	for _, s := range r.strategies {
		out = append(out, s.Analyze(symbol, candles))
	}
	return out
}

// Consensus runs all strategies and aggregates them into a single
// recommendation for the symbol.
func (r *Registry) Consensus(symbol string, candles []binance.Kline) Consensus {
	return Aggregate(symbol, r.AnalyzeAll(symbol, candles))
}

// ConsensusFor is a convenience using the default registry — what
// binance_klines calls to annotate its Summary.
func ConsensusFor(symbol string, candles []binance.Kline) Consensus {
	return DefaultRegistry().Consensus(symbol, candles)
}
