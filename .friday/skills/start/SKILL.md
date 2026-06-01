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
   讀盤時也會看到 4h ADX 的**市況分類**（TRENDING/RANGING/TRANSITIONAL）與一條**依市況加權、信心已校準**的
   4h 策略共識（見下方策略層說明）。
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
- **策略訊號層**（`internal/strategy`）—— 六個確定性策略：動量、突破、均值回歸、
  EMA 交叉（PRD-013）、布林通道（PRD-020：均值回歸 ＋ band-walk 兩種模式）五個單一標的策略，
  加上跨標的「背離」（BTC 平盤時某標的單獨走強/弱，PRD-013）。
  訊號彙總成共識後附在 `binance_klines` 的 Summary 行，供分析師驗證（而非自行臆測方向）；
  背離提示也會附在 `binance_klines` / `binance_mtf_klines` 輸出。
- **啟動信心校準**（PRD-015，`backtest.RunStrategy` / `Calibrate`）—— 啟動時用各標的近 33 天
  4h K 線回測每個策略，把實際勝率映射成信心值 `(勝率−0.5)×2`（取代寫死的 0.6/0.65 等魔術數字）。
  回測 <5 筆者沿用預設；勝率 ≤50%（無優勢）者信心歸 0 並在該標的**自動停用**。盡力而為——
  抓不到 K 線就回退預設，啟動照常。
- **市況偵測 + 動態加權**（PRD-016，`strategy.DetectRegime`）—— 以 4h ADX(14) 把市場分為
  TRENDING（>25）/ RANGING（<20）/ TRANSITIONAL，再依市況調整各策略投票權重（趨勢市加重
  動量/突破/EMA、降低均值回歸；盤整市相反；過渡市不調整）。`binance_mtf_klines` 會輸出 `Regime:` 行。
- **多時間框架策略共識**（PRD-017，`strategy.AggregateMTF`）—— 把校準＋市況加權後的策略引擎
  分別跑在 5m/1h/4h，再合成一個跨時框加權票（5m×1.0、1h×1.5、4h×2.0，衝突時高時框主導；
  含遲滯帶避免抖動）。`binance_mtf_klines` 輸出 `MTF Strategy:` 行，是分析師的**主要方向訊號**
  （與較粗略的 `Cross-TF:` 行衝突時以 MTF 為準）。
- **RSI 極端區過濾 ＋ MTF 反應度調校**（PRD-022，`strategy.RSIFilter`）—— 兩項由 12 筆實盤虧損
  （1勝11敗）分析驅動的訊號層修正：(1) 全域 RSI 閘門——任一時框 RSI(14) ≥75 或 ≤25 時，
  該時框的方向共識直接降為 NEUTRAL（不在峰頂做多、不在谷底做空），可用 `FRIDAY_RSI_FILTER=false` 停用；
  (2) MTF 反應度——遲滯帶由 0.1 降到 0.05（`FRIDAY_MTF_HYSTERESIS`），並加入「5m+1h 覆寫」：當 4h 為
  NEUTRAL 且 5m 與 1h 同向且信心各 ≥0.5 時，採其平均信心的方向（`FRIDAY_MTF_5M1H_OVERRIDE`），
  解決 149 輪僅 0.48% 出手的過度過濾。**4h 反向仍是硬否決**（5m/1h 做多但 4h 做空 → NEUTRAL）。
- **分析師決策品質**（PRD-023，純提示層 ＋ 記憶體）—— 由 12 筆實盤虧損分析驅動的三項修正：
  (1) **市況偏多閘門**——當 `Regime:` 為 TRENDING 且 4h 價格在 4h MA20 之下（空頭趨勢），做多偏向
  必須同時具備「`MTF Strategy:` 明確為 LONG」＋「恐懼貪婪 ≤25」，否則一律 NEUTRAL（禁止僅憑恐慌指數逆勢做多）；
  (2) **手續費感知**——每筆交易的預期波動須 ≥3× 來回手續費（~0.24%），ATR%/策略 TP 不足就不出手，
  並在理由中寫出「預期波動 / 手續費」倍數；(3) **回溯樣本門檻**——`recall_trades` 在相似交易 <5 筆時
  輸出「insufficient data (<5 similar trades) — do not use this to veto」，分析師不得以「回溯全虧」否決進場
  （打破 虧損→回溯全虧→不敢交易→無法翻身 的負回饋循環）。此外手續費預算接近上限時，會把警示注入下一輪的
  carry，分析師與風控都看得到（PRD-020 R9 接線）。
- **多時間框架分析**（PRD-008，`binance_mtf_klines`）—— 一次併發抓 5m/1h/4h，
  各自分類方向後給出 ALIGNED / CONFLICT / NO-EDGE 跨框架對齊判斷（衝突時以較高框架為準，
  避免拿 5m 多單去對抗 4h 空頭）；分析師的主要讀盤工具。
- **ATR 波動度倉位**（PRD-007，`risk.SuggestedSize`）—— 以「風險 ~1% 餘額 ÷ 2×ATR 停損距離」
  決定數量，讓低波動（BTC）與高波動（SOL）每筆風險相當；仍受 14% 目標 / 15% 硬上限約束。
  `binance_klines` 的 Summary 會附 ATR(14) 與建議停損價。
- **策略感知停損**（PRD-018）—— 每個策略本身帶有「失效價」（動量 = MA20、突破 = 區間邊界、
  均值回歸 = 進場價×0.99/1.01、EMA 交叉 = EMA21），會以 `inval=…` 顯示在共識行與 `MTF Strategy:` 行。
  風控設停損時：失效價比 2×ATR **更貼近進場**就用失效價（更緊的結構性保護），否則用 2×ATR 並把失效價
  記為心理硬停損；多策略一致時取**最近**的失效價。失效價為 0（不適用）則回退 2×ATR。
- **策略專屬停利**（PRD-020 §6）—— 部分策略另帶「停利目標」（突破 = 測量目標 entry±區間高度、
  均值回歸/布林 = 均值 MA20；動量/EMA 交叉刻意不設、靠移動停損），以 `tp=…` 顯示在共識行與
  `MTF Strategy:` 行。風控有 `tp=…` 時就以它作為**第一階停利**（取代固定 +10% 規則），移動停損與
  第二階（+20%）仍作為安全網。`backtest.BestTakeProfit` 提供逐策略停利百分比的粗掃描（校準輔助）。
- **停損/停利即時監控**（PRD-009，`risk.StopMonitor`）—— 背景 goroutine 每秒輪詢標記價，
  觸及停損/停利立即以 reduce-only 市價平倉，是獨立於 15 秒迴圈的快速保護網；Executor 開倉後
  以 `binance_stop_monitor` 註冊風控算出的 2×ATR 停損。僅存記憶體（重啟不保留）。
- **原生交易所停損**（PRD-020 §2）—— `binance_stop_monitor` 現在會「雙重保護」：除了上面的記憶體
  輪詢監控，同時在幣安掛上伺服器端的 `STOP_MARKET`（停損）＋ `TAKE_PROFIT_MARKET`（停利）reduce-only
  掛單（`timeInForce=GTC`、`workingType=MARK_PRICE`）。這些**原生掛單在 friday 當掉/重啟後仍存活**，
  補上記憶體監控重啟即消失的缺口。對同一標的再次註冊會先撤掉舊掛單；清除（stop/tp=0）會撤單。
  啟動時 `tool.CleanupOrphanStops` 會撤掉上一個 session 遺留、但部位已不存在的孤兒停損單。
- **手續費預算（防過度交易）**（PRD-020 §3，`risk.FeeBudget`）—— 滾動 30 分鐘視窗的手續費花費；
  一旦視窗內手續費超過餘額的 `FRIDAY_FEE_BUDGET_PCT`（預設 0.5%），`binance_order` 就擋下新開倉
  （平倉不受限），由 `log_trade` 以交易所對帳後的手續費＋資金費餵入。接近上限時風控提示會出現一行狀態。
- **投資組合相關性分組上限**（PRD-020 §4，`risk.PortfolioGroupValidator`）—— 把相關標的的**合計保證金**
  設上限（`crypto` BTC/ETH/SOL 30%、`stocks` NVDA/GOOGL/AMZN/META 40%，可用 `FRIDAY_GROUP_LIMITS` 調整），
  避免「7 個部位」其實只是兩個高度集中的賭注。`binance_order` 會把同組已開倉的保證金加總，
  超過上限的開倉直接擋下；同一份分組設定也會注入風控提示。平倉與不屬任何組的標的放行。
- **線上重新校準**（PRD-020 §5，`strategy.Recalibrator`）—— 背景 goroutine 每隔
  `FRIDAY_RECALIBRATE_HOURS`（預設 4 小時；設 0 停用）用最新 4h K 線重跑 PRD-015 信心校準，
  讓信心值跟得上市況轉變；任何一輪失敗就保留現有信心，不清空。
- **逐標的槓桿上限**（PRD-012，`binance.MaxLeverages`）—— 各標的最大槓桿不同
  （BTC 125x、ETH 100x、SOL 50x、美股永續僅 10x）。啟動時抓 leverageBracket，
  注入風控提示（每個標的顯示「≤Nx」），並在 `binance_leverage` 內把超過上限的請求自動夾到上限，
  避免 `-4028` 失敗。15% 保證金硬上限維持「擋下並回報、不自動縮量」。
- **逐名義槓桿分層夾制**（PRD-019，`binance.LeverageBrackets`）—— 幣安的槓桿上限其實是**按名義分層**的
  （最高槓桿只適用最小名義級距，例如美股永續 10x 只到 $5,000）。`binance_order` 下單前會用
  「名義 = 數量 × 標記價」反查該名義允許的最高槓桿，若目前設定過高就**自動降槓桿**以落入正確級距，
  避免 `-2027`（部位超過當前槓桿允許上限）；降檔後再用較低槓桿重跑保證金檢查。風控提示也會顯示
  每個標的「最高槓桿下的名義上限」（≤$Xk @max-lev）。
- **交易記憶 + 沙盒回測**（`internal/memory`、`internal/backtest`）——
  `recall_trades` 取出相似的歷史交易結果;`run_backtest` 可在實戰前先驗證策略勝率。
- **逐策略績效追蹤**（PRD-014）—— 每筆平倉交易會記下觸發它的策略名稱（`log_trade` 的 `strategy` 參數），
  `recall_trades` 因此能輸出整體與**逐策略**的勝負拆解（並可用 `strategy` 參數只查特定策略），
  勝負一律以交易所淨值（扣手續費後）計算。
- **逐輪分析記錄**（`~/.friday/memory/rounds.jsonl`）—— 每一輪完整的分析師判讀、風控決策、
  熔斷狀態與執行結果都會以一行 JSON 附加進此檔（與 `trades.jsonl` 同格式),供事後離線分析;
  有下單與全 WAIT 短路的回合都會記。
- **損益以交易所為準（不信任 LLM 自報）**—— `log_trade` 平倉後會比對
  `/fapi/v1/income` 帳本,記錄真實的「已實現損益 − 手續費 − 資金費率」淨值
  (`pnl_source:"exchange"`);WIN/LOSS 與熔斷都以這個淨值為準,避免機器人把
  賠錢的平倉誤記成獲利。若舊的 `trades.jsonl` 已被污染,執行
  `go run ./cmd/reconcile-memory`(預設只預覽,加 `-write` 才寫入,會先備份 `.bak`)
  用交易所真值修正。

---

## 維運與可觀測性（PRD-021）

- **盤後分析工具** `cmd/analyze` —— 讀 `rounds.jsonl` ＋ `trades.jsonl`，印出 6 段報告：
  session 總覽、逐策略／逐標的／逐市況（含勝率、總/平均損益、獲利因子 profit factor）、
  分析師方向準確率、熔斷時間線。`go run ./cmd/analyze`（純文字）或加 `-json`（結構化），
  可用 `-rounds`／`-trades` 指定路徑；檔案不存在/為空時優雅地顯示 0，不會崩。每筆交易會依
  其**開倉當輪**的市況歸類（round log 現在會記每個標的的市況）。
- **外部通知** `internal/notify` —— Discord／Telegram webhook（`FRIDAY_DISCORD_WEBHOOK_URL`
  或 `FRIDAY_TELEGRAM_BOT_TOKEN`＋`FRIDAY_TELEGRAM_CHAT_ID`，未設則停用）。只在重大事件推播：
  session 啟動/停止、熔斷 PAUSED/HALTED **狀態轉換**（每次轉換只推一則，非每輪）、
  以及單筆平倉淨損益超過餘額 `FRIDAY_NOTIFY_PNL_PCT`（預設 ±5%）。
- **紙上交易模式** `FRIDAY_PAPER=true` —— 不下任何真實單，改用虛擬帳本
  （`risk.PaperPortfolio`，初始餘額 `FRIDAY_PAPER_BALANCE` 預設 1000 USDT）。交易工具
  （`binance_order`/`binance_leverage`/`binance_close_all`/`binance_stop_monitor`）變成記錄
  「PAPER: would have…」並更新虛擬部位的 no-op；`binance_position`/`binance_balance` 回傳虛擬狀態；
  **絕不呼叫真實帳戶端點**；行情資料仍取自真實交易所。停損監控以紙上 broker 運作（真實標記價、虛擬平倉）。
  round/trade log 會標記 `paper:true`，啟動時印出醒目橫幅。安全驗證策略、不耗測試網餘額的首選。

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
5. （可選）PRD-020 生產強化環境變數，不設則用預設：
   `FRIDAY_FEE_BUDGET_PCT`(0.005，手續費預算上限)、
   `FRIDAY_GROUP_LIMITS`（相關性分組上限，格式 `name:pct:SYM1,SYM2;…`，不設用內建 crypto/stocks 組）、
   `FRIDAY_RECALIBRATE_HOURS`(4，線上重新校準間隔；0 停用)。
6. （可選）PRD-021 維運環境變數：`FRIDAY_PAPER`(true 開紙上交易)、`FRIDAY_PAPER_BALANCE`(1000)、
   `FRIDAY_DISCORD_WEBHOOK_URL` / `FRIDAY_TELEGRAM_BOT_TOKEN`＋`FRIDAY_TELEGRAM_CHAT_ID`(通知)、
   `FRIDAY_NOTIFY_PNL_PCT`(0.05，大額損益通知門檻)。
7. （可選）PRD-022 訊號調校環境變數：`FRIDAY_RSI_FILTER`(true，RSI 極端區過濾)、
   `FRIDAY_MTF_HYSTERESIS`(0.05，MTF 遲滯帶)、`FRIDAY_MTF_5M1H_OVERRIDE`(true，5m+1h 覆寫)。
8. `go build ./...` 與 `go test ./...` 通過。

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
