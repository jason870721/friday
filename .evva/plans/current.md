# Friday Signal Quantity & Analyst Resilience 改善計畫

基於 `~/.friday/memory/rounds.jsonl`（106 輪，兩個交易時段）和 `trades.jsonl`（2 筆交易，
+$22.38）的完整數據分析，本計畫針對 MTF Strategy 訊號率過低（~3% 非 NEUTRAL 輪次）和
Analyst 長連 NEUTRAL 後分析品質退化兩個核心問題，提出四個 PRD。

## 數據分析摘要

兩個交易時段共 106 輪（Session 1: 16 輪，6/1；Session 2: 90 輪，6/4）：

| 指標 | 數據 |
|------|------|
| 總輪數 | 106 |
| 有交易執行的輪數 | 3（2.8%） |
| 總交易筆數 | 2（ETH SHORT，分兩次平倉） |
| 淨盈虧 | +$22.38 |
| MTF Strategy NEUTRAL 輪數 | ~103（97%） |
| Cross-TF ALIGNED 出現次數 | 20+ 次（全被 MTF NEUTRAL 否決） |
| Analyst 退化為「凍結」 | Round 80+（連續 10+ 輪無意義輸出） |

### 根因分析

MTF Strategy 幾乎永遠 NEUTRAL 的原因鏈：

1. **5m 蠟燭數量過少（20 根）** → EMA cross 策略需要 50 根無法參與投票，
   其他策略也僅勉強達標（momentum/mean_reversion 需要 20，bollinger 需要 21）
2. **單策略觸發條件嚴格** → 5 個策略各有窄門檻（momentum 需 RSI 50-70 + 3 連漲，
   mean_reversion 需偏離 MA20 2% + RSI <30 或 >70…），多數輪次 0-1 個策略觸發
3. **Aggregate 需要 ≥2 同向信號** → 單一策略觸發不足以形成 Consensus
4. **Calibration 可能歸零邊緣策略** → backtest 樣本不足的策略被設為 confidence=0，直接禁用
5. **MTF 加權不利 5m** → 4h 權重 2.0 但 4h 幾乎永遠 NEUTRAL（48 根慢蠟燭難以形成 ≥2 同向），
   5m+1h override 需兩者同時 confidence ≥0.5，門檻仍高
6. **Regime weights 在熊市中壓制 mean_reversion（×0.3）和 bollinger**，
   只剩 momentum/breakout/ema_cross 能有效投票，但 ema_cross 在 5m 無法運作

結果：每輪實際能投票的策略只有 2-3 個，且多數輪次無任何策略觸發 → Consensus NEUTRAL →
MTF Strategy NEUTRAL → 不交易。

---

## PRD-024 — Signal Quantity: Increase 5m Candle Count for Full Strategy Participation

### 問題

`binance_mtf_klines` 的 5m 時間框架只取 **20 根蠟燭**（`internal/tool/binance_mtf_klines.go:60`）。
這導致：

- **EMA cross 策略完全無法參與 5m 投票**（需要 50 根，只有 20 根 → `insufficient candles`）
- Momentum / MeanReversion 僅勉強達標（需要 20 根）
- Bollinger 僅勉強達標（需要 21 根）
- 校準 backtest 也只用 200 根 4h 蠟燭，5m 根本沒有足夠數據計算有效勝率

5 個策略中，在 5m 上實際能運作的只有 3-4 個（且 bollinger 在 20 根蠟燭上頻寬不穩定）。
這是 MTF Strategy 訊號率過低的**結構性原因**——不是策略條件太嚴，而是根本沒有足夠數據
讓策略運行。

### 方案

**將 5m 蠟燭數量從 20 提高到 96**（8 小時的 5m 數據，與 1h×24 和 4h×48 的時間跨度一致）：

- `internal/tool/binance_mtf_klines.go:60`: `{"5m", 20}` → `{"5m", 96}`
- 同步更新 `binance_mtf_klines` 的 description：`5m (last 100 min)` → `5m (last 8 hours)`

這讓 **所有 5 個策略都能在 5m 時間框架上完整運行**，大幅提高每輪的策略投票數量。

#### 副作用與緩解

- 5m klines 回應體積增大（20 → 96 根，約 4.8×），但仍在可接受範圍（~15KB JSON）
- LLM 的 `binance_mtf_klines` 輸出會稍長，但 Analyst 本來就要讀所有 TF 的 summary
- API rate limit：Binance 的 klines endpoint weight 為 2（不論 limit），96 根不增加 weight
- 若擔心延遲：fetch 已是 concurrent（goroutine），5m/1h/4h 並行請求

### 影響範圍

- `internal/tool/binance_mtf_klines.go`（改 `mtfFrames` 的 5m Limit，~1 行）
- 無需改測試（現有測試不依賴特定 limit 值）

### 驗證

- `go build ./... && go test ./internal/tool/...`
- 啟動 friday，觀察 5m summary 中 `Strategy signals:` 行是否比之前更頻繁出現非 NEUTRAL 結果
- 特別檢查 EMA cross 策略是否開始在 5m 上產生訊號（之前永遠是 "insufficient candles"）

---

## PRD-025 — MTF Strategy: Quorum-Based Voting When 4h is Silent

### 問題

PRD-022 已降低 hysteresis 到 0.05 並加入 5m+1h override，但從 Session 2 的數據看，
MTF Strategy 連續 87 輪 NEUTRAL（Round 4–91），即使：

- BTC 從 62,221 漲到 64,223（+3.2%）
- Cross-TF 出現 10+ 次 ALIGNED BULLISH
- BTC 5m 連續 10 輪站穩 MA20，RSI > 50
- 1h RSI 從 25 回升到 45

override 沒觸發的原因是 **5m 和 1h 很少同時 confidence ≥0.5**——當 5m 有訊號時
1h 還沒有，反之亦然。加權投票在 4h NEUTRAL 時變成單一 TF 獨撐（net ≈ ±0.6，
hysteresis=0.05 應可通過，但實際數據中 5m 的單一 Consensus 本身也很少形成）。

根本問題不在 MTF 聚合層，而在**單時間框架內的 Consensus 形成率太低**（PRD-024
解決一部分）。但即使 PRD-024 提高了策略投票數，MTF 的 override 門檻（兩個 TF
都要 confidence ≥0.5）仍然太嚴。

### 方案

兩個互補改動：

**A. 降低 5m+1h override 的 confidence 門檻：0.5 → 0.35**
（`internal/strategy/aggregator.go:218`，`lowerTFOverride`）

- 當前：5m 和 1h 都要 confidence ≥0.5。單一策略的 base confidence 是 0.6（momentum）
  或 0.55（ema_cross/breakout），但經過 calibration 和 regime weight 調整後可能降到
  0.3-0.5。0.5 的門檻把太多邊緣有效的訊號排除在外。
- 改為 0.35：只要兩個低時間框架的共識方向一致且至少有低到中等的信心，就採用。
  4h 反對仍然是 hard veto（不變）。

**B. 新增 Quorum 模式**（`FRIDAY_MTF_QUORUM=true`，預設 true）

當 4h 是 NEUTRAL 時，改用 **2-of-3 quorum** 取代加權投票：

- 任意 2 個時間框架方向一致 → 採用該方向，confidence = 兩者的平均
- 三個方向都不同或全 NEUTRAL → NEUTRAL
- 4h 有明確方向時回到加權投票模式（保留現有邏輯）

這比 override 更寬鬆：不需要兩個都是低時間框架，1h+4h 一致也可以觸發。

### 影響範圍

- `internal/strategy/aggregator.go`（`AggregateMTF` 加入 quorum 分支，~25 行）
- `internal/strategy/rsi_filter.go`（新增 `FRIDAY_MTF_QUORUM` env var，~5 行）
- `internal/strategy/strategy_test.go`（新增 quorum test cases）

### 驗證

- `go test ./internal/strategy/...`
- 單元測試：5m LONG 0.4 + 1h LONG 0.4 + 4h NEUTRAL → LONG（override 0.35 門檻通過）
- 單元測試：5m LONG 0.6 + 1h NEUTRAL + 4h LONG 0.5 → LONG（quorum: 5m+4h 一致）
- 單元測試：5m LONG 0.5 + 1h SHORT 0.5 + 4h NEUTRAL → NEUTRAL（quorum 無 2 票一致）
- 單元測試：5m LONG 0.5 + 1h NEUTRAL + 4h SHORT 0.5 → NEUTRAL（4h 反向，加權模式 veto）
- 啟動 friday 確認 MTF Strategy 非 NEUTRAL 的頻率明顯提升

---

## PRD-026 — Analyst Resilience: Prevent Degradation on Long NEUTRAL Streaks

### 問題

在 Session 2 中，Analyst 的輸出從 Round 1-70 的詳細分析（每個 symbol 有完整的 RSI、
MA20、動能、key_levels、summary），退化到 Round 80+ 的單字「凍結」：

```
Round 85 summary: "凍結。"
Round 88 summary: "凍結。"
Round 90 summary: "凍結。"
```

這發生在 MTF Strategy 連續 80+ 輪 NEUTRAL 之後。Analyst（LLM）在反覆看到相同的
「MTF Strategy: NEUTRAL」輸出後，學習到「反正不會交易，不需要詳細分析」，開始偷懶。

但這造成兩個問題：
1. **喪失情境感知**：當市場真的轉折時（如 Round 79 的 BTC 突破 64k），Analyst 可能
   因為長期輸出簡化而錯過信號
2. **Round log 失去價值**：post-mortem 分析工具（PRD-021）依賴詳細的 round log，
   「凍結」沒有任何可用資訊

### 方案

三個互補改動：

**A. Analyst prompt 加入「禁止退化」規則**（`internal/orchestrator/prompt_templates.go`）

在 Analyst system prompt 的 Output 段落加入：

> **Analysis quality rule (MANDATORY):** Even when every symbol's bias is NEUTRAL and
> no trade is expected, you MUST write a concrete, numeric summary for EACH symbol
> (price, RSI, MA20 relationship, momentum direction) — just as you would for an
> actionable setup. The round log is used for post-mortem analysis and a one-word
> summary like "凍結" destroys its value. A NEUTRAL round still has important
> information: "BTC held 64,200 (RSI 87, overbought), ETH consolidated at 1,797,
> SOL ranged 71.40-71.50" — write THAT, not "凍結".

**B. 在 carry 中注入「連續 NEUTRAL 輪數提醒」**（`internal/orchestrator/orchestrator.go`）

當連續 NEUTRAL ≥ 10 輪時，在下一輪的 Analyst prompt 中加入：

> `⚠️ 已連續 N 輪無交易。市場可能在醞釀突破——請保持警惕，不要因長期觀望而降低分析品質。`

這提醒 Analyst 不要陷入「自動駕駛」模式。實作方式：在 `runRound` 中追蹤
`consecutiveNeutral` 計數器，anyActionable 為 false 時 +1，有交易時歸零。

**C. Analyst 在無 MTF 訊號時，應更重視 Cross-TF 和價格行為**

在 Analyst prompt 的方法段落補充：

> When the "MTF Strategy:" line is NEUTRAL for many consecutive rounds but the
> "Cross-TF:" line repeatedly shows ALIGNED (BULLISH or BEARISH), and price is
> persistently above/below 5m MA20 — flag this divergence in your analyst_notes.
> The MTF Strategy may be lagging; the Cross-TF and price action may be leading
> indicators of an imminent signal. Describe WHAT you are waiting for (e.g. "waiting
> for MTF Strategy LONG confirmation; BTC needs 1h RSI > 50 or 5m+1h consensus ≥2").

### 影響範圍

- `internal/orchestrator/prompt_templates.go`（analystSystemPrompt 加兩條規則，~15 行）
- `internal/orchestrator/orchestrator.go`（`Orchestrator` struct 加 `consecutiveNeutral` 欄位，
  `runRound` 中更新計數並注入提醒到 carry，~15 行）

### 驗證

- `go build ./... && go vet ./...`
- 檢查 Analyst prompt 包含新的 analysis quality rule
- 啟動 friday paper mode，讓它跑 10+ 輪 NEUTRAL，確認 carry 中出現提醒
- 觀察 Analyst 輸出不再出現「凍結」等退化摘要

---

## PRD-027 — Strategy Engine Observability: Per-Strategy Reason Surface

### 問題

目前當 MTF Strategy 輸出 NEUTRAL 時，外界完全不知道**為什麼**——是沒有策略觸發？
還是觸發了但方向衝突？還是被 RSI filter 攔截？還是 calibration 歸零？

從 round log 只能看到最終結果 `MTF Strategy: NEUTRAL (5m:NEUTRAL + 1h:NEUTRAL + 4h:NEUTRAL) → weighted NEUTRAL 0.00`，
無法判斷是 5 個策略全都沒觸發，還是觸發了但被 filter/calibration/aggregation 否決。

這造成：
1. **無法遠端診斷**：不看程式碼就不知道 NEUTRAL 的原因
2. **無法 tuning**：不知道該放寬哪個參數
3. **LLM 缺乏 context**：Analyst 只知道「MTF Strategy NEUTRAL」，不知道「差一點就 LONG」
   （例如 momentum 觸發但缺第二票，或 mean_reversion 觸發但被 RSI filter 攔截）

### 方案

**在 MTF Strategy 輸出中加入 per-TF 策略觸發細節。**

修改 `internal/tool/binance_mtf_klines.go` 中 MTF Strategy 行的渲染：

當前輸出：
```
MTF Strategy: NEUTRAL (5m:NEUTRAL + 1h:NEUTRAL + 4h:NEUTRAL) → weighted NEUTRAL 0.00
```

改為：
```
MTF Strategy: NEUTRAL (5m:NEUTRAL + 1h:NEUTRAL + 4h:NEUTRAL) → weighted NEUTRAL 0.00
  5m signals: momentum LONG(0.55) inval=63250 — only 1 directional (need ≥2)
  1h signals: mean_reversion LONG(0.18) — regime-weighted down from 0.60 in TRENDING
  4h signals: none fired (bollinger inside bands, ema_cross insufficient candles)
```

具體實作：

**A. 在 `Consensus` 中加入 `SignalDetails` 欄位**（`internal/strategy/strategy.go`）

```go
type Consensus struct {
    // ... existing fields ...
    SignalDetails string // per-strategy firing reasons for observability
}
```

**B. `Aggregate` 函數產生 `SignalDetails`**（`internal/strategy/aggregator.go`）

在 `summarise` 邏輯中，為每個輸入 signal（不論方向）產生一行描述：
- directional signals：`"momentum LONG(0.55) inval=63250"`
- neutral signals with reasons：`"mean_reversion: not stretched (1.2% from MA20, RSI 45)"`
- insufficient data：`"ema_cross: insufficient candles for EMA50 (have 20, need 50)"`

對於 NEUTRAL consensus，特別標註「差多少」：
- `"only 1 directional (need ≥2)"`
- `"conflict (momentum LONG vs breakout SHORT)"`

**C. `mtfPart` 渲染時包含 `SignalDetails`**（`internal/strategy/aggregator.go`）

在 MTF 行後附加每個 TF 的 signal details（僅當內容非空時）。

### 影響範圍

- `internal/strategy/strategy.go`（`Consensus` struct 加 `SignalDetails` 欄位，~2 行）
- `internal/strategy/aggregator.go`（`Aggregate` 中填充 `SignalDetails`，`summarise` 改寫，~30 行；
  `mtfPart` 和 `AggregateMTF` 中渲染 details，~15 行）
- `internal/strategy/strategy_test.go`（更新 test expectations，~10 行）
- `internal/tool/binance_mtf_klines.go`（MTF 行渲染時附加 details，~5 行）

### 非目標

- 不改變任何策略邏輯或 threshold
- 不新增 log 檔案（details 只在 LLM prompt 中呈現，回合日誌已有 rounds.jsonl）
- 不增加 API 呼叫（details 從已有的 signal 輸出中提取）

### 驗證

- `go test ./internal/strategy/...`
- 單元測試：確認 NEUTRAL consensus 的 `SignalDetails` 包含 "only 1 directional"
- 單元測試：確認 directional consensus 的 details 列出各策略名稱和信心值
- 啟動 friday，觀察 `binance_mtf_klines` 的 MTF Strategy 段落包含 per-TF signal details

---

## 實施順序

```
PRD-024 (5m candle count) ─── 根本性提高策略投票參與率，所有後續 PRD 的基礎
    │
    ├── PRD-027 (observability) ─── 與 024 並行，讓 024 的效果可被觀測和驗證
    │
    ├── PRD-025 (MTF quorum/override) ─── 在 024 提高訊號量後，讓聚合層更靈敏
    │
    └── PRD-026 (Analyst resilience) ─── 最後改 prompt，在更好的訊號基礎上防止退化
```

PRD-024 和 PRD-027 可完全並行（改不同檔案，無依賴）。
PRD-025 依賴 PRD-024（需要更多訊號才有意義）。
PRD-026 是純 prompt 改動，可與前面任一 PRD 並行。

### 預期效果

| 指標 | 當前 | 目標 |
|------|------|------|
| MTF Strategy 非 NEUTRAL 率 | ~3%（3/106 輪） | 15-25% |
| 5m 策略投票數（平均每輪） | 0-1 個 | 3-5 個 |
| Analyst 退化率（80+ 輪後） | 100%（10/10 輪「凍結」） | 0% |
| EMA cross 在 5m 的參與率 | 0%（蠟燭不足） | 正常參與 |
