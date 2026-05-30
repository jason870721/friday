package strategy

import (
	"fmt"
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
		out = append(out, fmt.Sprintf("%s(%.0f%%)", s.Strategy, s.Confidence*100))
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

// FormatSummary appends the strategy consensus to a klines Summary line, in
// the form the Analyst reads.
func FormatSummary(base string, c Consensus) string {
	if c.Direction == Neutral {
		return fmt.Sprintf("%s Strategy signals: no clear edge (%s)", base, c.Summary)
	}
	return fmt.Sprintf("%s Strategy signals: %s (confidence %.0f%%) — %s",
		base, c.Direction, c.Confidence*100, c.Summary)
}
