package strategy

import "github.com/johnny1110/friday/internal/binance"

// Regime is the market state inferred from trend strength (PRD-016). It decides
// which strategy TYPE is favoured: trend-followers (momentum, breakout,
// ema_cross) in a trend; mean-reversion in a range.
type Regime int

const (
	RegimeTransitional Regime = iota // ADX in [20,25] — no committed direction
	RegimeTrending                   // ADX > 25
	RegimeRanging                    // ADX < 20
)

func (r Regime) String() string {
	switch r {
	case RegimeTrending:
		return "TRENDING"
	case RegimeRanging:
		return "RANGING"
	default:
		return "TRANSITIONAL"
	}
}

// Favors renders the human-readable hint shown to the Analyst alongside the
// regime classification.
func (r Regime) Favors() string {
	switch r {
	case RegimeTrending:
		return "favoring momentum/breakout/ema_cross"
	case RegimeRanging:
		return "favoring mean_reversion"
	default:
		return "no regime edge — strategies vote at base weight"
	}
}

// ADX thresholds for regime classification (PRD-016 R2).
const (
	regimeTrendingADX = 25.0
	regimeRangingADX  = 20.0
)

// DetectRegime classifies the market from ADX(14) over the candle series:
// ADX > 25 → Trending, ADX < 20 → Ranging, otherwise Transitional. Insufficient
// candles for ADX → Transitional (the conservative, no-adjustment fallback).
func DetectRegime(ks []binance.Kline) Regime {
	adx, ok := binance.ADX(ks, 14)
	if !ok {
		return RegimeTransitional
	}
	switch {
	case adx > regimeTrendingADX:
		return RegimeTrending
	case adx < regimeRangingADX:
		return RegimeRanging
	default:
		return RegimeTransitional
	}
}

// regimeWeights are the per-strategy confidence multipliers per regime (PRD-016
// R3) — domain knowledge ("mean-reversion loses in trends"), not fitted values.
// A strategy absent from a regime's map (and the empty Transitional map) keeps
// its weight at ×1.0.
var regimeWeights = map[Regime]map[string]float64{
	RegimeTrending: {
		"momentum":       1.2,
		"breakout":       1.1,
		"ema_cross":      1.2,
		"mean_reversion": 0.3,
	},
	RegimeRanging: {
		"momentum":       0.5,
		"breakout":       0.6,
		"ema_cross":      0.5,
		"mean_reversion": 1.3,
	},
	RegimeTransitional: {}, // all ×1.0 — no adjustment
}

// regimeWeight returns the multiplier for a strategy under a regime (1.0 when
// the regime or strategy has no specific entry).
func regimeWeight(regime Regime, strategy string) float64 {
	if m, ok := regimeWeights[regime]; ok {
		if w, ok := m[strategy]; ok {
			return w
		}
	}
	return 1.0
}

// applyRegimeWeights returns copies of the signals with each Confidence scaled
// by its strategy's regime multiplier (clamped to [0,1]).
func applyRegimeWeights(signals []Signal, regime Regime) []Signal {
	out := make([]Signal, len(signals))
	for i, s := range signals {
		s.Confidence = clamp01(s.Confidence * regimeWeight(regime, s.Strategy))
		out[i] = s
	}
	return out
}

// ConsensusWithRegime runs the strategies, scales each signal's confidence by
// the regime weight (PRD-016 R4), then aggregates — so trend-followers dominate
// in a trend and mean-reversion dominates in a range. The regime is detected
// from the same candles; callers without enough candles for ADX should use
// Consensus (no-regime) instead.
func (r *Registry) ConsensusWithRegime(symbol string, candles []binance.Kline) Consensus {
	regime := DetectRegime(candles)
	weighted := applyRegimeWeights(r.AnalyzeAll(symbol, candles), regime)
	c := Aggregate(symbol, weighted)
	// PRD-022 R4: carry this timeframe's RSI(14) so AggregateMTF can apply the
	// extreme-zone entry filter (≥15 closes required; else left 0 = unavailable).
	if rsi, ok := binance.RSI(binance.ClosesOf(candles), 14); ok {
		c.RSI = rsi
	}
	return c
}

// ConsensusForWithRegime is the package-level convenience used by
// binance_mtf_klines (which always has enough 4h candles for ADX): default
// registry + this symbol's startup calibration + regime weighting.
func ConsensusForWithRegime(symbol string, candles []binance.Kline) Consensus {
	r := DefaultRegistry()
	r.SetCalibration(calibrationFor(symbol))
	return r.ConsensusWithRegime(symbol, candles)
}
