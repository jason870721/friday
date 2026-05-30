// Package strategy is friday's deterministic signal engine (PRD-006). It
// moves trade-direction discovery out of the LLM and into pluggable,
// backtestable Go strategies. Each strategy reads candles + indicators and
// emits a Signal; an aggregator combines them into a Consensus that is
// surfaced to the Analyst, whose role becomes validating signals (with a
// cited override) rather than inventing direction.
package strategy

import "github.com/johnny1110/friday/internal/binance"

// Direction is a strategy's directional call.
type Direction int

const (
	Neutral Direction = iota
	Long
	Short
)

func (d Direction) String() string {
	switch d {
	case Long:
		return "LONG"
	case Short:
		return "SHORT"
	default:
		return "NEUTRAL"
	}
}

// Signal is one strategy's output for one symbol at one point in time.
type Signal struct {
	Symbol       string
	Direction    Direction
	Confidence   float64 // 0.0–1.0
	Reason       string  // human-readable, surfaced to the LLM
	Invalidation float64 // price level at which this signal is void (0 = n/a)
	Strategy     string  // which strategy produced this
}

// Strategy is the pluggable single-symbol interface. Cross-symbol logic
// (e.g. divergence) lives in its own function, not here.
type Strategy interface {
	Name() string
	Analyze(symbol string, candles []binance.Kline) Signal
}

// Consensus is the aggregated recommendation for one symbol.
type Consensus struct {
	Symbol     string
	Direction  Direction
	Confidence float64
	Signals    []Signal // all inputs (for LLM context)
	Summary    string   // natural-language summary for the LLM
}
