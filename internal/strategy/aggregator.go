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
	c.SignalDetails = signalDetails(c, longs, shorts)
	return c
}

// signalDetails renders a one-line, per-strategy diagnostic of WHY the consensus
// turned out the way it did (PRD-024 R11): each confident directional signal as
// "momentum LONG(0.55) inval=63250", each abstaining one as "ema_cross: <its
// reason>", joined by " | ". For a NEUTRAL consensus the resolving reason is
// appended ("— only 1 directional (need ≥2)" / "— conflict (…)"). Empty when
// there were no signals at all.
func signalDetails(c Consensus, longs, shorts []Signal) string {
	if len(c.Signals) == 0 {
		return ""
	}
	parts := make([]string, 0, len(c.Signals))
	for _, s := range c.Signals {
		if s.Direction != Neutral && s.Confidence > 0 {
			seg := fmt.Sprintf("%s %s(%.2f)", s.Strategy, s.Direction, s.Confidence)
			if s.Invalidation != 0 {
				seg += fmt.Sprintf(" inval=%.2f", s.Invalidation)
			}
			if s.TakeProfit != 0 {
				seg += fmt.Sprintf(" tp=%.2f", s.TakeProfit)
			}
			parts = append(parts, seg)
			continue
		}
		reason := s.Reason
		if reason == "" {
			reason = "no signal"
		}
		parts = append(parts, fmt.Sprintf("%s: %s", s.Strategy, reason))
	}
	detail := strings.Join(parts, " | ")
	if r := neutralReason(c, longs, shorts); r != "" {
		detail += " — " + r
	}
	return detail
}

// neutralReason explains a NEUTRAL consensus in a few words (PRD-024 R11); ""
// for a directional consensus (the per-strategy parts already say enough).
func neutralReason(c Consensus, longs, shorts []Signal) string {
	if c.Direction != Neutral {
		return ""
	}
	switch n := len(longs) + len(shorts); {
	case len(longs) > 0 && len(shorts) > 0:
		return fmt.Sprintf("conflict (%s vs %s)", strings.Join(names(longs), "/"), strings.Join(names(shorts), "/"))
	case n == 0:
		return "no strategy fired"
	case n == 1:
		return "only 1 directional (need ≥2)"
	default:
		return fmt.Sprintf("only %d directional (need ≥2 aligned)", n)
	}
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

// mtfTFWeights are the cross-timeframe vote weights. 5m leads because the
// strategies (momentum/breakout/mean-reversion/ema_cross/bollinger) all
// perform best on short timeframes (backtest: 5m 62% WR / 1h 46%).
var (
	mtfTFWeights = map[string]float64{"5m": 2.0, "1h": 1.0, "4h": 0.5}
	mtfTFOrder   = []string{"5m", "1h", "4h"}
)

// AggregateMTF combines per-timeframe consensuses into one cross-timeframe vote
// (PRD-017, tuned by PRD-022/024). Each TF is first passed through the RSI
// extreme-zone filter (using that TF's own RSI), then contributes
// `±Confidence × tfWeight` (+ for LONG, − for SHORT, 0 for NEUTRAL); the net
// decides direction (within a ±hysteresis dead-band, default 0.05) and
// `abs(net) / Σweights` the confidence. Three refinements then apply, in order
// (PRD-024 R5: quorum → override → the weighted net is the fallback):
//
//   - 2-of-3 quorum (PRD-024 R4, FRIDAY_MTF_QUORUM, default on): when the 4h is
//     NEUTRAL/absent, any 2 timeframes sharing a direction adopt it at their
//     average confidence; a lower-TF directional conflict resolves to NEUTRAL; a
//     lone directional TF falls through so a single 5m signal can still trade.
//   - 5m+1h override (PRD-022 R8, threshold lowered to 0.35 by PRD-024 R3): when
//     the 4h is NEUTRAL and the 5m and 1h agree with confidence ≥0.35 each, adopt
//     their shared direction at the average of their confidences.
//   - 4h hard veto (PRD-022 R8): when the 4h is directional and OPPOSES the
//     WEIGHTED result, force NEUTRAL — never trade against the 4h trend. (It keys
//     off the net, so a lone lower-TF dissent against a with-4h net does NOT veto.)
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

	// PRD-024 R5: order is quorum → 5m+1h override → weighted sum (the weighted
	// `out` above is the fallback the others refine). Quorum and override both
	// require the 4h to be silent; the 4h veto below requires it to be directional,
	// so the two are mutually exclusive.
	quorumHandled := false
	if mtfQuorumEnabled() && fourNeutral {
		// PRD-024 R4: with the 4h silent, a 2-of-3 quorum forms a directional vote
		// even when the weighted sum is too timid (the fix for the perpetually-
		// NEUTRAL 4h drowning out aligned lower TFs). A directional conflict among
		// the lower TFs is decided NEUTRAL; a lone directional TF is left to fall
		// through so a single 5m signal can still trade via the weighted path.
		if dir, conf, decided := quorumDecision(filtered, present); decided {
			out.Direction, out.Confidence = dir, conf
			if dir == Neutral {
				note = " [quorum: no majority]"
			} else {
				note = " [quorum]"
			}
			quorumHandled = true
		}
	}
	if !quorumHandled && mtf5m1hOverrideEnabled() && fourNeutral && lowerTFOverride(filtered) != Neutral {
		// PRD-022 R8 (threshold lowered to 0.35 by PRD-024 R3): aligned 5m+1h with a
		// non-opposing 4h → adopt their direction at the average of their confidences.
		five, oneH := filtered["5m"], filtered["1h"]
		out.Direction = five.Direction
		out.Confidence = clamp01((five.Confidence + oneH.Confidence) / 2)
		note = " [5m+1h override]"
	}

	// PRD-022 R8: 4h opposition is a hard veto — never trade against the 4h.
	// Check the WEIGHTED result, not individual lower TFs: a lone 5m dissent
	// is noise; what matters is whether the net vote opposes the dominant TF.
	if has4h && four.Direction != Neutral && out.Direction != Neutral && out.Direction != four.Direction {
		out.Direction = Neutral
		out.Confidence = 0
		note = " [4h veto]"
	}

	out.Summary = fmt.Sprintf("(%s) → weighted %s %.2f%s", strings.Join(parts, " + "), out.Direction, out.Confidence, note)
	// PRD-024 R13: append each present TF's per-strategy detail line beneath the
	// summary so a NEUTRAL round shows WHY (which strategies fired / conflicted /
	// were RSI-filtered). The tool prints out.Summary verbatim, so no tool change.
	for _, tf := range present {
		if d := strings.TrimSpace(filtered[tf].SignalDetails); d != "" {
			out.Summary += fmt.Sprintf("\n  %s: %s", tf, d)
		}
	}
	return out
}

// quorumDecision evaluates the 4h-silent 2-of-3 quorum (PRD-024 R4). Because the
// 4h is NEUTRAL in this branch, at most the 5m and 1h can be directional, so:
//   - 2 TFs share a direction (no opposition) → that direction, avg confidence,
//     decided=true;
//   - the directional TFs conflict (one LONG, one SHORT) → NEUTRAL, decided=true;
//   - ≤1 directional TF → decided=false, letting the caller fall through to the
//     override/weighted path (a lone 5m signal can still trade).
func quorumDecision(filtered map[string]Consensus, present []string) (Direction, float64, bool) {
	var longs, shorts []Consensus
	for _, tf := range present {
		switch filtered[tf].Direction {
		case Long:
			longs = append(longs, filtered[tf])
		case Short:
			shorts = append(shorts, filtered[tf])
		}
	}
	switch {
	case len(longs) >= 2 && len(shorts) == 0:
		return Long, avgConsensusConf(longs), true
	case len(shorts) >= 2 && len(longs) == 0:
		return Short, avgConsensusConf(shorts), true
	case len(longs) > 0 && len(shorts) > 0:
		return Neutral, 0, true // directional conflict → no clean majority
	default:
		return Neutral, 0, false // ≤1 directional → fall through
	}
}

func avgConsensusConf(cs []Consensus) float64 {
	if len(cs) == 0 {
		return 0
	}
	var sum float64
	for _, c := range cs {
		sum += c.Confidence
	}
	return clamp01(sum / float64(len(cs)))
}

// lowerTFOverrideFloor is the minimum per-TF confidence the 5m+1h override needs
// (PRD-024 R3 lowered it from 0.5 to 0.35). Calibration + regime weighting often
// pull a directionally-correct signal to 0.35–0.5, below the old arbitrary floor.
const lowerTFOverrideFloor = 0.35

// lowerTFOverride returns the shared 5m+1h direction when both are present,
// agree on a non-NEUTRAL direction, and each has confidence ≥0.35 — else Neutral
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
	if five.Confidence < lowerTFOverrideFloor || oneH.Confidence < lowerTFOverrideFloor {
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
