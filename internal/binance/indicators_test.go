package binance

import (
	"math"
	"strings"
	"testing"
)

// klinesFromCloses builds a candle fixture where only the close matters
// for these indicators — open/high/low/volume are left zero.
func klinesFromCloses(closes ...float64) []Kline {
	ks := make([]Kline, len(closes))
	for i, c := range closes {
		ks[i] = Kline{Close: c}
	}
	return ks
}

func TestSMA(t *testing.T) {
	// Last 3 of [1,2,3,4,5] → (3+4+5)/3 = 4.
	if got, ok := SMA([]float64{1, 2, 3, 4, 5}, 3); !ok || got != 4 {
		t.Errorf("SMA last-3 = %v (ok=%v); want 4", got, ok)
	}
	// Full window average of [2,4,6,8] → 5.
	if got, ok := SMA([]float64{2, 4, 6, 8}, 4); !ok || got != 5 {
		t.Errorf("SMA full = %v (ok=%v); want 5", got, ok)
	}
	// Too few values → not ok.
	if _, ok := SMA([]float64{1, 2}, 3); ok {
		t.Error("SMA with fewer values than period: want ok=false")
	}
}

func TestEMA(t *testing.T) {
	// Hand-calculated: seed = SMA(1,2,3) = 2; α = 2/(3+1) = 0.5.
	//   value 4 → 2 + 0.5·(4−2) = 3;  value 5 → 3 + 0.5·(5−3) = 4.
	if got, ok := EMA([]float64{1, 2, 3, 4, 5}, 3); !ok || math.Abs(got-4) > 1e-9 {
		t.Errorf("EMA([1..5],3) = %v (ok=%v); want 4", got, ok)
	}
	// period 2 on [1,2,3,4]: seed 1.5, α=2/3 → 2.5 → 3.5.
	if got, ok := EMA([]float64{1, 2, 3, 4}, 2); !ok || math.Abs(got-3.5) > 1e-9 {
		t.Errorf("EMA([1..4],2) = %v (ok=%v); want 3.5", got, ok)
	}
	// Exactly `period` values → equals the SMA (no smoothing steps).
	if got, ok := EMA([]float64{2, 4, 6}, 3); !ok || math.Abs(got-4) > 1e-9 {
		t.Errorf("EMA(exact period) = %v (ok=%v); want SMA 4", got, ok)
	}
	// A constant series stays constant.
	if got, ok := EMA([]float64{5, 5, 5, 5, 5}, 3); !ok || math.Abs(got-5) > 1e-9 {
		t.Errorf("EMA(constant) = %v (ok=%v); want 5", got, ok)
	}
	// Too few values → not ok.
	if _, ok := EMA([]float64{1, 2}, 3); ok {
		t.Error("EMA with fewer values than period: want ok=false")
	}
}

func TestRSI_MonotonicEdges(t *testing.T) {
	rising := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	if got, ok := RSI(rising, 14); !ok || got != 100 {
		t.Errorf("RSI(strictly rising) = %v (ok=%v); want 100", got, ok)
	}
	falling := []float64{16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1}
	if got, ok := RSI(falling, 14); !ok || got != 0 {
		t.Errorf("RSI(strictly falling) = %v (ok=%v); want 0", got, ok)
	}
}

func TestADX_StrongTrendIsHigh(t *testing.T) {
	// A clean, steady uptrend: highs/lows/closes all rise by a constant →
	// +DM dominates, -DM ~0 → ADX should be high.
	n := 40
	ks := make([]Kline, n)
	for i := range ks {
		base := 100 + float64(i)
		ks[i] = Kline{High: base + 1, Low: base - 1, Close: base + 0.5}
	}
	adx, ok := ADX(ks, 14)
	if !ok {
		t.Fatal("ADX(strong trend): want ok=true")
	}
	if adx < 40 {
		t.Errorf("ADX(clean uptrend) = %.1f; want > 40 (strong trend)", adx)
	}
}

func TestADX_ChoppyIsLow(t *testing.T) {
	// A flat zig-zag → directional movement cancels → low ADX.
	n := 40
	ks := make([]Kline, n)
	for i := range ks {
		osc := float64(i % 2) // 0,1,0,1...
		ks[i] = Kline{High: 100 + osc + 1, Low: 100 + osc - 1, Close: 100 + osc}
	}
	adx, ok := ADX(ks, 14)
	if !ok {
		t.Fatal("ADX(choppy): want ok=true")
	}
	if adx >= 40 {
		t.Errorf("ADX(choppy) = %.1f; want < 40", adx)
	}
}

func TestADX_TooShort(t *testing.T) {
	ks := make([]Kline, 20) // < 2*14+1
	if _, ok := ADX(ks, 14); ok {
		t.Error("ADX with < 2*period+1 candles: want ok=false")
	}
}

func TestRSI_Range(t *testing.T) {
	mixed := []float64{44, 44.34, 44.09, 44.15, 43.61, 44.33, 44.83, 45.10, 45.42, 45.84, 46.08, 45.89, 46.03, 45.61, 46.28, 46.28}
	got, ok := RSI(mixed, 14)
	if !ok {
		t.Fatal("RSI(mixed): want ok=true")
	}
	if got <= 0 || got >= 100 {
		t.Errorf("RSI(mixed) = %v; want strictly within (0,100)", got)
	}
	// This series is net-bullish, so RSI should sit comfortably above the
	// midline.
	if got <= 50 {
		t.Errorf("RSI(net-bullish series) = %v; want > 50", got)
	}
}

func TestRSI_TooShort(t *testing.T) {
	if _, ok := RSI([]float64{1, 2, 3}, 14); ok {
		t.Error("RSI with fewer than period+1 closes: want ok=false")
	}
}

func TestSemanticSummary_Empty(t *testing.T) {
	if got := SemanticSummary(nil); !strings.Contains(got, "No candle data") {
		t.Errorf("SemanticSummary(nil) = %q; want a no-data message", got)
	}
}

func TestSemanticSummary_RisingSeries(t *testing.T) {
	// 21 strictly rising closes: above MA20, RSI maxed (overbought),
	// momentum rising.
	closes := make([]float64, 21)
	for i := range closes {
		closes[i] = float64(100 + i)
	}
	got := SemanticSummary(klinesFromCloses(closes...))

	for _, want := range []string{"above MA20", "overbought", "rising"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary %q missing %q", got, want)
		}
	}
}

func TestSemanticSummary_ShortSeries(t *testing.T) {
	// Only 5 candles: MA20 and RSI(14) both unavailable, but momentum
	// (last 3) is still reported.
	got := SemanticSummary(klinesFromCloses(10, 11, 12, 13, 14))
	if !strings.Contains(got, "MA20 unavailable") {
		t.Errorf("summary %q should report MA20 unavailable", got)
	}
	if !strings.Contains(got, "RSI(14) unavailable") {
		t.Errorf("summary %q should report RSI unavailable", got)
	}
	if !strings.Contains(got, "rising") {
		t.Errorf("summary %q should still report momentum", got)
	}
}

// guard against NaN/Inf leaking out of RSI on a flat series.
func TestRSI_FlatSeries(t *testing.T) {
	flat := make([]float64, 16) // all zeros → no gain, no loss
	got, ok := RSI(flat, 14)
	if !ok {
		t.Fatal("RSI(flat): want ok=true")
	}
	if math.IsNaN(got) || math.IsInf(got, 0) {
		t.Errorf("RSI(flat) = %v; want a finite value", got)
	}
}
