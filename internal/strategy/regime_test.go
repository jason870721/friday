package strategy

import (
	"math"
	"testing"

	"github.com/johnny1110/friday/internal/binance"
)

// trendingKlines is a strong, steady climb — high directional movement → ADX
// well above 25.
func trendingKlines(n int) []binance.Kline {
	ks := make([]binance.Kline, n)
	for i := range n {
		b := 100.0 + float64(i)*2
		ks[i] = binance.Kline{High: b + 1, Low: b - 1, Close: b + 0.5}
	}
	return ks
}

// rangingKlines oscillates in a tight band — directional movement cancels →
// ADX below 20.
func rangingKlines(n int) []binance.Kline {
	ks := make([]binance.Kline, n)
	for i := range n {
		b := 100.0
		if i%2 == 1 {
			b = 100.5
		}
		ks[i] = binance.Kline{High: b + 0.2, Low: b - 0.2, Close: b}
	}
	return ks
}

func TestDetectRegime_Trending(t *testing.T) {
	if r := DetectRegime(trendingKlines(40)); r != RegimeTrending {
		adx, _ := binance.ADX(trendingKlines(40), 14)
		t.Errorf("strong trend → %v (ADX %.1f); want TRENDING", r, adx)
	}
}

func TestDetectRegime_Ranging(t *testing.T) {
	if r := DetectRegime(rangingKlines(40)); r != RegimeRanging {
		adx, _ := binance.ADX(rangingKlines(40), 14)
		t.Errorf("choppy band → %v (ADX %.1f); want RANGING", r, adx)
	}
}

func TestDetectRegime_InsufficientCandles(t *testing.T) {
	if r := DetectRegime(trendingKlines(10)); r != RegimeTransitional {
		t.Errorf("too few candles for ADX → %v; want TRANSITIONAL (fallback)", r)
	}
}

func TestApplyRegimeWeights_Trending(t *testing.T) {
	// AC: trending up-weights momentum ×1.2 and down-weights mean-reversion ×0.3.
	sigs := []Signal{
		{Strategy: "momentum", Direction: Long, Confidence: 0.5},
		{Strategy: "mean_reversion", Direction: Short, Confidence: 0.5},
		{Strategy: "ema_cross", Direction: Long, Confidence: 0.5},
	}
	w := applyRegimeWeights(sigs, RegimeTrending)
	if math.Abs(w[0].Confidence-0.6) > 1e-9 {
		t.Errorf("momentum = %.4f; want 0.60 (0.5×1.2)", w[0].Confidence)
	}
	if math.Abs(w[1].Confidence-0.15) > 1e-9 {
		t.Errorf("mean_reversion = %.4f; want 0.15 (0.5×0.3)", w[1].Confidence)
	}
	if math.Abs(w[2].Confidence-0.6) > 1e-9 {
		t.Errorf("ema_cross = %.4f; want 0.60 (0.5×1.2)", w[2].Confidence)
	}
}

func TestApplyRegimeWeights_TransitionalIsNeutral(t *testing.T) {
	sigs := []Signal{{Strategy: "momentum", Direction: Long, Confidence: 0.5}}
	if w := applyRegimeWeights(sigs, RegimeTransitional); math.Abs(w[0].Confidence-0.5) > 1e-9 {
		t.Errorf("transitional should not adjust weight, got %.4f", w[0].Confidence)
	}
}
