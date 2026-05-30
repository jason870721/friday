package strategy

import (
	"fmt"

	"github.com/johnny1110/friday/internal/binance"
)

// Breakout trades a close that clears the recent range on rising volume.
// With the limited candle window friday fetches (e.g. 5m×20), "range" is
// the high/low of the prior candles in the series rather than a true 24h
// extreme — a local breakout. Volume must exceed 1.5× the average to
// confirm.
//
//	Long : last close > prior-bars high  AND last volume > 1.5× avg volume
//	Short: last close < prior-bars low   AND last volume > 1.5× avg volume
type Breakout struct{}

func (Breakout) Name() string { return "breakout" }

func (Breakout) Analyze(symbol string, ks []binance.Kline) Signal {
	sig := Signal{Symbol: symbol, Direction: Neutral, Strategy: "breakout"}
	if len(ks) < 6 {
		sig.Reason = "insufficient candles for a range"
		return sig
	}

	prior := ks[:len(ks)-1]
	last := ks[len(ks)-1]

	priorHigh, priorLow := prior[0].High, prior[0].Low
	var volSum float64
	for _, k := range prior {
		if k.High > priorHigh {
			priorHigh = k.High
		}
		if k.Low < priorLow {
			priorLow = k.Low
		}
		volSum += k.Volume
	}
	avgVol := volSum / float64(len(prior))
	volSurge := avgVol > 0 && last.Volume > avgVol*1.5

	conf := 0.65 + adxBoost(ks)

	switch {
	case last.Close > priorHigh && volSurge:
		sig.Direction = Long
		sig.Confidence = clamp01(conf)
		sig.Invalidation = priorHigh
		sig.Reason = fmt.Sprintf("close %.4f broke range high %.4f on %.1f× avg volume", last.Close, priorHigh, last.Volume/avgVol)
	case last.Close < priorLow && volSurge:
		sig.Direction = Short
		sig.Confidence = clamp01(conf)
		sig.Invalidation = priorLow
		sig.Reason = fmt.Sprintf("close %.4f broke range low %.4f on %.1f× avg volume", last.Close, priorLow, last.Volume/avgVol)
	default:
		sig.Reason = fmt.Sprintf("no breakout (close %.4f in range %.4f–%.4f, volSurge=%v)", last.Close, priorLow, priorHigh, volSurge)
	}
	return sig
}
