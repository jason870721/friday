# P1 — ATR Position Sizing + Stop Monitor + Multi-Timeframe Analysis

## Context

The current system has three structural weaknesses in how it sizes positions, protects open trades, and reads market context:

1. **Position sizing ignores volatility.** A flat 15% margin cap treats BTC at 2% daily vol the same as SOL at 8% daily vol. The same dollar position carries 4× the risk on the more volatile asset.

2. **Stop-loss exists only in the LLM's "mind".** The prompt instructs the LLM to check uPnL every 15s and close at -15% of margin. But the LLM can forget, get distracted processing other symbols, or the price can gap through the stop level within a single 15s window. If the process crashes, positions are completely unprotected.

3. **Single-timeframe tunnel vision.** The agent reads only 5m candles. A 5m bullish setup against a 4h downtrend is a trap — but the agent can't see it without manually calling klines multiple times across different intervals, which it often skips.

---

## P1-1: ATR-Based Position Sizing

### Design

Add ATR(14) to the indicator library and use it to compute a **volatility-aware suggested position size**. This doesn't replace the 15% hard cap — it provides a smarter *target* within that cap.

**Formula:**

```
stop_distance  = ATR(14) × stop_multiplier      // e.g. 2.0 → stop at 2× ATR from entry
risk_usdt      = balance × risk_per_trade        // e.g. 1% of balance
position_qty   = risk_usdt / stop_distance
margin         = position_qty × entry_price / leverage
```

**Example (BTC at $87,000, ATR(14) = $850, balance $10,000):**
```
stop_distance = 850 × 2.0 = $1,700
risk_usdt     = 10,000 × 0.01 = $100
position_qty  = 100 / 1700 = 0.0588 BTC
notional      = 0.0588 × 87,000 = $5,116
margin@25x    = 5,116 / 25 = $205
```

$205 margin is well within the $1,500 cap. On SOL (ATR $4.50, balance $10,000):
```
stop_distance = 4.50 × 2.0 = $9.00
risk_usdt     = $100
position_qty  = 100 / 9 = 11.1 SOL
notional      = 11.1 × 165 = $1,832
margin@25x    = $73
```

These are *much* smaller than the current "80–100% of max_per_pos" default. This is correct — the current sizing is dangerously large. The LLM can scale up on high-conviction setups, but the default is now calibrated to actual volatility.

### Implementation

**1. Add ATR to `internal/binance/indicators.go`**

```go
// ATR returns the Average True Range over `period` candles using Wilder's
// smoothing. ok is false when the series is shorter than period+1.
func ATR(ks []Kline, period int) (atr float64, ok bool)
```

Pure and deterministic — unit-testable with fixed candle fixtures. Uses the same Wilder smoothing pattern as the existing `RSI()` function.

**2. Add `SuggestedSize` to a new `internal/risk/position.go`**

```go
package risk

// SizeParams are the inputs to the position-sizing function.
type SizeParams struct {
    Balance       float64
    EntryPrice    float64
    ATR           float64
    Leverage      float64
    RiskPerTrade  float64 // e.g. 0.01 for 1%
    StopMultiplier float64 // e.g. 2.0
    MaxMarginPct  float64 // hard cap, e.g. 0.15
}

// SizeResult is the suggested position parameters.
type SizeResult struct {
    Quantity     float64
    Notional     float64
    Margin       float64
    StopPrice    float64  // for long: entry - ATR*mult; for short: entry + ATR*mult
    CappedByLimit bool    // true if margin hit MaxMarginPct
}

func SuggestedSize(dir Direction, p SizeParams) SizeResult
```

**3. Surface ATR in SemanticSummary**

Extend `internal/binance/indicators.go` `SemanticSummary()` to include ATR:
```
"Current close 87250. Price is above MA20 (86800). RSI(14) is 62.3 (neutral). ATR(14) is 852.4. Short-term momentum is rising."
```

**4. Surface suggested size in klines output**

The `binance_klines` tool's Summary line gains a sizing hint when ATR is available:
```
"Sizing hint (1% risk, 2x ATR stop): ~0.059 BTC (margin ~$205 at 25x, stop ~$85,550). Cap allows up to $1,500 margin."
```

### Files

| File | Change |
|------|--------|
| `internal/binance/indicators.go` | Add `ATR()` function |
| `internal/binance/indicators_test.go` | ATR unit tests |
| `internal/risk/position.go` | `SuggestedSize()` function + types |
| `internal/risk/position_test.go` | Size calculation tests (edge: tiny ATR, huge vol) |
| `internal/binance/indicators.go` | Extend `SemanticSummary()` with ATR |
| `internal/tool/binance_klines.go` | Add sizing hint to Summary output |

---

## P1-2: Stop-Loss / Take-Profit Execution Monitor

### Design

A goroutine-based `StopMonitor` that runs independently of the LLM loop, polls price every 1–2 seconds, and fires market orders when SL or TP levels are crossed. It operates **in addition to** (not instead of) the LLM's 15s risk checks — the LLM remains the primary risk manager, the monitor is the fast-reaction safety net.

**Architecture:**

```
main.go goroutine
    │
    ├── Agent loop (15s)        ← LLM: reads positions, decides, places orders
    │       │                       Sets SL/TP via StopMonitor.SetLevels()
    │       ▼
    └── StopMonitor (1–2s)      ← Go: polls price, fires market orders on breach
            │                       Reads SL/TP levels from shared state
            ▼
        binance.Client
```

**Key types (`internal/risk/stop_monitor.go`):**

```go
type StopLevels struct {
    StopPrice     float64 // trigger for stop-loss market close
    TakeProfit    float64 // trigger for take-profit market close
    PositionQty   float64 // abs(positionAmt) — needed to place the close order
    PositionSide  string  // "LONG" or "SHORT"
}

type StopMonitor struct {
    mu       sync.RWMutex
    levels   map[string]StopLevels // key = symbol
    client   *binance.Client
    interval time.Duration         // price poll interval (1s default)
    logger   *slog.Logger
}

func NewStopMonitor(client *binance.Client, interval time.Duration, logger *slog.Logger) *StopMonitor

// SetLevels registers or updates SL/TP for a symbol. Called by the agent
// after opening a position. Passing zero values clears the levels.
func (sm *StopMonitor) SetLevels(symbol string, levels StopLevels)

// Start begins the polling loop. Runs until ctx is cancelled.
func (sm *StopMonitor) Start(ctx context.Context)
```

**Polling loop logic (per iteration, per symbol with active levels):**

1. Fetch `binance.Client.Price(ctx, symbol)` → mark price
2. For LONG position: if mark ≤ stopPrice → MarketOrder(SELL, reduceOnly=true). If mark ≥ takeProfit → same.
3. For SHORT position: if mark ≥ stopPrice → MarketOrder(BUY, reduceOnly=true). If mark ≤ takeProfit → same.
4. On execution: log the event, clear the levels for that symbol, continue polling.

**Edge cases:**
- **Gaps**: price can open beyond SL on a new candle. The monitor catches this on the next poll (within 1–2s) — still much faster than the 15s LLM cycle.
- **Partial fills**: the monitor fires a market order for the full position quantity. Binance fills what it can. The LLM reconciles next round via `binance_position`.
- **Duplicate execution**: the `SetLevels` call clears old levels before setting new ones. The monitor is idempotent — if the LLM already closed the position, the monitor's levels were cleared.
- **Process crash**: levels are in-memory only. If the process dies, protection is lost. This is an explicit tradeoff vs. exchange-level stop orders. Documented limitation — exchange stop orders can be added later as a second layer.

**LLM integration:**

The LLM sets SL/TP levels via a new `binance_stop_monitor` tool:

```
Tool: binance_stop_monitor
Description: Register or clear stop-loss / take-profit trigger prices for a symbol.
             The stop monitor poll price every second and executes a market close
             when price reaches the trigger. Set stop=0 or tp=0 to clear that level.
Parameters: symbol, stop_price (0 to clear), take_profit_price (0 to clear)
```

The tool calls `StopMonitor.SetLevels()` via a shared instance.

### Alternative considered: Exchange STOP_MARKET orders

Binance supports `STOP_MARKET` and `TAKE_PROFIT_MARKET` orders natively. These survive process crashes and don't need polling. However:
- They add order lifecycle complexity (cancel old, replace when position changes)
- They're visible on the exchange order book (signal leakage in mainnet)
- The current system is explicitly "MARKET only" with LLM-managed risk

The goroutine monitor is simpler, consistent with the existing execution model, and sufficient for testnet use. Exchange stop orders can be a follow-up.

### Files

| File | Change |
|------|--------|
| `internal/risk/stop_monitor.go` | StopMonitor struct + polling loop |
| `internal/risk/stop_monitor_test.go` | Unit tests (mock Binance client) |
| `internal/tool/binance_stop_monitor.go` | New tool: register/clear SL/TP levels |
| `cmd/friday/main.go` | Start StopMonitor goroutine before agent loop |

---

## P1-3: Multi-Timeframe Analysis

### Design

A new `binance_mtf_klines` tool that fetches three timeframes concurrently and returns a pre-computed cross-TF alignment summary. This replaces the need for the LLM to make three separate `binance_klines` calls.

**Timeframes:**
- **5m × 20** — entry/execution timeframe (100 minutes of data)
- **1h × 24** — trend confirmation timeframe (24 hours of data)
- **4h × 24** — macro context timeframe (4 days of data)

**Output format:**

```
BTCUSDT Multi-Timeframe Analysis
─────────────────────────────────
5m  (×20): Price 87,250 above MA20 (86,800). RSI(14) 62.3 (neutral). Momentum rising.
           → SHORT-TERM: BULLISH
1h  (×24): Price 87,180 below MA20 (87,400). RSI(14) 48.1 (neutral). Momentum mixed.
           → MEDIUM-TERM: NEUTRAL (slight bearish bias)
4h  (×24): Price 87,050 below MA20 (88,200). RSI(14) 35.7 (neutral-oversold edge). Momentum falling.
           → MACRO: BEARISH

CROSS-TF ALIGNMENT: CONFLICT
Short-term bullish against medium-term bearish backdrop.
Higher-timeframe bias dominates — favor SHORT entries on 5m weakness or WAIT for 1h reversal confirmation.
```

**Cross-TF alignment logic (in Go):**

```go
type TFSummary struct {
    Interval  string
    Direction string // "BULLISH", "BEARISH", "NEUTRAL"
    Details   string // human-readable indicator summary
}

func CrossTFSummary(tfs []TFSummary) string {
    // All three aligned → "ALIGNED: <direction> across all timeframes — high conviction"
    // 5m conflicts with 4h → "CONFLICT: short-term vs macro — higher TF bias dominates"
    // All neutral → "NO EDGE: all timeframes neutral/choppy — WAIT"
}
```

Direction classification per timeframe:
- **BULLISH**: price > MA20 AND RSI 50–70 AND last 3 closes rising
- **BEARISH**: price < MA20 AND RSI 30–50 AND last 3 closes falling
- **NEUTRAL**: anything else (mixed signals)

### Implementation

**New tool: `internal/tool/binance_mtf_klines.go`**

```go
const BinanceMTFKlinesToolName tools.ToolName = "binance_mtf_klines"

// Schema: { symbol: string }

func (BinanceMTFKlinesTool) Execute(ctx context.Context, ...) (tools.Result, error) {
    // Fetch 5m, 1h, 4h klines concurrently (3 goroutines)
    // Compute SemanticSummary for each
    // Classify direction per TF
    // Compute cross-TF alignment
    // Return formatted result
}
```

The tool uses a shared `*binance.Client` (same as all other tools) and makes 3 concurrent `cli.Klines()` calls via goroutines.

**Why a separate tool instead of extending `binance_klines`?**

- `binance_klines` stays simple — one interval, one call. Useful for quick checks.
- `binance_mtf_klines` is the "full analysis" tool. The LLM calls it once per symbol per round.
- Separation keeps the schema clean and the responsibility clear.

### Update system prompt

The system prompt's "Fast Market Read" section (line 125–133 of `prompt.go`) currently instructs the LLM to pull klines at 5m × 20. Change it to use `binance_mtf_klines` as the primary data source, falling back to `binance_klines` only for additional intervals.

### Files

| File | Change |
|------|--------|
| `internal/tool/binance_mtf_klines.go` | New tool: multi-TF klines + cross-TF analysis |
| `internal/binance/indicators.go` | Add `ClassifyDirection(ks []Kline) string` helper |
| `internal/bootstrap/bootstrap.go` | Register `binance_mtf_klines` as custom tool |
| `internal/bootstrap/prompt.go` | Update "Fast Market Read" section to prefer `binance_mtf_klines` |

---

## Implementation Order

**Phase 1: ATR + Position Sizing** (foundation for both sizing and SL)

1. Add `ATR()` to `internal/binance/indicators.go` + tests
2. Add `SuggestedSize()` to `internal/risk/position.go` + tests
3. Extend `SemanticSummary()` with ATR
4. Add sizing hint to `binance_klines` Summary output

**Phase 2: Multi-Timeframe** (standalone, no dependencies)

5. Add `ClassifyDirection()` helper to `internal/binance/indicators.go`
6. Create `internal/tool/binance_mtf_klines.go`
7. Register in `bootstrap.go`
8. Update system prompt

**Phase 3: Stop Monitor** (depends on ATR for stop distance calculation)

9. Create `internal/risk/stop_monitor.go` + tests
10. Create `internal/tool/binance_stop_monitor.go`
11. Wire StopMonitor startup in `cmd/friday/main.go`
12. Register tool in `bootstrap.go`
13. Update system prompt

---

## Verification

### ATR + Position Sizing
- `go test ./internal/binance/...` — ATR tests pass (rising, falling, flat, edge cases)
- `go test ./internal/risk/...` — SuggestedSize tests pass (various volatilities, edge: tiny ATR, cap hit)
- Manual: run friday, check `binance_klines` output includes ATR and sizing hint

### Multi-Timeframe
- `go test ./internal/binance/...` — ClassifyDirection tests pass
- Manual: call `binance_mtf_klines BTCUSDT` in a live session, verify all three TFs render and cross-TF alignment is computed correctly

### Stop Monitor
- `go test ./internal/risk/...` — StopMonitor tests: LONG stop triggered, SHORT stop triggered, TP triggered, no trigger (price stays within range), levels cleared mid-flight, duplicate SetLevels
- Manual: open a position, set a tight stop, observe the monitor close it within 2 seconds
- `go build ./...` — no compilation errors
