# P0 — Strategy Layer + System-Level Circuit Breakers

## Overview

Two complementary changes that shift Friday from "LLM as trader" to "LLM as strategy supervisor with hard safety boundaries."

1. **Strategy Layer** — a deterministic Go engine that computes objective trading signals from market data. The LLM's role changes from *deciding direction* to *validating signals against macro/sentiment context*. Signals are backtestable, traceable, and auditable.

2. **Circuit Breakers** — session-level safety switches in the `risk` package that block all new entries when the system is bleeding. Complement the existing per-trade `MarginCapValidator` with daily loss limits, consecutive-loss pauses, and drawdown-triggered emergency shutdown.

---

## P0-1: Strategy Layer

### Problem

The current system prompt (line 135–142 of `prompt.go`) defines five "Setup Triggers" — momentum continuation, breakout, fast reversal, catch-up, divergence. The LLM is expected to:
- Read OHLCV numbers + semantic summary
- Pattern-match against these triggers
- Decide direction, timing, and sizing
- All in one reasoning pass

This has fundamental problems:
- **Inconsistency**: same market conditions on two different rounds may yield opposite decisions because LLM sampling is non-deterministic.
- **Untestable**: you can't unit-test the LLM's decision logic. You can only observe outcomes.
- **No edge**: LLMs are language models, not trading models. They have no statistical advantage over random entry in efficient markets.
- **Overfit to prompt**: every tweak to the system prompt changes behavior in unpredictable ways. There's no isolated strategy to improve.

### Design

Introduce a **strategy pipeline** that runs *before* the LLM sees data:

```
OHLCV candles
    │
    ▼
┌─────────────────────┐
│  Indicator Engine    │  ← existing (SMA, RSI in binance/indicators.go)
│  (extended)          │
└─────────┬───────────┘
          │
          ▼
┌─────────────────────┐
│  Strategy Registry   │  ← NEW: internal/strategy/
│  (pluggable)         │
└─────────┬───────────┘
          │
          ▼
┌─────────────────────┐
│  Signal Aggregator   │  ← NEW: combines signals → final recommendation
└─────────┬───────────┘
          │
          ▼
    Structured signal
    (direction, confidence,
     invalidation, reasoning)
          │
          ▼
    Appended to klines Summary
    → LLM reads it as context
```

### New package: `internal/strategy/`

```
internal/strategy/
├── strategy.go      // Signal type + Strategy interface
├── registry.go      // Registry: register, list, execute all
├── aggregator.go    // Combine signals into one recommendation
├── momentum.go      // Momentum-continuation strategy
├── breakout.go      // Breakout strategy
├── mean_revert.go   // Mean-reversion strategy
├── divergence.go    // Cross-symbol divergence strategy
└── strategy_test.go
```

#### `strategy.go` — Core types

```go
package strategy

// Signal is one strategy's output for one symbol at one point in time.
type Signal struct {
    Symbol      string
    Direction   Direction      // Long, Short, Neutral
    Confidence  float64        // 0.0–1.0
    Reason      string         // human-readable reasoning (goes to LLM)
    Invalidation float64       // price level at which this signal is invalid
    Strategy    string         // which strategy produced this
}

type Direction int
const (
    Neutral Direction = iota
    Long
    Short
)

// Strategy is the pluggable interface. Each strategy reads candles +
// indicators and returns a Signal.
type Strategy interface {
    Name() string
    Analyze(symbol string, candles []binance.Kline) Signal
}
```

#### `registry.go` — Strategy registry

```go
type Registry struct {
    strategies []Strategy
}

func NewRegistry(strategies ...Strategy) *Registry { ... }
func (r *Registry) AnalyzeAll(symbol string, candles []binance.Kline) []Signal { ... }
```

#### `aggregator.go` — Signal combination

Simple majority-vote with confidence weighting:

```go
type Aggregator struct{}

// Aggregate takes N signals and produces a consensus recommendation.
// Rules:
// - ≥2 Long signals + no Short → Long, confidence = avg(long_confidences)
// - ≥2 Short signals + no Long → Short, confidence = avg(short_confidences)
// - Mixed signals → Neutral (conflict)
// - All Neutral → Neutral (no edge)
func (a *Aggregator) Aggregate(signals []Signal) Consensus

type Consensus struct {
    Direction   Direction
    Confidence  float64
    Signals     []Signal  // all input signals (for LLM context)
    Summary     string    // natural-language summary for LLM
}
```

### Initial strategies (implement the 5 setup triggers in Go)

| Strategy | File | Logic |
|----------|------|-------|
| `momentum_continuation` | `momentum.go` | Last 3 closes rising + price > MA20 + RSI 50–70 → Long. Mirror for Short. |
| `breakout` | `breakout.go` | Close breaks above 24h high + volume > avg volume × 1.5 → Long. Mirror for Short. |
| `mean_reversion` | `mean_revert.go` | Price deviates >2% from MA20 + RSI <30 or >70 → fade back to MA20. |
| `divergence` | `divergence.go` | SOL moving strongly while BTC flat (or vice versa) → trade the mover. |

### Wiring into the existing flow

In `internal/binance/indicators.go` (or a new `internal/binance/summary.go`):
- `SemanticSummary()` currently returns indicator text.
- Extend it (or add a new function) to also accept strategy signals and append them:

```go
func SemanticSummaryWithSignals(ks []Kline, consensus strategy.Consensus) string {
    base := SemanticSummary(ks)
    if consensus.Direction == strategy.Neutral {
        return base + " Strategy signals: no clear edge (mixed/neutral)."
    }
    return fmt.Sprintf("%s Strategy signals: %s (confidence %.0f%%). %s",
        base, consensus.Direction, consensus.Confidence*100, consensus.Summary)
}
```

### LLM role change

The system prompt changes:

**Before (current):**
> "Find setup triggers, decide direction, execute."

**After:**
> "You receive pre-computed strategy signals with confidence scores. Your job is to **validate** them against macro/sentiment context (Fear & Greed, funding rates, cross-symbol correlation) and either **approve** or **veto** each signal. You may NOT invent a trade direction that contradicts all signals. A veto must cite a specific data point (e.g., 'Fear & Greed at 85 extreme greed, overriding the momentum-long signal on BTC')."

This changes the LLM from decision-maker to supervisor — it can say "no" with a reason, but it can't go rogue.

---

## P0-2: System-Level Circuit Breakers

### Problem

The current guardrail (`internal/risk/guardrail.go`) only blocks individual oversized orders. There is no session-level safety:
- The agent could lose 5% on 20 consecutive small trades and drain the account.
- The agent could enter a feedback loop of bad decisions with no automatic pause.
- There is no daily loss limit — the agent runs until the user manually stops it.

### Design

Add a `CircuitBreaker` to the `internal/risk` package that tracks session-level metrics and blocks new entries when thresholds are breached.

#### `internal/risk/circuit_breaker.go`

```go
package risk

import "time"

// BreakerState tracks the session-level health of the trading system.
type BreakerState int
const (
    StateNormal   BreakerState = iota  // all clear
    StateWarning                       // approaching limits
    StatePaused                        // new entries blocked, closes only
    StateHalted                        // emergency: all positions closed, loop continues but no trades
)

// CircuitBreaker monitors session PnL and trade outcomes to prevent runaway losses.
type CircuitBreaker struct {
    // Config (set at construction, overridable via env)
    DailyLossLimitPct    float64 // e.g. 0.10 = 10% of starting balance → StatePaused
    MaxConsecutiveLosses int     // e.g. 5 → StatePaused for cooldown cycles
    DrawdownHaltPct      float64 // e.g. 0.20 = 20% drawdown → StateHalted
    CooldownCycles       int     // how many cycles to stay in StatePaused (e.g. 20 = 5 min at 15s)

    // Live state
    StartingBalance      float64
    SessionRealizedPnL   float64   // sum of closed-trade PnL this session
    ConsecutiveLosses    int
    CurrentDrawdown      float64   // unrealized + realized as fraction of starting balance
    State                BreakerState
    PausedCyclesRemaining int
    PauseReason          string
}

// RecordTrade is called after every closed position.
func (cb *CircuitBreaker) RecordTrade(pnl float64, balance float64)

// Check is called before every opening order. Returns an error if blocked.
func (cb *CircuitBreaker) Check(balance float64) error

// UpdateDrawdown is called each round with current unrealized PnL.
func (cb *CircuitBreaker) UpdateDrawdown(unrealizedPnL float64)

// Status returns a natural-language summary for the LLM.
func (cb *CircuitBreaker) Status() string
```

#### Breach behavior table

| Condition | State | Action |
|-----------|-------|--------|
| Daily realized loss ≤ -10% starting balance | `StatePaused` | Block new entries; allow closes only. Cooldown = 20 cycles (5 min). After cooldown, resume at half size. |
| 5 consecutive losses | `StatePaused` | Same as above. Reset consecutive counter on first win. |
| Total drawdown (realized + unrealized) ≤ -20% starting balance | `StateHalted` | `binance_close_all` triggered automatically via a Go-level goroutine signal. Loop continues but only WAIT allowed. User must manually reset (env var or TUI command). |
| Cooldown expires | `StateNormal` | Resume trading at `reduced_per_pos` (7.5% of balance, already in prompt). |

#### Wiring

In `internal/tool/binance_order.go`, add the circuit breaker check *before* the existing MarginCapValidator:

```go
func (BinanceOrderTool) Execute(...) (tools.Result, error) {
    // ... decode input ...

    // 1. Circuit breaker: session-level gate
    if berr := globalBreaker.Check(...); berr != nil {
        return tools.Result{IsError: true, Content: berr.Error()}, nil
    }

    // 2. Margin cap: per-trade gate (existing)
    // ... existing guardrail code ...
}
```

The `globalBreaker` singleton is initialized in `bootstrap.go` and shared with the agent. It is updated each round via a new tool or via the existing balance/position calls.

#### LLM awareness

The circuit breaker status is injected into the system prompt's dynamic state section (the wakeup prompt), so the LLM knows not to try opening into a paused/halted breaker:

```
"Round 47 | bal=$10240 | Breaker: NORMAL (daily PnL +$120, 2 wins) | BTC: FLAT | ETH: FLAT | SOL: FLAT | total uPnL=$0. Analyse all three independently and decide."
```

On breach:
```
"Round 47 | bal=$9240 | Breaker: PAUSED (daily loss -$760/-10.0%, cooldown 17 cycles remain) | BTC: FLAT | ... | CLOSE ONLY this round."
```

---

## Implementation plan

### Phase A: Circuit Breakers (lower risk, immediate safety gain)

1. **Create `internal/risk/circuit_breaker.go`**
   - `CircuitBreaker` struct, `RecordTrade()`, `Check()`, `UpdateDrawdown()`, `Status()`
   - Configurable thresholds via env vars (`FRIDAY_DAILY_LOSS_PCT`, `FRIDAY_MAX_CONSEC_LOSSES`, `FRIDAY_DRAWDOWN_HALT_PCT`, `FRIDAY_COOLDOWN_CYCLES`) with sensible defaults

2. **Create `internal/risk/circuit_breaker_test.go`**
   - Test each breach condition triggers correctly
   - Test cooldown expiry
   - Test reduce-only orders pass through
   - Test reset behavior

3. **Wire into `internal/tool/binance_order.go`**
   - Add `globalBreaker` singleton (package-level var, initialized in bootstrap)
   - Check before MarginCapValidator
   - Return actionable error to LLM on block

4. **Wire into `internal/bootstrap/bootstrap.go`**
   - Initialize `globalBreaker` during agent construction
   - Read thresholds from env vars

5. **Update system prompt in `internal/bootstrap/prompt.go`**
   - Add breaker state to wakeup prompt format
   - Add "Breaker awareness" section: when PAUSED or HALTED, only CLOSE/WAIT allowed
   - Add breaker status line to report format

### Phase B: Strategy Layer (higher complexity, requires careful design)

1. **Create `internal/strategy/strategy.go`**
   - `Signal`, `Direction`, `Consensus` types
   - `Strategy` interface

2. **Create `internal/strategy/registry.go`**
   - `Registry` type, `AnalyzeAll()`

3. **Create `internal/strategy/aggregator.go`**
   - `Aggregator`, `Aggregate()`

4. **Implement initial strategies**
   - `internal/strategy/momentum.go` — momentum continuation
   - `internal/strategy/breakout.go` — breakout detection
   - `internal/strategy/mean_revert.go` — mean reversion
   - `internal/strategy/divergence.go` — cross-symbol divergence

5. **Strategy tests**
   - `internal/strategy/strategy_test.go` — fixture-based tests with known candle patterns

6. **Extend `internal/binance/indicators.go`**
   - Add `SemanticSummaryWithSignals()` or modify `SemanticSummary()` to accept optional signals
   - Add ADX indicator (for regime detection, feeds into strategy confidence)

7. **Update `internal/bootstrap/prompt.go`**
   - Rewrite "Setup Triggers" section → "Strategy Signal Validation"
   - Change LLM role from decision-maker to supervisor
   - Add veto protocol

8. **Integration test**
   - Run a full loop with strategy signals + circuit breaker and verify the transcript shows signal-driven decisions with LLM veto capability

---

## Files to create

| File | Purpose |
|------|---------|
| `internal/strategy/strategy.go` | Core types (Signal, Direction, Strategy interface, Consensus) |
| `internal/strategy/registry.go` | Strategy registry, runs all strategies per symbol |
| `internal/strategy/aggregator.go` | Signal aggregation + consensus logic |
| `internal/strategy/momentum.go` | Momentum-continuation strategy |
| `internal/strategy/breakout.go` | Breakout strategy |
| `internal/strategy/mean_revert.go` | Mean-reversion (RSI-based) strategy |
| `internal/strategy/divergence.go` | Cross-symbol divergence strategy |
| `internal/strategy/strategy_test.go` | Unit tests for all strategies |
| `internal/risk/circuit_breaker.go` | CircuitBreaker struct + methods |
| `internal/risk/circuit_breaker_test.go` | Unit tests for circuit breaker |

## Files to modify

| File | Change |
|------|--------|
| `internal/bootstrap/bootstrap.go` | Initialize strategy registry + circuit breaker; pass to agent context |
| `internal/bootstrap/prompt.go` | Rewrite strategy/trigger sections; add breaker awareness; add signal validation protocol |
| `internal/tool/binance_order.go` | Add circuit breaker check before MarginCapValidator |
| `internal/binance/indicators.go` | Add ADX indicator; extend SemanticSummary to accept signals |
| `internal/binance/indicators_test.go` | ADX tests |

---

## Risks & tradeoffs

1. **Over-constraining the LLM**: if signals are too restrictive, the system loses flexibility. Mitigation: the LLM can still veto and explain why, and we can add/remove strategies without changing the prompt.

2. **Strategy quality**: the initial Go strategies are simple heuristics. They may not outperform the LLM's current discretionary approach. Mitigation: strategies are pluggable — we can iterate. The key value is *traceability and backtestability*.

3. **Divergence strategy complexity**: cross-symbol analysis requires data from multiple symbols in one strategy call. This breaks the current per-symbol loop pattern. Mitigation: implement a two-pass approach — first pass collects all symbol signals, second pass runs divergence detection.

4. **Circuit breaker state persistence**: a restart resets the breaker state, losing the daily loss counter. Mitigation: start simple (in-memory only), add SQLite persistence later if needed. Document this limitation.

5. **No strategy for the LLM to "invent" trades**: if market conditions don't trigger any strategy but there is a genuine opportunity, the system will miss it. Mitigation: add a "WAIT" override where the LLM can flag that it sees a setup not covered by existing strategies, and log it for strategy development.
