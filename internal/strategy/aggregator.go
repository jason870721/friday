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
		if s.Invalidation != 0 {
			out = append(out, fmt.Sprintf("%s(%.0f%% inval=%.2f)", s.Strategy, s.Confidence*100, s.Invalidation))
		} else {
			out = append(out, fmt.Sprintf("%s(%.0f%%)", s.Strategy, s.Confidence*100))
		}
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

// mtfHysteresis is the dead-band around 0 in which the weighted net score is
// read as NEUTRAL, so the MTF direction doesn't flip on tiny round-to-round
// changes.
const mtfHysteresis = 0.1

// AggregateMTF combines per-timeframe consensuses into one cross-timeframe vote
// (PRD-017). Each TF contributes `±Confidence × tfWeight` (+ for LONG, − for
// SHORT, 0 for NEUTRAL); the net decides direction (with a ±0.1 hysteresis
// band) and `abs(net) / Σweights` the confidence. Higher timeframes dominate on
// conflict but a lower TF still moves the final confidence. With a single TF
// present it degrades to that TF's consensus unchanged.
func AggregateMTF(consensusByTF map[string]Consensus) Consensus {
	present := make([]string, 0, len(mtfTFOrder))
	for _, tf := range mtfTFOrder {
		if _, ok := consensusByTF[tf]; ok {
			present = append(present, tf)
		}
	}

	switch len(present) {
	case 0:
		return Consensus{Direction: Neutral, Summary: "no timeframe data for an MTF vote."}
	case 1:
		return consensusByTF[present[0]] // graceful degrade: one TF → its own consensus
	}

	var net, sumW float64
	parts := make([]string, 0, len(present))
	for _, tf := range present {
		c := consensusByTF[tf]
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

	out := Consensus{Symbol: consensusByTF[present[0]].Symbol}
	switch {
	case net > mtfHysteresis:
		out.Direction = Long
	case net < -mtfHysteresis:
		out.Direction = Short
	default:
		out.Direction = Neutral
	}
	out.Confidence = clamp01(math.Abs(net) / sumW)
	out.Summary = fmt.Sprintf("(%s) → weighted %s %.2f", strings.Join(parts, " + "), out.Direction, out.Confidence)
	return out
}

// mtfPart renders one timeframe's contribution: "4h:LONG 0.71 inval=96100.50",
// "4h:LONG 0.71" (no invalidation), or "1h:NEUTRAL".
func mtfPart(tf string, c Consensus) string {
	if c.Direction == Neutral {
		return tf + ":NEUTRAL"
	}
	if inval := c.Invalidation(); inval != 0 {
		return fmt.Sprintf("%s:%s %.2f inval=%.2f", tf, c.Direction, c.Confidence, inval)
	}
	return fmt.Sprintf("%s:%s %.2f", tf, c.Direction, c.Confidence)
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

// FormatSummary appends the strategy consensus to a klines Summary line, in
// the form the Analyst reads.
func FormatSummary(base string, c Consensus) string {
	if c.Direction == Neutral {
		return fmt.Sprintf("%s Strategy signals: no clear edge (%s)", base, c.Summary)
	}
	return fmt.Sprintf("%s Strategy signals: %s (confidence %.0f%%) — %s",
		base, c.Direction, c.Confidence*100, c.Summary)
}
