package orchestrator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// RoundRecord is one round's full pipeline outcome, appended to the round log
// for offline analysis (separate from trade memory, which only records CLOSED
// trades). It captures every handoff — the Analyst's read, the Risk Manager's
// numeric decisions, and what the Executor did — so a session can be replayed
// and the analysis quality scored against the trades it produced.
type RoundRecord struct {
	Round        int              `json:"round"`
	Time         string           `json:"time"` // RFC3339 (UTC)
	Sentiment    string           `json:"sentiment"`
	Analysis     []SymbolAnalysis `json:"analysis"`
	AnalystNotes string           `json:"analyst_notes,omitempty"`
	Balance      float64          `json:"balance"`
	Decisions    []RiskDecision   `json:"decisions"`
	RiskNotes    string           `json:"risk_notes,omitempty"`
	Breaker      string           `json:"breaker,omitempty"`
	Executed     bool             `json:"executed"` // did the Executor run (vs. an all-WAIT/VETO short-circuit)
	Report       string           `json:"report,omitempty"`
	Carry        string           `json:"carry,omitempty"`
}

// RoundRecorder appends RoundRecords to a JSONL file, one record per line —
// the same append-only format trade memory uses (~/.friday/memory/trades.jsonl),
// so the same tooling (jq, pandas, a reconcile pass) reads both. Safe for
// concurrent use; a nil *RoundRecorder is a no-op so the orchestrator and its
// tests can run without one.
type RoundRecorder struct {
	mu   sync.Mutex
	path string
}

// NewRoundRecorder returns a recorder that appends to path (e.g.
// ~/.friday/memory/rounds.jsonl). The file and its directory are created lazily
// on the first successful round.
func NewRoundRecorder(path string) *RoundRecorder {
	return &RoundRecorder{path: path}
}

// SetRoundRecorder installs the per-round analysis log (called from bootstrap).
// nil disables round logging.
func (o *Orchestrator) SetRoundRecorder(r *RoundRecorder) { o.recorder = r }

// Log appends one record as a single JSON line. A nil recorder is a no-op.
func (r *RoundRecorder) Log(rec RoundRecord) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(r.path), 0o755); err != nil {
		return fmt.Errorf("roundlog: mkdir: %w", err)
	}
	f, err := os.OpenFile(r.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("roundlog: open for append: %w", err)
	}
	defer f.Close()
	if err := json.NewEncoder(f).Encode(rec); err != nil {
		return fmt.Errorf("roundlog: encode: %w", err)
	}
	return nil
}

// recordRound assembles a RoundRecord from this round's handoffs and appends it.
// Failures are non-fatal — round logging never blocks trading — but are
// surfaced as pipeline narration so a broken log path is visible.
func (o *Orchestrator) recordRound(report AnalystReport, decisions RiskDecisions, execRes ExecutionResult, executed bool, round int) {
	if o.recorder == nil {
		return
	}
	rec := RoundRecord{
		Round:        round,
		Time:         time.Now().UTC().Format(time.RFC3339),
		Sentiment:    report.Sentiment,
		Analysis:     report.Symbols,
		AnalystNotes: report.Notes,
		Balance:      decisions.Balance,
		Decisions:    decisions.Decisions,
		RiskNotes:    decisions.RiskNotes,
		Executed:     executed,
		Report:       execRes.Report,
		Carry:        execRes.Carry,
	}
	if o.breaker != nil {
		rec.Breaker = o.breaker.Status()
	}
	if err := o.recorder.Log(rec); err != nil {
		o.narrate(roleOrch, fmt.Sprintf("round-log write failed: %v", err))
	}
}
