# Friday Roadmap

Two tranches of work. The **completed** tranche (PRD-001..004) realised the
original 4-phase upgrade in [`plan.md`](./plan.md). The **planned** tranche
(PRD-005..009) comes from the P0/P1 plans in
[`.evva/plans/`](../.evva/plans/) and pushes Friday from "LLM as trader"
toward "LLM as supervisor over deterministic Go strategy + hard safety
rails".

---

## ⚠️ Architecture-alignment note (read before implementing PRD-005+)

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

## Planned — P0: strategy supervision + system safety

Source: [`.evva/plans/p0-strategy-and-circuit-breaker.md`](../.evva/plans/p0-strategy-and-circuit-breaker.md)

- ⬜ **[PRD-005](./PRD/PRD-005.md)** — System-Level Circuit Breakers. Session-wide safety switches (daily-loss limit, consecutive-loss pause, drawdown halt) that block new entries when the system is bleeding. *Lowest-risk, highest-immediate-safety — do this first.* Depends on PRD-002/003.
- ⬜ **[PRD-006](./PRD/PRD-006.md)** — Strategy Layer. Deterministic Go signal engine (`internal/strategy/`: momentum / breakout / mean-reversion / divergence) + aggregator; the Analyst shifts from inventing direction to validating Go-computed signals. Depends on PRD-003; complements PRD-005.

## Planned — P1: volatility-aware sizing, stops, multi-timeframe

Source: [`.evva/plans/p1-volatility-stop-mtf.md`](../.evva/plans/p1-volatility-stop-mtf.md)

- ⬜ **[PRD-007](./PRD/PRD-007.md)** — ATR Position Sizing. ATR(14) indicator + `risk.SuggestedSize` (risk-per-trade ÷ ATR stop distance); feeds the Risk Manager a volatility-calibrated target within the 15% cap. Foundation for PRD-009. Depends on PRD-002/003.
- ⬜ **[PRD-008](./PRD/PRD-008.md)** — Multi-Timeframe Analysis. `binance_mtf_klines` tool (5m / 1h / 4h fetched concurrently) + cross-TF alignment; the Analyst reads macro context, not just 5m. Depends on PRD-003.
- ⬜ **[PRD-009](./PRD/PRD-009.md)** — Stop-Loss/TP Execution Monitor. A goroutine polling price every ~1s that fires reduce-only market closes on SL/TP breach — a fast safety net independent of the 15s LLM loop. Depends on PRD-007 (ATR stop distance).

---

## Suggested implementation order

`PRD-005` (safety first) → `PRD-007` (ATR foundation) → `PRD-009` (stops,
needs ATR) → `PRD-008` (MTF, standalone) → `PRD-006` (strategy layer,
largest behavioural change). PRD-005/007/008/009 are mostly additive; PRD-006
changes the Analyst's role and is best done once the safety rails are in.
