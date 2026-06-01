# Friday PnL 改善計畫

基於 `~/.friday/memory/trades.jsonl`（12 筆交易，1W/11L，WR=8.3%）和
`rounds.jsonl`（149 輪分析）的虧損分析，本計畫針對五個核心問題提出兩個 PRD：

| 問題 | 數據 | 歸屬 |
|------|------|------|
| RSI 進場在極端值（接刀/追高） | SOL LONG@RSI=30, NVDA SHORT@RSI=26 | PRD-022 |
| MTF 過濾過嚴（0.48% 訊號率） | 149 輪僅 5 次非 WAIT，Cross-TF 17 次被否決 | PRD-022 |
| 手續費侵蝕（45% 總虧損） | 12 筆手續費 -$61.95，淨虧 -$198.29 | PRD-023 |
| 逆勢偏多（8 LONG vs 4 SHORT） | LONG 虧 -$176.48，恐懼指數驅動逆勢做多 | PRD-023 |
| Recall 負回饋循環 | 全虧紀錄 → 不敢交易 → 無法累積贏單 | PRD-023 |

所有現有 PRD (001–020) 已實作完成。SOL 虧損（-$103.47）不在本次範圍內。

---

## PRD-022 — Strategy Signal Quality: RSI Entry Filter + MTF Responsiveness

### 1. RSI Zone Entry Filter

#### 問題

多筆交易在不利的 RSI 水位進場：SOL LONG @ RSI=30（接刀續跌）、
NVDA SHORT @ RSI=26（超賣放空被軋）、NVDA LONG @ RSI=71（過熱追高）。
現有策略各自有內部 RSI 條件，但沒有一個**全局的 RSI 進場過濾器**
在訊號匯總階段攔截極端 RSI 的訊號。

#### 方案

在 `internal/strategy/` 新增 RSI zone 過濾，在 `AggregateMTF` 之前
對每個 TF 的 Consensus 做 RSI 區間檢查：

- LONG 時若 5m RSI ≥ 75（超買）或 ≤ 25（深超賣）：降級為 NEUTRAL
- SHORT 時若 5m RSI ≤ 25（超賣）或 ≥ 75（超買）：降級為 NEUTRAL
- 中性區（25 < RSI < 75）正常通過
- `FRIDAY_RSI_FILTER=false` 可關閉

**RSI zone label 也加入 klines semantic summary**（`indicators.go`），
讓 Analyst 看到 `RSI 71.3 (overbought)` 而非只有數字。

#### 影響範圍

- `internal/strategy/rsi_filter.go`（新增，~60 行）
- `internal/strategy/aggregator.go`（`AggregateMTF` 內調用 filter）
- `internal/binance/indicators.go`（`SemanticSummary` 加 RSI zone label）
- `internal/strategy/strategy_test.go`（table-driven tests）

### 2. MTF Strategy Responsiveness Tuning

#### 問題

`AggregateMTF` 權重 5m=1.0 / 1h=1.5 / 4h=2.0，hysteresis ±0.1。
4h 幾乎總是 NEUTRAL，5m+1h 即使同時給出 ALIGNED 也無法克服 4h 的零權重。
149 輪中 MTF Strategy 僅 5 次觸發，Cross-TF 的 17 次方向訊號全部被否決。

#### 方案

兩個改動，各有獨立 env var 開關：

**A. 降低 hysteresis：±0.1 → ±0.05**（`FRIDAY_MTF_HYSTERESIS`，預設 0.05）
- 當前：5m LONG 0.6 + 4h NEUTRAL → net +0.6 < 0.1，NEUTRAL
- 改後：net +0.6 > 0.05 → LONG。讓單一 TF 在沒有高 TF 反對時可以觸發。

**B. 5m+1h 共識覆寫**（`FRIDAY_MTF_5M1H_OVERRIDE`，預設 true）
- 當 5m 和 1h 方向一致且各自 confidence ≥ 0.5，即使 4h 是 NEUTRAL，
  也採用 5m+1h 的方向（confidence 取兩者平均）
- 只有 4h 明確反向時才否決（4h SHORT vs 5m+1h LONG → NEUTRAL）

#### 影響範圍

- `internal/strategy/aggregator.go`（調整 `AggregateMTF`，~20 行）
- `internal/strategy/strategy_test.go`（更新 test cases）

### 驗證

- `go test ./internal/strategy/...`
- 單元測試：RSI=80 LONG → NEUTRAL, RSI=20 SHORT → NEUTRAL
- 單元測試：5m LONG 0.6 + 4h NEUTRAL → LONG（原 NEUTRAL）
- 單元測試：5m LONG 0.7 + 1h LONG 0.6 + 4h NEUTRAL → LONG（override）
- 單元測試：5m LONG 0.7 + 4h SHORT 0.5 → NEUTRAL（4h 反向，不覆寫）
- 啟動 friday 確認 klines summary 出現 `(overbought)` / `(oversold)` label

---

## PRD-023 — Analyst Decision Quality: Regime Clamp + Fee Awareness + Recall Fix

### 1. Regime-Aware Bias Clamp

#### 問題

8 筆 LONG 在 BTC 從 ~74000 跌到 ~71100 的趨勢中逆勢虧損 -$176.48。
Fear & Greed 23-29 頻繁觸發「極恐逆勢做多」邏輯（entry_reason:
"極度恐慌(23)經典逆勢做多"、"恐懼28順風"）。

`strategy.DetectRegime`（PRD-016）已能判斷 trending/ranging/transitional，
但 Analyst prompt 中**沒有任何規則**限制在 trending bearish 市場中開 LONG。

#### 方案

在 `analystSystemPrompt` 中加入：

> **Regime-aware bias rule:** When the `Regime:` line shows **TRENDING** and
> the 4h price is below 4h MA20 (bearish trend):
> - LONG bias requires BOTH: (a) `MTF Strategy:` explicitly says LONG, AND
>   (b) Fear & Greed ≤ 25 (extreme fear). Without both, you MUST use NEUTRAL.
> - SHORT bias is permitted with `MTF Strategy: SHORT` or
>   `Cross-TF: ALIGNED BEARISH`.
> - When `Regime:` is RANGING or TRANSITIONAL, all biases are permitted.

#### 影響範圍

- `internal/orchestrator/prompts.go`（`analystSystemPrompt`，~10 行）

### 2. Fee-Aware Entry Rule + Fee Budget Surface

#### 問題

手續費佔總虧損 45.4%。PRD-020 已實作 `FeeBudget` guardrail（30min / 0.5%），
但 Analyst prompt 中**完全沒有提到手續費**，且 FeeBudget 狀態也未注入 prompt
（PRD-020 的 R9 未實作）。

#### 方案

**A. Analyst prompt 加入手續費規則：**

> **Fee-aware sizing rule:** Every trade must expect a move that clears at
> least **3× the round-trip fee** (~0.08% taker → ~0.24% minimum expected
> move). If ATR or strategy TP < 3× fee, do not open. State expected move.

**B. Fee budget surface：** orchestrator 構建 round prompt 時，若
`FeeBudget.Status().Near` 為 true，注入：

> `⚠️ Fee budget: $X.XX spent / ${cap} in the last 30m.`

#### 影響範圍

- `internal/orchestrator/prompts.go`（`analystSystemPrompt`，~5 行）
- `internal/orchestrator/orchestrator.go`（讀取 `FeeBudget.Status()` 注入 prompt，~10 行）

### 3. Recall Memory: Minimum Sample Threshold

#### 問題

`recall_trades` 在樣本不足（2-3 筆全虧）時回傳勝率，Analyst 大量引用來否決進場
（"回溯相似條件交易全部虧損"、"Recall 3筆全敗"）。因整體勝率僅 8.3%，幾乎所有
recall 查詢都回傳全虧，形成**負回饋循環**：虧損 → recall 全虧 → 不敢交易 →
無法累積贏單 → recall 繼續全虧。

#### 方案

`recall_trades` 在相似交易 < 5 筆時，標記為 `"insufficient data"` 而非報告勝率。
Analyst prompt 提醒：樣本不足時不應作為否決理由。

#### 影響範圍

- `internal/memory/store.go`（`Similar` 回傳值加入 `IsConclusive`，~5 行）
- `internal/tool/recall_trades.go`（格式化輸出 "insufficient data (<5 trades)"，~5 行）
- `internal/orchestrator/prompts.go`（Analyst prompt 提醒，~3 行）

### 驗證

- 檢查 prompt 輸出包含新增的三條規則
- 單元測試：3 筆相似交易 → `IsConclusive=false`，6 筆 → `IsConclusive=true`
- `go test ./internal/memory/...`
- 啟動 friday 確認 prompt 有 fee budget status 行

---

## 實施順序

```
PRD-022 (signal quality) ── 先改策略引擎，讓訊號品質和數量都提升
    │
    └── PRD-023 (decision quality) ── 再改 Analyst，讓它在更好訊號基礎上做更聰明的決策
```

兩個 PRD 可部分並行（PRD-022 改 Go 程式碼，PRD-023 主要改 prompt，不衝突）。
