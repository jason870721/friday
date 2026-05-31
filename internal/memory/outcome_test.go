package memory

import "testing"

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
