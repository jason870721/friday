package orchestrator

import (
	"strings"
	"testing"
)

func threeSymOrch() *Orchestrator {
	return &Orchestrator{symbols: []MarketSymbol{
		{Name: "BTCUSDT"}, {Name: "ETHUSDT"}, {Name: "NVDAUSDT"},
	}}
}

func TestPositionsLineAll(t *testing.T) {
	o := threeSymOrch()

	// Unknown state (query failed) → empty, so the carry stands (no false "flat").
	if got := o.positionsLineAll(nil, false); got != "" {
		t.Errorf("ok=false should yield empty line, got %q", got)
	}

	// Flat → authoritative NONE.
	got := o.positionsLineAll(map[string]string{}, true)
	if !strings.Contains(got, "NONE") || !strings.Contains(got, "FLAT") {
		t.Errorf("empty snapshot should report FLAT/NONE, got %q", got)
	}

	// Held → lists only the held symbols, in stable symbol order.
	got = o.positionsLineAll(map[string]string{"ETHUSDT": "LONG 0.5 @ 2000.0000 uPnL +1.20"}, true)
	if !strings.Contains(got, "ETHUSDT LONG 0.5 @ 2000.0000") {
		t.Errorf("should list the held position, got %q", got)
	}
	if strings.Contains(got, "BTCUSDT") || strings.Contains(got, "NVDAUSDT") {
		t.Errorf("should not list flat symbols, got %q", got)
	}
}

func TestPositionLineForSymbol(t *testing.T) {
	o := threeSymOrch()
	snap := map[string]string{"BTCUSDT": "SHORT 0.01 @ 67000.0000 uPnL -3.00"}

	// Unknown → empty (carry stands).
	if got := o.positionLineForSymbol("NVDAUSDT", snap, false); got != "" {
		t.Errorf("ok=false should yield empty line, got %q", got)
	}

	// The symptom case: NVDA is NOT in the snapshot → authoritative FLAT, so the
	// Analyst stops parroting a stale carry that still claims a holding.
	got := o.positionLineForSymbol("NVDAUSDT", snap, true)
	if !strings.Contains(got, "NVDAUSDT") || !strings.Contains(got, "FLAT") {
		t.Errorf("NVDA absent from snapshot should report FLAT, got %q", got)
	}

	// Held symbol → echoes the real line.
	got = o.positionLineForSymbol("BTCUSDT", snap, true)
	if !strings.Contains(got, "SHORT 0.01 @ 67000.0000") {
		t.Errorf("held symbol should show its real position, got %q", got)
	}
}
