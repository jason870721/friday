package memory

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStore_LogPersistsAndReloads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trades.jsonl")

	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if s.Len() != 0 {
		t.Fatalf("fresh store len = %d; want 0", s.Len())
	}

	rec := TradeRecord{
		Symbol:      "SOLUSDT",
		Time:        1000,
		Features:    Features{RSI: 70, PriceVsMA: 0.3, Momentum: 1, Funding: 0.01, Sentiment: 23},
		EntryReason: "momentum + divergence",
		Bias:        "LONG",
		PnL:         12.5,
	}
	if err := s.Log(rec); err != nil {
		t.Fatalf("log: %v", err)
	}

	// Reopen from disk — the record should survive and Outcome be derived.
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if s2.Len() != 1 {
		t.Fatalf("reloaded len = %d; want 1", s2.Len())
	}
	got := s2.Similar("", Features{RSI: 70, PriceVsMA: 0.3, Momentum: 1, Funding: 0.01, Sentiment: 23}, 1)
	if len(got) != 1 {
		t.Fatalf("similar returned %d; want 1", len(got))
	}
	if got[0].Record.Outcome != "WIN" {
		t.Errorf("outcome = %q; want WIN (PnL +12.5)", got[0].Record.Outcome)
	}
}

func TestStore_SimilarConclusive(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "trades.jsonl"))
	f := Features{RSI: 50, PriceVsMA: 0, Momentum: 0, Funding: 0, Sentiment: 50}

	// 3 comparable trades → inconclusive (< ConclusiveMinSamples).
	for range 3 {
		_ = s.Log(TradeRecord{Symbol: "BTCUSDT", Bias: "LONG", PnL: -1, Features: f})
	}
	if top, ok := s.SimilarConclusive("BTCUSDT", "", f, 10); ok {
		t.Errorf("3 matches → conclusive=%v (%d returned); want false", ok, len(top))
	}

	// Grow to 6 comparable trades → conclusive (≥5), even when k caps the slice.
	for range 3 {
		_ = s.Log(TradeRecord{Symbol: "BTCUSDT", Bias: "LONG", PnL: -1, Features: f})
	}
	top, ok := s.SimilarConclusive("BTCUSDT", "", f, 3)
	if !ok {
		t.Errorf("6 matches → conclusive=false; want true")
	}
	if len(top) != 3 {
		t.Errorf("k=3 should cap the slice at 3, got %d (pool drives conclusiveness, not the slice)", len(top))
	}
}

func TestStore_SimilarRanksClosestFirst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trades.jsonl")
	s, _ := Open(path)

	// An overbought-greed setup and an oversold-fear setup.
	overbought := TradeRecord{Symbol: "BTCUSDT", Bias: "LONG", PnL: -20,
		Features: Features{RSI: 80, PriceVsMA: 1.5, Momentum: 1, Funding: 0.05, Sentiment: 85}}
	oversold := TradeRecord{Symbol: "BTCUSDT", Bias: "LONG", PnL: 30,
		Features: Features{RSI: 25, PriceVsMA: -1.2, Momentum: -1, Funding: -0.02, Sentiment: 15}}
	_ = s.Log(overbought)
	_ = s.Log(oversold)

	// Query close to the oversold-fear setup → it should rank first.
	q := Features{RSI: 28, PriceVsMA: -1.0, Momentum: -1, Funding: -0.01, Sentiment: 18}
	got := s.Similar("BTCUSDT", q, 2)
	if len(got) != 2 {
		t.Fatalf("got %d; want 2", len(got))
	}
	if got[0].Record.PnL != 30 {
		t.Errorf("closest record PnL = %.0f; want 30 (the oversold setup)", got[0].Record.PnL)
	}
	if got[0].Similarity < got[1].Similarity {
		t.Errorf("results not sorted by descending similarity: %v", got)
	}
}

func TestStore_SimilarFiltersBySymbol(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trades.jsonl")
	s, _ := Open(path)
	_ = s.Log(TradeRecord{Symbol: "BTCUSDT", Features: Features{RSI: 50}})
	_ = s.Log(TradeRecord{Symbol: "ETHUSDT", Features: Features{RSI: 50}})

	got := s.Similar("ETHUSDT", Features{RSI: 50}, 10)
	if len(got) != 1 || got[0].Record.Symbol != "ETHUSDT" {
		t.Errorf("symbol filter failed: got %+v", got)
	}
}

func TestStore_StrategyFieldRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trades.jsonl")
	s, _ := Open(path)
	if err := s.Log(TradeRecord{Symbol: "BTCUSDT", Strategy: "momentum", PnL: 5}); err != nil {
		t.Fatal(err)
	}
	reopened, _ := Open(path)
	got := reopened.Similar("BTCUSDT", Features{}, 1)
	if len(got) != 1 || got[0].Record.Strategy != "momentum" {
		t.Fatalf("strategy did not round-trip: %+v", got)
	}
}

func TestStore_PrePRD014RecordLoads(t *testing.T) {
	// A record written before PRD-014 has no "strategy" key — it must load with
	// a zero-value Strategy rather than erroring.
	path := filepath.Join(t.TempDir(), "trades.jsonl")
	legacy := `{"symbol":"ETHUSDT","bias":"LONG","pnl":3.0,"outcome":"WIN"}` + "\n"
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open legacy file: %v", err)
	}
	got := s.Similar("ETHUSDT", Features{}, 1)
	if len(got) != 1 || got[0].Record.Strategy != "" {
		t.Fatalf("legacy record should load with empty strategy: %+v", got)
	}
}

func TestStore_SimilarByStrategy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trades.jsonl")
	s, _ := Open(path)
	f := Features{RSI: 60, Momentum: 1}
	_ = s.Log(TradeRecord{Symbol: "BTCUSDT", Strategy: "momentum", PnL: 10, Features: f})
	_ = s.Log(TradeRecord{Symbol: "BTCUSDT", Strategy: "breakout", PnL: -4, Features: f})
	_ = s.Log(TradeRecord{Symbol: "BTCUSDT", Strategy: "momentum", PnL: 7, Features: f})

	got := s.SimilarByStrategy("BTCUSDT", "momentum", f, 5)
	if len(got) != 2 {
		t.Fatalf("momentum-only returned %d; want 2", len(got))
	}
	for _, m := range got {
		if m.Record.Strategy != "momentum" {
			t.Errorf("got a %q record; want only momentum", m.Record.Strategy)
		}
	}
	// Empty strategy = no filter (same as Similar).
	if all := s.SimilarByStrategy("BTCUSDT", "", f, 5); len(all) != 3 {
		t.Errorf("empty strategy filter returned %d; want all 3", len(all))
	}
}

func TestOpen_MissingFileIsEmpty(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "does-not-exist.jsonl"))
	if err != nil {
		t.Fatalf("open missing file: %v", err)
	}
	if s.Len() != 0 {
		t.Errorf("len = %d; want 0", s.Len())
	}
}
