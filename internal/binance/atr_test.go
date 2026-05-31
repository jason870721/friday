package binance

import (
	"math"
	"strings"
	"testing"
)

func TestATR_HandComputed(t *testing.T) {
	// True ranges (each candle ranges 8→10, 9→11, ...; prev close in band) are
	// all 2, so ATR(3) over the first 4 candles is exactly 2.
	ks := []Kline{
		{High: 10, Low: 8, Close: 9},
		{High: 11, Low: 9, Close: 10},
		{High: 12, Low: 10, Close: 11},
		{High: 13, Low: 11, Close: 12},
	}
	atr, ok := ATR(ks, 3)
	if !ok || math.Abs(atr-2) > 1e-9 {
		t.Fatalf("ATR(3) = %v, ok=%v; want 2", atr, ok)
	}

	// A wide 5th candle: TR = max(20−10, |20−12|, |10−12|) = 10. Wilder smooth:
	// (2×(3−1) + 10) / 3 = 14/3.
	ks = append(ks, Kline{High: 20, Low: 10, Close: 15})
	atr, ok = ATR(ks, 3)
	if !ok || math.Abs(atr-14.0/3.0) > 1e-9 {
		t.Fatalf("ATR(3) after wide candle = %v, ok=%v; want %v", atr, ok, 14.0/3.0)
	}
}

func TestATR_TooShort(t *testing.T) {
	ks := []Kline{{High: 10, Low: 8, Close: 9}, {High: 11, Low: 9, Close: 10}}
	if _, ok := ATR(ks, 14); ok {
		t.Error("ATR(14) on 2 candles should be ok=false")
	}
}

func TestSemanticSummary_IncludesATR(t *testing.T) {
	ks := make([]Kline, 20)
	for i := range ks {
		base := 100.0 + float64(i)
		ks[i] = Kline{High: base + 1, Low: base - 1, Close: base}
	}
	got := SemanticSummary(ks)
	if !strings.Contains(got, "ATR(14)") {
		t.Errorf("summary missing ATR(14):\n%s", got)
	}
}
