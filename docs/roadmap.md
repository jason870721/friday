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

---

## Suggested implementation order

All planned PRDs (005–009) plus operational hardening (010–011) are
implemented. Build order followed: `PRD-005` (circuit breakers) → `PRD-006`
(strategy layer) → `PRD-007` (ATR sizing) → `PRD-008` (multi-timeframe) →
`PRD-009` (stop monitor). Future work lives in the Out-of-Scope sections of the
individual PRDs (e.g. exchange-native STOP_MARKET orders, fee/churn budgeting,
divergence live-wiring).
