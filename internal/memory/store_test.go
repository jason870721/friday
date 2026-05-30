package memory

import (
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

func TestOpen_MissingFileIsEmpty(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "does-not-exist.jsonl"))
	if err != nil {
		t.Fatalf("open missing file: %v", err)
	}
	if s.Len() != 0 {
		t.Errorf("len = %d; want 0", s.Len())
	}
}
