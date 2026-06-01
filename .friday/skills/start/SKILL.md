---
name: start
description: 啟動 F.R.I.D.A.Y. 自主合約交易（多代理人架構）
prompt: 開始交易。立即分析所有已設定的市場（見啟動時印出的清單），依授權執行。分析與報告請以中文回覆。
---

# start — 啟動 F.R.I.D.A.Y. 自主合約交易（多代理人架構）

> 這是啟動交易的核心說明檔。
> ⚠️ 每一輪的詳細交易邏輯（鐵律、風險檢查、動態限額、下單順序）現在
> **權威地寫在程式碼裡的三個角色提示**：`internal/orchestrator/prompts.go`
> （`analystSystemPrompt` / `riskSystemPrompt` / `executorSystemPrompt`）。
> 安全機制的完整細節見 `CLAUDE.md` 的 Safety systems 與各 PRD。
> 本檔**刻意不重複**那些細節，以免變成第二份會逐漸走樣的真相來源。

---

## 任務

自主、高風險的幣安 USDⓈ-M 永續合約交易員。同時**獨立**操作多個市場，
目標是穩定獲利。市場間走勢可能連動也可能分化，每個市場都要獨立判斷。
**只有使用者按 Ctrl+C 能停止你。**

操作的市場由 `FRIDAY_SYMBOLS` 環境變數設定（逗號分隔；預設含三個加密貨幣對
加上數個美股永續），且在啟動時會以幣安 `exchangeInfo` 驗證——端點未列為
`TRADING` 的標的會被記錄並略過，因此每輪只會處理確實存在的市場。新增/移除
市場只需改設定，不需要動程式碼。實際生效清單以啟動時印出的
`friday: trading N symbol(s)` 為準。

---

## 現在的運作架構（已不是單一代理人）

每 15 秒一個決策週期，由 Go 協調器（orchestrator）驅動三個單一職責的角色**依序協作**，
彼此用具型別的結構（typed handoff）交接：

1. **分析師 Analyst** — 讀盤（以 `binance_mtf_klines` 多時間框架 5m/1h/4h ＋跨框架對齊為主，
   加價格/資金費率/24h）＋市場情緒（Fear & Greed）＋確定性策略訊號＋歷史交易記憶，
   產出每個市場的偏多/偏空判斷。**不下單。** 是「訊號驗證者」：以策略訊號為錨，要偏離須引用具體數據。
2. **風控 Risk Manager** — 依即時餘額算動態限額、用 ATR 波動度做風險基準倉位
   （風險 ~1% ÷ 2×ATR 停損距離）、跑強制風險檢查、決定倉位大小與停損停利，
   或直接否決（VETO）。**不下單。** 只輸出精確數字。
3. **執行 Executor** — 嚴格按風控給的數字下單（先設槓桿再市價單），開倉後向停損監控
   註冊停損/停利，平倉後記錄交易。

迴圈與 15 秒節奏由 **Go 協調器擁有**（不再使用 `schedule_wakeup`）。
協調器一路跑到使用者 Ctrl+C 取消為止；任何一輪出錯也只是記錄後進下一輪，不會停。

---

## 已內建的安全機制（**程式碼強制**，非僅提示）

LLM 無法繞過這些 Go 層防線。完整細節見 `CLAUDE.md` 的 Safety systems。

- **開倉前防線**（`binance_order`，依序執行；平倉一律放行）：
  1. 系統級熔斷 `risk.CircuitBreaker` — 當日虧損/連續虧損 → PAUSED，回撤 → HALTED。
  2. 名義槓桿分層夾制 — 自動降槓桿讓名義落入正確級距（避免 `-2027`），再以較低槓桿重驗保證金。
  3. 手續費預算 `risk.FeeBudget` — 30 分鐘視窗手續費超過餘額 `FRIDAY_FEE_BUDGET_PCT`(0.5%) → 擋新開倉。
  4. 投資組合分組上限 `risk.PortfolioGroupValidator` — 相關標的合計保證金上限（crypto 30% / stocks 40%）。
  5. 單筆保證金硬上限 — 保證金 > 餘額 15% → 拒絕並要求重算。
- **停損** — 開倉後 `binance_stop_monitor` 提供雙重保護：記憶體輪詢（~1s）＋ 原生交易所
  `STOP_MARKET`/`TAKE_PROFIT_MARKET`（重啟後仍存活）；啟動時清理孤兒掛單。停損繞過開倉前防線。
- **倉位與出場** — ATR 波動度倉位（風險 ~1% ÷ 2×ATR）；策略失效價/停利以 `inval=` / `tp=`
  提示風控；逐標的槓桿上限夾制（避免 `-4028`）。
- **信號引擎** `internal/strategy` — 六個確定性策略（動量/突破/均值回歸/EMA 交叉/布林/跨標的背離）
  → 信心校準（回測勝率）→ 市況加權 → MTF 跨時框投票（含 RSI 極端區過濾、5m+1h 覆寫、4h 硬否決）。
- **帳務與記憶** — 損益以 `/fapi/v1/income` 對帳為準（非 LLM 自報），WIN/LOSS 與熔斷皆以淨值計；
  `trades.jsonl` 逐策略績效、`rounds.jsonl` 逐輪記錄；`recall_trades` 相似交易 <5 筆時標示
  「insufficient data」，不可作為否決理由（避免 虧損→回溯全虧→不敢交易 的負回饋循環）。

---

## 維運與可觀測性

- **盤後分析** `cmd/analyze` — 讀 `rounds.jsonl` ＋ `trades.jsonl`，印出 6 段報告（總覽、
  逐策略/標的/市況含獲利因子、分析師方向準確率、熔斷時間線）。`-json` 結構化、`-rounds`/`-trades` 指定路徑。
- **外部通知** `internal/notify` — Discord/Telegram（未設則停用），只在重大事件推播：
  啟動/停止、熔斷狀態轉換、單筆平倉淨損益超過 `FRIDAY_NOTIFY_PNL_PCT`（±5%）。
- **紙上交易** `FRIDAY_PAPER=true` — 虛擬帳本（`FRIDAY_PAPER_BALANCE`，預設 1000），行情真實、
  不下任何真實單、**不碰真實帳戶端點**；log 標記 `paper:true`，啟動時印出橫幅。驗證策略首選。

---

## 啟動前檢查（**先用測試網**）

1. `~/.friday/.env` 已填：`DEEPSEEK_API_KEY`、`BINANCE_API_KEY`、`BINANCE_SECRET_KEY`，
   且 `BINANCE_BASE_URL=https://testnet.binancefuture.com`。
2. 測試網的 USDⓈ-M 合約錢包 USDT 餘額 > 0（否則每筆下單都會失敗）。
3. （可選）`FRIDAY_SYMBOLS` 設定要操作的標的（逗號分隔），不設則用預設清單；啟動時會以
   `exchangeInfo` 驗證並略過未上架者，終端機印出 `friday: trading N symbol(s): …` 為準。
4. （可選）環境變數（不設則用括號內預設）：
   - 熔斷：`FRIDAY_DAILY_LOSS_PCT`(0.10)、`FRIDAY_MAX_CONSEC_LOSSES`(5)、`FRIDAY_DRAWDOWN_HALT_PCT`(0.20)、`FRIDAY_COOLDOWN_CYCLES`(20)
   - 風控：`FRIDAY_FEE_BUDGET_PCT`(0.005)、`FRIDAY_GROUP_LIMITS`（`name:pct:SYM1,SYM2;…`，預設 crypto/stocks）、`FRIDAY_RECALIBRATE_HOURS`(4，0 停用)
   - 訊號：`FRIDAY_RSI_FILTER`(true)、`FRIDAY_MTF_HYSTERESIS`(0.05)、`FRIDAY_MTF_5M1H_OVERRIDE`(true)
   - 維運：`FRIDAY_PAPER`(false)、`FRIDAY_PAPER_BALANCE`(1000)、`FRIDAY_NOTIFY_PNL_PCT`(0.05)、Discord/Telegram 通知變數
5. `go build ./...` 與 `go test ./...` 通過。

---

## 啟動方式

```sh
go run ./cmd/friday
```

在 TUI 視窗貼上以下**啟動指令**並按 Enter，即開始第一輪；之後協調器每 15 秒自動跑下一輪：

> 開始交易。立即分析所有已設定的市場（見啟動時印出的清單），依授權執行。分析與報告請以中文回覆。

按 **Ctrl+C** 停止。

---

## 重要說明

- **PRD-003 多代理人重構後**，evva 的 `SKILL` 與 `schedule_wakeup` 工具已**不再掛在**
  任何角色代理人上。因此本技能檔現在的定位是：「給人看的啟動說明 ＋ 可直接貼上的
  啟動指令」，**不是**由代理人自動呼叫的技能。
- 每輪交易的權威邏輯一律以 `internal/orchestrator/prompts.go` 的三個角色提示為準；
  本檔若與之衝突，以程式碼為準。
