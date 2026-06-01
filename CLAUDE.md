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
cmd/analyze/                  post-mortem analyser (PRD-021): reads rounds.jsonl + trades.jsonl → 6-section text/JSON report
internal/bootstrap/           config load, env, symbol resolution + exchangeInfo preflight, builds the orchestrator + circuit breaker + guards/notifier/paper
internal/orchestrator/        the 3-role pipeline, prompts, typed handoffs, round loop, per-round analysis log (roundlog.go), session/breaker notifications
internal/tui/                 bubbletea Model + role-tagged event rendering + "/<name>" slash-skill commands (skills.go)
internal/binance/             Binance Futures REST client (klines, orders incl. STOP_MARKET/TAKE_PROFIT_MARKET/cancel/openOrders, exchangeInfo, income ledger, leverage brackets, TradFi-Perps sign) + indicators (SMA, EMA, RSI, ADX, ATR, BollingerBands, ClassifyDirection, SemanticSummary)
internal/strategy/            deterministic signal engine (momentum, breakout, mean-reversion, ema_cross, bollinger, cross-symbol divergence) + aggregator (single-TF + MTF cross-timeframe vote) + startup confidence calibration store (PRD-015) + ADX regime detection & regime-weighted consensus (PRD-016) + MTF strategy consensus (PRD-017) + online recalibration goroutine (PRD-020) + RSI extreme-zone filter & MTF tuning (PRD-022, rsi_filter.go)
internal/risk/                MarginCapValidator (15% guardrail), CircuitBreaker (session safety), FeeBudget (anti-overtrading), PortfolioGroupValidator (correlation-group caps), SuggestedSize (ATR sizing), StopMonitor (SL/TP poller), PaperPortfolio (paper-trading book, PRD-021)
internal/notify/              external notifications (PRD-021): Notifier interface + Discord/Telegram/Multi + NewFromEnv
internal/memory/              embedded vector trade-memory (file-backed, cosine similarity); PnL reconciled against the exchange ledger; per-strategy outcome stats (PRD-014); recall conclusiveness threshold (PRD-023, SimilarConclusive)
internal/backtest/            sandbox simulator: rule-based (run_backtest) + strategy-aware RunStrategy/Calibrate for startup confidence calibration (PRD-015) + per-strategy TP sweep (PRD-020)
internal/tool/                friday's custom tools (binance_*, fear_greed_index, recall_trades, run_backtest, log_trade, submit_* via orchestrator); paper-mode interception + notifier/guard globals (guards.go)
docs/PRD/                     one PRD per deliverable; docs/roadmap.md is the index
.friday/skills/<name>/SKILL.md startup/kickoff docs (Mandarin); the TUI turns each into a "/<name>" command — frontmatter `prompt:` is what it sends (e.g. `/start`)
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
  OPEN (using PRD-007's 2×ATR stop). In-memory only; bypasses the gates so
  flattening always succeeds.
- **Native exchange stops** — `binance_stop_monitor` ALSO places server-side
  `STOP_MARKET` (stop-loss) + `TAKE_PROFIT_MARKET` (take-profit) reduce-only
  orders (PRD-020 §2: `binance.StopMarketOrder`/`TakeProfitMarketOrder`,
  `timeInForce=GTC`, `workingType=MARK_PRICE`) — these survive a friday crash/
  restart, unlike the in-memory monitor. Dual protection: native order + local
  poller. Re-arming a symbol cancels its prior native orders; clearing
  (stop/tp=0) cancels them. `main` runs `tool.CleanupOrphanStops` at startup to
  cancel native stops left by a previous session whose position is gone.
- **Fee budget (anti-overtrading)** — `risk.FeeBudget` (PRD-020 §3): a rolling
  30-min window of fee spend; `binance_order` blocks a new OPEN once windowed
  fees exceed `FRIDAY_FEE_BUDGET_PCT` (default 0.5%) of balance, fed by
  `log_trade` with the exchange-reconciled commission+funding. Reduce-only
  closes bypass. The Risk Manager round prompt surfaces a status line when near.
- **Portfolio correlation-group caps** — `risk.PortfolioGroupValidator`
  (PRD-020 §4): caps the COMBINED margin of correlated symbols (`crypto`
  BTC/ETH/SOL 30%, `stocks` NVDA/GOOGL/AMZN/META 40%, tunable via
  `FRIDAY_GROUP_LIMITS`) so "7 positions" aren't really two concentrated bets.
  `binance_order` sums the group's existing open margin and blocks an OPEN that
  breaches the cap; the same `GroupLimits` is rendered into the Risk Manager
  prompt. Reduce-only and ungrouped symbols pass.
- **Online re-calibration** — `strategy.Recalibrator` (PRD-020 §5): a goroutine
  that re-runs the PRD-015 confidence sweep on fresh 4h candles every
  `FRIDAY_RECALIBRATE_HOURS` (default 4; 0 disables) so confidences track regime
  shifts. Failures keep the existing confidences.
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

## Operations & observability (PRD-021)

- **Post-mortem tool** — `cmd/analyze` reads `rounds.jsonl` + `trades.jsonl` and
  prints a 6-section report (session overview; per-strategy / per-symbol /
  per-regime stats with win rate, total/avg PnL and profit factor; Analyst
  directional accuracy; breaker timeline). `-json` for structured output,
  `-rounds`/`-trades` to override paths; missing/empty files degrade to zeros.
  Trades are attributed to a regime via the round they were OPENED in (the round
  log now records each symbol's regime, sourced from `binance_mtf_klines`).
- **External notifications** — `internal/notify` (`Notifier` + Discord/Telegram/
  Multi + `NewFromEnv`). The orchestrator fires session start/stop and
  breaker PAUSED↔HALTED↔NORMAL **transitions** (deduped via `lastBreakerState`,
  once per transition); `log_trade` fires a large-PnL alert when a close's net
  ≥ `FRIDAY_NOTIFY_PNL_PCT` (default 5%) of balance. nil notifier = no-op.
- **Paper trading** — `FRIDAY_PAPER=true` installs a `risk.PaperPortfolio`
  (virtual balance `FRIDAY_PAPER_BALANCE`, default 1000). The trading tools
  (`binance_order`/`binance_leverage`/`binance_close_all`/`binance_stop_monitor`)
  become no-ops that log "PAPER: would have…" and update the virtual book;
  `binance_position`/`binance_balance` return virtual state; **account endpoints
  are never called**; market-data tools stay live. The StopMonitor runs against
  a paper broker (real mark price, virtual close). Round/trade logs are tagged
  `paper:true`. A banner prints at startup.

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

All PRDs (001–022) are implemented; the P2 strategy-engine tranche (013–018)
and the P3 tranche (020 production hardening + 021 operations & observability)
are complete. **PRD-020** added native exchange STOP_MARKET/TAKE_PROFIT_MARKET
stops, a fee-budget guardrail, portfolio correlation-group caps, online
re-calibration, strategy-specific take-profits, and a Bollinger Band strategy
(now 5 single-symbol votes). **PRD-021** added the `cmd/analyze` post-mortem
tool, Discord/Telegram notifications, and a `FRIDAY_PAPER` paper-trading mode.
**PRD-022** (PnL-analysis-driven signal hardening) added a global RSI
extreme-zone filter and tuned the MTF vote (lower hysteresis, a 5m+1h override,
and a 4h hard veto). **PRD-023** (Analyst decision quality) added three
Analyst-prompt rules (regime-aware bias clamp, fee-aware sizing, recall
sample-size), surfaced the fee budget into the round carry when near the cap,
and added a recall minimum-sample threshold (`memory.SimilarConclusive` /
`ConclusiveMinSamples`; `recall_trades` reports "insufficient data" on a thin
pool so a 2-3-trade all-loss sample can't veto entries). Future work lives in
the Out-of-Scope sections of the individual PRDs (e.g. OCO orders, dynamic
correlation clustering, Brier-score conviction scoring, recall time-decay
weighting, regime-aware sizing, a real-time dashboard).

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
