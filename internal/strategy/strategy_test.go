package strategy

import (
	"strings"
	"testing"

	"github.com/johnny1110/friday/internal/binance"
)

// candlesFromCloses builds a fixture where high/low straddle the close and
// volume is constant — enough for the MA20/RSI-based strategies.
func candlesFromCloses(closes ...float64) []binance.Kline {
	ks := make([]binance.Kline, len(closes))
	for i, c := range closes {
		ks[i] = binance.Kline{Close: c, High: c * 1.002, Low: c * 0.998, Volume: 100}
	}
	return ks
}

// risingWithPullbacks builds a net-up series whose RSI lands in the 50–70
// momentum band (not overbought) and whose last three closes rise.
func risingWithPullbacks(n int) []float64 {
	cs := make([]float64, 0, n)
	v := 100.0
	for i := 0; i < n-3; i++ {
		if i%2 == 0 {
			v += 1.5
		} else {
			v -= 1.2 // deeper pullbacks keep RSI out of overbought
		}
		cs = append(cs, v)
	}
	// Force the last three to rise gently (don't spike RSI past 70).
	cs = append(cs, v+0.5, v+1.0, v+1.5)
	return cs
}

func TestMomentum_Long(t *testing.T) {
	sig := Momentum{}.Analyze("BTCUSDT", candlesFromCloses(risingWithPullbacks(30)...))
	if sig.Direction != Long {
		t.Fatalf("momentum on a healthy uptrend = %v (%s); want Long", sig.Direction, sig.Reason)
	}
	if sig.Confidence <= 0 {
		t.Errorf("confidence = %.2f; want > 0", sig.Confidence)
	}
}

func TestMeanReversion_OversoldLong(t *testing.T) {
	// Strictly falling → RSI 0 (<30) and price well below MA20 → fade up.
	closes := make([]float64, 25)
	for i := range closes {
		closes[i] = 200 - float64(i)*3
	}
	sig := MeanReversion{}.Analyze("ETHUSDT", candlesFromCloses(closes...))
	if sig.Direction != Long {
		t.Fatalf("mean-reversion on oversold = %v (%s); want Long", sig.Direction, sig.Reason)
	}
}

func TestBreakout_Long(t *testing.T) {
	ks := make([]binance.Kline, 0, 11)
	for range 10 {
		ks = append(ks, binance.Kline{Close: 100, High: 101, Low: 99, Volume: 100})
	}
	// Final candle breaks the 101 range high on 3× volume.
	ks = append(ks, binance.Kline{Close: 103, High: 103.5, Low: 100, Volume: 300})

	sig := Breakout{}.Analyze("SOLUSDT", ks)
	if sig.Direction != Long {
		t.Fatalf("breakout = %v (%s); want Long", sig.Direction, sig.Reason)
	}
}

func TestEMACross(t *testing.T) {
	ramp := func(start, step float64, n int) []float64 {
		cs := make([]float64, n)
		for i := range cs {
			cs[i] = start + float64(i)*step
		}
		return cs
	}

	// Steadily rising → EMA9 > EMA21 and last close > EMA50 → Long.
	if sig := (EMACross{}).Analyze("BTCUSDT", candlesFromCloses(ramp(100, 1, 60)...)); sig.Direction != Long {
		t.Errorf("rising series → %v (%s); want Long", sig.Direction, sig.Reason)
	}
	// Steadily falling → inverse → Short.
	if sig := (EMACross{}).Analyze("BTCUSDT", candlesFromCloses(ramp(200, -1, 60)...)); sig.Direction != Short {
		t.Errorf("falling series → %v (%s); want Short", sig.Direction, sig.Reason)
	}
	// Flat → all EMAs coincide → Neutral.
	if sig := (EMACross{}).Analyze("BTCUSDT", candlesFromCloses(repeat(100, 60)...)); sig.Direction != Neutral {
		t.Errorf("flat series → %v (%s); want Neutral", sig.Direction, sig.Reason)
	}
	// Too few candles for EMA50 → Neutral with a clear reason.
	if sig := (EMACross{}).Analyze("BTCUSDT", candlesFromCloses(ramp(100, 1, 30)...)); sig.Direction != Neutral {
		t.Errorf("short series → %v; want Neutral", sig.Direction)
	}
}

func TestBollinger_MeanReversionLong(t *testing.T) {
	// 20 flat closes at 100 then a sharp drop to 95: the last close gaps below
	// the lower band and RSI collapses (<35) → mean-reversion LONG, TP = MA20.
	closes := append(repeat(100, 20), 95)
	sig := Bollinger{}.Analyze("BTCUSDT", candlesFromCloses(closes...))
	if sig.Direction != Long {
		t.Fatalf("bollinger on a band-tag oversold = %v (%s); want Long", sig.Direction, sig.Reason)
	}
	if sig.TakeProfit == 0 {
		t.Errorf("mean-reversion LONG should carry a TP (the mean), got 0")
	}
	if sig.Invalidation == 0 {
		t.Errorf("mean-reversion LONG should carry an invalidation (the lower band), got 0")
	}
}

func TestBollinger_FlatIsNeutral(t *testing.T) {
	// A perfectly flat series sits inside the bands with no band-walk → Neutral.
	sig := Bollinger{}.Analyze("BTCUSDT", candlesFromCloses(repeat(100, 30)...))
	if sig.Direction != Neutral {
		t.Errorf("flat series → %v (%s); want Neutral", sig.Direction, sig.Reason)
	}
}

func TestBollinger_ShortSeriesNeutral(t *testing.T) {
	if sig := (Bollinger{}).Analyze("BTCUSDT", candlesFromCloses(repeat(100, 10)...)); sig.Direction != Neutral {
		t.Errorf("short series → %v; want Neutral", sig.Direction)
	}
}

func TestRegistry_AppliesCalibratedConfidence(t *testing.T) {
	// PRD-015: a calibrated base REPLACES the hardcoded 0.6, with ADX boost
	// added on top — so a momentum Long here reads ≥0.9, not 0.6.
	r := DefaultRegistry()
	r.SetCalibration(map[string]float64{"momentum": 0.9})
	sigs := r.AnalyzeAll("BTCUSDT", candlesFromCloses(risingWithPullbacks(30)...))

	found := false
	for _, s := range sigs {
		if s.Strategy != "momentum" {
			continue
		}
		found = true
		if s.Direction != Long {
			t.Fatalf("momentum should be Long on an uptrend, got %v (%s)", s.Direction, s.Reason)
		}
		if s.Confidence < 0.9 {
			t.Errorf("confidence = %.2f; want ≥0.9 (calibrated 0.9 + ADX), not the hardcoded 0.6", s.Confidence)
		}
	}
	if !found {
		t.Fatal("no momentum signal produced")
	}
}

func TestAnalyzeAll_ExcludesZeroCalibratedStrategy(t *testing.T) {
	// PRD-016 R6: a strategy calibrated to 0 on a symbol is auto-disabled —
	// absent from the signal list entirely.
	r := DefaultRegistry()
	r.SetCalibration(map[string]float64{"momentum": 0})
	sigs := r.AnalyzeAll("BTCUSDT", candlesFromCloses(risingWithPullbacks(30)...))
	for _, s := range sigs {
		if s.Strategy == "momentum" {
			t.Fatal("momentum calibrated to 0 should be absent from the signal list")
		}
	}
	if len(sigs) != 4 { // 5 default strategies minus the disabled momentum
		t.Errorf("got %d signals; want 4 (momentum disabled)", len(sigs))
	}
}

func TestAggregate_IgnoresZeroConfidence(t *testing.T) {
	// PRD-015: a directional signal calibrated to 0 (no historical edge) abstains.
	sigs := []Signal{
		{Direction: Long, Confidence: 0, Strategy: "a"},
		{Direction: Long, Confidence: 0, Strategy: "b"},
		{Direction: Long, Confidence: 0.6, Strategy: "c"},
	}
	if c := Aggregate("X", sigs); c.Direction != Neutral {
		t.Errorf("two zero-confidence longs + one real long should not form consensus, got %v", c.Direction)
	}
}

func TestDefaultRegistry_HasFiveStrategies(t *testing.T) {
	// PRD-013: momentum, breakout, mean-reversion, ema_cross. PRD-020 §7: bollinger.
	sigs := DefaultRegistry().AnalyzeAll("BTCUSDT", candlesFromCloses(repeat(100, 60)...))
	if len(sigs) != 5 {
		t.Fatalf("DefaultRegistry produced %d signals; want 5", len(sigs))
	}
	want := map[string]bool{"momentum": true, "breakout": true, "mean_reversion": true, "ema_cross": true, "bollinger": true}
	for _, s := range sigs {
		delete(want, s.Strategy)
	}
	if len(want) != 0 {
		t.Errorf("missing strategies in DefaultRegistry: %v", want)
	}
}

func TestAggregate_Rules(t *testing.T) {
	long := func(s string) Signal { return Signal{Direction: Long, Confidence: 0.6, Strategy: s} }
	short := func(s string) Signal { return Signal{Direction: Short, Confidence: 0.6, Strategy: s} }
	neutral := func(s string) Signal { return Signal{Direction: Neutral, Strategy: s} }

	// ≥2 Long, no Short → Long.
	if c := Aggregate("X", []Signal{long("a"), long("b"), neutral("c")}); c.Direction != Long {
		t.Errorf("2 long → %v; want Long", c.Direction)
	}
	// Conflict → Neutral.
	if c := Aggregate("X", []Signal{long("a"), short("b"), neutral("c")}); c.Direction != Neutral {
		t.Errorf("long+short → %v; want Neutral", c.Direction)
	}
	// Lone directional → Neutral (need ≥2).
	if c := Aggregate("X", []Signal{long("a"), neutral("b"), neutral("c")}); c.Direction != Neutral {
		t.Errorf("lone long → %v; want Neutral", c.Direction)
	}
	// All neutral → Neutral.
	if c := Aggregate("X", []Signal{neutral("a"), neutral("b")}); c.Direction != Neutral {
		t.Errorf("all neutral → %v; want Neutral", c.Direction)
	}
}

func TestAggregateMTF(t *testing.T) {
	long := func(c float64) Consensus { return Consensus{Direction: Long, Confidence: c} }
	short := func(c float64) Consensus { return Consensus{Direction: Short, Confidence: c} }
	neutral := Consensus{Direction: Neutral}

	// All three LONG → LONG with confidence > 0.
	if c := AggregateMTF(map[string]Consensus{"5m": long(0.6), "1h": long(0.6), "4h": long(0.6)}); c.Direction != Long || c.Confidence <= 0 {
		t.Errorf("all-LONG → %v %.2f; want LONG >0", c.Direction, c.Confidence)
	}

	// 5m LONG 0.7 (+0.7), 1h NEUTRAL (0), 4h SHORT 0.6 (−1.2) → net −0.5 → SHORT.
	// The weighted result follows the 4h, so the veto does NOT fire (PRD-022 R8).
	c := AggregateMTF(map[string]Consensus{"5m": long(0.7), "1h": neutral, "4h": short(0.6)})
	if c.Direction != Short {
		t.Errorf("5m LONG / 4h SHORT → %v (%s); want SHORT (4h dominates)", c.Direction, c.Summary)
	}
	// Summary carries every TF's contribution in canonical order (the tool prints
	// it verbatim as the "MTF Strategy:" line).
	for _, want := range []string{"5m:LONG 0.70", "1h:NEUTRAL", "4h:SHORT 0.60", "weighted SHORT"} {
		if !strings.Contains(c.Summary, want) {
			t.Errorf("MTF summary %q missing %q", c.Summary, want)
		}
	}

	// Single TF present → that TF's consensus, unchanged (graceful degrade).
	only := AggregateMTF(map[string]Consensus{"4h": long(0.71)})
	if only.Direction != Long || only.Confidence != 0.71 {
		t.Errorf("single-TF → %v %.2f; want the 4h consensus LONG 0.71", only.Direction, only.Confidence)
	}

	// Empty → NEUTRAL, no panic.
	if e := AggregateMTF(map[string]Consensus{}); e.Direction != Neutral {
		t.Errorf("empty MTF → %v; want NEUTRAL", e.Direction)
	}

	// Hysteresis: equal-and-opposite small votes net to ~0 → NEUTRAL.
	if h := AggregateMTF(map[string]Consensus{"5m": long(0.15), "1h": short(0.10)}); h.Direction != Neutral {
		// 0.15×1.0 − 0.10×1.5 = 0.15 − 0.15 = 0 → within ±0.1 band.
		t.Errorf("net-zero MTF → %v; want NEUTRAL (hysteresis)", h.Direction)
	}
}

func TestDivergence(t *testing.T) {
	flat := candlesFromCloses(repeat(100, 12)...)
	moverUp := candlesFromCloses(appendRamp(100, 12, +1.5)...) // ~+18% over window
	data := map[string][]binance.Kline{
		"BTCUSDT": flat,
		"SOLUSDT": moverUp,
	}
	sigs := DivergenceSignals(data, "BTCUSDT", 2.0, 0.5)
	if len(sigs) != 1 || sigs[0].Symbol != "SOLUSDT" || sigs[0].Direction != Long {
		t.Fatalf("expected a SOLUSDT Long divergence signal, got %+v", sigs)
	}
}

func TestFormatSummary_IncludesInvalidation(t *testing.T) {
	// PRD-018: a LONG consensus from momentum + breakout renders both
	// strategies' invalidation levels in the summary.
	c := Aggregate("BTCUSDT", []Signal{
		{Strategy: "momentum", Direction: Long, Confidence: 0.62, Invalidation: 95234.12},
		{Strategy: "breakout", Direction: Long, Confidence: 0.74, Invalidation: 96100.50},
	})
	out := FormatSummary("base.", c)
	for _, want := range []string{"momentum(62% inval=95234.12)", "breakout(74% inval=96100.50)"} {
		if !strings.Contains(out, want) {
			t.Errorf("FormatSummary %q missing %q", out, want)
		}
	}
}

func TestNames_OmitsZeroInvalidation(t *testing.T) {
	// PRD-018 AC: Invalidation = 0 (n/a) is omitted, not printed as inval=0.00.
	c := Aggregate("X", []Signal{
		{Strategy: "a", Direction: Long, Confidence: 0.6},                      // no invalidation
		{Strategy: "b", Direction: Long, Confidence: 0.6, Invalidation: 100.0}, // has one
	})
	out := FormatSummary("base.", c)
	if strings.Contains(out, "inval=0.00") {
		t.Errorf("zero invalidation should be omitted: %q", out)
	}
	if !strings.Contains(out, "a(60%)") || !strings.Contains(out, "b(60% inval=100.00)") {
		t.Errorf("unexpected rendering: %q", out)
	}
}

func TestConsensus_Invalidation_ClosestToEntry(t *testing.T) {
	// LONG: closest-to-entry = the HIGHEST invalidation (tightest stop); a
	// disagreeing or n/a signal is ignored.
	long := Consensus{Direction: Long, Signals: []Signal{
		{Direction: Long, Invalidation: 95000},
		{Direction: Long, Invalidation: 96000},
		{Direction: Short, Invalidation: 99000},
		{Direction: Long, Invalidation: 0},
	}}
	if got := long.Invalidation(); got != 96000 {
		t.Errorf("LONG closest invalidation = %.0f; want 96000", got)
	}
	// SHORT: closest = the LOWEST invalidation.
	short := Consensus{Direction: Short, Signals: []Signal{
		{Direction: Short, Invalidation: 105000},
		{Direction: Short, Invalidation: 104000},
	}}
	if got := short.Invalidation(); got != 104000 {
		t.Errorf("SHORT closest invalidation = %.0f; want 104000", got)
	}
	if n := (Consensus{Direction: Neutral}).Invalidation(); n != 0 {
		t.Errorf("neutral invalidation = %.0f; want 0", n)
	}
}

func TestFormatSummary(t *testing.T) {
	c := Consensus{Direction: Long, Confidence: 0.65, Summary: "LONG consensus from momentum + breakout."}
	got := FormatSummary("Current close 100.", c)
	if !strings.Contains(got, "LONG") || !strings.Contains(got, "65%") {
		t.Errorf("FormatSummary = %q; want LONG + 65%%", got)
	}
	n := FormatSummary("base.", Consensus{Direction: Neutral, Summary: "no strategy fired — no edge."})
	if !strings.Contains(n, "no clear edge") {
		t.Errorf("neutral summary = %q; want 'no clear edge'", n)
	}
}

func repeat(v float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func appendRamp(start float64, n int, step float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = start + float64(i)*step
	}
	return out
}
