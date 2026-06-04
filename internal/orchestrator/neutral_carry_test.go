package orchestrator

import (
	"strings"
	"testing"
)

// countingNotifier records how many alerts fired (PRD-024 streak-milestone test).
type countingNotifier struct{ n *int }

func (c countingNotifier) Notify(_, _ string) error { *c.n++; return nil }

// PRD-024: the streak-milestone alerts must re-arm on each NEW streak — the
// reset of consecutiveNeutral must also reset lastNeutralNotified, or a process
// that once hit 100 would never alert on a later idle period again.
func TestNotifyNeutralStreak_RearmsPerStreak(t *testing.T) {
	var fired int
	o := &Orchestrator{notifier: countingNotifier{&fired}}

	// First streak: crossing 10 then 20 fires twice.
	o.consecutiveNeutral = 10
	o.notifyNeutralStreak()
	o.consecutiveNeutral = 20
	o.notifyNeutralStreak()
	if fired != 2 {
		t.Fatalf("first streak fired %d alerts; want 2 (at 10 and 20)", fired)
	}

	// An actionable round resets both counters (as runRound does).
	o.consecutiveNeutral = 0
	o.lastNeutralNotified = 0

	// Second streak: crossing 10 again must fire — the regression this guards.
	o.consecutiveNeutral = 10
	o.notifyNeutralStreak()
	if fired != 3 {
		t.Errorf("second streak did not re-arm: fired=%d; want 3 (milestone 10 again)", fired)
	}
}

// PRD-024 R9: the consecutive-NEUTRAL streak warning is appended once the streak
// reaches neutralWarnAfter, refreshed (not accumulated) each round, and stripped
// once an actionable round resets the counter.
func TestCarryWithNeutralWarning_AppendsRefreshesStrips(t *testing.T) {
	o := &Orchestrator{}

	// Below the threshold → no warning.
	o.consecutiveNeutral = neutralWarnAfter - 1
	if c := o.carryWithNeutralWarning("BTC: FLAT"); strings.Contains(c, neutralWarningMarker) {
		t.Fatalf("streak %d (< %d) should not warn: %q", o.consecutiveNeutral, neutralWarnAfter, c)
	}

	// At the threshold → warning appended, base preserved.
	o.consecutiveNeutral = neutralWarnAfter
	c1 := o.carryWithNeutralWarning("BTC: FLAT")
	if !strings.Contains(c1, neutralWarningMarker) {
		t.Fatalf("streak %d should warn: %q", o.consecutiveNeutral, c1)
	}
	if !strings.Contains(c1, "BTC: FLAT") {
		t.Errorf("base carry was dropped: %q", c1)
	}

	// Re-applying each round refreshes, not accumulates.
	o.consecutiveNeutral = neutralWarnAfter + 5
	c2 := o.carryWithNeutralWarning(c1)
	if n := strings.Count(c2, neutralWarningMarker); n != 1 {
		t.Errorf("warning appears %d times; want 1 (no per-round growth): %q", n, c2)
	}

	// Counter reset (actionable round) → the stale warning is stripped.
	o.consecutiveNeutral = 0
	if c := o.carryWithNeutralWarning(c2); strings.Contains(c, neutralWarningMarker) {
		t.Errorf("reset streak should strip the warning: %q", c)
	}
}
