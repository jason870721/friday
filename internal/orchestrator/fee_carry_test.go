package orchestrator

import (
	"strings"
	"testing"
	"time"

	"github.com/johnny1110/friday/internal/risk"
)

func TestCarryWithFeeWarning_AppendsAndRefreshes(t *testing.T) {
	fb := risk.NewFeeBudget(30*time.Minute, 0.01) // cap = 1% of balance
	fb.Record(6, 1000)                            // $6 spent vs $10 cap → 60% ≥ 50% → near
	o := &Orchestrator{feeBudget: fb}

	c1 := o.carryWithFeeWarning("BTC: FLAT")
	if !strings.Contains(c1, feeWarningMarker) {
		t.Fatalf("near-cap carry should include the fee warning: %q", c1)
	}
	if !strings.Contains(c1, "BTC: FLAT") {
		t.Errorf("base carry was dropped: %q", c1)
	}
	// PRD-023 R3: re-applying each round must REFRESH, not accumulate.
	c2 := o.carryWithFeeWarning(c1)
	if n := strings.Count(c2, feeWarningMarker); n != 1 {
		t.Errorf("fee warning appears %d times; want 1 (no per-round growth): %q", n, c2)
	}
}

func TestCarryWithFeeWarning_BelowThresholdAndNil(t *testing.T) {
	fb := risk.NewFeeBudget(30*time.Minute, 0.01)
	fb.Record(1, 1000) // 10% < 50% → not near
	o := &Orchestrator{feeBudget: fb}
	if c := o.carryWithFeeWarning("state"); strings.Contains(c, feeWarningMarker) {
		t.Errorf("below 50%% of cap should not warn: %q", c)
	}

	// A prior warning in the carry is stripped once spend drops back below 50%.
	stale := "state\n" + feeWarningMarker + " old warning"
	if c := o.carryWithFeeWarning(stale); strings.Contains(c, feeWarningMarker) {
		t.Errorf("stale warning should be stripped when no longer near: %q", c)
	}

	// nil fee budget → carry passes through untouched.
	o2 := &Orchestrator{}
	if got := o2.carryWithFeeWarning("x"); got != "x" {
		t.Errorf("nil feeBudget passthrough = %q; want x", got)
	}
}
