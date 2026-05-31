# Friday — an agentic crypto-futures auto-trader

A quantitative trading agent for Binance USDⓈ-M perpetual futures, built on
the evva Go agent SDK (`github.com/johnny1110/evva`). Goal: stable profit.
Testnet by default.

The covered markets are **configurable**, not hardcoded. `FRIDAY_SYMBOLS`
(comma-separated; default `BTCUSDT,ETHUSDT,SOLUSDT,…`) is resolved at
startup and **validated against the venue's `exchangeInfo`** — any symbol the
endpoint doesn't list as `TRADING` is logged and dropped, so the pipeline only
ever iterates markets that actually exist (see `bootstrap.resolveSymbols`). The
validated list (with each symbol's real `LOT_SIZE` step) is injected into the
three role prompts and the `submit_*` schemas; adding/removing a market is a
config change, not a code change.

## Architecture (multi-agent, post PRD-003)

Friday is **not** a single agent. A deterministic Go **orchestrator**
(`internal/orchestrator`) runs a three-role pipeline every 15s, passing
typed structs between roles:

```
Analyst → Risk Manager → Executor      (one round, every 15s)
```

- **Analyst** — reads market data (multi-timeframe 5m/1h/4h via
  `binance_mtf_klines` + cross-TF alignment + a 4h ADX **market-regime** read
  and a **regime-weighted, calibrated** strategy consensus) + sentiment +
  deterministic strategy signals + trade memory; emits an `AnalystReport`.
  Validates signals (anchors bias to the strategy consensus, overrides only
  with a cited reason). No trading tools.
- **Risk Manager** — computes dynamic caps, runs the mandatory risk
  checks, sizes positions by ATR volatility (risk ÷ 2×ATR stop, within the
  caps) / sets stops, or vetoes; emits `RiskDecisions`. No trading tools.
- **Executor** — places exactly the Risk Manager's orders (leverage →
  market order), registers the stop with the SL/TP monitor, logs closed
  trades; emits `ExecutionResult`.

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
cmd/friday/main.go            entry point: sink → bootstrap → stop monitor → bubbletea TUI
cmd/reconcile-memory/         one-off tool: rewrite trades.jsonl PnL/outcome from the exchange income ledger
internal/bootstrap/           config load, env, symbol resolution + exchangeInfo preflight, builds the orchestrator + circuit breaker
internal/orchestrator/        the 3-role pipeline, prompts, typed handoffs, round loop, per-round analysis log (roundlog.go)
internal/tui/                 bubbletea Model + role-tagged event rendering
internal/binance/             Binance Futures REST client (klines, orders, exchangeInfo, income ledger, leverage brackets, TradFi-Perps sign) + indicators (SMA, EMA, RSI, ADX, ATR, ClassifyDirection, SemanticSummary)
internal/strategy/            deterministic signal engine (momentum, breakout, mean-reversion, ema_cross, cross-symbol divergence) + aggregator (single-TF + MTF cross-timeframe vote) + startup confidence calibration store (PRD-015) + ADX regime detection & regime-weighted consensus (PRD-016) + MTF strategy consensus (PRD-017)
internal/risk/                MarginCapValidator (15% guardrail), CircuitBreaker (session safety), SuggestedSize (ATR sizing), StopMonitor (SL/TP poller)
internal/memory/              embedded vector trade-memory (file-backed, cosine similarity); PnL reconciled against the exchange ledger; per-strategy outcome stats (PRD-014)
internal/backtest/            sandbox simulator: rule-based (run_backtest) + strategy-aware RunStrategy/Calibrate for startup confidence calibration (PRD-015)
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
- **Stop-loss/TP monitor** — `risk.StopMonitor` (PRD-009): a goroutine started
  in `main.go` polling mark price ~every 1s, firing reduce-only closes the
  instant a registered level breaks — a fast backstop independent of the 15s
  loop. The Executor registers levels via `binance_stop_monitor` after each
  OPEN (using PRD-007's 2×ATR stop). In-memory only (no persistence across
  restarts); bypasses the gates so flattening always succeeds.
- **Per-symbol leverage caps** — `binance.MaxLeverages` (leverageBracket) is
  resolved at startup, shown to the Risk Manager in-prompt as each symbol's
  `≤Nx`, and `binance_leverage` clamps an over-cap request down to the symbol's
  max (PRD-012) — so a 100× ask on a 10× stock perp is corrected, not rejected
  with `-4028`. The 15% margin guardrail stays reject-and-report (no auto-resize).
- **Per-notional leverage tier clamp** — leverage brackets are tiered by
  notional (the max leverage only covers the *smallest* tier). `binance_order`
  computes the order's notional and auto-lowers leverage to the tier it falls
  into (`binance.LeverageBrackets` / `MaxLeverageForNotional`) *before* the
  order, so a position can't exceed the tier its leverage allows and fail with
  `-2027` (PRD-019). Margin is re-validated at the lower leverage; the Risk
  Manager prompt shows each symbol's `≤$Xk @max-lev` notional ceiling.
- **Per-round analysis log** — every round's full Analyst→Risk→Executor
  outcome (sentiment, per-symbol bias/conviction/setups, the Risk Manager's
  numeric decisions + notes, balance, breaker status, whether the Executor ran,
  and the report/carry) is appended as one JSON line to
  `~/.friday/memory/rounds.jsonl` by `orchestrator.RoundRecorder` (roundlog.go) —
  the same append-only JSONL format as `trades.jsonl`, so it loads into jq/pandas
  for offline analysis. Written on BOTH the actionable and the all-WAIT
  short-circuit paths; non-fatal on write failure. Distinct from trade memory,
  which only records CLOSED trades.
- **PnL is exchange-truth, not agent-reported** — `log_trade` reconciles a
  closed trade against the `/fapi/v1/income` ledger (realised PnL − commission
  − funding) and stores the true net (`pnl_source:"exchange"`); WIN/LOSS and
  the circuit breaker both key off that net, never the LLM's estimate (which
  was unreliable). `cmd/reconcile-memory` backfills the same correction onto an
  existing `trades.jsonl`.

## Roadmap status (see docs/roadmap.md)

Implemented & verified: **PRD-001** (semantic klines + ReAct),
**PRD-002** (sentiment + margin guardrail), **PRD-003** (multi-agent
refactor), **PRD-004** (vector memory + backtest), **PRD-005** (circuit
breakers), **PRD-006** (strategy layer), **PRD-007** (ATR position sizing),
**PRD-008** (multi-timeframe analysis), **PRD-009** (stop-loss/TP execution
monitor), plus operational hardening **PRD-010** (configurable venue-validated
symbols), **PRD-011** (exchange-truth PnL reconciliation), **PRD-012**
(per-symbol leverage caps), **PRD-019** (per-notional leverage tier clamp), and
**PRD-013** (strategy portfolio expansion: EMA crossover + live divergence
wiring), **PRD-014** (per-strategy performance tracking), **PRD-015** (startup
confidence calibration from backtested win rates), **PRD-016** (ADX market
regime detection + regime-weighted strategy consensus), **PRD-017** (MTF
strategy consensus — the strategy engine on 5m/1h/4h, combined into one weighted
cross-timeframe vote), and **PRD-018** (strategy-aware exits — each strategy's
invalidation level surfaced as a candidate stop).

All PRDs (001–019) are implemented; the P2 strategy-engine tranche (013–018) is
complete. Future work lives in the Out-of-Scope sections of the individual PRDs
(e.g. per-TF calibration, strategy-specific take-profits, exchange-native
STOP_MARKET orders, fee/churn budgeting).

## Build / run / test

```sh
go build ./...
go test ./...                          # all internal/* packages have unit tests
go run ./cmd/friday                    # launches the TUI; paste the kickoff prompt from SKILL.md
go run ./cmd/reconcile-memory          # dry-run: fix trades.jsonl PnL from the exchange ledger (-write to apply)
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
