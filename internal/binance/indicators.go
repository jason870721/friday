package binance

import (
	"fmt"
	"math"
	"strings"
)

// Technical indicators over a candle series, plus a natural-language
// rendering of them. PRD-001 (Phase 1): the LLM reads market data far
// more reliably from a one-line semantic summary ("price above MA20, RSI
// overbought") than from a wall of OHLCV numbers. These helpers are pure
// and deterministic so they can be unit-tested against fixed fixtures.

// closesOf extracts the close price from each candle, oldest-first
// (matching the order Klines returns).
func closesOf(ks []Kline) []float64 {
	out := make([]float64, len(ks))
	for i, k := range ks {
		out[i] = k.Close
	}
	return out
}

// SMA returns the simple moving average of the last `period` values.
// ok is false when there are fewer than `period` values to average.
func SMA(values []float64, period int) (avg float64, ok bool) {
	if period < 1 || len(values) < period {
		return 0, false
	}
	var sum float64
	for _, v := range values[len(values)-period:] {
		sum += v
	}
	return sum / float64(period), true
}

// EMA returns the exponential moving average of `values`, weighting recent
// values more heavily than SMA. It seeds from the SMA of the first `period`
// values, then applies α = 2/(period+1) smoothing across the remainder. ok is
// false when there are fewer than `period` values. With exactly `period`
// values it equals the SMA (no smoothing steps run).
func EMA(values []float64, period int) (ema float64, ok bool) {
	if period < 1 || len(values) < period {
		return 0, false
	}
	var seed float64
	for _, v := range values[:period] {
		seed += v
	}
	ema = seed / float64(period)
	alpha := 2.0 / (float64(period) + 1)
	for _, v := range values[period:] {
		ema = (v-ema)*alpha + ema
	}
	return ema, true
}

// RSI returns Wilder's Relative Strength Index over `closes` for the given
// period (14 is conventional). It seeds the average gain/loss from the
// first `period` deltas, then applies Wilder smoothing across the rest.
// ok is false when there are fewer than period+1 closes (one delta short).
//
// Edge cases: a strictly rising series has zero losses → RSI 100; a
// strictly falling series has zero gains → RSI 0.
func RSI(closes []float64, period int) (rsi float64, ok bool) {
	if period < 1 || len(closes) < period+1 {
		return 0, false
	}

	var gain, loss float64
	for i := 1; i <= period; i++ {
		d := closes[i] - closes[i-1]
		if d >= 0 {
			gain += d
		} else {
			loss -= d
		}
	}
	avgGain := gain / float64(period)
	avgLoss := loss / float64(period)

	for i := period + 1; i < len(closes); i++ {
		d := closes[i] - closes[i-1]
		var g, l float64
		if d >= 0 {
			g = d
		} else {
			l = -d
		}
		avgGain = (avgGain*float64(period-1) + g) / float64(period)
		avgLoss = (avgLoss*float64(period-1) + l) / float64(period)
	}

	if avgLoss == 0 {
		return 100, true
	}
	rs := avgGain / avgLoss
	return 100 - 100/(1+rs), true
}

// ADX returns Wilder's Average Directional Index over the candle series for
// the given period (14 conventional) — a measure of trend STRENGTH (not
// direction): roughly <20 choppy, >25 trending, >40 strong trend. ok is
// false when there are fewer than 2*period+1 candles (ADX needs `period`
// directional-movement values to seed, then `period` more to smooth).
func ADX(ks []Kline, period int) (adx float64, ok bool) {
	if period < 1 || len(ks) < 2*period+1 {
		return 0, false
	}

	// Per-candle True Range and directional movement.
	n := len(ks)
	tr := make([]float64, n)
	plusDM := make([]float64, n)
	minusDM := make([]float64, n)
	for i := 1; i < n; i++ {
		up := ks[i].High - ks[i-1].High
		down := ks[i-1].Low - ks[i].Low
		if up > down && up > 0 {
			plusDM[i] = up
		}
		if down > up && down > 0 {
			minusDM[i] = down
		}
		hl := ks[i].High - ks[i].Low
		hc := math.Abs(ks[i].High - ks[i-1].Close)
		lc := math.Abs(ks[i].Low - ks[i-1].Close)
		tr[i] = math.Max(hl, math.Max(hc, lc))
	}

	// Wilder-smoothed sums seeded over the first `period` values (indices
	// 1..period), then smoothed forward; DX computed each step; ADX is the
	// Wilder average of DX over the final `period` steps.
	var smTR, smPlus, smMinus float64
	for i := 1; i <= period; i++ {
		smTR += tr[i]
		smPlus += plusDM[i]
		smMinus += minusDM[i]
	}

	dx := func(sTR, sP, sM float64) float64 {
		if sTR == 0 {
			return 0
		}
		pdi := 100 * sP / sTR
		mdi := 100 * sM / sTR
		if pdi+mdi == 0 {
			return 0
		}
		return 100 * math.Abs(pdi-mdi) / (pdi + mdi)
	}

	var dxSum float64
	dxCount := 0
	for i := period + 1; i < n; i++ {
		smTR = smTR - smTR/float64(period) + tr[i]
		smPlus = smPlus - smPlus/float64(period) + plusDM[i]
		smMinus = smMinus - smMinus/float64(period) + minusDM[i]
		dxSum += dx(smTR, smPlus, smMinus)
		dxCount++
	}
	if dxCount == 0 {
		return 0, false
	}
	return dxSum / float64(dxCount), true
}

// ATR returns Wilder's Average True Range over the candle series for the given
// period (14 conventional) — the average absolute per-candle range, in price
// units (a volatility measure). True range is max(high−low, |high−prevClose|,
// |low−prevClose|); ATR seeds from the simple average of the first `period`
// true ranges, then applies Wilder smoothing. ok is false when there are fewer
// than period+1 candles (true range needs a previous close).
func ATR(ks []Kline, period int) (atr float64, ok bool) {
	if period < 1 || len(ks) < period+1 {
		return 0, false
	}
	n := len(ks)
	tr := make([]float64, n)
	for i := 1; i < n; i++ {
		hl := ks[i].High - ks[i].Low
		hc := math.Abs(ks[i].High - ks[i-1].Close)
		lc := math.Abs(ks[i].Low - ks[i-1].Close)
		tr[i] = math.Max(hl, math.Max(hc, lc))
	}

	var sum float64
	for i := 1; i <= period; i++ {
		sum += tr[i]
	}
	atr = sum / float64(period)
	for i := period + 1; i < n; i++ {
		atr = (atr*float64(period-1) + tr[i]) / float64(period)
	}
	return atr, true
}

// Direction labels for ClassifyDirection (PRD-008), matching the Analyst's
// bias vocabulary.
const (
	DirectionBullish = "BULLISH"
	DirectionBearish = "BEARISH"
	DirectionNeutral = "NEUTRAL"
)

// ClassifyDirection reduces a candle series to a single coarse direction for
// cross-timeframe alignment (PRD-008):
//
//	BULLISH  price > MA20, RSI(14) in [50,70], last 3 closes rising
//	BEARISH  price < MA20, RSI(14) in [30,50], last 3 closes falling
//	NEUTRAL  otherwise (incl. too few candles for MA20/RSI)
//
// The RSI bands deliberately exclude the extremes (>70 / <30) so an
// overbought/oversold exhaustion move is not read as trend confirmation.
func ClassifyDirection(ks []Kline) string {
	cs := closesOf(ks)
	if len(cs) < 3 {
		return DirectionNeutral
	}
	ma, okMA := SMA(cs, 20)
	rsi, okRSI := RSI(cs, 14)
	if !okMA || !okRSI {
		return DirectionNeutral
	}
	last := cs[len(cs)-1]
	a, b, c := cs[len(cs)-3], cs[len(cs)-2], cs[len(cs)-1]
	rising := c > b && b > a
	falling := c < b && b < a

	switch {
	case last > ma && rsi >= 50 && rsi <= 70 && rising:
		return DirectionBullish
	case last < ma && rsi >= 30 && rsi <= 50 && falling:
		return DirectionBearish
	default:
		return DirectionNeutral
	}
}

// SemanticSummary renders a candle series into a single natural-language
// line the LLM can read at a glance: current price, position relative to
// MA20, RSI(14) with its zone, and short-term momentum from the last three
// closes. When the series is too short for an indicator, it says so rather
// than fabricating a value.
func SemanticSummary(ks []Kline) string {
	if len(ks) == 0 {
		return "No candle data available."
	}

	cs := closesOf(ks)
	last := cs[len(cs)-1]
	parts := []string{fmt.Sprintf("Current close %.4f.", last)}

	if ma, ok := SMA(cs, 20); ok {
		rel := "above"
		if last < ma {
			rel = "below"
		}
		parts = append(parts, fmt.Sprintf("Price is %s MA20 (%.4f).", rel, ma))
	} else {
		parts = append(parts, fmt.Sprintf("MA20 unavailable (need 20 candles, have %d).", len(cs)))
	}

	if rsi, ok := RSI(cs, 14); ok {
		zone := "neutral"
		switch {
		case rsi >= 70:
			zone = "overbought"
		case rsi <= 30:
			zone = "oversold"
		}
		parts = append(parts, fmt.Sprintf("RSI(14) is %.1f (%s).", rsi, zone))
	} else {
		parts = append(parts, fmt.Sprintf("RSI(14) unavailable (need 15 candles, have %d).", len(cs)))
	}

	if len(cs) >= 3 {
		a, b, c := cs[len(cs)-3], cs[len(cs)-2], cs[len(cs)-1]
		switch {
		case c > b && b > a:
			parts = append(parts, "Short-term momentum is rising (last 3 closes higher).")
		case c < b && b < a:
			parts = append(parts, "Short-term momentum is falling (last 3 closes lower).")
		default:
			parts = append(parts, "Short-term momentum is mixed/choppy.")
		}
	}

	if atr, ok := ATR(ks, 14); ok {
		pct := 0.0
		if last != 0 {
			pct = atr / last * 100
		}
		parts = append(parts, fmt.Sprintf("ATR(14) is %.4f (%.2f%% of price) — volatility for risk-based sizing.", atr, pct))
	}

	return strings.Join(parts, " ")
}
