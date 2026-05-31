# 策略引擎優化計畫

## Context

Friday 目前有三個策略（Momentum、Breakout、MeanReversion），但策略層是整個系統中最缺乏實證驗證的部分：信心值寫死（0.6 / 0.65）、策略數量太少導致 aggregator 難以形成共識、沒有績效追蹤所以不知道哪個策略在賺錢。

目標：讓策略引擎從「寫死的投票箱」變成「有實證依據、會自我調適的決策系統」。

---

## 對應 PRD

六個 PRD 依依賴鏈排序。每個 PRD 的完整需求、驗收標準、設計取捨記錄在 `docs/PRD/` 下。

### [PRD-013](../docs/PRD/PRD-013.md) — Strategy Portfolio Expansion

接上 Divergence 策略（已寫好但未接線）+ 新增 EMA 交叉策略。策略從 3 票變 5 票，aggregator 在強趨勢時不再因為 momentum / mean-reversion 互斥而卡住。

**依賴：** PRD-006
**阻擋：** PRD-015（需要 ≥4 策略才能有意義的校準）

### [PRD-014](../docs/PRD/PRD-014.md) — Strategy Performance Tracking

`TradeRecord` 加入 `strategy` 欄位，每筆平倉交易歸因到觸發策略。`recall_trades` 改為回傳勝負統計而非裸列表。Memory store 新增 `OutcomeSummary()`、`SimilarByStrategy()`。

**依賴：** PRD-004, PRD-006
**阻擋：** PRD-015（校準需要策略歸因的歷史交易數據）

### [PRD-015](../docs/PRD/PRD-015.md) — Confidence Calibration

用回測引擎重播策略邏輯，把寫死的信心值（0.6 魔術數字）取代為歷史勝率。`backtest.RunStrategy` → `strategy.Calibrate` → 啟動時注入 Registry。勝率 ≤50% 的策略自動歸零信心（不投票）。

**依賴：** PRD-004, PRD-006, PRD-014
**阻擋：** PRD-016（regime-aware 權重需要校準後的基礎權重）

### [PRD-016](../docs/PRD/PRD-016.md) — Market Regime Detection

用 ADX(14) 分類市場狀態（Trending / Ranging / Transitional），動態調整策略權重：趨勢時 momentum ×1.2、mean_reversion ×0.3；盤整時反過來。零信心策略自動排除。

**依賴：** PRD-006, PRD-015

### [PRD-017](../docs/PRD/PRD-017.md) — MTF Strategy Consensus

把策略引擎跑到 `binance_mtf_klines` 已拉回的 1h / 4h K 線上，三個時框加權投票（5m×1.0, 1h×1.5, 4h×2.0），高時框在衝突時主導。取代目前只用 `ClassifyDirection` 的粗糙跨時框判斷。

**依賴：** PRD-006, PRD-008

### [PRD-018](../docs/PRD/PRD-018.md) — Strategy-Aware Exits

把策略已計算的 `Invalidation` 等級（Momentum→MA20, Breakout→range boundary, MeanReversion→entry×0.99, EMACross→EMA21）寫進 klines Summary，Risk Manager 優先使用比 2×ATR 更近的策略止損。

**依賴：** PRD-006, PRD-007

---

## 實施順序

```
PRD-013 (擴充) ─┐
                 ├──→ PRD-015 (校準) ──→ PRD-016 (市場狀態)
PRD-014 (追蹤) ─┘

PRD-017 (MTF 投票) ──→ 並行
PRD-018 (出場)    ──→ 並行
```

---

## 驗證方式

每個 PRD 完成後：
1. 該 PRD 涉及的 `_test.go` 全綠
2. `go build ./cmd/friday` 編譯通過
3. 手動跑一輪確認新輸出字段正確出現在 tool 回傳中

全部完成後：
4. 跑一次完整 testnet session（≥30 輪），確認：
   - 策略共識不再頻繁出現 "no consensus"
   - `recall_trades` 回傳包含勝負統計
   - MTF 策略線出現在 `binance_mtf_klines` 輸出
   - 止損使用了策略 invalidation（觀察 Executor 的 stop_loss 數值）
