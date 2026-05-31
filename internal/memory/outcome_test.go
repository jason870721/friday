package memory

import (
	"math"
	"path/filepath"
	"testing"
)

func TestDeriveOutcome_ExchangeUsesNet(t *testing.T) {
	// A realised "win" that is a NET loss after fees must be recorded as LOSS —
	// this is the whole point of the reconciliation fix.
	r := TradeRecord{PnL: 0.5, NetPnL: -0.1, PnLSource: "exchange"}
	r.DeriveOutcome()
	if r.Outcome != "LOSS" {
		t.Errorf("Outcome = %q; want LOSS (net is negative)", r.Outcome)
	}
}

func TestDeriveOutcome_ReportedUsesPnL(t *testing.T) {
	// Without exchange reconciliation, fall back to the raw PnL.
	r := TradeRecord{PnL: 1.2, PnLSource: "reported"}
	r.DeriveOutcome()
	if r.Outcome != "WIN" {
		t.Errorf("Outcome = %q; want WIN", r.Outcome)
	}

	r2 := TradeRecord{PnL: -3.0}
	r2.DeriveOutcome()
	if r2.Outcome != "LOSS" {
		t.Errorf("Outcome = %q; want LOSS", r2.Outcome)
	}
}

func TestOutcomeStatsOf_CountsAndAverages(t *testing.T) {
	sc := func(pnl float64, src string, net float64) Scored {
		return Scored{Record: TradeRecord{PnL: pnl, NetPnL: net, PnLSource: src}}
	}
	stats := OutcomeStatsOf([]Scored{
		sc(10, "reported", 0),    // win +10
		sc(4, "reported", 0),     // win +4
		sc(-6, "reported", 0),    // loss -6
		sc(5, "exchange", -1),    // realised +5 but NET -1 → loss (PRD-011 basis)
		sc(0, "reported", 0),     // flat
	})
	if stats.Wins != 2 || stats.Losses != 2 || stats.Flats != 1 {
		t.Fatalf("counts = %+v; want 2 wins / 2 losses / 1 flat", stats)
	}
	if math.Abs(stats.AvgWin-7) > 1e-9 { // (10+4)/2
		t.Errorf("AvgWin = %v; want 7", stats.AvgWin)
	}
	if math.Abs(stats.AvgLoss-(-3.5)) > 1e-9 { // (-6 + -1)/2
		t.Errorf("AvgLoss = %v; want -3.5", stats.AvgLoss)
	}
}

func TestOutcomeSummary_TopKSimilar(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trades.jsonl")
	s, _ := Open(path)
	f := Features{RSI: 50}
	// Three matching-feature trades; k=2 should consider only the top 2.
	_ = s.Log(TradeRecord{Symbol: "BTCUSDT", PnL: 10, Features: f})
	_ = s.Log(TradeRecord{Symbol: "BTCUSDT", PnL: 8, Features: f})
	_ = s.Log(TradeRecord{Symbol: "BTCUSDT", PnL: -5, Features: f})

	st := s.OutcomeSummary("BTCUSDT", f, 2)
	if st.Wins+st.Losses+st.Flats != 2 {
		t.Fatalf("OutcomeSummary considered %d trades; want top-2", st.Wins+st.Losses+st.Flats)
	}
}

func TestLog_DerivesNetOutcomeOnWrite(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir + "/trades.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	// Realised +2 but commission/funding drag it to net −0.5 → LOSS.
	if err := s.Log(TradeRecord{Symbol: "NVDAUSDT", PnL: 2, Commission: -2.5, NetPnL: -0.5, PnLSource: "exchange"}); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dir + "/trades.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	got := reopened.Similar("NVDAUSDT", Features{}, 1)
	if len(got) != 1 || got[0].Record.Outcome != "LOSS" {
		t.Fatalf("persisted outcome = %+v; want one LOSS record", got)
	}
}
