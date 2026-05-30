package strategy

import (
	"fmt"

	"github.com/johnny1110/friday/internal/binance"
)

// MeanReversion fades a stretched, exhausted move back toward MA20:
//
//	Long : price > 2% BELOW MA20 AND RSI < 30 (oversold) → expect bounce up
//	Short: price > 2% ABOVE MA20 AND RSI > 70 (overbought) → expect fade down
//
// This is deliberately the counterpart to Momentum — on a strong trend the
// two disagree, which the aggregator resolves to Neutral (no consensus),
// the correct "no clean edge" answer.
type MeanReversion struct{}

func (MeanReversion) Name() string { return "mean_reversion" }

func (MeanReversion) Analyze(symbol string, ks []binance.Kline) Signal {
	sig := Signal{Symbol: symbol, Direction: Neutral, Strategy: "mean_reversion"}
	closes := closesOf(ks)
	if len(closes) < 20 {
		sig.Reason = "insufficient candles for MA20"
		return sig
	}
	ma, _ := binance.SMA(closes, 20)
	rsi, ok := binance.RSI(closes, 14)
	if !ok || ma == 0 {
		sig.Reason = "insufficient candles for RSI"
		return sig
	}
	last := closes[len(closes)-1]
	devPct := (last - ma) / ma * 100

	switch {
	case devPct <= -2 && rsi < 30:
		sig.Direction = Long
		sig.Confidence = 0.6
		sig.Invalidation = last * 0.99 // a touch below; further breakdown invalidates the fade
		sig.Reason = fmt.Sprintf("oversold: %.2f%% below MA20, RSI %.0f — fade back up", -devPct, rsi)
	case devPct >= 2 && rsi > 70:
		sig.Direction = Short
		sig.Confidence = 0.6
		sig.Invalidation = last * 1.01
		sig.Reason = fmt.Sprintf("overbought: %.2f%% above MA20, RSI %.0f — fade back down", devPct, rsi)
	default:
		sig.Reason = fmt.Sprintf("not stretched (%.2f%% from MA20, RSI %.0f)", devPct, rsi)
	}
	return sig
}
