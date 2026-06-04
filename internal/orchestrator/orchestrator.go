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

	// maxRounds bounds the loop for a headless batch run (0 = unbounded, the
	// normal live mode). Set via SetMaxRounds before Run.
	maxRounds int

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

	// consecutiveNeutral counts back-to-back non-actionable rounds (PRD-024 R9).
	// Once it reaches neutralWarnAfter the carry carries an anti-degradation
	// warning so the Analyst keeps producing real analysis through long lulls.
	consecutiveNeutral int

	// lastTradeSummary is the most recent position snapshot from an executed
	// round (e.g. "ETH SHORT @1781.17 → +$11.73") — surfaced in the neutral
	// warning so the Analyst remembers what an actual trade looks like.
	lastTradeSummary string

	// lastCloseCall describes the most recent round where a signal was close
	// but not quite actionable (e.g. a non-NEUTRAL bias with setups that the
	// Risk Manager WAITed on).
	lastCloseCall string

	// lastNeutralNotified is the highest consecutive-NEUTRAL milestone already
	// alerted on for the CURRENT streak (PRD-024). Reset to 0 whenever an
	// actionable round resets consecutiveNeutral, so each new streak re-alerts
	// from the first milestone. Per-instance (not a package global) so sessions
	// don't share state.
	lastNeutralNotified int

	// mtfStreak tracks, per symbol, the current run of consecutive rounds the
	// MTF Strategy has held the same non-NEUTRAL direction (the signal-
	// persistence gate). Injected into the Analyst prompt so a fresh/flickering
	// ×1 signal is held back until it confirms (×2+), killing flicker re-entries.
	mtfStreak map[string]mtfStreakEntry
}

// mtfStreakEntry is one symbol's MTF-direction persistence run.
type mtfStreakEntry struct {
	dir   string // LONG / SHORT / NEUTRAL
	count int    // consecutive rounds at this non-NEUTRAL direction (0 when NEUTRAL)
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

		// PRD-024 R9: on a long NEUTRAL streak, warn the next round so the Analyst
		// stays vigilant and keeps producing real analysis instead of "凍結".
		carry = o.carryWithNeutralWarning(carry)

		// Periodic full compaction: prevent session bloat across
		// hundreds of rounds (see compactEvery).
		if round%compactEvery == 0 {
			o.compactAll(ctx)
		}

		// Advance the circuit-breaker cooldown once per round (PRD-005).
		if o.breaker != nil {
			o.breaker.Tick()
		}

		// Headless batch bound: stop after maxRounds (0 = unbounded live mode),
		// skipping the trailing inter-round sleep.
		if o.maxRounds > 0 && round >= o.maxRounds {
			return lastReport, nil
		}

		select {
		case <-ctx.Done():
			return lastReport, nil
		case <-time.After(o.interval):
		}
	}
}

// SetMaxRounds bounds Run to n rounds then return (0 = unbounded). For a
// headless batch run; call before Run.
func (o *Orchestrator) SetMaxRounds(n int) { o.maxRounds = n }

// SetInterval overrides the inter-round delay (default 15s). Pass 0 to run
// rounds back-to-back in a headless batch. Call before Run.
func (o *Orchestrator) SetInterval(d time.Duration) { o.interval = d }

// runRound runs one Analyst → Risk → Executor pass and returns the
// executor's result. The typed handoffs let the orchestrator make
// deterministic decisions in Go — e.g. skip the Executor entirely when
// the Risk Manager approved nothing.
func (o *Orchestrator) runRound(ctx context.Context, round int, carry string) (ExecutionResult, error) {
	o.narrate(roleOrch, fmt.Sprintf("──────── Round %d ────────", round))

	// 1. Analyst.
	o.capAnalysis.reset()

	// Preload MTF data for all symbols so the Analyst doesn't spend 7 tool-call
	// turns on the most expensive operation (each mtf_klines fetches 96+24+48
	// candles AND runs the strategy engine). The prompt already contains this
	// data; the Analyst only needs fast tools (price, funding, recall).
	mtfData := o.preloadMTF(ctx)
	// PRD-024 review fix: update the per-symbol MTF-direction persistence streaks
	// from this round's data and inject them so the Analyst holds back fresh
	// (×1) signals until they confirm (the signal-persistence gate).
	o.updateMTFStreaks(parseMTFDirections(mtfData, o.symbols))
	if _, err := o.analyst.Run(ctx, o.analystPrompt(round, carry, mtfData, o.persistenceLine())); err != nil {
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
		o.consecutiveNeutral++      // PRD-024 R9: track the NEUTRAL streak for the carry warning
		o.captureCloseCall(report)  // capture "close but not quite" signals for the carry warning
		o.notifyNeutralStreak()     // alert operator on long idle periods
		rep := "No actionable trades this round. " + decisions.RiskNotes
		o.narrate(roleOrch, rep)
		res := ExecutionResult{Report: rep, Carry: carry}
		o.recordRound(report, decisions, res, false, round)
		return res, nil
	}
	o.consecutiveNeutral = 0  // PRD-024 R9: an actionable round breaks the streak
	o.lastNeutralNotified = 0 // …and re-arms the per-streak milestone alerts

	// 3. Executor.
	o.capExec.reset()
	if _, err := o.executor.Run(ctx, o.executorPrompt(round, decisions)); err != nil {
		return ExecutionResult{}, fmt.Errorf("executor run: %w", err)
	}
	var execRes ExecutionResult
	if err := o.capExec.into(&execRes); err != nil {
		return ExecutionResult{}, fmt.Errorf("executor output: %w", err)
	}
	o.captureLastTrade(execRes.Carry) // PRD-024: remember the trade for the neutral warning
	o.notifyTradeOpened(decisions)    // alert operator when a new position is entered
	o.recordRound(report, decisions, execRes, true, round)
	return execRes, nil
}

// --- prompt builders (inject upstream handoffs as JSON) ---

func (o *Orchestrator) analystPrompt(round int, carry, mtfData, persistence string) string {
	return fmt.Sprintf(
		"Round %d. Previous state: %s%s\n\n--- Pre-loaded MTF data (already fetched — do NOT call binance_mtf_klines) ---\n%s\n%s\n--- End MTF data ---\n\nAnalyse %s from the pre-loaded data above, using the other tools (price, ticker, funding, fear_greed_index, position, recall_trades) for supplementary data. Then call submit_analysis with all of them.",
		round, orFlat(carry), o.breakerLine(), mtfData, persistence, symbolNames(o.symbols))
}

// parseMTFDirections extracts each symbol's current MTF Strategy direction
// (LONG/SHORT/NEUTRAL) from the pre-loaded MTF text block. Each symbol's section
// starts with "<SYM> multi-timeframe read:" and carries a "MTF Strategy: <DIR> …"
// line; a symbol with no such line (data unavailable) is reported NEUTRAL.
func parseMTFDirections(mtfData string, symbols []MarketSymbol) map[string]string {
	dirs := make(map[string]string, len(symbols))
	cur := ""
	for _, ln := range strings.Split(mtfData, "\n") {
		if i := strings.Index(ln, " multi-timeframe read:"); i > 0 {
			cur = strings.TrimSpace(ln[:i])
			continue
		}
		trimmed := strings.TrimSpace(ln)
		if cur != "" && strings.HasPrefix(trimmed, "MTF Strategy:") {
			rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "MTF Strategy:"))
			switch {
			case strings.HasPrefix(rest, "LONG"):
				dirs[cur] = "LONG"
			case strings.HasPrefix(rest, "SHORT"):
				dirs[cur] = "SHORT"
			default:
				dirs[cur] = "NEUTRAL"
			}
			cur = ""
		}
	}
	return dirs
}

// updateMTFStreaks advances each symbol's MTF-direction persistence run from this
// round's parsed directions: a held non-NEUTRAL direction increments the count, a
// flip restarts at 1, and NEUTRAL (or missing data) resets to 0.
func (o *Orchestrator) updateMTFStreaks(dirs map[string]string) {
	if o.mtfStreak == nil {
		o.mtfStreak = make(map[string]mtfStreakEntry, len(o.symbols))
	}
	for _, s := range o.symbols {
		d := dirs[s.Name]
		if d == "" {
			d = "NEUTRAL"
		}
		st := o.mtfStreak[s.Name]
		switch {
		case d == "NEUTRAL":
			st = mtfStreakEntry{dir: "NEUTRAL", count: 0}
		case st.dir == d:
			st.count++
		default:
			st = mtfStreakEntry{dir: d, count: 1}
		}
		o.mtfStreak[s.Name] = st
	}
}

// persistenceLine renders the "Signal persistence:" prompt line the
// signal-persistence gate reads: one entry per symbol with an ACTIVE directional
// streak, tagged "confirmed" at ≥2 rounds or "unconfirmed — WAIT" at ×1.
func (o *Orchestrator) persistenceLine() string {
	parts := make([]string, 0, len(o.symbols))
	for _, s := range o.symbols {
		st := o.mtfStreak[s.Name]
		if st.count == 0 || st.dir == "" || st.dir == "NEUTRAL" {
			continue
		}
		tag := "unconfirmed — WAIT"
		if st.count >= 2 {
			tag = "confirmed"
		}
		parts = append(parts, fmt.Sprintf("%s %s ×%d (%s)", s.Name, st.dir, st.count, tag))
	}
	if len(parts) == 0 {
		return "Signal persistence: all symbols NEUTRAL / no active MTF streak this round."
	}
	return "Signal persistence: " + strings.Join(parts, "; ")
}

// preloadMTF fetches the multi-timeframe read for every symbol concurrently in
// Go and returns the combined text block for injection into the Analyst prompt.
// This eliminates 7 sequential LLM tool-call turns (the most expensive operation
// per round) — the LLM reads the data directly instead of calling the tool.
func (o *Orchestrator) preloadMTF(ctx context.Context) string {
	type result struct {
		symbol string
		text   string
		err    error
	}
	ch := make(chan result, len(o.symbols))
	for _, sym := range o.symbols {
		go func(s string) {
			text, err := tool.FetchMTF(ctx, s)
			ch <- result{s, text, err}
		}(sym.Name)
	}

	var b strings.Builder
	for range o.symbols {
		r := <-ch
		if r.err != nil {
			fmt.Fprintf(&b, "[%s] MTF data unavailable: %v\n\n", r.symbol, r.err)
			continue
		}
		b.WriteString(r.text)
		b.WriteString("\n")
	}
	return b.String()
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
	o.notifyf("🚀 Friday 啟動",
		fmt.Sprintf("%s 模式 — 交易 %d 個標的（%s）於 %s", mode, len(o.symbols), symbolNames(o.symbols), ep))
}

// notifySessionStop sends a brief summary on clean shutdown (PRD-021 §3).
func (o *Orchestrator) notifySessionStop(rounds int, lastReport string) {
	body := fmt.Sprintf("已執行 %d 輪。", rounds)
	if o.breaker != nil {
		body += " 熔斷器: " + o.breaker.Status() + "。"
	}
	if r := strings.TrimSpace(lastReport); r != "" {
		body += "\n最後一輪: " + truncateLine(r, 300)
	}
	o.notifyf("🛑 Friday 關閉", body)
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
		o.notifyf("⚠️ Friday 熔斷: "+state, o.breaker.Status())
	case "NORMAL":
		if prev == "PAUSED" || prev == "HALTED" {
			o.notifyf("✅ Friday 熔斷恢復", o.breaker.Status())
		}
	}
}

// notifyTradeOpened sends a notification for each new position opened this round
// (PRD-024). An OPEN_LONG or OPEN_SHORT decision that the Executor acted on
// warrants an immediate alert — the operator shouldn't need to watch the TUI to
// know the system is in a trade.
func (o *Orchestrator) notifyTradeOpened(d RiskDecisions) {
	if o.notifier == nil {
		return
	}
	for _, dec := range d.Decisions {
		if dec.Action != "OPEN_LONG" && dec.Action != "OPEN_SHORT" {
			continue
		}
		dir := "LONG"
		if dec.Action == "OPEN_SHORT" {
			dir = "SHORT"
		}
		title := fmt.Sprintf("🔔 Friday 開倉: %s %s", dec.Symbol, dir)
		body := fmt.Sprintf("%s %s 數量=%.4f 槓桿=%dx 止損=%.2f — %s",
			dir, dec.Symbol, dec.Quantity, dec.Leverage, dec.StopLoss, dec.Reason)
		o.notifyf(title, body)
	}
}

// neutralStreakMilestones are the consecutive-NEUTRAL counts at which the
// operator is alerted (PRD-024). 50 rounds ≈ 12.5 min of silence; 100 ≈ 25 min.
var neutralStreakMilestones = []int{10, 20, 30, 40, 50, 75, 100}

// notifyNeutralStreak alerts the operator when the system has been idle for an
// extended period — a possible sign of a broken strategy engine or a structural
// market shift that warrants investigation. The body includes the last known
// close-call signal and trade so the operator has context without opening the TUI.
func (o *Orchestrator) notifyNeutralStreak() {
	if o.notifier == nil {
		return
	}
	for _, m := range neutralStreakMilestones {
		if o.consecutiveNeutral == m && m > o.lastNeutralNotified {
			o.lastNeutralNotified = m
			title := fmt.Sprintf("⏳ Friday: %d rounds without a trade", m)
			body := fmt.Sprintf("已連續 %d 輪無交易（~%d 分鐘）。", m, m*15/60)
			if o.lastTradeSummary != "" {
				body += fmt.Sprintf(" 上次交易: %s。", o.lastTradeSummary)
			}
			if o.lastCloseCall != "" {
				body += fmt.Sprintf(" 最近訊號: %s。", o.lastCloseCall)
			}
			body += " 若此閒置時間超出預期，請檢查策略引擎或市場狀態是否改變。"
			o.notifyf(title, body)
			return
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

// neutralWarnAfter is the consecutive-NEUTRAL streak length that triggers the
// anti-degradation carry warning (PRD-024 R9).
const neutralWarnAfter = 10

// neutralWarningMarker tags the anti-degradation warning line in the carry so it
// can be stripped and refreshed each round (rather than accumulating).
const neutralWarningMarker = "⚠️ 已連續"

// carryWithNeutralWarning refreshes the long-NEUTRAL-streak warning in the carry
// (PRD-024 R9): it strips any prior warning, then appends a current one when the
// streak has reached neutralWarnAfter. Strip-then-append keeps the carry from
// growing a warning line every round (same pattern as carryWithFeeWarning).
// When available, it also surfaces the last trade and last close-call signal so
// the Analyst has concrete context beyond just a counter.
func (o *Orchestrator) carryWithNeutralWarning(carry string) string {
	base := stripNeutralWarning(carry)
	if o.consecutiveNeutral < neutralWarnAfter {
		return base
	}
	warn := fmt.Sprintf("%s %d 輪無交易。", neutralWarningMarker, o.consecutiveNeutral)
	if o.lastTradeSummary != "" {
		warn += fmt.Sprintf(" 上次交易: %s。", o.lastTradeSummary)
	}
	if o.lastCloseCall != "" {
		warn += fmt.Sprintf(" 最近訊號: %s。", o.lastCloseCall)
	}
	warn += " 市場可能在醞釀突破——請保持警惕，不要因長期觀望而降低分析品質。"
	if base == "" {
		return warn
	}
	return base + "\n" + warn
}

// captureCloseCall extracts a "close but not quite" signal summary from the
// Analyst report when the round produces no actionable trades (PRD-024 R9).
// It looks for symbols with a non-NEUTRAL bias and setups — a signal the Risk
// Manager WAITed on, worth reminding the Analyst about on long NEUTRAL streaks.
func (o *Orchestrator) captureCloseCall(r AnalystReport) {
	var best SymbolAnalysis
	for _, s := range r.Symbols {
		if s.Bias == "NEUTRAL" || len(s.Setups) == 0 {
			continue
		}
		if len(s.Setups) > len(best.Setups) {
			best = s
		}
	}
	if best.Symbol == "" {
		return
	}
	setup := strings.Join(best.Setups, ", ")
	if len(setup) > 120 {
		setup = setup[:117] + "..."
	}
	o.lastCloseCall = fmt.Sprintf("%s %s (%s)", best.Symbol, best.Bias, setup)
}

// captureLastTrade extracts a one-line trade summary from the executor's carry
// string (PRD-024 R9). The carry contains position state like
// "ETH: SHORT qty=1.468 entry=1781.17 peak=+$11.73 | BTC: FLAT | ...".
// It picks the first non-FLAT entry as the most recent trade.
func (o *Orchestrator) captureLastTrade(carry string) {
	if carry == "" {
		return
	}
	for _, part := range strings.Split(carry, "|") {
		part = strings.TrimSpace(part)
		if part == "" || strings.HasSuffix(part, "FLAT") {
			continue
		}
		// "SYM: DIR qty=X.XX entry=Y.YY peak=+$Z.ZZ" — keep it concise.
		o.lastTradeSummary = part
		return
	}
}

// stripNeutralWarning removes any previously-appended NEUTRAL-streak warning
// line(s) from a carry string so the warning is refreshed, not duplicated.
func stripNeutralWarning(s string) string {
	if !strings.Contains(s, neutralWarningMarker) {
		return s
	}
	lines := strings.Split(s, "\n")
	kept := lines[:0]
	for _, ln := range lines {
		if strings.Contains(ln, neutralWarningMarker) {
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
