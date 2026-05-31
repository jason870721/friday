package tool

import (
	"testing"

	"github.com/johnny1110/friday/internal/binance"
)

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

func TestMaxLeverageForNotional_ToolLookup(t *testing.T) {
	// PRD-019: binance_order uses this to lower leverage so a position's notional
	// fits the tier it falls into — preventing -2027.
	SetLeverageBrackets(map[string][]binance.LeverageBracket{
		"AMZNUSDT": {
			{NotionalCap: 5000, InitialLeverage: 10},
			{NotionalCap: 25000, InitialLeverage: 5},
		},
	})
	defer SetLeverageBrackets(map[string][]binance.LeverageBracket{}) // reset

	if mx, ok := maxLeverageForNotional("AMZNUSDT", 4000); !ok || mx != 10 {
		t.Errorf("$4k notional → %d,%v; want 10,true (fits top tier)", mx, ok)
	}
	if mx, ok := maxLeverageForNotional("AMZNUSDT", 12000); !ok || mx != 5 {
		t.Errorf("$12k notional → %d,%v; want 5,true (must drop a tier)", mx, ok)
	}
	if _, ok := maxLeverageForNotional("DOGEUSDT", 1000); ok {
		t.Error("symbol without a bracket table should return ok=false (fail open)")
	}
}
