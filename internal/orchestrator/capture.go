package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"github.com/johnny1110/evva/pkg/tools"
)

// capture is the slot a submitTool writes the agent's structured output
// into. The orchestrator resets it before each agent run and reads it
// after. Rounds run sequentially, but the mutex keeps it safe if a future
// version parallelises symbols.
type capture struct {
	mu  sync.Mutex
	raw json.RawMessage
	set bool
}

func (c *capture) reset() {
	c.mu.Lock()
	c.raw, c.set = nil, false
	c.mu.Unlock()
}

func (c *capture) store(raw json.RawMessage) {
	c.mu.Lock()
	c.raw, c.set = append(json.RawMessage(nil), raw...), true
	c.mu.Unlock()
}

// into unmarshals the captured JSON into dst. Returns an error if nothing
// was captured (the agent never called its submit tool) or the JSON is
// malformed.
func (c *capture) into(dst any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.set {
		return fmt.Errorf("no structured output was submitted")
	}
	return json.Unmarshal(c.raw, dst)
}

// submitTool is the LLM-facing tool each role agent calls exactly once to
// hand back its structured result. The input is validated against schema
// by the SDK; Execute simply records it.
type submitTool struct {
	name   string
	desc   string
	schema json.RawMessage
	cap    *capture
}

func newSubmitTool(name, desc, schema string, cap *capture) *submitTool {
	return &submitTool{name: name, desc: desc, schema: json.RawMessage(schema), cap: cap}
}

func (t *submitTool) Name() string            { return t.name }
func (t *submitTool) Description() string      { return t.desc }
func (t *submitTool) Schema() json.RawMessage { return t.schema }

func (t *submitTool) Execute(_ context.Context, logger *slog.Logger, raw json.RawMessage) (tools.Result, error) {
	// Sanity-check it parses as an object before accepting — gives the
	// model a chance to retry on malformed JSON rather than failing the
	// whole round downstream.
	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("%s: input is not a valid JSON object: %v", t.name, err)}, nil
	}
	logger.Debug("submit.capture", "tool", t.name, "bytes", len(raw))
	t.cap.store(raw)
	return tools.Result{Content: "received — your structured output was recorded."}, nil
}

// Compile-time assertion.
var _ tools.Tool = (*submitTool)(nil)

// --- submit-tool JSON schemas (mirror the structs in types.go) ---

const submitAnalysisName = "submit_analysis"

const submitAnalysisDesc = `Submit your final market-analysis report for this round.
Call this EXACTLY ONCE, after you have analysed all three symbols. This is
how your analysis is handed to the Risk Manager — nothing else you write is
passed downstream.`

const submitAnalysisSchema = `{
	"type": "object",
	"additionalProperties": false,
	"required": ["sentiment", "symbols"],
	"properties": {
		"sentiment": {"type": "string", "description": "Fear & Greed reading, e.g. '23 (Extreme Fear)'."},
		"notes": {"type": "string", "description": "Optional cross-market notes."},
		"symbols": {
			"type": "array",
			"minItems": 3,
			"items": {
				"type": "object",
				"additionalProperties": false,
				"required": ["symbol", "bias", "conviction", "summary"],
				"properties": {
					"symbol": {"type": "string", "description": "BTCUSDT / ETHUSDT / SOLUSDT."},
					"bias": {"type": "string", "enum": ["BULLISH", "BEARISH", "NEUTRAL"]},
					"conviction": {"type": "string", "enum": ["HIGH", "MEDIUM", "LOW"]},
					"setups": {"type": "array", "items": {"type": "string"}, "description": "Matched setup triggers."},
					"key_levels": {"type": "string", "description": "Support/resistance vs 24h range."},
					"summary": {"type": "string", "description": "One-line tape read incl. MA20/RSI/momentum from binance_klines."}
				}
			}
		}
	}
}`

const submitRiskName = "submit_risk_decisions"

const submitRiskDesc = `Submit your final risk decisions for this round.
Call this EXACTLY ONCE. These numeric parameters are handed verbatim to the
Executor — it places exactly what you specify and decides nothing itself.
Use VETO to block a proposed trade, WAIT to stand down.`

const submitRiskSchema = `{
	"type": "object",
	"additionalProperties": false,
	"required": ["balance", "decisions"],
	"properties": {
		"balance": {"type": "number", "description": "Live wallet balance (USDT) you computed caps from."},
		"risk_notes": {"type": "string", "description": "Which of the 7 risk checks were evaluated and which tripped."},
		"decisions": {
			"type": "array",
			"minItems": 3,
			"items": {
				"type": "object",
				"additionalProperties": false,
				"required": ["symbol", "action", "reason"],
				"properties": {
					"symbol": {"type": "string"},
					"action": {"type": "string", "enum": ["OPEN_LONG", "OPEN_SHORT", "ADD", "CLOSE", "WAIT", "VETO"]},
					"quantity": {"type": "number", "description": "Base-asset qty for OPEN/ADD/CLOSE, rounded to step size."},
					"leverage": {"type": "integer", "description": "Leverage for OPEN/ADD."},
					"reduce_only": {"type": "boolean", "description": "True for CLOSE."},
					"stop_loss": {"type": "number", "description": "Mental stop price (informational)."},
					"take_profit": {"type": "number", "description": "Take-profit price (informational)."},
					"reason": {"type": "string", "description": "One-sentence justification, or the veto reason."}
				}
			}
		}
	}
}`

const submitExecName = "submit_execution"

const submitExecDesc = `Submit your execution summary for this round. Call
this EXACTLY ONCE, after placing all approved orders. 'report' is shown to
the user; 'carry' is the one-line state threaded into the next round.`

const submitExecSchema = `{
	"type": "object",
	"additionalProperties": false,
	"required": ["report", "carry"],
	"properties": {
		"report": {"type": "string", "description": "Human-readable round report: actions taken, fills, and per-symbol status."},
		"carry": {"type": "string", "description": "One-line state for next round: per-symbol position summary with peak uPnL, e.g. 'BTC: LONG qty=0.1 entry=73500 peak=+$80 | ETH: FLAT | SOL: FLAT'."}
	}
}`
