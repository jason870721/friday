package orchestrator

import (
	"encoding/json"
	"strings"
	"testing"
)

func twoSymbols() []MarketSymbol {
	return []MarketSymbol{
		{Name: "BTCUSDT", StepSize: "0.001"},
		{Name: "NVDAUSDT", StepSize: "0.1"},
	}
}

func TestRenderPrompt_SubstitutesTokensAndPreservesPercent(t *testing.T) {
	got := analystSystemPrompt(twoSymbols())
	if !strings.Contains(got, "BTCUSDT, NVDAUSDT") {
		t.Errorf("missing rendered symbol list in:\n%s", got)
	}
	if !strings.Contains(got, "2 markets") {
		t.Errorf("missing rendered count")
	}
	if strings.Contains(got, "{{") {
		t.Errorf("unsubstituted token left in prompt")
	}
	// The dense literal '%' signs in the prompt body must survive verbatim —
	// this is the whole reason renderPrompt uses a replacer, not fmt.
	if !strings.Contains(got, "+0.05%") {
		t.Errorf("funding threshold '%%' was mangled")
	}
}

func TestRiskPrompt_RendersStepSizes(t *testing.T) {
	got := riskSystemPrompt(twoSymbols())
	if !strings.Contains(got, "BTCUSDT 0.001") || !strings.Contains(got, "NVDAUSDT 0.1") {
		t.Errorf("missing per-symbol step sizes in:\n%s", got)
	}
	if !strings.Contains(got, "max_positions  = 2") {
		t.Errorf("max_positions not pinned to symbol count")
	}
	if !strings.Contains(got, "balance × 15%") {
		t.Errorf("percent caps mangled")
	}
}

func TestStepSizeHint_FallsBackWhenUnknown(t *testing.T) {
	hint := stepSizeHint([]MarketSymbol{{Name: "BTCUSDT"}}) // no StepSize
	if !strings.Contains(hint, "LOT_SIZE") {
		t.Errorf("stepSizeHint = %q; want generic fallback", hint)
	}
}

func TestStepSizeHint_IncludesMaxLeverage(t *testing.T) {
	// PRD-012: the steps hint carries each symbol's max leverage as "(≤Nx)";
	// a zero MaxLeverage omits it.
	hint := stepSizeHint([]MarketSymbol{
		{Name: "BTCUSDT", StepSize: "0.001", MaxLeverage: 125},
		{Name: "NVDAUSDT", StepSize: "0.01", MaxLeverage: 10},
		{Name: "FOOUSDT", StepSize: "0.1"}, // unknown leverage
	})
	if !strings.Contains(hint, "BTCUSDT 0.001 (≤125x)") || !strings.Contains(hint, "NVDAUSDT 0.01 (≤10x)") {
		t.Errorf("missing per-symbol leverage caps in %q", hint)
	}
	if strings.Contains(hint, "FOOUSDT 0.1 (≤") {
		t.Errorf("unknown leverage should omit the (≤Nx) suffix: %q", hint)
	}
}

func TestStepSizeHint_IncludesNotionalCeiling(t *testing.T) {
	// PRD-019: when a max-leverage notional ceiling is known, the hint shows it
	// as "≤$Xk @max-lev" so the Risk Manager sizes within the tier (avoids -2027).
	hint := stepSizeHint([]MarketSymbol{
		{Name: "AMZNUSDT", StepSize: "0.01", MaxLeverage: 10, MaxNotional: 5000},
		{Name: "BTCUSDT", StepSize: "0.001", MaxLeverage: 125, MaxNotional: 50000},
		{Name: "NVDAUSDT", StepSize: "0.1", MaxLeverage: 10}, // cap unknown
	})
	if !strings.Contains(hint, "AMZNUSDT 0.01 (≤10x, ≤$5k @max-lev)") {
		t.Errorf("missing notional ceiling for AMZNUSDT in %q", hint)
	}
	if !strings.Contains(hint, "BTCUSDT 0.001 (≤125x, ≤$50k @max-lev)") {
		t.Errorf("missing notional ceiling for BTCUSDT in %q", hint)
	}
	// A known leverage but unknown notional cap keeps the bare "(≤Nx)".
	if !strings.Contains(hint, "NVDAUSDT 0.1 (≤10x)") || strings.Contains(hint, "NVDAUSDT 0.1 (≤10x,") {
		t.Errorf("unknown notional cap should omit the @max-lev part: %q", hint)
	}
}

func TestShortUSD(t *testing.T) {
	cases := map[float64]string{
		300:     "$300",
		5000:    "$5k",
		25000:   "$25k",
		1200000: "$1.2M",
		2000000: "$2M",
	}
	for v, want := range cases {
		if got := shortUSD(v); got != want {
			t.Errorf("shortUSD(%g) = %q; want %q", v, got, want)
		}
	}
}

func TestSubmitSchemas_PinMinItemsToSymbolCount(t *testing.T) {
	for _, n := range []int{1, 3, 7} {
		for _, schema := range []string{submitAnalysisSchema(n), submitRiskSchema(n)} {
			var probe map[string]any
			if err := json.Unmarshal([]byte(schema), &probe); err != nil {
				t.Fatalf("schema(%d) is not valid JSON: %v", n, err)
			}
			if strings.Contains(schema, "SYMBOL_COUNT") {
				t.Errorf("schema(%d) left the SYMBOL_COUNT token unsubstituted", n)
			}
		}
	}
}
