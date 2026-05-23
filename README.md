# friday

An autonomous crypto-futures trading agent built on the [evva](https://github.com/johnny1110/evva)
Go SDK. Friday loops every 15 seconds across BTCUSDT / ETHUSDT / SOLUSDT
on Binance USDⓈ-M Futures (testnet by default), reading market data
through purpose-built tools and deciding LONG / SHORT / CLOSE / WAIT
per symbol — running indefinitely until you hit Ctrl+C.

Friday also serves as a downstream reference implementation of the evva
SDK, exercising the full `pkg/` surface (config, profile, custom tools,
LLM registry, event sink, bubbletea TUI). Rough edges discovered along
the way are captured in [docs/sdk-feedback.md](docs/sdk-feedback.md).

---

## What friday does

- **Self-paced loop.** Each round the agent pulls fresh market data,
  re-reads its positions, runs seven mandatory risk checks, decides, and
  schedules the next wake-up 15 seconds later via `schedule_wakeup`.
- **Dynamic risk caps.** Per-position margin, total margin, hard stop,
  and profit-protection thresholds are all computed every round as
  percentages of the live wallet balance — no hard-coded dollar figures.
- **High-risk mandate.** Aggressive sizing, leverage up to 100x, biased
  toward action. Discipline survives only where it protects the account
  from zero (the seven risk checks).
- **Stop is yours alone.** Friday has no authority to halt the loop;
  only Ctrl+C from the user stops it.

The full mandate, decision-loop pseudo-code, dynamic-cap formulas, and
Chinese starting prompt all live in
[`.evva/plans/current.md`](./.evva/plans/current.md).

---

## Quick start

### 1. Get Binance Futures testnet credentials

1. Sign up at <https://testnet.binancefuture.com> (separate account from
   real Binance).
2. **API Management** → create an API key with futures trading enabled.
   Save the API key and secret.
3. **Faucet** → claim testnet USDT, then **Wallet → Transfer** the USDT
   from spot into the USDⓈ-M Futures wallet. Verify the futures wallet
   balance is > 0, or every order will fail.

### 2. Configure friday

```sh
# First run auto-creates ~/.friday/config/friday-config.yml and a starter .env.
go run ./cmd/friday
```

Edit `~/.friday/.env`:

```bash
# DeepSeek LLM
DEEPSEEK_API_KEY=sk-...

# Binance USDⓈ-M Futures (testnet)
BINANCE_API_KEY=your_testnet_api_key
BINANCE_SECRET_KEY=your_testnet_secret_key
BINANCE_BASE_URL=https://testnet.binancefuture.com

# Agent loop — 15s cycles × ~5760 = a full day. SDK default 30 stops in 7.5 min.
MAX_ITERS=12000

LOG_LEVEL=info
```

### 3. Launch the loop

```sh
go run ./cmd/friday
```

In the TUI, paste the starting prompt from
[`.evva/plans/current.md`](./.evva/plans/current.md) (the "Starting Prompt"
section). Friday will run the first analysis round immediately and
schedule itself every 15 seconds thereafter. Hit **Ctrl+C** when you
want to stop.

---

## Configuration

Env vars (set in `~/.friday/.env`):

| Variable               | Default                                  | Notes                                                  |
|------------------------|------------------------------------------|--------------------------------------------------------|
| `DEEPSEEK_API_KEY`     | (required)                               | DeepSeek API bearer token.                             |
| `BINANCE_API_KEY`      | (required for trading tools)             | Binance Futures API key.                               |
| `BINANCE_SECRET_KEY`   | (required for trading tools)             | Binance Futures secret.                                |
| `BINANCE_BASE_URL`     | `https://testnet.binancefuture.com`      | Switch to `https://fapi.binance.com` for mainnet.      |
| `MAX_ITERS`            | `30`                                     | Agent loop cap per `Run()`. Raise to 12000+ for a day. |
| `LOG_LEVEL`            | `info`                                   | `debug` / `info` / `warn` / `error`.                   |
| `LOG_DIR`              | `~/.friday/logs`                         | Set to empty string to disable file logs.              |
| `LOGLEVEL` / `LOGDIR`  | —                                        | Friday-flavoured aliases for the above two.            |

`~/.friday/config/friday-config.yml` is auto-generated on first run and
holds non-secret defaults (provider, model, etc).

---

## Trading tools

Friday ships ten Binance-specific tools wired into the agent on top of
the evva general-purpose kit:

### Market data

| Tool              | What it returns                                          |
|-------------------|----------------------------------------------------------|
| `binance_price`   | Current mark price for a symbol.                         |
| `binance_ticker`  | 24h change%, high, low, volume, quote volume.            |
| `binance_klines`  | OHLCV candles (default 5m × 20).                         |
| `binance_funding` | Most recent funding-rate observation.                    |
| `binance_fee`     | Account's maker/taker commission rates.                  |

### Account

| Tool                | What it returns                                                       |
|---------------------|-----------------------------------------------------------------------|
| `binance_balance`   | USDT wallet balance (total + available + cross uPnL).                 |
| `binance_position`  | Open positions: side, size, entry, mark, uPnL, liquidation price.     |

### Trading (MARKET only)

| Tool                | What it does                                                                         |
|---------------------|--------------------------------------------------------------------------------------|
| `binance_leverage`  | Set leverage for a symbol before opening.                                            |
| `binance_order`     | Place a MARKET order (BUY = long / close short; SELL = short / close long). `reduce_only` available for closes. |
| `binance_close_all` | Emergency: cancel all open orders + reduce-only market-close every open position.    |

Plus the evva built-in `schedule_wakeup` (sleep + re-enter the
conversation), wired into the active tool catalog at bootstrap. See
[`internal/bootstrap/bootstrap.go`](./internal/bootstrap/bootstrap.go).

---

## Keybindings

| Key       | Action                                                         |
|-----------|----------------------------------------------------------------|
| `Enter`   | Send the prompt.                                               |
| `Ctrl+C`  | If a Run is in flight: cancel it. Otherwise: quit.             |
| `Ctrl+L`  | Clear the transcript.                                          |
| `Esc`     | Quit immediately.                                              |
| `↑` / `↓` | Scroll the transcript.                                         |

---

## Project layout

```
cmd/friday/main.go              entry point
internal/bootstrap/             cfg, agent persona (SystemPrompt), tool wiring
internal/tui/                   bubbletea Model + Sink + render
internal/binance/               Binance Futures REST client (signed/unsigned)
internal/tool/                  friday's custom tools (10 binance_* + echo)
.evva/plans/current.md          trading mandate + starting prompt + rules
docs/sdk-feedback.md            evva SDK rough-edge notes
```

`internal/` is friday-private. Only the evva `pkg/*` packages are
imported from upstream.

---

## Adding a custom tool

Friday's `echo` tool is the minimal reference example. The wiring is two
steps:

1. **Implement `pkg/tools.Tool`** (Name, Description, Schema, Execute).
   See [`internal/tool/echo.go`](./internal/tool/echo.go) — about 100 LOC
   including the JSON-schema string and doc comments.
2. **Register at agent construction** via `agent.WithCustomTool`. The
   evva SDK auto-adds the tool name to the active catalog, so you no
   longer need a manual `append` line:

```go
ag, _ := agent.NewWithProfile(prof,
    // …
    agent.WithCustomTool(fridaytool.EchoToolName, func(pkgtools.State) (pkgtools.Tool, error) {
        return fridaytool.NewEcho(), nil
    }),
)
```

To exercise `echo` from the TUI: prompt friday with something like
*"echo hello three times"* — the model will see `echo` in its catalog
and dispatch it.

For a richer example with a stateful HTTP client, look at any of the
`binance_*` tools — they share a process-wide
[`sharedBinanceClient()`](./internal/tool/binance_client.go) singleton
that reads credentials from env on first call.

---

## Safety notes

- **Testnet first, always.** Default `BINANCE_BASE_URL` points at
  `testnet.binancefuture.com`. Don't change it until you've validated a
  full session.
- **Mandatory caps.** The system prompt enforces dynamic per-position
  (15% of balance), total margin (60%), hard stop (-10%), and profit
  guard (+20%) limits every round. These are baked into
  [`internal/bootstrap/prompt.go`](./internal/bootstrap/prompt.go) — read
  it before increasing leverage or removing checks.
- **Ctrl+C is the only off switch.** Friday will not stop on its own,
  even after `binance_close_all` triggers. If you want to halt, hit
  Ctrl+C in the TUI.
