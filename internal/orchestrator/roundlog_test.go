package orchestrator

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// readRoundLog decodes every JSONL line in the round log file.
func readRoundLog(t *testing.T, path string) []RoundRecord {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open round log: %v", err)
	}
	defer f.Close()
	var recs []RoundRecord
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r RoundRecord
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("decode round record: %v", err)
		}
		recs = append(recs, r)
	}
	return recs
}

func TestRoundRecorder_AppendsJSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "rounds.jsonl")
	r := NewRoundRecorder(path)

	if err := r.Log(RoundRecord{Round: 1, Sentiment: "23 (Extreme Fear)"}); err != nil {
		t.Fatalf("log round 1: %v", err)
	}
	if err := r.Log(RoundRecord{Round: 2, Sentiment: "55 (Greed)"}); err != nil {
		t.Fatalf("log round 2: %v", err)
	}

	recs := readRoundLog(t, path)
	if len(recs) != 2 {
		t.Fatalf("records = %d; want 2 (append, one per line)", len(recs))
	}
	if recs[0].Round != 1 || recs[1].Round != 2 {
		t.Errorf("rounds = %d,%d; want 1,2", recs[0].Round, recs[1].Round)
	}
}

func TestRoundRecorder_NilIsNoOp(t *testing.T) {
	var r *RoundRecorder // nil
	if err := r.Log(RoundRecord{Round: 1}); err != nil {
		t.Errorf("nil recorder Log should be a no-op, got %v", err)
	}
}

// runRound should append a record on BOTH paths: the all-WAIT short-circuit
// (Executed=false) and a full pipeline run (Executed=true), capturing the
// Analyst read and the Risk decisions either way.
func TestRunRound_WritesRoundLog(t *testing.T) {
	o, _ := newTestOrch()
	path := filepath.Join(t.TempDir(), "rounds.jsonl")
	o.recorder = NewRoundRecorder(path)

	// Round 1: actionable → executor runs → Executed=true.
	o.analyst = &fakeRunner{cap: o.capAnalysis, payload: `{
		"sentiment":"23 (Extreme Fear)",
		"symbols":[
			{"symbol":"BTCUSDT","bias":"BULLISH","conviction":"HIGH","summary":"above MA20"},
			{"symbol":"ETHUSDT","bias":"NEUTRAL","conviction":"LOW","summary":"chop"},
			{"symbol":"SOLUSDT","bias":"BULLISH","conviction":"MEDIUM","summary":"grind"}
		]}`}
	o.risk = &fakeRunner{cap: o.capRisk, payload: `{
		"balance":5000,"risk_notes":"ok",
		"decisions":[
			{"symbol":"BTCUSDT","action":"OPEN_LONG","quantity":0.01,"leverage":20,"reason":"momentum"},
			{"symbol":"ETHUSDT","action":"WAIT","reason":"no setup"},
			{"symbol":"SOLUSDT","action":"WAIT","reason":"low conviction"}
		]}`}
	o.executor = &fakeRunner{cap: o.capExec, payload: `{
		"report":"Opened BTC long.","carry":"BTC: LONG qty=0.01"}`}

	if _, err := o.runRound(context.Background(), 1, ""); err != nil {
		t.Fatalf("round 1: %v", err)
	}

	// Round 2: all WAIT → short-circuit → Executed=false, still logged.
	o.risk = &fakeRunner{cap: o.capRisk, payload: `{
		"balance":4900,"risk_notes":"flat",
		"decisions":[
			{"symbol":"BTCUSDT","action":"WAIT","reason":"chop"},
			{"symbol":"ETHUSDT","action":"WAIT","reason":"chop"},
			{"symbol":"SOLUSDT","action":"WAIT","reason":"chop"}
		]}`}
	if _, err := o.runRound(context.Background(), 2, "BTC: LONG qty=0.01"); err != nil {
		t.Fatalf("round 2: %v", err)
	}

	recs := readRoundLog(t, path)
	if len(recs) != 2 {
		t.Fatalf("round records = %d; want 2 (both paths logged)", len(recs))
	}
	r1 := recs[0]
	if !r1.Executed || r1.Round != 1 || r1.Sentiment != "23 (Extreme Fear)" {
		t.Errorf("round 1 record = %+v; want Executed=true, round 1, fear sentiment", r1)
	}
	if len(r1.Analysis) != 3 || len(r1.Decisions) != 3 || r1.Balance != 5000 {
		t.Errorf("round 1 should carry the full analysis + decisions + balance: %+v", r1)
	}
	if r1.Time == "" {
		t.Error("round record should be timestamped")
	}
	if r2 := recs[1]; r2.Executed || r2.Round != 2 {
		t.Errorf("round 2 record = %+v; want Executed=false, round 2 (short-circuit still logged)", r2)
	}
}
