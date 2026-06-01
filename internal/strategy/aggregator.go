package strategy

import (
	"fmt"
	"math"
	"strings"
)

// Aggregate combines N single-symbol signals into a consensus.
//
// Rules (per the P0 plan):
//   - ≥2 Long signals and no Short  → Long, confidence = avg(long confidences)
//   - ≥2 Short signals and no Long  → Short, confidence = avg(short confidences)
//   - mixed (both a Long and a Short) → Neutral (conflict)
//   - otherwise (all neutral, or a lone directional signal) → Neutral (no edge)
func Aggregate(symbol string, signals []Signal) Consensus {
	var longs, shorts []Signal
	for _, s := range signals {
		// A zero-confidence directional signal abstains: calibration (PRD-015)
		// sets confidence to 0 for a strategy with no historical edge (≤50% win
		// rate) on this symbol, and such a vote should not sway consensus.
		if s.Confidence <= 0 {
			continue
		}
		switch s.Direction {
		case Long:
			longs = append(longs, s)
		case Short:
			shorts = append(shorts, s)
		}
	}

	c := Consensus{Symbol: symbol, Direction: Neutral, Signals: signals}

	switch {
	case len(longs) >= 2 && len(shorts) == 0:
		c.Direction = Long
		c.Confidence = avgConfidence(longs)
	case len(shorts) >= 2 && len(longs) == 0:
		c.Direction = Short
		c.Confidence = avgConfidence(shorts)
	}

	c.Summary = summarise(c, longs, shorts)
	return c
}

func avgConfidence(sigs []Signal) float64 {
	if len(sigs) == 0 {
		return 0
	}
	var sum float64
	for _, s := range sigs {
		sum += s.Confidence
	}
	return sum / float64(len(sigs))
}

func summarise(c Consensus, longs, shorts []Signal) string {
	fired := firedNames(c.Signals)
	switch c.Direction {
	case Long:
		return fmt.Sprintf("%s consensus from %s.", c.Direction, strings.Join(names(longs), " + "))
	case Short:
		return fmt.Sprintf("%s consensus from %s.", c.Direction, strings.Join(names(shorts), " + "))
	default:
		if len(longs) > 0 && len(shorts) > 0 {
			return fmt.Sprintf("conflict (%s vs %s) — no consensus, treat as no edge.",
				strings.Join(names(longs), "/"), strings.Join(names(shorts), "/"))
		}
		if len(fired) > 0 {
			return fmt.Sprintf("no consensus — only %s fired (need ≥2 aligned).", strings.Join(fired, ", "))
		}
		return "no strategy fired — no edge."
	}
}

func names(sigs []Signal) []string {
	out := make([]string, 0, len(sigs))
	for _, s := range sigs {
		// PRD-018: surface each strategy's invalidation level (the price at which
		// its thesis is void) so the Risk Manager can use it as the stop. Omitted
		// when 0 (n/a) rather than printed as inval=0.00.
		// PRD-020 §6: also surface the strategy-specific take-profit (tp=…) when
		// non-zero, so the Risk Manager can use it as the tier-1 TP target.
		extra := ""
		if s.Invalidation != 0 {
			extra += fmt.Sprintf(" inval=%.2f", s.Invalidation)
		}
		if s.TakeProfit != 0 {
			extra += fmt.Sprintf(" tp=%.2f", s.TakeProfit)
		}
		out = append(out, fmt.Sprintf("%s(%.0f%%%s)", s.Strategy, s.Confidence*100, extra))
	}
	return out
}

func firedNames(sigs []Signal) []string {
	var out []string
	for _, s := range sigs {
		if s.Direction != Neutral {
			out = append(out, fmt.Sprintf("%s→%s", s.Strategy, s.Direction))
		}
	}
	return out
}

// mtfTFWeights are the cross-timeframe vote weights (PRD-017): a higher
// timeframe is structurally more important, so it outweighs a lower one — but
// not enough to silence it. mtfTFOrder fixes a deterministic display/iteration
// order over the (unordered) input map.
var (
	mtfTFWeights = map[string]float64{"5m": 1.0, "1h": 1.5, "4h": 2.0}
	mtfTFOrder   = []string{"5m", "1h", "4h"}
)

// AggregateMTF combines per-timeframe consensuses into one cross-timeframe vote
// (PRD-017, tuned by PRD-022). Each TF is first passed through the RSI
// extreme-zone filter (using that TF's own RSI), then contributes
// `±Confidence × tfWeight` (+ for LONG, − for SHORT, 0 for NEUTRAL); the net
// decides direction (within a ±hysteresis dead-band, default 0.05) and
// `abs(net) / Σweights` the confidence. Two PRD-022 refinements then apply:
//
//   - 5m+1h override: when the 4h is NEUTRAL (or absent) and the 5m and 1h agree
//     with confidence ≥0.5 each, adopt their shared direction at the average of
//     their confidences — so aligned lower-timeframe signals aren't drowned out
//     by a perpetually-NEUTRAL 4h.
//   - 4h hard veto: when the 4h is directional and OPPOSES the weighted result,
//     force NEUTRAL — never trade a lower-TF setup against the 4h trend.
//
// With a single TF present it degrades to that TF's (RSI-filtered) consensus.
func AggregateMTF(consensusByTF map[string]Consensus) Consensus {
	// PRD-022 R5: filter each TF by its OWN RSI before it can vote.
	filtered := make(map[string]Consensus, len(consensusByTF))
	for tf, c := range consensusByTF {
		filtered[tf] = RSIFilter(c, c.RSI)
	}

	present := make([]string, 0, len(mtfTFOrder))
	for _, tf := range mtfTFOrder {
		if _, ok := filtered[tf]; ok {
			present = append(present, tf)
		}
	}

	switch len(present) {
	case 0:
		return Consensus{Direction: Neutral, Summary: "no timeframe data for an MTF vote."}
	case 1:
		return filtered[present[0]] // graceful degrade: one TF → its own (filtered) consensus
	}

	var net, sumW float64
	parts := make([]string, 0, len(present))
	for _, tf := range present {
		c := filtered[tf]
		w := mtfTFWeights[tf]
		if w == 0 {
			w = 1.0
		}
		sumW += w
		switch c.Direction {
		case Long:
			net += c.Confidence * w
		case Short:
			net -= c.Confidence * w
		}
		parts = append(parts, mtfPart(tf, c))
	}

	hyst := mtfHysteresisValue()
	out := Consensus{Symbol: filtered[present[0]].Symbol}
	switch {
	case net > hyst:
		out.Direction = Long
	case net < -hyst:
		out.Direction = Short
	default:
		out.Direction = Neutral
	}
	out.Confidence = clamp01(math.Abs(net) / sumW)

	note := ""
	four, has4h := filtered["4h"]
	fourNeutral := !has4h || four.Direction == Neutral

	switch {
	case mtf5m1hOverrideEnabled() && fourNeutral && lowerTFOverride(filtered) != Neutral:
		// PRD-022 R8: aligned 5m+1h with a non-opposing 4h → adopt their direction
		// at the average of their confidences.
		five, oneH := filtered["5m"], filtered["1h"]
		out.Direction = five.Direction
		out.Confidence = clamp01((five.Confidence + oneH.Confidence) / 2)
		note = " [5m+1h override]"
	case has4h && four.Direction != Neutral && out.Direction != Neutral && four.Direction != out.Direction:
		// PRD-022 R8: 4h opposition is a hard veto — never trade against the 4h.
		out.Direction = Neutral
		out.Confidence = 0
		note = " [4h veto]"
	}

	out.Summary = fmt.Sprintf("(%s) → weighted %s %.2f%s", strings.Join(parts, " + "), out.Direction, out.Confidence, note)
	return out
}

// lowerTFOverride returns the shared 5m+1h direction when both are present,
// agree on a non-NEUTRAL direction, and each has confidence ≥0.5 — else Neutral
// (no override). The 4h-neutrality precondition is checked by the caller.
func lowerTFOverride(filtered map[string]Consensus) Direction {
	five, ok5 := filtered["5m"]
	oneH, ok1 := filtered["1h"]
	if !ok5 || !ok1 {
		return Neutral
	}
	if five.Direction == Neutral || five.Direction != oneH.Direction {
		return Neutral
	}
	if five.Confidence < 0.5 || oneH.Confidence < 0.5 {
		return Neutral
	}
	return five.Direction
}

// mtfPart renders one timeframe's contribution: "4h:LONG 0.71 inval=96100.50
// tp=98500.00", "4h:LONG 0.71" (no levels), or "1h:NEUTRAL".
func mtfPart(tf string, c Consensus) string {
	if c.Direction == Neutral {
		return tf + ":NEUTRAL"
	}
	out := fmt.Sprintf("%s:%s %.2f", tf, c.Direction, c.Confidence)
	if inval := c.Invalidation(); inval != 0 {
		out += fmt.Sprintf(" inval=%.2f", inval)
	}
	if tp := c.TakeProfit(); tp != 0 {
		out += fmt.Sprintf(" tp=%.2f", tp)
	}
	return out
}

// Invalidation returns the consensus's effective stop level (PRD-018): the
// CLOSEST-to-entry invalidation among the signals that agree with the consensus
// direction — the most conservative (tightest) stop, so the position exits as
// soon as any contributing strategy's thesis breaks. For LONG that is the
// highest invalidation (nearest below price); for SHORT the lowest (nearest
// above). 0 when no agreeing signal carries one, or the consensus is NEUTRAL.
func (c Consensus) Invalidation() float64 {
	best := 0.0
	for _, s := range c.Signals {
		if s.Direction != c.Direction || s.Invalidation == 0 {
			continue
		}
		switch {
		case best == 0:
			best = s.Invalidation
		case c.Direction == Long && s.Invalidation > best:
			best = s.Invalidation
		case c.Direction == Short && s.Invalidation < best:
			best = s.Invalidation
		}
	}
	return best
}

// TakeProfit returns the consensus's effective take-profit target (PRD-020 §6):
// the CLOSEST-to-entry take-profit among the signals that agree with the
// consensus direction — the most conservative (nearest) target, so a winner is
// banked rather than held for the most optimistic strategy. For LONG that is the
// lowest TP (nearest above price); for SHORT the highest (nearest below). 0 when
// no agreeing signal carries one, or the consensus is NEUTRAL.
func (c Consensus) TakeProfit() float64 {
	best := 0.0
	for _, s := range c.Signals {
		if s.Direction != c.Direction || s.TakeProfit == 0 {
			continue
		}
		switch {
		case best == 0:
			best = s.TakeProfit
		case c.Direction == Long && s.TakeProfit < best:
			best = s.TakeProfit
		case c.Direction == Short && s.TakeProfit > best:
			best = s.TakeProfit
		}
	}
	return best
}

// FormatSummary appends the strategy consensus to a klines Summary line, in
// the form the Analyst reads.
func FormatSummary(base string, c Consensus) string {
	if c.Direction == Neutral {
		return fmt.Sprintf("%s Strategy signals: no clear edge (%s)", base, c.Summary)
	}
	return fmt.Sprintf("%s Strategy signals: %s (confidence %.0f%%) — %s",
		base, c.Direction, c.Confidence*100, c.Summary)
}
