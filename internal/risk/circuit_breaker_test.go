package risk

import (
	"strings"
	"testing"
)

func TestBreaker_NormalAllowsEntries(t *testing.T) {
	cb := NewCircuitBreaker(0.10, 5, 0.20, 20)
	cb.Observe(10000)
	if err := cb.Check(); err != nil {
		t.Errorf("normal breaker should allow entries, got %v", err)
	}
}

func TestBreaker_ConsecutiveLossesPause(t *testing.T) {
	cb := NewCircuitBreaker(0.10, 3, 0.20, 20)
	cb.Observe(10000)
	cb.RecordTrade(-10)
	cb.RecordTrade(-10)
	if err := cb.Check(); err != nil {
		t.Fatalf("2 losses (max 3) should not pause yet, got %v", err)
	}
	cb.RecordTrade(-10) // third → pause
	err := cb.Check()
	if err == nil || !strings.Contains(err.Error(), "PAUSED") {
		t.Errorf("3 consecutive losses should PAUSE, got %v", err)
	}
}

func TestBreaker_WinResetsConsecutiveCounter(t *testing.T) {
	cb := NewCircuitBreaker(0.10, 3, 0.20, 20)
	cb.Observe(10000)
	cb.RecordTrade(-10)
	cb.RecordTrade(-10)
	cb.RecordTrade(+5) // resets counter
	cb.RecordTrade(-10)
	cb.RecordTrade(-10)
	if err := cb.Check(); err != nil {
		t.Errorf("counter should have reset after the win, got %v", err)
	}
}

func TestBreaker_DailyLossPause(t *testing.T) {
	cb := NewCircuitBreaker(0.10, 100, 0.50, 20) // high consec/drawdown to isolate daily loss
	cb.Observe(10000)
	cb.RecordTrade(-600)
	cb.RecordTrade(-500) // realized -1100 ≤ -10% of 10000
	cb.Observe(10000)    // evaluate daily-loss threshold
	err := cb.Check()
	if err == nil || !strings.Contains(err.Error(), "PAUSED") {
		t.Errorf("daily loss over 10%% should PAUSE, got %v", err)
	}
}

func TestBreaker_DrawdownHalt(t *testing.T) {
	cb := NewCircuitBreaker(0.50, 100, 0.20, 20) // high daily/consec to isolate drawdown
	cb.Observe(10000)
	cb.Observe(7900) // balance down 21% from start → halt
	err := cb.Check()
	if err == nil || !strings.Contains(err.Error(), "HALTED") {
		t.Errorf("21%% drawdown should HALT, got %v", err)
	}
	// Halt does not auto-clear via cooldown.
	for range 50 {
		cb.Tick()
	}
	if cb.State() != StateHalted {
		t.Errorf("halt should persist through cooldown, state=%v", cb.State())
	}
	cb.Reset()
	if err := cb.Check(); err != nil {
		t.Errorf("after reset, should allow entries, got %v", err)
	}
}

func TestBreaker_CooldownExpiryResumes(t *testing.T) {
	cb := NewCircuitBreaker(0.10, 2, 0.20, 3) // cooldown 3 cycles
	cb.Observe(10000)
	cb.RecordTrade(-10)
	cb.RecordTrade(-10) // pause
	if cb.State() != StatePaused {
		t.Fatalf("expected Paused, got %v", cb.State())
	}
	cb.Tick()
	cb.Tick()
	if cb.State() != StatePaused {
		t.Errorf("still in cooldown after 2/3 ticks, got %v", cb.State())
	}
	cb.Tick() // 3rd tick → resume
	if cb.State() != StateNormal {
		t.Errorf("cooldown should have expired to Normal, got %v", cb.State())
	}
	if err := cb.Check(); err != nil {
		t.Errorf("resumed breaker should allow entries, got %v", err)
	}
}

func TestBreaker_StatusReflectsState(t *testing.T) {
	cb := NewCircuitBreaker(0.10, 1, 0.20, 5)
	cb.Observe(10000)
	if !strings.Contains(cb.Status(), "NORMAL") {
		t.Errorf("status = %q; want NORMAL", cb.Status())
	}
	cb.RecordTrade(-10) // 1 loss, max 1 → pause
	if !strings.Contains(cb.Status(), "PAUSED") {
		t.Errorf("status = %q; want PAUSED", cb.Status())
	}
}
