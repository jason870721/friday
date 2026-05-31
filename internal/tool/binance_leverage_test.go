package tool

import "testing"

func TestMaxLeverageClamp(t *testing.T) {
	// Install caps, then confirm the lookup the tool uses to clamp.
	SetMaxLeverages(map[string]int{"NVDAUSDT": 10, "BTCUSDT": 125})
	defer SetMaxLeverages(map[string]int{}) // reset for other tests

	if mx, ok := maxLeverageFor("NVDAUSDT"); !ok || mx != 10 {
		t.Errorf("NVDAUSDT cap = %d,%v; want 10,true", mx, ok)
	}
	// 100x request on a 10x symbol clamps to 10.
	req, max := 100, 0
	if m, ok := maxLeverageFor("NVDAUSDT"); ok {
		max = m
	}
	got := req
	if max > 0 && got > max {
		got = max
	}
	if got != 10 {
		t.Errorf("clamped leverage = %d; want 10", got)
	}
	// Unknown symbol → no clamp data.
	if _, ok := maxLeverageFor("DOGEUSDT"); ok {
		t.Error("DOGEUSDT should be unknown")
	}
}
