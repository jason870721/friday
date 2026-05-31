# Friday Roadmap

Three tranches of work. The **4-phase upgrade** (PRD-001..004) from
[`plan.md`](./plan.md) and the **P0 safety/strategy** tranche (PRD-005..006)
from the P0/P1 plans in [`.evva/plans/`](../.evva/plans/), and the **P1**
tranche (PRD-007..009) are all complete. Together they push Friday from "LLM as
trader" toward "LLM as supervisor over deterministic Go strategy + hard safety
rails". A separate **operational-hardening** tranche (PRD-010..011) captures
fixes that came out of running real testnet sessions.

---

## ⚠️ Architecture-alignment note (read before implementing PRD-007+)

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

---

## Planned — P2: strategy-engine hardening

Source: [`.evva/plans/current.md`](../.evva/plans/current.md)

The strategy engine is the least empirically validated part of Friday —
confidences are magic numbers (0.6/0.65), there are only 3 single-symbol
strategies, and no feedback loop exists from trade outcomes back to signal
quality. This tranche makes the strategy layer self-calibrating, expands the
signal portfolio from 3 to 5+ votes, and wires strategy awareness into every
layer from memory through exit logic.

- [ ] **[PRD-013](./PRD/PRD-013.md)** — Strategy Portfolio Expansion. Wire the
  already-implemented divergence strategy into the live klines flow; add an EMA
  crossover strategy as a fourth single-symbol vote so the aggregator can form
  consensus on strong trends. Depends on PRD-006.
- [ ] **[PRD-014](./PRD/PRD-014.md)** — Strategy Performance Tracking. Add a
  `strategy` field to `TradeRecord` so every closed trade is attributed to its
  triggering strategy; add outcome-filtered similarity queries so `recall_trades`
  returns win/loss breakdowns instead of bare similar trades. Depends on
  PRD-004/006.
- [ ] **[PRD-015](./PRD/PRD-015.md)** — Confidence Calibration. Replace hardcoded
  strategy confidences with backtest-derived win rates, recomputed per-symbol at
  startup (or periodically). `backtest.RunStrategy` replays a strategy over
  historical candles; `strategy.Calibrate` maps win rate → confidence. Depends on
  PRD-004/006/014.
- [ ] **[PRD-016](./PRD/PRD-016.md)** — Market Regime Detection. Classify the
  current market as trending/ranging/transitional from ADX(14), then
  dynamically up-weight strategies suited to the regime and down-weight (or
  disable) those that underperform in it. Depends on PRD-006.
- [ ] **[PRD-017](./PRD/PRD-017.md)** — MTF Strategy Consensus. Run the strategy
  engine on all three timeframes already fetched by `binance_mtf_klines`
  (5m/1h/4h), then aggregate into a weighted cross-timeframe vote where higher
  timeframes dominate on conflict. Depends on PRD-006/008.
- [ ] **[PRD-018](./PRD/PRD-018.md)** — Strategy-Aware Exits. Surface each
  strategy's `Invalidation` level (already computed — MA20 for momentum, range
  boundary for breakout, entry×0.99 for mean-reversion) in the klines Summary and
  instruct the Risk Manager to prefer it over the generic 2×ATR stop when it
  offers tighter protection. Depends on PRD-006/007.

---

## Suggested implementation order

All planned PRDs (005–009) plus operational hardening (010–012) are
implemented. Build order followed: `PRD-005` (circuit breakers) → `PRD-006`
(strategy layer) → `PRD-007` (ATR sizing) → `PRD-008` (multi-timeframe) →
`PRD-009` (stop monitor). Operational hardening (PRD-010/011/012) was driven by
testnet sessions.

The P2 tranche (PRD-013..018) is ordered by dependency chain:
`PRD-013` (expansion, no deps beyond 006) + `PRD-014` (tracking, no hard deps) →
`PRD-015` (calibration, needs 014's strategy attribution) → `PRD-016` (regime,
needs 015's calibrated weights) → `PRD-017` (MTF voting) + `PRD-018` (exits) in
parallel.
