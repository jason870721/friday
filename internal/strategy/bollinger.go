package strategy

import (
	"fmt"

	"github.com/johnny1110/friday/internal/binance"
)

// Bollinger trades the Bollinger Bands (MA20 ± 2σ) — PRD-020 §7. Unlike the
// fixed-2%-deviation MeanReversion strategy, the bands ADAPT to volatility
// (they widen in high vol, narrow in low vol), so a "stretched" read is always
// relative to the symbol's current regime. It runs two complementary modes:
//
//   - Mean-reversion (fade a band touch back toward the mean):
//     LONG  : close ≤ lower band AND RSI(14) < 35  → fade up. Invalidation = lower band.
//     SHORT : close ≥ upper band AND RSI(14) > 65  → fade down. Invalidation = upper band.
//     The RSI filter prevents fading a band touch that is actually a breakout.
//   - Band-walk (ride a trend hugging a band on EXPANDING bandwidth):
//     LONG  : close > mid, near the upper band, bandwidth > 5% and expanding,
//     RSI in [50,70] → trend continuation. Invalidation = mid band.
//     SHORT : mirror (close < mid, near lower, RSI in [30,50]).
//     Bandwidth expansion distinguishes a true trend from a slow grind.
//
// If both fire (rare, contradictory), the higher-confidence one wins.
type Bollinger struct{}

func (Bollinger) Name() string { return "bollinger" }

// bbConfMeanRev / bbConfBandWalk are the base confidences before the shared ADX
// boost. Mean-reversion is the slightly stronger base (a tagged band with an
// RSI extreme is a cleaner setup than a still-developing band-walk).
const (
	bbConfMeanRev  = 0.55
	bbConfBandWalk = 0.50
	bbNearBandPct  = 0.005 // "near" a band = within 0.5% of it
	bbMinBandwidth = 0.05  // band-walk needs bandwidth > 5%
)

func (Bollinger) Analyze(symbol string, ks []binance.Kline) Signal {
	sig := Signal{Symbol: symbol, Direction: Neutral, Strategy: "bollinger"}
	closes := closesOf(ks)
	if len(closes) < 21 { // need 20 for the bands + one prior bar for the bandwidth trend
		sig.Reason = fmt.Sprintf("insufficient candles for Bollinger (have %d, need 21)", len(closes))
		return sig
	}
	mid, upper, lower, bw, ok := binance.BollingerBands(closes, 20, 2.0)
	if !ok {
		sig.Reason = "Bollinger bands unavailable"
		return sig
	}
	rsi, ok := binance.RSI(closes, 14)
	if !ok {
		sig.Reason = "insufficient candles for RSI"
		return sig
	}
	last := closes[len(closes)-1]

	// Bandwidth one candle ago, to tell expanding from contracting.
	_, _, _, prevBW, okPrev := binance.BollingerBands(closes[:len(closes)-1], 20, 2.0)
	expanding := okPrev && bw > prevBW

	conf := clamp01(bbConfMeanRev + adxBoost(ks))
	walkConf := clamp01(bbConfBandWalk + adxBoost(ks))

	// Candidate signals from each mode; pick the higher-confidence one. A
	// degenerate (zero-width) band means no volatility and no meaningful tag,
	// so neither mode should fire.
	var best Signal
	if bw <= 0 {
		sig.Reason = fmt.Sprintf("flat bands (bandwidth %.2f%%, close %.4f) — no edge", bw*100, last)
		return sig
	}

	switch {
	case last <= lower && rsi < 35:
		best = Signal{Symbol: symbol, Strategy: "bollinger", Direction: Long, Confidence: conf,
			Invalidation: lower, TakeProfit: mid,
			Reason: fmt.Sprintf("close %.4f at/below lower band %.4f, RSI %.0f — fade up toward mid %.4f", last, lower, rsi, mid)}
	case last >= upper && rsi > 65:
		best = Signal{Symbol: symbol, Strategy: "bollinger", Direction: Short, Confidence: conf,
			Invalidation: upper, TakeProfit: mid,
			Reason: fmt.Sprintf("close %.4f at/above upper band %.4f, RSI %.0f — fade down toward mid %.4f", last, upper, rsi, mid)}
	}

	// Band-walk: only when not already a mean-reversion fade.
	nearUpper := upper > 0 && (upper-last)/upper <= bbNearBandPct
	nearLower := lower > 0 && (last-lower)/lower <= bbNearBandPct
	var walk Signal
	switch {
	case last > mid && nearUpper && bw > bbMinBandwidth && expanding && rsi >= 50 && rsi <= 70:
		walk = Signal{Symbol: symbol, Strategy: "bollinger", Direction: Long, Confidence: walkConf,
			Invalidation: mid,
			Reason:       fmt.Sprintf("band-walk up: close %.4f hugging upper %.4f, bandwidth %.1f%% expanding, RSI %.0f", last, upper, bw*100, rsi)}
	case last < mid && nearLower && bw > bbMinBandwidth && expanding && rsi >= 30 && rsi <= 50:
		walk = Signal{Symbol: symbol, Strategy: "bollinger", Direction: Short, Confidence: walkConf,
			Invalidation: mid,
			Reason:       fmt.Sprintf("band-walk down: close %.4f hugging lower %.4f, bandwidth %.1f%% expanding, RSI %.0f", last, lower, bw*100, rsi)}
	}

	switch {
	case best.Direction != Neutral && walk.Direction != Neutral:
		if walk.Confidence > best.Confidence {
			return walk
		}
		return best
	case best.Direction != Neutral:
		return best
	case walk.Direction != Neutral:
		return walk
	default:
		sig.Reason = fmt.Sprintf("inside the bands (close %.4f in %.4f–%.4f, RSI %.0f, bandwidth %.1f%%)", last, lower, upper, rsi, bw*100)
		return sig
	}
}
