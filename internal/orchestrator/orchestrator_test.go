package orchestrator

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/johnny1110/evva/pkg/event"
)

// fakeRunner stands in for an agent.Agent: when Run is called it writes a
// canned JSON payload into the capture (simulating the submit_* tool) and
// returns. This lets us test the orchestrator pipeline without an LLM.
type fakeRunner struct {
	cap     *capture
	payload string
	calls   int
}

func (f *fakeRunner) Run(_ context.Context, _ string) (string, error) {
	f.calls++
	if f.payload != "" {
		f.cap.store(json.RawMessage(f.payload))
	}
	return "", nil
}

// collectEmitter records role tags so tests can assert narration/tagging.
type collectEmitter struct{ roles []string }

func (c *collectEmitter) EmitRole(role string, _ event.Event) {
	c.roles = append(c.roles, role)
}

func newTestOrch() (*Orchestrator, *collectEmitter) {
	em := &collectEmitter{}
	o := &Orchestrator{
		emitter:     em,
		capAnalysis: &capture{},
		capRisk:     &capture{},
		capExec:     &capture{},
	}
	return o, em
}

func TestRunRound_FullPipeline(t *testing.T) {
	o, em := newTestOrch()

	o.analyst = &fakeRunner{cap: o.capAnalysis, payload: `{
		"sentiment": "23 (Extreme Fear)",
		"symbols": [
			{"symbol":"BTCUSDT","bias":"BULLISH","conviction":"HIGH","summary":"above MA20, RSI 64"},
			{"symbol":"ETHUSDT","bias":"NEUTRAL","conviction":"LOW","summary":"chop"},
			{"symbol":"SOLUSDT","bias":"BULLISH","conviction":"MEDIUM","summary":"grind up"}
		]}`}
	o.risk = &fakeRunner{cap: o.capRisk, payload: `{
		"balance": 5000,
		"risk_notes": "all checks pass, flat",
		"decisions": [
			{"symbol":"BTCUSDT","action":"OPEN_LONG","quantity":0.01,"leverage":20,"reason":"momentum"},
			{"symbol":"ETHUSDT","action":"WAIT","reason":"no setup"},
			{"symbol":"SOLUSDT","action":"WAIT","reason":"low conviction"}
		]}`}
	exec := &fakeRunner{cap: o.capExec, payload: `{
		"report": "Opened BTC long 0.01 @ market.",
		"carry": "BTC: LONG qty=0.01 | ETH: FLAT | SOL: FLAT"}`}
	o.executor = exec

	res, err := o.runRound(context.Background(), 1, "")
	if err != nil {
		t.Fatalf("runRound: %v", err)
	}
	if exec.calls != 1 {
		t.Errorf("executor calls = %d; want 1 (there was an actionable decision)", exec.calls)
	}
	if !strings.Contains(res.Carry, "BTC: LONG") {
		t.Errorf("carry = %q; want BTC long state", res.Carry)
	}
	// Pipeline narration should have tagged the orchestrator role.
	if !slices.Contains(em.roles, roleOrch) {
		t.Errorf("expected %q narration tags, got %v", roleOrch, em.roles)
	}
}

func TestRunRound_SkipsExecutorWhenNothingActionable(t *testing.T) {
	o, _ := newTestOrch()

	o.analyst = &fakeRunner{cap: o.capAnalysis, payload: `{
		"sentiment":"50 (Neutral)",
		"symbols":[
			{"symbol":"BTCUSDT","bias":"NEUTRAL","conviction":"LOW","summary":"chop"},
			{"symbol":"ETHUSDT","bias":"NEUTRAL","conviction":"LOW","summary":"chop"},
			{"symbol":"SOLUSDT","bias":"NEUTRAL","conviction":"LOW","summary":"chop"}
		]}`}
	o.risk = &fakeRunner{cap: o.capRisk, payload: `{
		"balance":5000,
		"risk_notes":"flat, no setups",
		"decisions":[
			{"symbol":"BTCUSDT","action":"WAIT","reason":"chop"},
			{"symbol":"ETHUSDT","action":"VETO","reason":"counter-trend"},
			{"symbol":"SOLUSDT","action":"WAIT","reason":"chop"}
		]}`}
	exec := &fakeRunner{cap: o.capExec}
	o.executor = exec

	res, err := o.runRound(context.Background(), 1, "prev state")
	if err != nil {
		t.Fatalf("runRound: %v", err)
	}
	if exec.calls != 0 {
		t.Errorf("executor calls = %d; want 0 (all WAIT/VETO → skip executor)", exec.calls)
	}
	if !strings.Contains(res.Report, "No actionable trades") {
		t.Errorf("report = %q; want no-actionable-trades message", res.Report)
	}
	// Carry should be preserved unchanged when no trades happen.
	if res.Carry != "prev state" {
		t.Errorf("carry = %q; want unchanged 'prev state'", res.Carry)
	}
}

func TestRunRound_AnalystMissingOutputErrors(t *testing.T) {
	o, _ := newTestOrch()
	o.analyst = &fakeRunner{cap: o.capAnalysis} // submits nothing
	o.risk = &fakeRunner{cap: o.capRisk}
	o.executor = &fakeRunner{cap: o.capExec}

	if _, err := o.runRound(context.Background(), 1, ""); err == nil {
		t.Fatal("expected error when analyst submits no structured output")
	}
}

func TestAnyActionable(t *testing.T) {
	yes := RiskDecisions{Decisions: []RiskDecision{{Action: "WAIT"}, {Action: "CLOSE"}}}
	no := RiskDecisions{Decisions: []RiskDecision{{Action: "WAIT"}, {Action: "VETO"}}}
	if !anyActionable(yes) {
		t.Error("CLOSE should be actionable")
	}
	if anyActionable(no) {
		t.Error("only WAIT/VETO should not be actionable")
	}
}
