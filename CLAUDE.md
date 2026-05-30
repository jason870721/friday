# Friday — an agentic crypto-futures auto-trader

A quantitative trading agent for Binance USDⓈ-M perpetual futures
(BTCUSDT / ETHUSDT / SOLUSDT), built on the evva Go agent SDK
(`github.com/johnny1110/evva`). Goal: stable profit. Testnet by default.

## Architecture (multi-agent, post PRD-003)

Friday is **not** a single agent. A deterministic Go **orchestrator**
(`internal/orchestrator`) runs a three-role pipeline every 15s, passing
typed structs between roles:

```
Analyst → Risk Manager → Executor      (one round, every 15s)
```

- **Analyst** — reads market data + sentiment + deterministic strategy
  signals + trade memory; emits an `AnalystReport`. Validates signals
  (anchors bias to the strategy consensus, overrides only with a cited
  reason). No trading tools.
- **Risk Manager** — computes dynamic caps, runs the mandatory risk
  checks, sizes positions / sets stops, or vetoes; emits `RiskDecisions`.
  No trading tools.
- **Executor** — places exactly the Risk Manager's orders (leverage →
  market order), logs closed trades; emits `ExecutionResult`.

Each handoff is captured via a schema-validated `submit_*` tool. The
**orchestrator owns the loop and the 15s cadence** (`orchestrator.Run`,
`time.After`) — `schedule_wakeup` is no longer used. Only Ctrl+C
(context cancel) stops it.

### ⚠️ Source of truth for trading logic

The per-round mandate (iron rules, risk checks, caps, sizing, execution
order, breaker awareness) lives in the **three role system prompts** in
`internal/orchestrator/prompts.go`. Do not duplicate that logic elsewhere
(e.g. `.friday/skills/start/SKILL.md` deliberately does not restate it).

## Package layout

```
cmd/friday/main.go            entry point: sink → bootstrap → bubbletea TUI
internal/bootstrap/           config load, env, builds the orchestrator + circuit breaker
internal/orchestrator/        the 3-role pipeline, prompts, typed handoffs, round loop
internal/tui/                 bubbletea Model + role-tagged event rendering
internal/binance/             Binance Futures REST client + indicators (SMA, RSI, ADX, ATR*, SemanticSummary)
internal/strategy/            deterministic signal engine (momentum, breakout, mean-reversion, divergence) + aggregator
internal/risk/                MarginCapValidator (15% guardrail), CircuitBreaker (session safety)
internal/memory/              embedded vector trade-memory (file-backed, cosine similarity)
internal/backtest/            sandbox strategy simulator over historical klines
internal/tool/                friday's custom tools (binance_*, fear_greed_index, recall_trades, run_backtest, log_trade, submit_* via orchestrator)
docs/PRD/                     one PRD per deliverable; docs/roadmap.md is the index
.friday/skills/start/SKILL.md startup / kickoff doc (Mandarin)
```

`internal/` is friday-private; only evva `pkg/*` is imported upstream.

## Safety systems (code-enforced, not just prompt)

- **Per-trade guardrail** — `risk.MarginCapValidator` blocks any opening
  order whose margin > 15% of balance, in `binance_order` before the call.
- **Session circuit breaker** — `risk.CircuitBreaker`: daily-loss / 
  consecutive-loss → PAUSE; drawdown → HALT. Checked in `binance_order`
  (before the guardrail), fed by `log_trade` (RecordTrade) and the
  orchestrator (Observe/Tick). Env-tunable: `FRIDAY_DAILY_LOSS_PCT`,
  `FRIDAY_MAX_CONSEC_LOSSES`, `FRIDAY_DRAWDOWN_HALT_PCT`,
  `FRIDAY_COOLDOWN_CYCLES`.
- Reduce-only closes always bypass both gates.

## Roadmap status (see docs/roadmap.md)

Implemented & verified: **PRD-001** (semantic klines + ReAct),
**PRD-002** (sentiment + margin guardrail), **PRD-003** (multi-agent
refactor), **PRD-004** (vector memory + backtest), **PRD-005** (circuit
breakers), **PRD-006** (strategy layer).

Planned (P1): **PRD-007** ATR position sizing, **PRD-008** multi-timeframe
analysis, **PRD-009** stop-loss/TP execution monitor. (`ATR` in the
indicator list above is added by PRD-007 — not yet present.)

## Build / run / test

```sh
go build ./...
go test ./...          # all internal/* packages have unit tests
go run ./cmd/friday    # launches the TUI; paste the kickoff prompt from SKILL.md
```

## Conventions

- **Testnet first**: `BINANCE_BASE_URL` defaults to the testnet; validate
  a full session before touching mainnet.
- Each custom tool lives in its own file in `internal/tool/`, exposes a
  `New<X>()` + a `tools.Tool` impl; tools are registered per-role in
  `orchestrator.New` (not in `bootstrap.go`).
- Pure/deterministic logic (indicators, strategies, risk, backtest, memory)
  is unit-tested with fixtures; LLM-behavioural changes are verified with a
  short live testnet run.
- Match surrounding style; keep changes minimal and grounded in existing
  patterns.
