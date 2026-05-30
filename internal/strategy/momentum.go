package strategy

import (
	"fmt"

	"github.com/johnny1110/friday/internal/binance"
)

// Momentum is the momentum-continuation strategy: trade in the direction of
// an established, non-exhausted push.
//
//	Long : last 3 closes rising AND price > MA20 AND RSI in 50–70
//	Short: last 3 closes falling AND price < MA20 AND RSI in 30–50
//
// RSI is kept below 70 (above 30) so we ride momentum but don't chase an
// already-overbought (oversold) move. When ADX(14) is available and the
// trend is strong (>25), confidence is boosted.
type Momentum struct{}

func (Momentum) Name() string { return "momentum" }

func (Momentum) Analyze(symbol string, ks []binance.Kline) Signal {
	sig := Signal{Symbol: symbol, Direction: Neutral, Strategy: "momentum"}
	closes := closesOf(ks)
	if len(closes) < 20 {
		sig.Reason = "insufficient candles for MA20"
		return sig
	}
	ma, _ := binance.SMA(closes, 20)
	rsi, ok := binance.RSI(closes, 14)
	if !ok {
		sig.Reason = "insufficient candles for RSI"
		return sig
	}
	last := closes[len(closes)-1]
	a, b, c := closes[len(closes)-3], closes[len(closes)-2], closes[len(closes)-1]
	rising := c > b && b > a
	falling := c < b && b < a

	conf := 0.6 + adxBoost(ks)

	switch {
	case rising && last > ma && rsi >= 50 && rsi <= 70:
		sig.Direction = Long
		sig.Confidence = clamp01(conf)
		sig.Invalidation = ma
		sig.Reason = fmt.Sprintf("3 rising closes, price %.4f > MA20 %.4f, RSI %.0f (not overbought)", last, ma, rsi)
	case falling && last < ma && rsi >= 30 && rsi <= 50:
		sig.Direction = Short
		sig.Confidence = clamp01(conf)
		sig.Invalidation = ma
		sig.Reason = fmt.Sprintf("3 falling closes, price %.4f < MA20 %.4f, RSI %.0f (not oversold)", last, ma, rsi)
	default:
		sig.Reason = fmt.Sprintf("no clean momentum (RSI %.0f, price %.4f vs MA20 %.4f)", rsi, last, ma)
	}
	return sig
}

// adxBoost adds up to +0.2 confidence when a strong trend (ADX>25) is
// present, 0 when ADX is unavailable or weak.
func adxBoost(ks []binance.Kline) float64 {
	adx, ok := binance.ADX(ks, 14)
	if !ok {
		return 0
	}
	switch {
	case adx >= 40:
		return 0.2
	case adx >= 25:
		return 0.1
	default:
		return 0
	}
}

func closesOf(ks []binance.Kline) []float64 {
	out := make([]float64, len(ks))
	for i, k := range ks {
		out[i] = k.Close
	}
	return out
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
