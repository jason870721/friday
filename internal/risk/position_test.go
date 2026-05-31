package risk

import (
	"math"
	"testing"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func baseParams() SizeParams {
	return SizeParams{
		Balance:        10000,
		EntryPrice:     100,
		ATR:            2, // 2% of price
		Leverage:       20,
		RiskPerTrade:   DefaultRiskPerTrade,   // 1%
		StopMultiplier: DefaultStopMultiplier, // 2×
		MaxMarginPct:   0.15,
	}
}

func TestSuggestedSize_RiskBudgetAndStop(t *testing.T) {
	// risk = 10000×1% = $100; stop dist = 2×2 = 4; qty = 100/4 = 25.
	r := SuggestedSize(DirLong, baseParams())
	if !approx(r.Quantity, 25) {
		t.Errorf("Quantity = %v; want 25", r.Quantity)
	}
	if !approx(r.Notional, 2500) {
		t.Errorf("Notional = %v; want 2500", r.Notional)
	}
	if !approx(r.Margin, 125) { // 2500 / 20x
		t.Errorf("Margin = %v; want 125", r.Margin)
	}
	if !approx(r.StopPrice, 96) { // long: 100 − 4
		t.Errorf("StopPrice = %v; want 96 (long)", r.StopPrice)
	}
	if r.CappedByLimit {
		t.Error("should not be capped: margin 125 < 1500 cap")
	}
}

func TestSuggestedSize_ShortStopIsAboveEntry(t *testing.T) {
	r := SuggestedSize(DirShort, baseParams())
	if !approx(r.StopPrice, 104) { // short: 100 + 4
		t.Errorf("StopPrice = %v; want 104 (short)", r.StopPrice)
	}
}

func TestSuggestedSize_HigherVolMeansSmallerSize(t *testing.T) {
	lowVol := SuggestedSize(DirLong, baseParams())
	hi := baseParams()
	hi.ATR = 8 // 4× the volatility, same risk budget
	highVol := SuggestedSize(DirLong, hi)
	if !(highVol.Quantity < lowVol.Quantity) {
		t.Errorf("higher ATR should size smaller: low=%v high=%v", lowVol.Quantity, highVol.Quantity)
	}
	// 4× ATR → 1/4 the quantity at equal risk.
	if !approx(highVol.Quantity, lowVol.Quantity/4) {
		t.Errorf("expected quarter size; low=%v high=%v", lowVol.Quantity, highVol.Quantity)
	}
}

func TestSuggestedSize_CapBinds(t *testing.T) {
	// Tiny ATR ⇒ huge risk-based qty ⇒ margin would blow past the 15% cap.
	p := baseParams()
	p.ATR = 0.01 // stop dist 0.02 ⇒ qty 5000 ⇒ notional 500k ⇒ margin 25k ≫ 1500
	r := SuggestedSize(DirLong, p)
	if !r.CappedByLimit {
		t.Fatal("expected CappedByLimit")
	}
	if !approx(r.Margin, 1500) { // clamped to 15% of 10000
		t.Errorf("Margin = %v; want 1500 (cap)", r.Margin)
	}
	if !approx(r.Notional, 30000) || !approx(r.Quantity, 300) { // 1500×20x = 30000 / 100
		t.Errorf("post-clamp Notional/Quantity = %v/%v; want 30000/300", r.Notional, r.Quantity)
	}
}

func TestSuggestedSize_GuardsNonPositive(t *testing.T) {
	for _, bad := range []SizeParams{
		{}, // all zero
		{Balance: 10000, EntryPrice: 100, ATR: 0, Leverage: 20, RiskPerTrade: 0.01, StopMultiplier: 2},
		{Balance: 10000, EntryPrice: 0, ATR: 2, Leverage: 20, RiskPerTrade: 0.01, StopMultiplier: 2},
	} {
		if r := SuggestedSize(DirLong, bad); r != (SizeResult{}) {
			t.Errorf("expected zero result for %+v, got %+v", bad, r)
		}
	}
}
