# Friday Roadmap

The index of Friday's deliverables — one PRD per row in [`docs/PRD/`](./PRD/).
The arc pushes Friday from "LLM as trader" toward "LLM as supervisor over a
deterministic Go strategy engine behind hard, code-enforced safety rails":

- **4-phase upgrade** (PRD-001..004) — semantic data + ReAct, sentiment + margin guardrail, the multi-agent refactor, vector memory + backtest. *(plan: [`plan.md`](./plan.md))*
- **P0 safety/strategy** (PRD-005..006) — circuit breakers + the deterministic strategy layer.
- **P1 volatility/stops/MTF** (PRD-007..009) — ATR sizing, multi-timeframe reads, the stop-loss monitor.
- **Operational hardening** (PRD-010..012, 019) — fixes surfaced by real testnet sessions.
- **P2 strategy-engine hardening** (PRD-013..018) — portfolio expansion, per-strategy tracking, confidence calibration, regime detection, MTF consensus, strategy-aware exits.
- **P3 production + operations** (PRD-020..023) — native stops / fee budget / portfolio caps / online re-calibration / paper mode / observability, plus PnL-analysis-driven signal (022) and Analyst (023) hardening.

**Status: PRD-001–024 are all implemented** (PRD-023's testnet-session and
PRD-024's paper-mode acceptance items await a live run). Per-PRD detail and
deferred follow-ups live in each `docs/PRD/PRD-NNN.md`; the sections below track
them with checkboxes.

---

## ⚠️ Architecture-alignment note (when reading the old P0/P1 plan docs)

The P0/P1 plans were written against the **pre-refactor single-agent**
codebase. PRD-003 has since replaced that with the multi-agent
orchestrator, so several integration points named in the plans no longer
exist. Map them as follows when implementing:

| Plan says… | Current reality |
|------------|-----------------|
| edit the system prompt in `internal/bootstrap/prompt.go` | `prompt.go` is **deleted**; edit the relevant role prompt in [`internal/orchestrator/prompts.go`](../internal/orchestrator/prompts.go) (`analystSystemPrompt` / `riskSystemPrompt` / `executorSystemPrompt`) |
| inject state via the "wakeup prompt format" | the orchestrator owns the loop + 15s cadence; per-round state is the `carry` string and the role prompts built in [`orchestrator.go`](../internal/orchestrator/orchestrator.go) |
| `bootstrap.go` builds the agent + registers tools | tool registration + profiles live in `orchestrator.New`; `bootstrap.go` only loads config and constructs the orchestrator |
| "the LLM decides direction / sizing" | direction is the **Analyst**'s bias; sizing/veto is the **Risk Manager**; order placement is the **Executor** — wire new signals/limits into the matching role |
| per-trade guardrail in `binance_order.go` | still there (`risk.MarginCapValidator`, 15% cap) — the circuit breaker check goes **before** it |

Each PRD below names the corrected integration points.

---

## Completed — `plan.md` (4-phase upgrade)

- ✅ **[PRD-001](./PRD/PRD-001.md)** — Data Semanticization & ReAct. MA20/RSI + natural-language klines summary; `<Thought>` before any execution.
- ✅ **[PRD-002](./PRD/PRD-002.md)** — Sentiment + hard guardrail. `fear_greed_index`; `MarginCapValidator` blocks oversized orders (15% margin cap).
- ✅ **[PRD-003](./PRD/PRD-003.md)** — Multi-agent refactor. Analyst → Risk Manager → Executor with typed handoffs; orchestrator owns the loop.
- ✅ **[PRD-004](./PRD/PRD-004.md)** — Vector memory + sandbox backtest. `log_trade` / `recall_trades` (embedded store) + `run_backtest`.

---

## Completed — P0: strategy supervision + system safety

Source: [`.evva/plans/p0-strategy-and-circuit-breaker.md`](../.evva/plans/p0-strategy-and-circuit-breaker.md)

- ✅ **[PRD-005](./PRD/PRD-005.md)** — System-Level Circuit Breakers. Session-wide safety switches (daily-loss limit, consecutive-loss pause, drawdown halt) that block new entries when the system is bleeding. Depends on PRD-002/003.
- ✅ **[PRD-006](./PRD/PRD-006.md)** — Strategy Layer. Deterministic Go signal engine (`internal/strategy/`: momentum / breakout / mean-reversion / divergence) + aggregator; the Analyst shifts from inventing direction to validating Go-computed signals. Depends on PRD-003. *(divergence implemented + unit-tested; live cross-symbol wiring deferred — see PRD-006 §5.)*

## Planned — P1: volatility-aware sizing, stops, multi-timeframe

Source: [`.evva/plans/p1-volatility-stop-mtf.md`](../.evva/plans/p1-volatility-stop-mtf.md)

- ✅ **[PRD-007](./PRD/PRD-007.md)** — ATR Position Sizing. `binance.ATR(14)` + `risk.SuggestedSize` (risk-per-trade ÷ 2×ATR stop); ATR + a sizing hint surface in the `binance_klines` Summary and the Risk Manager sizes from the volatility target within the 14%/15% caps. Foundation for PRD-009. Depends on PRD-002/003.
- ✅ **[PRD-008](./PRD/PRD-008.md)** — Multi-Timeframe Analysis. `binance_mtf_klines` tool (5m / 1h / 4h fetched concurrently) + `ClassifyDirection` and a cross-TF ALIGNED/CONFLICT/NO-EDGE verdict (higher TF dominates); the Analyst's primary read. Depends on PRD-003.
- ✅ **[PRD-009](./PRD/PRD-009.md)** — Stop-Loss/TP Execution Monitor. `risk.StopMonitor` goroutine polling price ~every 1s that fires reduce-only closes on SL/TP breach; `binance_stop_monitor` lets the Executor register PRD-007's stop level after each OPEN — a fast safety net independent of the 15s LLM loop. Depends on PRD-007.

---

## Completed — operational hardening

Fixes surfaced by running real testnet sessions (not from the original plans).

- ✅ **[PRD-010](./PRD/PRD-010.md)** — Configurable, Venue-Validated Symbol Universe. `FRIDAY_SYMBOLS` + startup `exchangeInfo` preflight (drop non-`TRADING`, real `LOT_SIZE` steps); symbol set threaded into prompts/schemas; TradFi-Perps agreement auto-signed so US-stock perps (NVDA/GOOGL/AMZN/META) trade. Depends on PRD-003.
- ✅ **[PRD-011](./PRD/PRD-011.md)** — Exchange-Truth PnL Reconciliation. `log_trade` records the `/fapi/v1/income` net (realised − fees − funding) instead of the LLM's estimate; WIN/LOSS + circuit breaker key off the true net; `cmd/reconcile-memory` backfills corrupted history. Depends on PRD-004/005.
- ✅ **[PRD-012](./PRD/PRD-012.md)** — Per-Symbol Leverage Caps. `binance.MaxLeverages` (leverageBracket) captured at startup, injected into the Risk Manager prompt as each symbol's `≤Nx`, and clamped in `binance_leverage` so an over-cap request (e.g. 100x on a 10x stock perp) is corrected instead of failing -4028. Depends on PRD-007/010.
- ✅ **[PRD-019](./PRD/PRD-019.md)** — Per-Notional Leverage Tier Clamp. Captures the full notional→leverage tier table (`binance.LeverageBrackets`); `binance_order` auto-lowers leverage so a position's notional fits the tier it falls into (avoids -2027, e.g. AMZNUSDT's $5k @10× cap), then re-validates the margin guardrail at the lower leverage; the Risk Manager prompt shows each symbol's `≤$Xk @max-lev` ceiling. The per-notional-bracket follow-up deferred by PRD-012. Depends on PRD-012.

---

## Planned — P2: strategy-engine hardening

Source: [`.evva/plans/current.md`](../.evva/plans/current.md)

The strategy engine is the least empirically validated part of Friday —
confidences are magic numbers (0.6/0.65), there are only 3 single-symbol
strategies, and no feedback loop exists from trade outcomes back to signal
quality. This tranche makes the strategy layer self-calibrating, expands the
signal portfolio from 3 to 5+ votes, and wires strategy awareness into every
layer from memory through exit logic.

- [x] **[PRD-013](./PRD/PRD-013.md)** — Strategy Portfolio Expansion. ✅ Wired the
  divergence strategy into the live klines flow (a per-round klines cache in the
  tool layer lets the single-symbol klines tools cross-reference the BTC anchor
  and append a divergence hint); added an EMA crossover strategy (`EMA(9)/EMA(21)`
  + `EMA(50)` filter) as a fourth single-symbol vote, plus an `EMA` indicator. The
  aggregator now has 4 single-symbol votes + 1 cross-symbol vote. Depends on PRD-006.
- [x] **[PRD-014](./PRD/PRD-014.md)** — Strategy Performance Tracking. ✅ Added a
  `strategy` field to `TradeRecord` (populated by `log_trade`); `Store` gained
  `OutcomeSummary` + `SimilarByStrategy`, and `recall_trades` now appends a
  win/loss breakdown (overall + per-strategy) and accepts an optional `strategy`
  filter. Outcome stats key off `EffectivePnL` (net-after-fees). Depends on
  PRD-004/006.
- [x] **[PRD-015](./PRD/PRD-015.md)** — Confidence Calibration. ✅ Replaced
  hardcoded strategy confidences with backtest-derived win rates, computed
  per-symbol at startup. `backtest.RunStrategy` replays a strategy over 4h×200
  candles (exit = strategy's own invalidation); `backtest.Calibrate` maps win
  rate → confidence `(winRate−0.5)×2`; `bootstrap` injects them via
  `strategy.SetDefaultCalibration`, consumed by `ConsensusFor`/`AnalyzeAll` (ADX
  boost additive on top). A <5-trade strategy keeps its hardcoded default; a
  0-edge one abstains. Best-effort — failure falls back to defaults. Depends on
  PRD-004/006/014.
- [x] **[PRD-016](./PRD/PRD-016.md)** — Market Regime Detection. ✅
  `strategy.DetectRegime` (4h ADX(14)) classifies trending/ranging/transitional;
  `ConsensusWithRegime` scales each strategy's confidence by per-regime
  multipliers (trend-followers up in trends, mean-reversion up in ranges);
  zero-edge calibrated strategies are auto-disabled in `AnalyzeAll`.
  `binance_mtf_klines` (4h limit 24→48) appends the regime line + a
  regime-weighted 4h consensus. Depends on PRD-006/015.
- [x] **[PRD-017](./PRD/PRD-017.md)** — MTF Strategy Consensus. ✅ Runs the full
  calibrated + regime-weighted strategy engine on all three timeframes
  (`ConsensusForWithRegime` per TF), then `strategy.AggregateMTF` combines them
  into one weighted cross-timeframe vote (5m×1.0, 1h×1.5, 4h×2.0; ±0.1
  hysteresis). `binance_mtf_klines` emits the `MTF Strategy:` line (Analyst's
  primary directional signal); the qualitative `Cross-TF:` line stays as context.
  Depends on PRD-006/008.
- [x] **[PRD-018](./PRD/PRD-018.md)** — Strategy-Aware Exits. ✅ Each strategy's
  `Invalidation` level (MA20 / range boundary / entry×0.99 / EMA21) is now
  rendered in the consensus & `MTF Strategy:` lines (`names()` +
  `Consensus.Invalidation()`); the Analyst relays it into its report and the Risk
  Manager prompt uses the tighter of invalidation vs 2×ATR as the stop. Zero
  (n/a) invalidations are omitted. Depends on PRD-006/007.

---

## Suggested implementation order

All planned PRDs (005–009) plus operational hardening (010–012, 019) are
implemented. Build order followed: `PRD-005` (circuit breakers) → `PRD-006`
(strategy layer) → `PRD-007` (ATR sizing) → `PRD-008` (multi-timeframe) →
`PRD-009` (stop monitor). Operational hardening (PRD-010/011/012/019) was driven
by testnet sessions.

The P2 tranche (PRD-013..018) is now **complete**, built in dependency order:
`PRD-013` (expansion) + `PRD-014` (tracking) → `PRD-015` (calibration, needs
014's strategy attribution) → `PRD-016` (regime, needs 015's calibrated weights)
→ `PRD-017` (MTF voting) + `PRD-018` (strategy-aware exits).

All PRDs (001–024) are now implemented (PRD-023's testnet-session and PRD-024's paper-mode acceptance items await a live run).

---

## Planned — P4: signal quantity + MTF responsiveness + Analyst resilience

Source: [`.evva/plans/current.md`](../.evva/plans/current.md) — 106-round live-data analysis

The 106-round live-data analysis revealed MTF Strategy is NEUTRAL for 97% of rounds
(only 3 actionable rounds out of 106) and the Analyst collapses to one-word "凍結"
summaries after 80+ consecutive NEUTRAL rounds. This tranche fixes the structural
bottlenecks (5m candle count starving the strategy engine), relaxes the MTF
aggregation (quorum voting + lower override threshold), prevents Analyst degradation,
and adds per-strategy observability so NEUTRAL rounds are diagnosable.

- [x] **[PRD-024](./PRD/PRD-024.md)** — Signal Quantity & MTF Responsiveness &
  Analyst Resilience. ✅ Four changes: (1) 5m candle count 20→96 (`binance_mtf_klines`)
  so all 5 strategies — EMACross needs 50 — can vote on the entry timeframe; (2) MTF
  2-of-3 quorum when 4h is NEUTRAL (`FRIDAY_MTF_QUORUM`, default on) + 5m+1h override
  floor lowered 0.5→0.35 (the PRD-022 4h hard veto is unchanged — it still fires only
  when the *weighted* result opposes a directional 4h, so a lone lower-TF dissent does
  not block a with-4h trade); (3) Analyst-prompt anti-degradation rule (always write a
  concrete per-symbol summary, never "凍結") + Cross-TF-divergence flag + a
  `consecutiveNeutral` counter that injects a vigilance warning into the carry after 10
  flat rounds (`carryWithNeutralWarning`, strip-then-append); (4) per-strategy
  `Consensus.SignalDetails` rendered as indented lines beneath the MTF Strategy line so
  a NEUTRAL round shows *why* (which strategies fired / conflicted / were RSI-filtered).
  Depends on PRD-006/017/022.

---

## Planned — P3: production hardening + operations & observability

Source: [`.evva/plans/current.md`](../.evva/plans/current.md) — post-P2 review

The P2 tranche hardened the strategy engine. The remaining gaps fall into two
categories: production hardening (safety gaps only code can close, signal
improvements the backtest infrastructure enables) and operations (tooling to
make the system observable and testable without real money).

- [x] **[PRD-020](./PRD/PRD-020.md)** — Production Hardening (Safety + Signal). ✅
  Six changes within the trading loop: native STOP_MARKET/TAKE_PROFIT_MARKET
  orders for crash-survivable stops (`binance_stop_monitor` places them alongside
  the in-memory monitor; startup orphan cleanup in `main`), a fee-budget
  guardrail (`risk.FeeBudget`, blocks new OPENs in `binance_order`, fed by
  `log_trade`), portfolio correlation-group caps (`risk.PortfolioGroupValidator`,
  crypto 30% / stocks 40%, `FRIDAY_GROUP_LIMITS`), online re-calibration
  (`strategy.Recalibrator`, every `FRIDAY_RECALIBRATE_HOURS`), strategy-specific
  take-profits (`Signal.TakeProfit` — breakout measured move, mean-reversion/
  bollinger the mean — rendered as `tp=…`; `backtest.BestTakeProfit` sweep), and a
  Bollinger Band strategy (5th vote: mean-reversion + band-walk). Depends on
  PRD-005/006/007/009/010/011/015.
- [x] **[PRD-021](./PRD/PRD-021.md)** — Operations & Observability. ✅ Three
  operator-facing tools: `cmd/analyze` for session post-mortem analysis (6
  sections — overview, per-strategy/symbol/regime stats with profit factor,
  Analyst directional accuracy, breaker timeline; `-json` + path flags; reads
  rounds.jsonl + trades.jsonl, graceful on missing files), `internal/notify`
  Discord/Telegram webhook notifications (`NewFromEnv`; session start/stop +
  breaker PAUSED/HALTED transitions deduped in the orchestrator, large-PnL
  closes ≥`FRIDAY_NOTIFY_PNL_PCT` in `log_trade`), and `FRIDAY_PAPER=true` paper
  trading (`risk.PaperPortfolio`; trading tools become virtual no-ops, market
  data stays live, paper StopMonitor broker, round/trade logs tagged
  `paper:true`). Round log now records per-symbol regime for the post-mortem.
  Depends on PRD-003/004/005 + roundlog.go.
- [x] **[PRD-022](./PRD/PRD-022.md)** — Strategy Signal Quality: RSI Entry Filter
  + MTF Responsiveness. ✅ `strategy.RSIFilter` (`rsi_filter.go`) blocks any
  directional consensus when the TF's RSI(14) is in an extreme zone (≥75 or ≤25),
  applied per-TF inside `AggregateMTF`; `Consensus` gained an `RSI` field populated
  by `ConsensusWithRegime`; `binance.ClosesOf` exported. MTF hysteresis lowered
  0.1→0.05 (`FRIDAY_MTF_HYSTERESIS`), a 5m+1h consensus override fires when the 4h
  is NEUTRAL and both lower TFs agree ≥0.5 (`FRIDAY_MTF_5M1H_OVERRIDE`), and 4h
  opposition is now a hard veto. Env knobs: `FRIDAY_RSI_FILTER`. Depends on PRD-006/017.
- [x] **[PRD-023](./PRD/PRD-023.md)** — Analyst Decision Quality: Regime Clamp
  + Fee Awareness + Recall Fix. ✅ Three Analyst-prompt rules (regime-aware bias
  clamp — no counter-trend LONG in a bearish 4h trend without MTF-LONG + extreme
  fear; fee-aware sizing — expected move must clear ≥3× round-trip fee; recall
  sample-size reminder) + the fee-budget surfaced into the round carry when near
  the cap (`carryWithFeeWarning`, strip-then-append) + a recall minimum-sample
  threshold (`memory.SimilarConclusive` / `ConclusiveMinSamples=5`; `recall_trades`
  prints "insufficient data (<5 similar trades) — do not use this to veto" on a
  thin pool, breaking the losses→never-trade feedback loop). Depends on
  PRD-003/004/016/020.

---

## Suggested implementation order (P3)

**PRD-020** — implement in dependency order within the PRD:
native stops → fee budget → portfolio groups (safety, parallelizable) →
Bollinger (cheapest signal) → strategy TP → online calibration (needs TP infra).

**PRD-021** — independently parallelizable with PRD-020:
post-mortem tool (data already exists) + paper trading → notifications
(benefits from paper mode for safe event testing).
