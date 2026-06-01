package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/johnny1110/evva/pkg/agent"
	"github.com/johnny1110/evva/pkg/config"
	"github.com/johnny1110/evva/pkg/event"
	pkgtools "github.com/johnny1110/evva/pkg/tools"

	"github.com/johnny1110/friday/internal/notify"
	"github.com/johnny1110/friday/internal/risk"
	"github.com/johnny1110/friday/internal/tool"
)

// Role labels — used both as event tags (the TUI prefixes lines with
// them) and agent names.
const (
	roleAnalyst = "Analyst"
	roleRisk    = "Risk"
	roleExec    = "Executor"
	roleOrch    = "Pipeline"
)

const defaultInterval = 15 * time.Second

// compactEvery is the number of rounds between full session compactions.
// At 15 s/round and ~7.5K tokens/round, 50 rounds (~12.5 min, ~375K tokens)
// compacts well before the 1M context window fills, keeping the model crisp
// without burning a summarization LLM call every few minutes.
const compactEvery = 50

// RoleEmitter receives role-tagged events from the orchestrator. The TUI
// sink implements it (stamping each event with its source role so the
// transcript can prefix the line). Defined here so orchestrator does not
// import tui.
type RoleEmitter interface {
	EmitRole(role string, e event.Event)
}

// agentRunner is the slice of agent.Agent the orchestrator needs. Narrow
// interface so tests can inject fakes.
type agentRunner interface {
	Run(ctx context.Context, prompt string) (string, error)
}

// Orchestrator runs the Analyst → Risk Manager → Executor pipeline on a
// fixed cadence, threading typed handoff structs between the three agents
// and one carry-state line between rounds. It owns the loop — Run blocks
// until the context is cancelled (Ctrl+C in the TUI).
type Orchestrator struct {
	analyst  agentRunner
	risk     agentRunner
	executor agentRunner

	// Full agent references for lifecycle operations (compact, etc.).
	// agentRunner is the narrow interface for Run() so tests can inject
	// fakes; these hold the real agent.Agent handles.
	analystAg  agent.Agent
	riskAg     agent.Agent
	executorAg agent.Agent

	capAnalysis *capture
	capRisk     *capture
	capExec     *capture

	emitter  RoleEmitter
	interval time.Duration
	breaker  *risk.CircuitBreaker

	// feeBudget surfaces a fee-spend status line in the Risk Manager round
	// prompt when near the cap (PRD-020 §3). nil → no line. The hard gate lives
	// in binance_order; this is just awareness.
	feeBudget *risk.FeeBudget

	// notify-related state (PRD-021 §3). notifier is nil when no external channel
	// is configured. lastBreakerState dedups breaker alerts so each PAUSED/HALTED
	// transition fires once, not every round.
	notifier         notify.Notifier
	lastBreakerState string

	// paper marks the session as paper-trading (PRD-021 §4) — tagged into the
	// round log and the session notifications. regimeFor returns a symbol's
	// latest market regime for the round log (PRD-021 §2); nil → omit regimes.
	paper     bool
	regimeFor func(symbol string) string

	// symbolCount/endpoint are captured for the session start/stop notifications.
	endpoint string

	// recorder appends each round's full pipeline outcome to a JSONL file for
	// offline analysis (see roundlog.go). nil → round logging disabled.
	recorder *RoundRecorder

	// symbols is the venue-validated market list this session covers,
	// injected into every role prompt and submit schema. Resolved at
	// bootstrap from FRIDAY_SYMBOLS (see bootstrap.resolveSymbols).
	symbols []MarketSymbol
}

// New builds the three role agents (each with a disjoint tool set) and
// returns a ready orchestrator. breaker is the shared session circuit
// breaker (PRD-005); it may be nil (the pipeline then runs without
// session-level gating).
func New(cfg *config.Config, emitter RoleEmitter, breaker *risk.CircuitBreaker, symbols []MarketSymbol) (*Orchestrator, error) {
	if len(symbols) == 0 {
		return nil, fmt.Errorf("orchestrator: no symbols to trade")
	}
	o := &Orchestrator{
		emitter:     emitter,
		interval:    defaultInterval,
		capAnalysis: &capture{},
		capRisk:     &capture{},
		capExec:     &capture{},
		breaker:     breaker,
		symbols:     symbols,
	}

	analyst, err := buildAgent(cfg, "friday-analyst", roleAnalyst, analystSystemPrompt(symbols), emitter, 40,
		customTool(tool.BinancePriceToolName, func() pkgtools.Tool { return tool.NewBinancePrice() }),
		customTool(tool.BinanceTickerToolName, func() pkgtools.Tool { return tool.NewBinanceTicker() }),
		customTool(tool.BinanceKlinesToolName, func() pkgtools.Tool { return tool.NewBinanceKlines() }),
		customTool(tool.BinanceMTFKlinesToolName, func() pkgtools.Tool { return tool.NewBinanceMTFKlines() }),
		customTool(tool.BinanceFundingToolName, func() pkgtools.Tool { return tool.NewBinanceFunding() }),
		customTool(tool.BinanceFeeToolName, func() pkgtools.Tool { return tool.NewBinanceFee() }),
		customTool(tool.FearGreedIndexToolName, func() pkgtools.Tool { return tool.NewFearGreedIndex() }),
		customTool(tool.BinancePositionToolName, func() pkgtools.Tool { return tool.NewBinancePosition() }),
		// PRD-004: self-reflection (recall similar past trades) and
		// hypothesis validation (sandbox backtest) before forming a bias.
		customTool(tool.RecallTradesToolName, func() pkgtools.Tool { return tool.NewRecallTrades() }),
		customTool(tool.RunBacktestToolName, func() pkgtools.Tool { return tool.NewRunBacktest() }),
		submitOption(submitAnalysisName, submitAnalysisDesc, submitAnalysisSchema(len(symbols)), o.capAnalysis),
	)
	if err != nil {
		return nil, fmt.Errorf("build analyst: %w", err)
	}

	risk, err := buildAgent(cfg, "friday-risk", roleRisk, riskSystemPrompt(symbols), emitter, 30,
		customTool(tool.BinanceBalanceToolName, func() pkgtools.Tool { return tool.NewBinanceBalance() }),
		customTool(tool.BinancePositionToolName, func() pkgtools.Tool { return tool.NewBinancePosition() }),
		customTool(tool.BinancePriceToolName, func() pkgtools.Tool { return tool.NewBinancePrice() }),
		customTool(tool.BinanceFeeToolName, func() pkgtools.Tool { return tool.NewBinanceFee() }),
		submitOption(submitRiskName, submitRiskDesc, submitRiskSchema(len(symbols)), o.capRisk),
	)
	if err != nil {
		return nil, fmt.Errorf("build risk manager: %w", err)
	}

	executor, err := buildAgent(cfg, "friday-executor", roleExec, executorSystemPrompt(symbols), emitter, 40,
		customTool(tool.BinanceLeverageToolName, func() pkgtools.Tool { return tool.NewBinanceLeverage() }),
		customTool(tool.BinanceOrderToolName, func() pkgtools.Tool { return tool.NewBinanceOrder() }),
		customTool(tool.BinanceCloseAllToolName, func() pkgtools.Tool { return tool.NewBinanceCloseAll() }),
		customTool(tool.BinanceStopMonitorToolName, func() pkgtools.Tool { return tool.NewBinanceStopMonitor() }),
		customTool(tool.BinancePositionToolName, func() pkgtools.Tool { return tool.NewBinancePosition() }),
		// PRD-004: log every closed trade to memory for future recall.
		customTool(tool.LogTradeToolName, func() pkgtools.Tool { return tool.NewLogTrade() }),
		submitOption(submitExecName, submitExecDesc, submitExecSchema, o.capExec),
	)
	if err != nil {
		return nil, fmt.Errorf("build executor: %w", err)
	}

	o.analyst, o.risk, o.executor = analyst, risk, executor
	o.analystAg, o.riskAg, o.executorAg = analyst, risk, executor
	return o, nil
}

// Run executes the pipeline round after round until ctx is cancelled,
// pausing o.interval between rounds. Its signature mirrors
// agent.Agent.Run so the TUI can drive it unchanged.
func (o *Orchestrator) Run(ctx context.Context, prompt string) (string, error) {
	carry := strings.TrimSpace(prompt)
	var lastReport string
	roundsRun := 0

	// Session start / stop notifications (PRD-021 §3). Stop fires when Run
	// returns (clean Ctrl+C shutdown is the only way out of the loop).
	o.notifySessionStart()
	defer func() { o.notifySessionStop(roundsRun, lastReport) }()

	for round := 1; ; round++ {
		if ctx.Err() != nil {
			return lastReport, nil
		}

		res, err := o.runRound(ctx, round, carry)
		roundsRun = round
		if err != nil {
			if ctx.Err() != nil {
				return lastReport, nil
			}
			o.narrate(roleOrch, fmt.Sprintf("round %d failed: %v — retrying next cycle", round, err))
		} else {
			lastReport = res.Report
			if c := strings.TrimSpace(res.Carry); c != "" {
				carry = c
			}
		}

		// PRD-023 R3: surface the fee-budget status into the carry so BOTH the
		// Analyst and the Risk Manager see it next round when spend nears the cap.
		carry = o.carryWithFeeWarning(carry)

		// Periodic full compaction: prevent session bloat across
		// hundreds of rounds (see compactEvery).
		if round%compactEvery == 0 {
			o.compactAll(ctx)
		}

		// Advance the circuit-breaker cooldown once per round (PRD-005).
		if o.breaker != nil {
			o.breaker.Tick()
		}

		select {
		case <-ctx.Done():
			return lastReport, nil
		case <-time.After(o.interval):
		}
	}
}

// runRound runs one Analyst → Risk → Executor pass and returns the
// executor's result. The typed handoffs let the orchestrator make
// deterministic decisions in Go — e.g. skip the Executor entirely when
// the Risk Manager approved nothing.
func (o *Orchestrator) runRound(ctx context.Context, round int, carry string) (ExecutionResult, error) {
	o.narrate(roleOrch, fmt.Sprintf("──────── Round %d ────────", round))

	// 1. Analyst.
	o.capAnalysis.reset()
	if _, err := o.analyst.Run(ctx, o.analystPrompt(round, carry)); err != nil {
		return ExecutionResult{}, fmt.Errorf("analyst run: %w", err)
	}
	var report AnalystReport
	if err := o.capAnalysis.into(&report); err != nil {
		return ExecutionResult{}, fmt.Errorf("analyst output: %w", err)
	}
	o.narrate(roleOrch, "Analyst → Risk Manager · "+summariseReport(report))

	// 2. Risk Manager.
	o.capRisk.reset()
	if _, err := o.risk.Run(ctx, o.riskPrompt(round, carry, report)); err != nil {
		return ExecutionResult{}, fmt.Errorf("risk run: %w", err)
	}
	var decisions RiskDecisions
	if err := o.capRisk.into(&decisions); err != nil {
		return ExecutionResult{}, fmt.Errorf("risk output: %w", err)
	}
	o.narrate(roleOrch, "Risk Manager → Executor · "+summariseDecisions(decisions))

	// Circuit breaker (PRD-005): feed it the live balance so it can capture
	// the starting balance and evaluate the daily-loss / drawdown thresholds
	// before the Executor places anything. RecordTrade is fed separately by
	// the log_trade tool on each close.
	if o.breaker != nil {
		o.breaker.Observe(decisions.Balance)
		o.narrate(roleOrch, "Breaker · "+o.breaker.Status())
		// PRD-021 §3: alert once per PAUSED/HALTED transition (not every round).
		o.notifyBreakerTransition()
	}

	// Deterministic short-circuit: no orders to place → skip the Executor.
	if !anyActionable(decisions) {
		rep := "No actionable trades this round. " + decisions.RiskNotes
		o.narrate(roleOrch, rep)
		res := ExecutionResult{Report: rep, Carry: carry}
		o.recordRound(report, decisions, res, false, round)
		return res, nil
	}

	// 3. Executor.
	o.capExec.reset()
	if _, err := o.executor.Run(ctx, o.executorPrompt(round, decisions)); err != nil {
		return ExecutionResult{}, fmt.Errorf("executor run: %w", err)
	}
	var execRes ExecutionResult
	if err := o.capExec.into(&execRes); err != nil {
		return ExecutionResult{}, fmt.Errorf("executor output: %w", err)
	}
	o.recordRound(report, decisions, execRes, true, round)
	return execRes, nil
}

// --- prompt builders (inject upstream handoffs as JSON) ---

func (o *Orchestrator) analystPrompt(round int, carry string) string {
	return fmt.Sprintf(
		"Round %d. Previous state: %s%s\n\nAnalyse %s from fresh data now, then call submit_analysis with all of them.",
		round, orFlat(carry), o.breakerLine(), symbolNames(o.symbols))
}

func (o *Orchestrator) riskPrompt(round int, carry string, r AnalystReport) string {
	j, _ := json.MarshalIndent(r, "", "  ")
	return fmt.Sprintf(
		"Round %d. Previous state: %s%s%s\n\nThe Analyst submitted this report:\n%s\n\nCompute caps from the live balance, run the mandatory risk checks on open positions, and call submit_risk_decisions with a decision for each symbol (%s).",
		round, orFlat(carry), o.breakerLine(), o.feeBudgetLine(), string(j), symbolNames(o.symbols))
}

// SetFeeBudget installs the shared fee budget so the Risk Manager round prompt
// can surface a status line when spend nears the cap (PRD-020 §3).
func (o *Orchestrator) SetFeeBudget(fb *risk.FeeBudget) { o.feeBudget = fb }

// SetNotifier installs the external notifier for session/breaker events
// (PRD-021 §3). nil disables orchestrator-side notifications.
func (o *Orchestrator) SetNotifier(n notify.Notifier) { o.notifier = n }

// SetPaper marks this session as paper-trading (PRD-021 §4) — tagged into the
// round log and session notifications.
func (o *Orchestrator) SetPaper(paper bool) { o.paper = paper }

// SetEndpoint records the venue endpoint for the session start notification.
func (o *Orchestrator) SetEndpoint(endpoint string) { o.endpoint = endpoint }

// SetRegimeSource installs the per-symbol regime lookup used to tag the round
// log (PRD-021 §2). bootstrap passes tool.RegimeFor.
func (o *Orchestrator) SetRegimeSource(fn func(symbol string) string) { o.regimeFor = fn }

// notifyf sends a notification when a channel is configured (best-effort).
func (o *Orchestrator) notifyf(title, body string) {
	if o.notifier == nil {
		return
	}
	if err := o.notifier.Notify(title, body); err != nil {
		o.narrate(roleOrch, fmt.Sprintf("notify failed: %v", err))
	}
}

// notifySessionStart announces the session (PRD-021 §3): how many symbols, on
// which endpoint, and whether it is a paper run.
func (o *Orchestrator) notifySessionStart() {
	mode := "LIVE"
	if o.paper {
		mode = "PAPER"
	}
	ep := o.endpoint
	if ep == "" {
		ep = "the configured endpoint"
	}
	o.notifyf("🚀 Friday started",
		fmt.Sprintf("%s mode — trading %d symbol(s) (%s) on %s", mode, len(o.symbols), symbolNames(o.symbols), ep))
}

// notifySessionStop sends a brief summary on clean shutdown (PRD-021 §3).
func (o *Orchestrator) notifySessionStop(rounds int, lastReport string) {
	body := fmt.Sprintf("Ran %d round(s).", rounds)
	if o.breaker != nil {
		body += " Breaker: " + o.breaker.Status() + "."
	}
	if r := strings.TrimSpace(lastReport); r != "" {
		body += "\nLast: " + truncateLine(r, 300)
	}
	o.notifyf("🛑 Friday stopped", body)
}

// truncateLine caps s at n runes for a notification body.
func truncateLine(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// notifyBreakerTransition fires ONE notification per transition into a
// PAUSED/HALTED state (PRD-021 §3) — not every round — by tracking the last
// state word it alerted on.
func (o *Orchestrator) notifyBreakerTransition() {
	if o.breaker == nil || o.notifier == nil {
		return
	}
	state := o.breaker.State().String()
	if state == o.lastBreakerState {
		return
	}
	prev := o.lastBreakerState
	o.lastBreakerState = state
	// Only alert on entering a degraded state, or recovering to NORMAL from one.
	switch state {
	case "PAUSED", "HALTED":
		o.notifyf("⚠️ Friday breaker "+state, o.breaker.Status())
	case "NORMAL":
		if prev == "PAUSED" || prev == "HALTED" {
			o.notifyf("✅ Friday breaker recovered", o.breaker.Status())
		}
	}
}

// feeBudgetLine renders the fee-budget status as a prompt fragment, but ONLY
// when spend is near the cap (≥50%) — otherwise empty, to avoid prompt noise.
func (o *Orchestrator) feeBudgetLine() string {
	if o.feeBudget == nil {
		return ""
	}
	if line, near := o.feeBudget.Status(); near {
		return "\n" + line
	}
	return ""
}

// feeWarningMarker tags the fee-budget warning line in the carry so it can be
// stripped and refreshed each round (rather than accumulating).
const feeWarningMarker = "⚠️ Fee budget:"

// carryWithFeeWarning refreshes the fee-budget warning in the threaded carry
// string (PRD-023 R3): it strips any prior warning, then appends a current one
// when spend is near the cap (Status().near). Stripping-then-appending keeps the
// carry from growing a warning line every round.
func (o *Orchestrator) carryWithFeeWarning(carry string) string {
	base := stripFeeWarning(carry)
	if o.feeBudget == nil {
		return base
	}
	line, near := o.feeBudget.Status()
	if !near {
		return base
	}
	warn := feeWarningMarker + " " + strings.TrimPrefix(line, "fee budget: ")
	if base == "" {
		return warn
	}
	return base + "\n" + warn
}

// stripFeeWarning removes any previously-appended fee-budget warning line(s)
// from a carry string so the warning is refreshed, not duplicated.
func stripFeeWarning(s string) string {
	if !strings.Contains(s, feeWarningMarker) {
		return s
	}
	lines := strings.Split(s, "\n")
	kept := lines[:0]
	for _, ln := range lines {
		if strings.Contains(ln, feeWarningMarker) {
			continue
		}
		kept = append(kept, ln)
	}
	return strings.TrimRight(strings.Join(kept, "\n"), "\n")
}

// breakerLine renders the circuit-breaker status as a prompt fragment, or
// empty when there is no breaker. When paused/halted the Risk Manager must
// restrict itself to CLOSE/WAIT (enforced by its system prompt).
func (o *Orchestrator) breakerLine() string {
	if o.breaker == nil {
		return ""
	}
	return "\nCircuit breaker: " + o.breaker.Status()
}

func (o *Orchestrator) executorPrompt(round int, d RiskDecisions) string {
	j, _ := json.MarshalIndent(d, "", "  ")
	return fmt.Sprintf(
		"Round %d. Execute these Risk Manager decisions EXACTLY (a <Thought> before each order), then call submit_execution:\n%s",
		round, string(j))
}

// narrate emits a synthetic text event tagged with role so the TUI shows
// the orchestrator's own pipeline narration inline with the agents' output.
func (o *Orchestrator) narrate(role, msg string) {
	if o.emitter == nil {
		return
	}
	o.emitter.EmitRole(role, event.Event{
		Kind: event.KindText,
		Text: &event.TextPayload{Text: msg},
	})
}

// compactAll triggers a full compaction on each role agent, summarising
// the accumulated session history into a context brief. This keeps the
// model sharp across hundreds of rounds by preventing the system prompt
// from being diluted by stale klines / old biases still in the transcript.
//
// Failures are logged but non-fatal — the next round will retry.
func (o *Orchestrator) compactAll(ctx context.Context) {
	o.narrate(roleOrch, "Compacting agent sessions (full) …")
	for _, ag := range []agent.Agent{o.analystAg, o.riskAg, o.executorAg} {
		if ag == nil {
			continue
		}
		if err := ag.Compact(ctx, "full"); err != nil {
			o.narrate(roleOrch, fmt.Sprintf("Compact failed: %v — will retry next cycle", err))
		}
	}
	o.narrate(roleOrch, "Compaction complete.")
}

// --- small helpers ---

func orFlat(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(none — first round)"
	}
	return s
}

func anyActionable(d RiskDecisions) bool {
	for _, dec := range d.Decisions {
		if dec.actionable() {
			return true
		}
	}
	return false
}

func summariseReport(r AnalystReport) string {
	parts := make([]string, 0, len(r.Symbols))
	for _, s := range r.Symbols {
		parts = append(parts, fmt.Sprintf("%s %s/%s", s.Symbol, s.Bias, s.Conviction))
	}
	return fmt.Sprintf("sentiment %s; %s", r.Sentiment, strings.Join(parts, ", "))
}

func summariseDecisions(d RiskDecisions) string {
	parts := make([]string, 0, len(d.Decisions))
	for _, dec := range d.Decisions {
		parts = append(parts, fmt.Sprintf("%s %s", dec.Symbol, dec.Action))
	}
	return strings.Join(parts, ", ")
}
