package strategy

import (
	"strings"
	"testing"
)

func TestRSIFilter(t *testing.T) {
	long := Consensus{Direction: Long, Confidence: 0.6, Summary: "x"}
	short := Consensus{Direction: Short, Confidence: 0.6, Summary: "x"}
	neutral := Consensus{Direction: Neutral, Summary: "x"}

	// Extreme zones block either direction.
	if got := RSIFilter(long, 80); got.Direction != Neutral || got.Confidence != 0 {
		t.Errorf("Long@80 → %v %.2f; want Neutral 0", got.Direction, got.Confidence)
	}
	if got := RSIFilter(short, 20); got.Direction != Neutral {
		t.Errorf("Short@20 → %v; want Neutral", got.Direction)
	}
	if got := RSIFilter(long, 75); got.Direction != Neutral {
		t.Errorf("Long@75 (boundary) → %v; want Neutral", got.Direction)
	}
	if got := RSIFilter(short, 25); got.Direction != Neutral {
		t.Errorf("Short@25 (boundary) → %v; want Neutral", got.Direction)
	}
	if got := RSIFilter(long, 80); !strings.Contains(got.Summary, "blocked: RSI 80.0 in extreme zone") {
		t.Errorf("blocked summary missing: %q", got.Summary)
	}

	// In-range RSI passes unchanged.
	if got := RSIFilter(long, 50); got.Direction != Long || got.Confidence != 0.6 {
		t.Errorf("Long@50 → %v %.2f; want Long 0.6", got.Direction, got.Confidence)
	}
	if got := RSIFilter(short, 50); got.Direction != Short {
		t.Errorf("Short@50 → %v; want Short", got.Direction)
	}

	// Neutral is a no-op; RSI unavailable (0) passes through.
	if got := RSIFilter(neutral, 80); got.Direction != Neutral {
		t.Errorf("Neutral@80 → %v; want Neutral (no-op)", got.Direction)
	}
	if got := RSIFilter(long, 0); got.Direction != Long {
		t.Errorf("Long@0 (RSI unavailable) → %v; want Long (pass through)", got.Direction)
	}
}

func TestRSIFilter_DisabledByEnv(t *testing.T) {
	t.Setenv("FRIDAY_RSI_FILTER", "false")
	long := Consensus{Direction: Long, Confidence: 0.6}
	if got := RSIFilter(long, 80); got.Direction != Long {
		t.Errorf("FRIDAY_RSI_FILTER=false should disable; got %v", got.Direction)
	}
}

func TestAggregateMTF_PRD022(t *testing.T) {
	// RSI=50 everywhere so the per-TF RSI filter never fires in these cases.
	long := func(c float64) Consensus { return Consensus{Direction: Long, Confidence: c, RSI: 50} }
	short := func(c float64) Consensus { return Consensus{Direction: Short, Confidence: c, RSI: 50} }
	neutral := Consensus{Direction: Neutral, RSI: 50}

	// Lowered hysteresis: a lone 5m LONG 0.6 with neutral higher TFs fires LONG.
	if c := AggregateMTF(map[string]Consensus{"5m": long(0.6), "1h": neutral, "4h": neutral}); c.Direction != Long {
		t.Errorf("5m LONG 0.6 + neutrals → %v (%s); want LONG", c.Direction, c.Summary)
	}

	// 5m+1h override: aligned lower TFs with a neutral 4h adopt their avg confidence.
	got := AggregateMTF(map[string]Consensus{"5m": long(0.7), "1h": long(0.6), "4h": neutral})
	if got.Direction != Long {
		t.Errorf("5m+1h LONG + 4h neutral → %v; want LONG", got.Direction)
	}
	if got.Confidence < 0.64 || got.Confidence > 0.66 {
		t.Errorf("override confidence = %.3f; want ~0.65 (avg of 0.7,0.6)", got.Confidence)
	}
	if !strings.Contains(got.Summary, "5m+1h override") {
		t.Errorf("missing override note: %q", got.Summary)
	}

	// 4h hard veto: opposing 4h forces NEUTRAL even though the weighted net is LONG.
	if c := AggregateMTF(map[string]Consensus{"5m": long(0.7), "1h": long(0.6), "4h": short(0.5)}); c.Direction != Neutral {
		t.Errorf("5m+1h LONG + 4h SHORT → %v (%s); want NEUTRAL (4h veto)", c.Direction, c.Summary)
	}

	// RSI filter inside MTF: a 5m LONG at RSI 80 is blocked before voting, so a
	// lone extreme 5m can't carry the vote.
	if c := AggregateMTF(map[string]Consensus{
		"5m": {Direction: Long, Confidence: 0.6, RSI: 80},
		"1h": neutral, "4h": neutral,
	}); c.Direction != Neutral {
		t.Errorf("5m LONG@RSI80 + neutrals → %v (%s); want NEUTRAL (RSI-filtered)", c.Direction, c.Summary)
	}
}

func TestAggregateMTF_HysteresisEnv(t *testing.T) {
	// With a tiny net (0.04) and the default 0.05 band → NEUTRAL; lowering the
	// band via env to 0.01 lets it through as LONG.
	mk := map[string]Consensus{"5m": {Direction: Long, Confidence: 0.04, RSI: 50}, "1h": {Direction: Neutral, RSI: 50}}
	if c := AggregateMTF(mk); c.Direction != Neutral {
		t.Errorf("net 0.04 under default 0.05 band → %v; want NEUTRAL", c.Direction)
	}
	t.Setenv("FRIDAY_MTF_HYSTERESIS", "0.01")
	if c := AggregateMTF(mk); c.Direction != Long {
		t.Errorf("net 0.04 over 0.01 band → %v; want LONG", c.Direction)
	}
}
