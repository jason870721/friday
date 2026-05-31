package strategy

import (
	"fmt"

	"github.com/johnny1110/friday/internal/binance"
)

// EMACross is the trend-following EMA crossover strategy (PRD-013). It reads
// the fast/slow EMA relationship for direction and the long EMA for the
// regime filter, complementing momentum (which keys off MA20 + RSI) with a
// pure moving-average trend vote.
//
//	Long : EMA(9) > EMA(21) AND close > EMA(50) — fast above slow, price above the long trend
//	Short: EMA(9) < EMA(21) AND close < EMA(50)
//
// Confidence mirrors the other trend strategies (a 0.55 base + the shared ADX
// boost). The signal is invalidated when price crosses back through EMA(21).
type EMACross struct{}

func (EMACross) Name() string { return "ema_cross" }

func (EMACross) Analyze(symbol string, ks []binance.Kline) Signal {
	sig := Signal{Symbol: symbol, Direction: Neutral, Strategy: "ema_cross"}
	closes := closesOf(ks)
	if len(closes) < 50 {
		sig.Reason = fmt.Sprintf("insufficient candles for EMA50 (have %d, need 50)", len(closes))
		return sig
	}
	ema9, _ := binance.EMA(closes, 9)
	ema21, _ := binance.EMA(closes, 21)
	ema50, ok := binance.EMA(closes, 50)
	if !ok {
		sig.Reason = "insufficient candles for EMA50"
		return sig
	}
	last := closes[len(closes)-1]
	conf := 0.55 + adxBoost(ks)

	switch {
	case ema9 > ema21 && last > ema50:
		sig.Direction = Long
		sig.Confidence = clamp01(conf)
		sig.Invalidation = ema21
		sig.Reason = fmt.Sprintf("EMA9 %.4f > EMA21 %.4f and price %.4f > EMA50 %.4f — uptrend", ema9, ema21, last, ema50)
	case ema9 < ema21 && last < ema50:
		sig.Direction = Short
		sig.Confidence = clamp01(conf)
		sig.Invalidation = ema21
		sig.Reason = fmt.Sprintf("EMA9 %.4f < EMA21 %.4f and price %.4f < EMA50 %.4f — downtrend", ema9, ema21, last, ema50)
	default:
		sig.Reason = fmt.Sprintf("EMAs tangled (EMA9 %.4f / EMA21 %.4f / EMA50 %.4f, price %.4f)", ema9, ema21, ema50, last)
	}
	return sig
}
