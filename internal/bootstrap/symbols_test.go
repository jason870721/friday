package bootstrap

import (
	"slices"
	"testing"
)

func TestParseSymbolList_NormalisesDedupesAndDropsBlanks(t *testing.T) {
	got := parseSymbolList(" btcusdt, ETHUSDT ,,solusdt,BTCUSDT, ")
	want := []string{"BTCUSDT", "ETHUSDT", "SOLUSDT"}
	if !slices.Equal(got, want) {
		t.Errorf("parseSymbolList = %v; want %v", got, want)
	}
}

func TestParseSymbolList_Empty(t *testing.T) {
	if got := parseSymbolList("   ,, "); len(got) != 0 {
		t.Errorf("parseSymbolList = %v; want empty", got)
	}
}

func TestFallbackSymbols_KeepsOnlyKnownCorePairs(t *testing.T) {
	// When the preflight is unreachable we must not trade unvalidated tickers:
	// only symbols with a known-good fallback step survive.
	got := fallbackSymbols([]string{"BTCUSDT", "NVDAUSDT", "SOLUSDT", "FAKEUSDT"})
	names := make([]string, len(got))
	for i, s := range got {
		names[i] = s.Name
	}
	if !slices.Equal(names, []string{"BTCUSDT", "SOLUSDT"}) {
		t.Errorf("fallbackSymbols kept %v; want [BTCUSDT SOLUSDT]", names)
	}
	for _, s := range got {
		if s.StepSize == "" {
			t.Errorf("%s has empty step size in fallback", s.Name)
		}
	}
}
