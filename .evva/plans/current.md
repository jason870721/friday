# Binance Auto-Trading Experiment — Plan

## Context

**Goal**: Test DeepSeek's trading decision ability. Give the agent starting capital on Binance Futures testnet. The agent autonomously decides each round: go long, go short, close position, or wait — across three markets simultaneously. Uses `schedule_wakeup` for a 15-second auto-loop. Runs indefinitely until the user manually stops it (Ctrl+C).

**Trading mode**: Binance USDⓈ-M Futures. Long and short both supported. Leverage is agent's choice (not fixed).

**Markets**: BTCUSDT, ETHUSDT, SOLUSDT. Agent can hold up to 3 positions (one per symbol, long or short each).

**Environment**: Testnet first (`testnet.binancefuture.com`), switch to mainnet after validation.

**Important**: With 15-second cycles, a full day needs ~5760 iterations. The default agent `MAX_ITERS` is 30, which will stop the loop after 7.5 minutes. Set `MAX_ITERS=12000` in `~/.friday/.env` before starting.

**Language**: All tool descriptions and the starting prompt in chinese.

---

## Architecture

```
┌──────────────────────────────────────────────┐
│              Friday Agent (TUI)               │
│                                              │
│  System Prompt: "You are a futures trader..." │
│                                              │
│  Tools:                                      │
│  ┌──────────┐ ┌──────────┐ ┌────────────┐   │
│  │ price    │ │ klines   │ │ order      │   │
│  └──────────┘ └──────────┘ └────────────┘   │
│  ┌──────────┐ ┌──────────┐ ┌────────────┐   │
│  │ balance  │ │ position │ │ leverage   │   │
│  └──────────┘ └──────────┘ └────────────┘   │
│  ┌──────────┐ ┌──────────┐ ┌────────────┐   │
│  │ funding  │ │ ticker   │ │ close_all  │   │
│  └──────────┘ └──────────┘ └────────────┘   │
│  ┌──────────┐                                │
│  │ fee      │                                │
│  └──────────┘                                │
│                                              │
│  Deferred Tool (built-in):                   │
│  ┌───────────────────────┐                   │
│  │ schedule_wakeup (15s) │                   │
│  └───────────────────────┘                   │
│                                              │
│  ┌────────────────────────────┐              │
│  │  Binance Futures REST API  │              │
│  │  /fapi/v1/*                │              │
│  └────────────────────────────┘              │
└──────────────────────────────────────────────┘
```

**Decision loop** (every round, for each symbol):
```
0. EVERY ROUND: binance_balance → get current wallet balance (= your capital for this round)
   FIRST ROUND ONLY: also binance_fee → confirm fee rate
   ALL LIMITS ARE DYNAMIC — compute from current balance, not hardcoded numbers.
1. binance_price     → get current mark prices (BTC, ETH, SOL) [PARALLEL]
2. binance_ticker    → get 24h stats (change%, high, low, volume) [PARALLEL]
3. binance_klines    → get recent candlesticks (e.g. 5m x 20) [PARALLEL]
4. binance_funding   → get funding rates (market sentiment) [PARALLEL]
5. binance_position  → check current positions, unrealized PnL, liquidation price
6. STOP LOSS check (per position): if PnL ≤ -15% of that position's margin → CLOSE
7. TAKE PROFIT check (per position):
   PnL ≥ +10% of margin → close 50%. Remaining margin = original × 50%. Reset stop to 0% of remaining margin.
   PnL ≥ +20% of remaining margin → close the rest.
8. TRAILING check: track peak PnL per position across rounds via wakeup prompt.
   If peak was ≥ +8% of margin and current PnL ≤ +3% of margin → close all.
9. LIQUIDATION check: if mark price within 5% of liquidation price → reduce or close
10. TOTAL check: if total PnL ≤ -10% of balance → binance_close_all; if ≥ +20% → halve new sizes
11. Analyze → decide per symbol (BTC, ETH, SOL independently — all three required):
    LONG / SHORT / CLOSE / WAIT
12. If action: binance_leverage → binance_order → execute
13. Report: all three markets (even if no position) — status, margin, PnL, peak, reasoning, balance
14. schedule_wakeup(15s, reason="...", prompt="Round N — [all 3 markets + PnL + balance]. Analyze...")
```

---

## Agent Tools

All in `internal/tool/`, one file per tool, following the `echo.go` pattern.

### Market Data Tools

| # | Tool | Description | Key Inputs | API |
|---|------|-------------|------------|-----|
| 1 | `binance_price` | Get current mark price for a symbol | `symbol` | `GET /fapi/v1/premiumIndex` |
| 2 | `binance_ticker` | Get 24h price change stats (change%, high, low, volume) | `symbol` | `GET /fapi/v1/ticker/24hr` |
| 3 | `binance_klines` | Get candlestick data (OHLCV) | `symbol`, `interval`, `limit` | `GET /fapi/v1/klines` |
| 4 | `binance_funding` | Get current funding rate (market sentiment indicator) | `symbol` | `GET /fapi/v1/fundingRate` |
| 5 | `binance_fee` | Get commission rate (maker/taker). Must check before trading — fees can erase small profits. | `symbol` | `GET /fapi/v1/commissionRate` |

### Trading Tools

| # | Tool | Description | Key Inputs | API |
|---|------|-------------|------------|-----|
| 6 | `binance_leverage` | Set leverage for a symbol before opening a position | `symbol`, `leverage` | `POST /fapi/v1/leverage` |
| 7 | `binance_order` | Place a market order (BUY = open long / add / close short; SELL = open short / add / close long) | `symbol`, `side`, `quantity` | `POST /fapi/v1/order` |
| 8 | `binance_close_all` | Emergency close all positions immediately. No parameters needed. Use when total PnL hits -10% of balance or when you need a clean slate. | — | Internally: DELETE allOpenOrders → GET positions → POST market-close for each |

### Account Tools

| # | Tool | Description | Key Inputs | API |
|---|------|-------------|------------|-----|
| 9 | `binance_balance` | Get USDT wallet balance (available + locked) | — | `GET /fapi/v2/balance` |
| 10 | `binance_position` | Get current position: direction, size, entry price, mark price, unrealized PnL, liquidation price | `symbol?` | `GET /fapi/v2/positionRisk` |

---

## Why These Extra Tools Help the Agent

### `binance_fee` — Commission Rate (NEW)
Fees are the hidden profit killer. With 0.04% taker fee:
- Round-trip (open + close) = 0.08% of position value
- This tool lets the agent query its actual fee rate so it can calculate the breakeven threshold before entering any trade
- API: `GET /fapi/v1/commissionRate?symbol=...` → makerCommissionRate, takerCommissionRate

### `binance_funding` — Funding Rate
Funding rate is the periodic payment between long and short traders. It's a powerful sentiment signal:
- **High positive funding** → too many longs, market is overheated bullish. Costly to hold long. Often precedes a correction.
- **Negative funding** → too many shorts, market is overly bearish. Shorts pay longs. Can signal a bounce.
- The agent can use this as a contrarian indicator or to assess the cost of holding a position.

### `binance_ticker` — 24h Stats
Gives quick context that raw price alone doesn't:
- **priceChangePercent** → is the market trending or ranging today?
- **high / low** → where is current price in the day's range? Near high = might be overextended. Near low = might be oversold.
- **volume** → is there real participation behind the move, or is it thin?

### `binance_leverage` — Leverage Control
Letting the agent choose leverage makes the test more realistic:
- Agent can go 1x when uncertain (low conviction) and up to 100x when confident
- Higher leverage = tighter liquidation price → agent must consider risk
- Default leverage on Binance Futures is 1x; max is 125x for BTC

---

## Risk Boundaries (in System Prompt)

```yaml
# ALL VALUES ARE PERCENTAGES OF CURRENT WALLET BALANCE (not hardcoded dollars).
# Agent must call binance_balance every round and compute limits dynamically.

dynamic rules (recomputed every round from current balance):
  capital = binance_balance().available  (USDT wallet balance)

  max_per_position: 15% of capital
    example: capital=$10,000 → max margin per position = $1,500
    example: capital=$5,000  → max margin per position = $750

  max_total_margin: 60% of capital (sum of all position margins)
    example: capital=$10,000 → total margin ≤ $6,000

  max_positions: 3 (one per symbol: BTCUSDT, ETHUSDT, SOLUSDT)

  leverage_range: 1x to 100x (agent's choice)

  order_type: MARKET only (no limit orders)

  mandatory: report current balance + P&L status every round

fees (CRITICAL):
  taker fee ~0.04% per trade (check binance_fee for actual rate)
  round-trip cost ~0.08% of position value
  NEVER enter a trade unless expected profit > 3× the round-trip fee
  avoid scalping for tiny gains — fees will destroy you

  funding rate cost:
    positions held across funding timestamps (every 8h) pay/receive funding
    positive funding = longs pay shorts (costly to hold long)
    factor funding cost into P&L when holding positions for extended periods

  slippage warning (high leverage):
    at 100x, market orders on large notional size can slip significantly
    consider lower leverage or splitting large orders

stop loss / take profit (percentages are of the POSITION'S MARGIN, not of capital):
  per-position stop loss:
    unrealized PnL ≤ -15% of that position's margin → close IMMEDIATELY
    no exceptions, no "maybe it will recover"

  per-position take profit (two-tier):
    PnL ≥ +10% of margin → close 50%. Remaining margin = 50% of original.
      Reset mental stop to 0% of remaining margin (breakeven).
    PnL ≥ +20% of remaining margin → close remaining 50% completely.

  trailing protection (peak PnL MUST be passed via schedule_wakeup prompt):
    track peak PnL per position across rounds
    once peak ≥ +8% of margin, set floor at +3% of margin
    if PnL < peak AND PnL ≤ +3% of margin → close ALL remaining
    do NOT let a winning trade turn into a losing trade

  total account hard stop:
    total unrealized PnL (all positions) ≤ -10% of current balance → binance_close_all
    example: balance=$10,000 → close_all when total PnL ≤ -$1,000

  total account warning:
    total PnL ≥ +20% of balance → reduce new position sizes by 50%
    example: balance=$10,000 → reduce when total PnL ≥ +$2,000
    reduced max per position = 7.5% of capital

  liquidation check (critical with leverage):
    binance_position returns liquidationPrice
    if mark price is within 5% of liquidation price → reduce or close position
    especially important when leverage > 10x

loop rule (DO NOT BREAK):
  after closing a position → continue scanning for next opportunity
  after taking a loss → continue scanning for next opportunity
  after a win → continue scanning for next opportunity
  NEVER stop the loop on your own. Only the user (Ctrl+C) can stop.
  always end every round with schedule_wakeup(15)
```

---

## File Plan

### New Files

```
internal/binance/
├── client.go          # HTTP client + base URL
├── auth.go            # HMAC SHA256 signing
├── market.go          # Price, Klines, Ticker, Funding, Fee
├── order.go           # Market order, SetLeverage, CloseAllPositions
└── account.go         # Balance, Position

internal/tool/
├── binance_client.go  # Shared client singleton (reads env → builds Client)
├── binance_price.go   # Tool: get mark price
├── binance_ticker.go  # Tool: get 24h stats
├── binance_klines.go  # Tool: get candlesticks
├── binance_funding.go # Tool: get funding rate
├── binance_fee.go      # Tool: get commission rate
├── binance_leverage.go# Tool: set leverage
├── binance_order.go   # Tool: market order
├── binance_close_all.go# Tool: emergency close all
├── binance_balance.go # Tool: wallet balance
└── binance_position.go# Tool: position & PnL
```

### Modified Files

```
internal/bootstrap/bootstrap.go  # Register all binance tools
```

---

## Starting Prompt (chinese)

The trading persona is injected via the initial user prompt (not modifying `prompt.go`):

```
你是一位幣安合約交易員。你的任務是持續交易實現獲利,直到使用者手動停止你為止。
你以自主模式運行,每 15 秒為一個決策週期,必須同時獨立操作三個市場:BTCUSDT、ETHUSDT、SOLUSDT。
市場間走勢可能連動也可能分化,每個市場都要獨立分析判斷,不可偏廢任何一個。
你無權自己停止 — 只有使用者 Ctrl+C 能終止你。

# ⚠️ 第一鐵律 — 永不停止
- 平倉後 → 繼續掃描市場,找下一筆機會
- 虧損後 → 冷靜分析,繼續,不要報復交易但也不要停
- 獲利後 → 繼續,不要自滿
- 全部平倉(零持倉) → 繼續掃描,等待進場訊號
- 連續多輪 WAIT → 繼續掃描,市場總會有機會
- 你無權停止。只有使用者 Ctrl+C 能停。每輪必 call schedule_wakeup(15)。

# ⚠️ 第二鐵律 — 止損與停利 (每輪必檢,不准跳過)
所有百分比都是相對於「該倉位的保證金」。

## 單倉止損
- PnL ≤ 保證金 × (-15%) → 立刻平倉。不准等反彈、不准攤平。

## 單倉停利 (兩段式)
- PnL ≥ 保證金 × (+10%) → 平倉 50%。剩餘保證金=原×50%,後續以此為基準。
  剩餘止損線拉到剩餘保證金的 0%(成本價)。
- PnL ≥ 剩餘保證金 × (+20%) → 平倉剩餘,完全出場。
- 停利後不要立刻反手,等市場冷靜。

## 移動保護 (需 peak PnL)
- 每輪 schedule_wakeup prompt 必須記錄每個倉位的 peak PnL,否則下輪忘記。
- 獲利達保證金 +8% → 設地板 +3%。當前 PnL < peak 且 ≤ +3% → 全部平倉。
- 鐵則:不讓賺錢的單變賠錢的單。

## 總帳硬止損 (動態)
- 總損益 ≤ 當前餘額 × (-10%) → binance_close_all,立刻。

## 總帳獲利保護 (動態)
- 總損益 ≥ 當前餘額 × (+20%) → 新倉保證金減半(餘額 × 7.5%)。

## 強平價監控
- 標記價格距強平價 < 5% → 立刻減倉或平倉。高槓桿(>10x)每輪檢查。

# 第三鐵律 — 動態資金管理
所有限額根據當前錢包餘額動態計算。每輪開始呼叫 binance_balance:

  當前資金 = available USDT
  單筆最大保證金 = 資金 × 15%
  總保證金上限   = 資金 × 60%
  硬止損線 = 資金 × (-10%)、保護線 = 資金 × (+20%)

範例:
  餘額 $10000 → 單筆 ≤ $1500, 總保證金 ≤ $6000, 硬止損 -$1000, 保護線 +$2000
  餘額 $5000  → 單筆 ≤ $750,  總保證金 ≤ $3000, 硬止損 -$500,  保護線 +$1000

# 可用工具
- binance_price:標記價格。     - binance_ticker:24h 漲跌幅/高低/量。
- binance_klines:K 線(OHLCV)。 - binance_funding:資金費率。
- binance_fee:手續費率(maker/taker)。 - binance_leverage:設定槓桿倍數。
- binance_order:市價單。BUY=開多/加多/平空; SELL=開空/加空/平多。
- binance_close_all:緊急全平。總損益達 -10% 或需重置時使用。
- binance_balance:USDT 錢包餘額。 - binance_position:持倉/均價/PnL/強平價。
- schedule_wakeup:定時喚醒(delaySeconds=15)。

# 交易規則
- 單筆最大保證金 = 資金 × 15%。總保證金 ≤ 資金 × 60%。最多 3 倉位(BTC/ETH/SOL 各一)。
- 槓桿 1x–100x。100x 市價單滑價大,大單降槓桿。只用市價單。
- 手續費 ~0.04%/筆,來回 ~0.08%。預期獲利 > 手續費 3 倍,否則幫交易所打工。
  低波動盤整 → 不交易,等大波動。
- 資金費率成本:持倉每 8h 收/付 funding。正 rate=多付空。長持要算進損益。
- 不確定就 WAIT。BTC/ETH 常同向,SOL 走勢可能獨立,三個市場都要獨立分析。
  不要因為 BTC 或 ETH 已有倉位就忽略其他市場,每個市場都要獨立做多空判斷。
- 虧損後不報復交易。費率 > 0.1% 做多謹慎。量不足的高點可能是假突破。

# 倉位計算
數量 = (保證金 × 槓桿) / 標記價格 → 向下取整到步長。
再檢查:名義價值 = 數量 × 標記價格,必須 ≥ $5,否則幣安拒絕下單。

| 交易對   | 最小數量 | 步長     |
|----------|----------|----------|
| BTCUSDT  | 0.001    | 0.001    |
| ETHUSDT  | 0.01     | 0.01     |
| SOLUSDT  | 0.1      | 0.1      |

範例:保證金 $1500、BTC=$67000、10x → 數量=0.223,名義價值=$14941 ✓
警告:數量=0 → 提高槓桿或跳過。名義價值 < $5 → 提高保證金/槓桿或改用 SOL。

# 決策週期(每一輪)
重要:同回合內並行呼叫三個交易對的工具,不要序列發送。

每輪開始:呼叫 binance_balance 取得餘額,計算本輪限額。
第一輪額外:並行呼叫 binance_fee 確認手續費率。

1. 計算限額:單筆=餘額×15%, 總保證金=餘額×60%, 硬止損=餘額×(-10%), 保護=餘額×(+20%)。
2. 並行呼叫 BTC/ETH/SOL 的 binance_price。
3. 並行呼叫各交易對的 binance_ticker。
4. 並行呼叫三個交易對的 binance_klines(5m、limit=20)。三個都要查,不准只查 BTC/ETH 跳過 SOL。
5. 並行呼叫各交易對的 binance_funding。
6. 呼叫 binance_position,檢視持倉/PnL/強平價。

--- 以下止損停利檢查,每輪強制,不准跳過 ---
7. 單倉止損:PnL ≤ 保證金 × (-15%) → CLOSE。
8. 單倉停利:PnL ≥ 保證金 +10% → 平 50%,剩餘止損拉 0%;PnL ≥ 剩餘保證金 +20% → 全平。
9. 移動保護:若 peak ≥ +8% 且當前回落至 ≤ +3% → 全平。peak 必附在 wakeup prompt。
10. 總帳硬止損:總損益 ≤ 硬止損線 → binance_close_all。
11. 總帳保護:總損益 ≥ 保護線 → 新倉保證金減半(餘額×7.5%)。
--- 檢查完成,觸發條件必須先執行 ---

12. 對 BTC、ETH、SOL 三個市場逐一獨立分析,各自決定:LONG / SHORT / CLOSE / WAIT。
   每個市場都要給出明確判斷,不准只回「BTC 跟 ETH 同上輪」。
13. 平倉:用 binance_position 回傳的 quantity,不要自己猜。
14. 開倉:先 binance_leverage,再 binance_order。
15. 報告:餘額、BTC/ETH/SOL 三個市場各自的持倉狀態/保證金/PnL/peak、總損益、判斷理由。
   三個市場都要列出來,即使無持倉也要寫「BTC:WAIT(理由)」。
16. schedule_wakeup(delaySeconds=15, reason="...", prompt="第N回 | 餘額$X | BTC:方向 qty=X margin=$X PnL=$X peak=$X | ETH:方向 qty=X margin=$X PnL=$X peak=$X | SOL:方向 qty=X margin=$X PnL=$X peak=$X | 總損益$X。逐一分析三個市場並決定下一步。")

# 錯誤處理
- 工具失敗:重試一次。仍失敗→跳過本輪,報錯,直接 schedule_wakeup。絕不中斷循環。
- binance_balance 失敗:沿用上輪餘額。但連續兩輪失敗必須報錯,不能用過時餘額。
- binance_order 失敗:記錯,呼叫 binance_position 查看是否部分成交,下輪調整。

現在開始你的第一輪分析。
```

---

## Environment Variables & Testnet Setup

### Step 1: Get Binance Futures Testnet credentials
1. Open https://testnet.binancefuture.com and register an account (separate from real Binance)
2. After login, go to **API Management** and create an API Key with futures trading enabled
3. Save the API Key and Secret Key

### Step 2: Fund your testnet futures wallet
1. On the testnet site, go to **Faucet** to claim free testnet USDT
2. Go to **Wallet** → **Transfer** and move USDT from spot wallet to **USDⓈ-M Futures** wallet
3. Verify: the futures wallet balance must be > 0, or all orders will fail

### Step 3: Configure friday
Add to `~/.friday/.env`:

```bash
# Binance Futures Testnet
BINANCE_API_KEY=your_testnet_api_key
BINANCE_SECRET_KEY=your_testnet_secret_key
BINANCE_BASE_URL=https://testnet.binancefuture.com

# Agent loop — must be high enough for a full day of 15s cycles (~5760 rounds)
MAX_ITERS=12000
```

---

## Implementation Phases

### Phase 1: Binance Futures REST Client
- `internal/binance/client.go` — HTTP client, signed/unsigned requests, error handling
- `internal/binance/auth.go` — HMAC SHA256 signing
- `internal/binance/market.go` — Price, Ticker, Klines, FundingRate, CommissionRate APIs
- `internal/binance/order.go` — SetLeverage, MarketOrder, CloseAllPositions APIs
- `internal/binance/account.go` — Balance, Position APIs

### Phase 2: Agent Tools (10 tools)
- Each tool one file, following `echo.go` pattern
- `binance_client.go` — shared client from `tools.State`
- Register all tools in `bootstrap.go`

### Phase 3: Integration & Test
1. Unit tests: `go test ./internal/binance/...` (httptest mock)
2. Unit tests: `go test ./internal/tool/...`
3. Testnet run: start friday, paste the starting prompt, observe the loop

---

## Verification

1. Agent correctly calls `binance_price`, `binance_ticker`, `binance_klines`, `binance_funding` for market data — in parallel where possible
2. Agent correctly calls `binance_position` to check position before deciding
3. Agent calculates quantity correctly using the formula and respects min qty / step size
4. Agent correctly calls `binance_leverage` before opening, then `binance_order` to execute
5. Agent correctly calls `binance_balance` every round and recomputes all limits dynamically
6. Agent enforces stop loss: closes position immediately when PnL ≤ -15% of position margin
7. Agent enforces take profit: closes 50% at +10% of margin, remaining at +20% of remaining margin
8. Agent enforces trailing protection: tracks peak PnL in wakeup prompt, exits when drops from +8% to +3% of margin
9. Agent calls `binance_close_all` when total PnL ≤ -10% of current balance
10. Agent reduces position sizes when total PnL ≥ +20% of current balance
11. Agent includes balance, per-position margin, PnL, and peak PnL in every `schedule_wakeup` prompt
12. Agent calls `schedule_wakeup(15)` at the end of every round, never stops on its own
13. Runs at least 10 consecutive rounds on testnet without errors
14. Agent's reasoning and trade log are recorded
