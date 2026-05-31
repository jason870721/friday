# start — 啟動 F.R.I.D.A.Y. 自主合約交易（多代理人架構）

> 這是啟動交易的核心說明檔。
> ⚠️ 每一輪的詳細交易邏輯（鐵律、風險檢查、動態限額、下單順序）現在
> **權威地寫在程式碼裡的三個角色提示**：`internal/orchestrator/prompts.go`
> （`analystSystemPrompt` / `riskSystemPrompt` / `executorSystemPrompt`）。
> 本檔**刻意不重複**那些細節，以免變成第二份會逐漸走樣的真相來源。

---

## 任務

自主、高風險的幣安 USDⓈ-M 永續合約交易員。同時**獨立**操作多個市場，
目標是穩定獲利。市場間走勢可能連動也可能分化，每個市場都要獨立判斷。
**只有使用者按 Ctrl+C 能停止你。**

操作的市場由 `FRIDAY_SYMBOLS` 環境變數設定（逗號分隔；預設含三個加密貨幣對
加上數個美股永續），且在啟動時會以幣安 `exchangeInfo` 驗證——端點未列為
`TRADING` 的標的會被記錄並略過，因此每輪只會處理確實存在的市場。新增/移除
市場只需改設定，不需要動程式碼。美股永續是否可交易視端點而定（測試網與主網
不同），未上架者會在啟動時自動略過，端點上架後即自動生效；實際生效清單以
啟動時印出的 `friday: trading N symbol(s)` 為準。

---

## 現在的運作架構（已不是單一代理人）

每 15 秒一個決策週期，由 Go 協調器（orchestrator）驅動三個單一職責的角色**依序協作**，
彼此用具型別的結構（typed handoff）交接：

1. **分析師 Analyst** — 讀盤（以 `binance_mtf_klines` 多時間框架 5m/1h/4h ＋跨框架對齊為主，
   加價格/資金費率/24h）＋市場情緒（Fear & Greed）＋確定性策略訊號＋歷史交易記憶，
   產出每個市場的偏多/偏空判斷。**不下單。** 是「訊號驗證者」：偏向以策略訊號為錨，要偏離須引用具體數據。
2. **風控 Risk Manager** — 依即時餘額算動態限額、用 ATR 波動度做風險基準倉位
   （風險 ~1% ÷ 2×ATR 停損距離）、跑強制風險檢查、決定倉位大小與停損停利，
   或直接否決（VETO）。**不下單。** 只輸出精確數字。
3. **執行 Executor** — 嚴格按風控給的數字下單（先設槓桿再市價單），開倉後向背景停損監控
   註冊停損/停利，平倉後記錄交易。

迴圈與 15 秒節奏由 **Go 協調器擁有**（不再使用 `schedule_wakeup`）。
協調器一路跑到使用者 Ctrl+C 取消為止；任何一輪出錯也只是記錄後進下一輪，不會停。

---

## 已內建的安全機制（**程式碼強制**，不只是提示）

- **單筆保證金硬上限 15%**（`risk.MarginCapValidator`，於 `binance_order` 內）——
  任何開倉若保證金（名義 ÷ 槓桿）超過錢包餘額 15% 直接擋下，要求重算。平倉不受限。
- **系統級熔斷**（`risk.CircuitBreaker`）——
  - 當日已實現虧損 ≤ 餘額 −10%，或連續 5 筆虧損 → **PAUSED**（只准平倉/WAIT，冷卻後恢復）。
  - 總回撤 ≤ 起始餘額 −20% → **HALTED**（停止新倉，需手動重置）。
  - 熔斷狀態會注入風控的提示，風控在非 NORMAL 時只會輸出 CLOSE/WAIT。
- **策略訊號層**（`internal/strategy`）—— 動量/突破/均值回歸的確定性訊號，
  附在 `binance_klines` 的 Summary 行，供分析師驗證（而非自行臆測方向）。
- **多時間框架分析**（PRD-008，`binance_mtf_klines`）—— 一次併發抓 5m/1h/4h，
  各自分類方向後給出 ALIGNED / CONFLICT / NO-EDGE 跨框架對齊判斷（衝突時以較高框架為準，
  避免拿 5m 多單去對抗 4h 空頭）；分析師的主要讀盤工具。
- **ATR 波動度倉位**（PRD-007，`risk.SuggestedSize`）—— 以「風險 ~1% 餘額 ÷ 2×ATR 停損距離」
  決定數量，讓低波動（BTC）與高波動（SOL）每筆風險相當；仍受 14% 目標 / 15% 硬上限約束。
  `binance_klines` 的 Summary 會附 ATR(14) 與建議停損價。
- **停損/停利即時監控**（PRD-009，`risk.StopMonitor`）—— 背景 goroutine 每秒輪詢標記價，
  觸及停損/停利立即以 reduce-only 市價平倉，是獨立於 15 秒迴圈的快速保護網；Executor 開倉後
  以 `binance_stop_monitor` 註冊風控算出的 2×ATR 停損。僅存記憶體（重啟不保留）。
- **交易記憶 + 沙盒回測**（`internal/memory`、`internal/backtest`）——
  `recall_trades` 取出相似的歷史交易結果;`run_backtest` 可在實戰前先驗證策略勝率。
- **損益以交易所為準（不信任 LLM 自報）**—— `log_trade` 平倉後會比對
  `/fapi/v1/income` 帳本,記錄真實的「已實現損益 − 手續費 − 資金費率」淨值
  (`pnl_source:"exchange"`);WIN/LOSS 與熔斷都以這個淨值為準,避免機器人把
  賠錢的平倉誤記成獲利。若舊的 `trades.jsonl` 已被污染,執行
  `go run ./cmd/reconcile-memory`(預設只預覽,加 `-write` 才寫入,會先備份 `.bak`)
  用交易所真值修正。

---

## 啟動前檢查（**先用測試網**）

1. `~/.friday/.env` 已填：`DEEPSEEK_API_KEY`、`BINANCE_API_KEY`、`BINANCE_SECRET_KEY`，
   且 `BINANCE_BASE_URL=https://testnet.binancefuture.com`。
2. 測試網的 USDⓈ-M 合約錢包 USDT 餘額 > 0（否則每筆下單都會失敗）。
3. （可選）`FRIDAY_SYMBOLS` 設定要操作的標的（逗號分隔），不設則用預設
   清單。啟動時會以 `exchangeInfo` 驗證並略過未上架者；終端機會印出
   `friday: trading N symbol(s): …` 顯示最終生效的清單。
4. （可選）熔斷門檻環境變數，不設則用預設：
   `FRIDAY_DAILY_LOSS_PCT`(0.10)、`FRIDAY_MAX_CONSEC_LOSSES`(5)、
   `FRIDAY_DRAWDOWN_HALT_PCT`(0.20)、`FRIDAY_COOLDOWN_CYCLES`(20)。
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
  啟動指令」，**不是**由代理人自動呼叫的技能。若要把 `SKILL` 工具重新接回角色代理人，
  那是另一個小改動，需要時再做。
- 每輪交易的權威邏輯一律以 `internal/orchestrator/prompts.go` 的三個角色提示為準；
  本檔若與之衝突，以程式碼為準。
