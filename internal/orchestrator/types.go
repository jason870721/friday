// Package orchestrator implements friday's PRD-003 multi-agent
// architecture: the monolithic F.R.I.D.A.Y. trader is split into three
// single-responsibility agents that collaborate through TYPED Go handoff
// contracts, coordinated by a deterministic Go pipeline rather than by the
// LLM itself.
//
//	Analyst       → reads the tape + sentiment, emits an AnalystReport
//	Risk Manager  → sizes / sets stops / vetoes, emits RiskDecisions
//	Executor      → places the precise orders, emits an ExecutionResult
//
// Each boundary is a struct validated against a JSON schema (the agent
// hands it back by calling a submit_* tool). The orchestrator owns the
// round loop and the 15s cadence — none of the three agents schedule
// themselves, so "never stop the loop" is enforced in Go, not by prompt.
package orchestrator

// Bias / conviction enums are kept as plain strings (validated by the
// submit-tool JSON schema) so the structs marshal cleanly to and from the
// LLM without custom (un)marshalers.

// SymbolAnalysis is the Analyst's read of one symbol.
type SymbolAnalysis struct {
	Symbol     string   `json:"symbol"`
	Bias       string   `json:"bias"`       // BULLISH / BEARISH / NEUTRAL
	Conviction string   `json:"conviction"` // HIGH / MEDIUM / LOW
	Setups     []string `json:"setups"`     // matched setup triggers (≥2 to justify a trade)
	KeyLevels  string   `json:"key_levels"` // support / resistance notes
	Summary    string   `json:"summary"`    // one-line tape read incl. MA20 / RSI / momentum
}

// AnalystReport is the Analyst → Risk Manager handoff.
type AnalystReport struct {
	Sentiment string           `json:"sentiment"` // Fear & Greed reading, e.g. "23 (Extreme Fear)"
	Symbols   []SymbolAnalysis `json:"symbols"`
	Notes     string           `json:"notes,omitempty"`
}

// RiskDecision is the Risk Manager's verdict for one symbol. For
// OPEN_LONG / OPEN_SHORT / ADD it carries Quantity + Leverage; for CLOSE
// it carries Quantity + ReduceOnly; WAIT / VETO carry only a Reason.
type RiskDecision struct {
	Symbol     string  `json:"symbol"`
	Action     string  `json:"action"` // OPEN_LONG / OPEN_SHORT / ADD / CLOSE / WAIT / VETO
	Quantity   float64 `json:"quantity,omitempty"`
	Leverage   int     `json:"leverage,omitempty"`
	ReduceOnly bool    `json:"reduce_only,omitempty"`
	StopLoss   float64 `json:"stop_loss,omitempty"`
	TakeProfit float64 `json:"take_profit,omitempty"`
	Reason     string  `json:"reason"`
}

// RiskDecisions is the Risk Manager → Executor handoff.
type RiskDecisions struct {
	Balance   float64        `json:"balance"`
	Decisions []RiskDecision `json:"decisions"`
	RiskNotes string         `json:"risk_notes,omitempty"` // which of the 7 checks tripped
}

// ExecutionResult is the Executor's output for the round.
type ExecutionResult struct {
	Report string `json:"report"` // human-readable round report (shown in the TUI)
	Carry  string `json:"carry"`  // one-line state threaded into the next round
}

// actionable reports whether a RiskDecision asks the Executor to place an
// order (as opposed to WAIT / VETO, which are no-ops at the exchange).
func (d RiskDecision) actionable() bool {
	switch d.Action {
	case "OPEN_LONG", "OPEN_SHORT", "ADD", "CLOSE":
		return true
	default:
		return false
	}
}
