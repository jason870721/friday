# Friday — an agentic crypto-futures auto-trader

A quantitative trading agent for Binance USDⓈ-M perpetual futures, built on the
evva Go agent SDK (`github.com/johnny1110/evva`). Goal: stable profit. **Testnet
by default** (`BINANCE_BASE_URL`) — validate a full session before mainnet.

Covered markets are **config, not code**: `FRIDAY_SYMBOLS` (comma-separated) is
resolved at startup and validated against the venue's `exchangeInfo` — any symbol
not listed `TRADING` is dropped (`bootstrap.resolveSymbols`). The validated set
(with each symbol's `LOT_SIZE` step) is injected into the role prompts and the
`submit_*` schemas, so adding/removing a market needs no code change.

## Architecture (multi-agent)

Friday is **not** a single agent. A deterministic Go orchestrator
(`internal/orchestrator`) runs a three-role pipeline every 15s, passing typed
structs between roles:

```
Analyst → Risk Manager → Executor      (one round, every 15s)
```

- **Analyst** — reads multi-timeframe market data (`binance_mtf_klines`: 5m/1h/4h
  + cross-TF alignment + a 4h ADX regime read + a calibrated, regime-weighted
  strategy consensus), sentiment, and trade memory; emits an `AnalystReport`. It
  *validates* the deterministic strategy signal (overriding only with a cited
  reason) — it does not invent direction. No trading tools.
- **Risk Manager** — computes dynamic caps, runs the mandatory risk checks, sizes
  by ATR volatility (risk ÷ 2×ATR stop, within caps) and sets stops, or vetoes;
  emits `RiskDecisions`. No trading tools.
- **Executor** — places exactly the Risk Manager's orders, registers stops with
  the SL/TP monitor, logs closed trades; emits `ExecutionResult`.

Each handoff is a schema-validated `submit_*` tool call. The orchestrator owns
the loop and the 15s cadence (`orchestrator.Run`); only Ctrl+C stops it.

> **⚠️ Source of truth for trading logic.** The per-round mandate (iron rules,
> risk checks, caps, sizing, execution order, breaker awareness) lives in the
> three role prompts in `internal/orchestrator/prompts.go`. Do not duplicate it
> elsewhere — `.friday/skills/start/SKILL.md` deliberately doesn't restate it.

## Package layout

```
cmd/friday/             entry point: sink → bootstrap → stop monitor → bubbletea TUI
cmd/reconcile-memory/   one-off: rewrite trades.jsonl PnL/outcome from the income ledger
cmd/analyze/            session post-mortem: rounds.jsonl + trades.jsonl → text/JSON report
internal/bootstrap/     config/env load, symbol preflight, wires the orchestrator + guards
internal/orchestrator/  3-role pipeline, prompts, typed handoffs, round loop, round log (roundlog.go)
internal/binance/       Futures REST client (klines, orders, leverage brackets, income, …) + indicators
internal/strategy/      deterministic signal engine + aggregator (single-TF + MTF vote) + calibration/regime
internal/risk/          code-enforced guardrails (see Safety) + ATR sizing + stop monitor + paper book
internal/notify/        Discord/Telegram notifications (Notifier + NewFromEnv)
internal/memory/        file-backed vector trade-memory; exchange-reconciled PnL; per-strategy stats
internal/backtest/      sandbox simulator + strategy calibration / TP sweeps
internal/tool/          friday's custom tools (binance_*, recall_trades, log_trade, submit_*, …)
docs/PRD/               one PRD per deliverable; docs/roadmap.md is the index
.friday/skills/<n>/SKILL.md  Mandarin kickoff docs; the TUI turns each into a "/<name>" command
```

`internal/` is friday-private; only evva `pkg/*` is imported upstream.

## Safety systems (code-enforced, not just prompt)

These run in Go; the LLM cannot reason past them.

**`binance_order` pre-trade chain** — for OPENING orders only; reduce-only closes
bypass ALL of it (flattening risk must never be blocked), in this order:

1. **Circuit breaker** (`risk.CircuitBreaker`) — daily-loss / consecutive-loss →
   PAUSE; drawdown → HALT. Fed by `log_trade` + the orchestrator. Env:
   `FRIDAY_DAILY_LOSS_PCT`, `FRIDAY_MAX_CONSEC_LOSSES`, `FRIDAY_DRAWDOWN_HALT_PCT`,
   `FRIDAY_COOLDOWN_CYCLES`.
2. **Notional leverage clamp** *(adjusts, doesn't reject)* — auto-lowers leverage
   so the order's notional fits its bracket tier (avoids `-2027`); margin is then
   re-checked at the lower leverage.
3. **Fee budget** (`risk.FeeBudget`) — blocks a new OPEN once 30-min fee spend
   exceeds `FRIDAY_FEE_BUDGET_PCT` (default 0.5%) of balance.
4. **Portfolio group caps** (`risk.PortfolioGroupValidator`) — caps COMBINED
   margin per correlated group (crypto 30% / stocks 40%, `FRIDAY_GROUP_LIMITS`).
5. **Margin cap** (`risk.MarginCapValidator`) — rejects margin > 15% of balance.

**Stops** — armed via `binance_stop_monitor` after each OPEN as DUAL protection:
`risk.StopMonitor` (in-memory, polls mark price ~1s) AND native exchange
`STOP_MARKET`/`TAKE_PROFIT_MARKET` orders (survive a restart). `main` cancels
orphaned native stops at startup. Stops bypass the pre-trade chain.

**Leverage** — `binance_leverage` clamps an over-cap request to the symbol's max
(avoids `-4028`); the per-notional tier clamp is step 2 above.

**Truthful accounting** — `log_trade` reconciles each close against the
`/fapi/v1/income` ledger and stores the net (`pnl_source:"exchange"`); WIN/LOSS
and the breaker key off that, never the LLM's estimate. `cmd/reconcile-memory`
backfills old logs.

**Recall guard** — `recall_trades` reports "insufficient data" below
`memory.ConclusiveMinSamples` similar trades, so a thin all-loss sample can't
veto entries.

## Operations & observability

- **Round log** — every round's full Analyst→Risk→Executor outcome (incl.
  per-symbol regime) is appended to `~/.friday/memory/rounds.jsonl`
  (`orchestrator.RoundRecorder`); distinct from trade memory (CLOSED trades only).
- **Post-mortem** — `cmd/analyze` reports per-strategy / per-symbol / per-regime
  stats, Analyst accuracy, and the breaker timeline from those two logs.
- **Notifications** — `internal/notify` pushes session start/stop, breaker
  transitions, and large-PnL closes to Discord/Telegram when configured.
- **Online re-calibration** — `strategy.Recalibrator` re-runs the confidence
  sweep every `FRIDAY_RECALIBRATE_HOURS` so weights track regime shifts.
- **Paper mode** — `FRIDAY_PAPER=true` swaps real order placement for a virtual
  `risk.PaperPortfolio` (market data stays live; account endpoints never called).

## Build / run / test

```sh
go build ./...
go test ./...                  # every internal/* package has unit tests
go vet ./...
go run ./cmd/friday            # launches the TUI; paste the kickoff prompt from SKILL.md
go run ./cmd/analyze           # session post-mortem (-json, -rounds/-trades flags)
go run ./cmd/reconcile-memory  # dry-run PnL fix (-write to apply)
```

## Conventions

- **Testnet first** — never touch mainnet until a full testnet session validates.
- Each custom tool is its own file in `internal/tool/` (`New<X>()` + a
  `tools.Tool` impl); tools are registered per-role in `orchestrator.New`.
- Pure/deterministic logic (indicators, strategies, risk, backtest, memory) is
  unit-tested with fixtures; LLM-behavioural changes are verified with a short
  live testnet run.
- New cross-cutting config goes through env vars read in `bootstrap`; document
  the default in the `.env` template there.
- Match surrounding style; keep changes minimal and grounded in existing patterns.

## Roadmap

PRDs **001–023 are implemented**. See [docs/roadmap.md](docs/roadmap.md) for the
per-PRD index and status, and `docs/PRD/PRD-NNN.md` for each spec — the
Out-of-Scope sections there track deferred follow-ups.
