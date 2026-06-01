package risk

import (
	"testing"
	"time"
)

func TestFeeBudget_SumsWithinWindowAndExpires(t *testing.T) {
	fb := NewFeeBudget(30*time.Minute, 0.005)
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	now := base
	fb.now = func() time.Time { return now }

	fb.Record(2, 1000) // t=0
	now = base.Add(10 * time.Minute)
	fb.Record(2, 1000) // t=10m

	// Both inside the 30m window → $4 spent.
	if sum, _ := fb.windowedFee(now); sum != 4 {
		t.Errorf("windowed fee = %v; want 4", sum)
	}

	// Advance past the window so the first entry expires.
	now = base.Add(31 * time.Minute)
	if sum, _ := fb.windowedFee(now); sum != 2 {
		t.Errorf("after expiry, windowed fee = %v; want 2 (first entry rolled off)", sum)
	}
}

func TestFeeBudget_CheckErrorsAboveThreshold(t *testing.T) {
	fb := NewFeeBudget(30*time.Minute, 0.005) // 0.5% of 1000 = $5 cap
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	fb.now = func() time.Time { return now }

	fb.Record(4, 1000)
	if err := fb.Check(1000); err != nil {
		t.Errorf("under cap ($4 < $5) should pass, got %v", err)
	}
	fb.Record(2, 1000) // now $6 > $5
	if err := fb.Check(1000); err == nil {
		t.Error("over cap ($6 > $5) should error")
	}
}

func TestFeeBudget_RecordIgnoresNonPositiveAndIndeterminateBalance(t *testing.T) {
	fb := NewFeeBudget(0, 0) // defaults
	fb.Record(-3, 1000)      // funding received exceeded commission → not spend
	if sum, _ := fb.windowedFee(time.Now()); sum != 0 {
		t.Errorf("non-positive fee should record nothing, got %v", sum)
	}
	if err := fb.Check(0); err != nil {
		t.Errorf("indeterminate balance should pass, got %v", err)
	}
}
