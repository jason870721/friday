package orchestrator

import (
	"strings"
	"testing"
)

func TestAllNeutralBias(t *testing.T) {
	allN := AnalystReport{Symbols: []SymbolAnalysis{{Symbol: "BTCUSDT", Bias: "NEUTRAL"}, {Symbol: "ETHUSDT", Bias: " neutral "}}}
	if !allNeutralBias(allN) {
		t.Errorf("all-NEUTRAL (incl. whitespace/case) should be true")
	}
	oneDir := AnalystReport{Symbols: []SymbolAnalysis{{Symbol: "BTCUSDT", Bias: "NEUTRAL"}, {Symbol: "ETHUSDT", Bias: "BEARISH"}}}
	if allNeutralBias(oneDir) {
		t.Errorf("a directional bias must make it false")
	}
	if !allNeutralBias(AnalystReport{}) {
		t.Errorf("empty report should be all-NEUTRAL (nothing to act on)")
	}
}

func TestParseMTFDirections(t *testing.T) {
	syms := []MarketSymbol{{Name: "BTCUSDT"}, {Name: "ETHUSDT"}, {Name: "SOLUSDT"}}
	mtf := "BTCUSDT multi-timeframe read:\n[5m] … → BEARISH\nMTF Strategy: SHORT (5m:SHORT …) → weighted SHORT 0.42\n" +
		"ETHUSDT multi-timeframe read:\n[5m] … → NEUTRAL\nMTF Strategy: NEUTRAL (…) → weighted NEUTRAL 0.00\n" +
		"SOLUSDT multi-timeframe read:\nMTF Strategy: LONG (…) → weighted LONG 0.55\n"

	got := parseMTFDirections(mtf, syms)
	for sym, want := range map[string]string{"BTCUSDT": "SHORT", "ETHUSDT": "NEUTRAL", "SOLUSDT": "LONG"} {
		if got[sym] != want {
			t.Errorf("%s → %q; want %q", sym, got[sym], want)
		}
	}
}

// The persistence gate must count consecutive same-direction rounds, reset on a
// flip, and zero out on NEUTRAL — and only "confirmed" (≥2) streaks should read
// as actionable in the prompt line.
func TestMTFStreaks_AccumulateFlipReset(t *testing.T) {
	o := &Orchestrator{symbols: []MarketSymbol{{Name: "BTCUSDT"}, {Name: "ETHUSDT"}}}

	// Round 1: BTC SHORT, ETH NEUTRAL → both unconfirmed/none.
	o.updateMTFStreaks(map[string]string{"BTCUSDT": "SHORT", "ETHUSDT": "NEUTRAL"})
	if o.mtfStreak["BTCUSDT"] != (mtfStreakEntry{dir: "SHORT", count: 1}) {
		t.Fatalf("round1 BTC = %+v; want SHORT×1", o.mtfStreak["BTCUSDT"])
	}
	if l := o.persistenceLine(); !strings.Contains(l, "BTCUSDT SHORT ×1 (unconfirmed") || strings.Contains(l, "ETHUSDT") {
		t.Errorf("round1 line = %q; want BTC ×1 unconfirmed, ETH absent", l)
	}

	// Round 2: BTC holds SHORT → confirmed ×2.
	o.updateMTFStreaks(map[string]string{"BTCUSDT": "SHORT", "ETHUSDT": "NEUTRAL"})
	if o.mtfStreak["BTCUSDT"].count != 2 {
		t.Errorf("round2 BTC count = %d; want 2", o.mtfStreak["BTCUSDT"].count)
	}
	if l := o.persistenceLine(); !strings.Contains(l, "BTCUSDT SHORT ×2 (confirmed)") {
		t.Errorf("round2 line = %q; want BTC ×2 confirmed", l)
	}

	// Round 3: BTC flips to LONG → restart at ×1.
	o.updateMTFStreaks(map[string]string{"BTCUSDT": "LONG", "ETHUSDT": "NEUTRAL"})
	if o.mtfStreak["BTCUSDT"] != (mtfStreakEntry{dir: "LONG", count: 1}) {
		t.Errorf("round3 BTC = %+v; want LONG×1 (flip resets)", o.mtfStreak["BTCUSDT"])
	}

	// Round 4: BTC goes NEUTRAL → count zeroed, no active streak listed.
	o.updateMTFStreaks(map[string]string{"BTCUSDT": "NEUTRAL", "ETHUSDT": "NEUTRAL"})
	if o.mtfStreak["BTCUSDT"].count != 0 {
		t.Errorf("round4 BTC count = %d; want 0 (NEUTRAL resets)", o.mtfStreak["BTCUSDT"].count)
	}
	if l := o.persistenceLine(); !strings.Contains(l, "no active MTF streak") {
		t.Errorf("round4 line = %q; want the no-streak message", l)
	}
}
