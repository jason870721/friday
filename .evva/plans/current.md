# Friday 專案計畫

## 目前狀態

PRD-001 ~ PRD-019 全部實作完成。

---

## 下一步：P3 Tranche（2 個 PRD）

### [PRD-020](../docs/PRD/PRD-020.md) — Production Hardening（安全 + 訊號）

六個交易系統內的改動：
- **Native STOP_MARKET** — 止損單活過 crash
- **Fee Budget Guardrail** — 過度交易自動暫停（手續費佔虧損 ~36%）
- **Portfolio Correlation Sizing** — 相關幣種組合曝險上限
- **Online Re-Calibration** — 信心值每 4 小時隨市場更新
- **Strategy-Specific TP** — 每個策略有自己的停利目標
- **Bollinger Band Strategy** — 第五個策略

實施順序：native stops → fee budget → portfolio groups → Bollinger → TP → calibration

### [PRD-021](../docs/PRD/PRD-021.md) — Operations & Observability（營運工具）

三個操作者工具：
- **`cmd/analyze`** — 賽後分析（per-strategy win rate、Analyst accuracy、breaker timeline）
- **Discord/Telegram 通知** — breaker trip、大額盈虧、session 啟停
- **Paper Trading** — `FRIDAY_PAPER=true`，不碰交易所的模擬交易

可與 PRD-020 並行開發。
