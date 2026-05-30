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
