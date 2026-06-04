package risk

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// A restart WITHIN the trading day must keep the consecutive-loss streak — the
// live failure mode was 5 consecutive losses never tripping the pause because
// each restart reset the in-memory breaker to NORMAL.
func TestBreaker_PersistenceSurvivesSameDayRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "breaker.json")

	cb := NewCircuitBreaker(0.10, 5, 0.20, 20)
	cb.EnablePersistence(path)
	cb.Observe(1000)
	for i := 0; i < 3; i++ {
		cb.RecordTrade(-10) // 3 consecutive losses, still NORMAL (< 5)
	}
	if cb.State() != StateNormal {
		t.Fatalf("3 losses should stay NORMAL; got %v", cb.State())
	}

	// Restart: a fresh breaker pointed at the same file restores the streak.
	cb2 := NewCircuitBreaker(0.10, 5, 0.20, 20)
	cb2.EnablePersistence(path)
	cb2.RecordTrade(-10)
	cb2.RecordTrade(-10) // restored 3 + 2 = 5 → must pause
	if cb2.State() != StatePaused {
		t.Errorf("restored streak (3) + 2 losses should PAUSE; got %v", cb2.State())
	}
}

// A snapshot from an earlier day is stale: daily metrics reset, but a HALT
// latches across the boundary as a safety backstop.
func TestBreaker_PersistenceNewDayResetsButLatchesHalt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "breaker.json")

	// New-day reset: an old-dated streak does not carry over.
	writeSnapshot(t, path, persistedBreaker{Day: "2000-01-01", ConsecutiveLosses: 4, State: int(StateNormal)})
	cb := NewCircuitBreaker(0.10, 5, 0.20, 20)
	cb.EnablePersistence(path)
	cb.RecordTrade(-10) // would be the 5th if the stale 4 had carried over
	if cb.State() != StateNormal {
		t.Errorf("stale (prior-day) streak must reset; got %v", cb.State())
	}

	// HALT latches across the day boundary even though the day is stale.
	writeSnapshot(t, path, persistedBreaker{Day: "2000-01-01", State: int(StateHalted), Reason: "yesterday's drawdown"})
	cbH := NewCircuitBreaker(0.10, 5, 0.20, 20)
	cbH.EnablePersistence(path)
	if cbH.State() != StateHalted {
		t.Errorf("a prior-day HALT must latch; got %v", cbH.State())
	}
}

func writeSnapshot(t *testing.T, path string, p persistedBreaker) {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}
