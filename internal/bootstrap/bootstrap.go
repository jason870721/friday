// Package bootstrap wires friday's runtime: load config, fold in
// ~/.friday/.env overrides, install DeepSeek credentials, build the
// Profile, construct the agent.
//
// Nothing in this package imports evva's internal/* — the entire
// surface is pkg/agent + pkg/config + pkg/tools/kits + pkg/event +
// pkg/llm/builtins. That last blank import side-effect-registers
// Anthropic, DeepSeek, and Ollama into pkg/llm.DefaultRegistry so the
// Profile's "deepseek" name resolves at agent construction.
//
// Phase 19 R2 revamp: every multi-step shim friday used to carry —
// pre-Load env aliasing, post-Load credential wiring, hand-rolled
// kit composition, .env first-run handhold — collapsed into
// declarative LoadOptions fields and named helper functions.
// See docs/sdk-feedback.md for the round-by-round story.
package bootstrap

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/johnny1110/evva/pkg/config"
	"github.com/johnny1110/evva/pkg/constant"
	_ "github.com/johnny1110/evva/pkg/llm/builtins"

	"github.com/johnny1110/friday/internal/notify"
	"github.com/johnny1110/friday/internal/orchestrator"
	"github.com/johnny1110/friday/internal/risk"
	fridaytool "github.com/johnny1110/friday/internal/tool"
)

// envTemplate is what friday writes into ~/.friday/.env on first
// launch (via LoadOptions.SeedEnvTemplate). Closes the chicken-and-egg
// gap where the YAML existed but the .env didn't — users now land in
// a complete config tree they can edit.
const envTemplate = `# friday env vars — edit and save, then rerun ` + "`go run ./cmd/friday`" + `.
DEEPSEEK_API_KEY=
LOG_LEVEL=info
# LOG_DIR=/var/log/friday

# Agent loop iteration cap. 15s cycles × ~5760 = one full day. SDK
# default is 30, which the binance trading loop would hit in 7.5
# minutes — raise it here.
MAX_ITERS=12000

# Binance USDⓈ-M Futures (testnet by default). Required by the
# binance_* trading tools; leave blank to disable them at runtime.
BINANCE_API_KEY=
BINANCE_SECRET_KEY=
BINANCE_BASE_URL=https://testnet.binancefuture.com

# Active trading pairs (comma-separated). Validated at startup against the
# endpoint's exchangeInfo: any symbol not listed as TRADING is logged and
# skipped, so the bot never iterates an unavailable market. The stock perps
# below depend on the endpoint — ones the venue doesn't list are skipped until
# it does. Watch the "friday: trading N symbol(s)" log line for the live set.
FRIDAY_SYMBOLS=BTCUSDT,ETHUSDT,SOLUSDT,NVDAUSDT,GOOGLUSDT,AMZNUSDT,METAUSDT

# Production hardening (PRD-020).
# Fee-budget guardrail: blocks new OPENs once fee spend over a rolling 30-min
# window exceeds this fraction of balance (anti-overtrading). 0.005 = 0.5%.
FRIDAY_FEE_BUDGET_PCT=0.005
# Portfolio correlation-group caps: "name:pct:SYM1,SYM2;…" — combined margin per
# group is capped at pct% of balance. Empty = the built-in crypto/stocks groups.
# FRIDAY_GROUP_LIMITS=crypto:30:BTCUSDT,ETHUSDT,SOLUSDT;stocks:40:NVDAUSDT,GOOGLUSDT,AMZNUSDT,METAUSDT
# Online re-calibration: re-run the strategy-confidence backtest sweep this often
# (hours) so confidences track regime shifts. 0 disables (startup calibration only).
FRIDAY_RECALIBRATE_HOURS=4

# Operations & observability (PRD-021).
# Paper trading: no real orders — a virtual book trades against live market data.
# FRIDAY_PAPER=true
# FRIDAY_PAPER_BALANCE=1000
# External notifications (significant events only): configure either/both.
# FRIDAY_DISCORD_WEBHOOK_URL=
# FRIDAY_TELEGRAM_BOT_TOKEN=
# FRIDAY_TELEGRAM_CHAT_ID=
# Notify on a closed trade whose net PnL exceeds this fraction of balance (±5%).
FRIDAY_NOTIFY_PNL_PCT=0.05

# Signal-quality tuning (PRD-022).
# RSI extreme-zone filter: block any directional MTF consensus when the
# timeframe's RSI(14) is ≥75 or ≤25 (don't long a peak / short a trough).
FRIDAY_RSI_FILTER=true
# MTF hysteresis dead-band (raw weighted net below this reads NEUTRAL).
FRIDAY_MTF_HYSTERESIS=0.05
# 5m+1h override: when 4h is NEUTRAL and 5m+1h agree (≥0.35 each), adopt their
# direction at the average confidence (4h opposition stays a hard veto).
FRIDAY_MTF_5M1H_OVERRIDE=true
# MTF 2-of-3 quorum (PRD-024): when 4h is NEUTRAL, any 2 timeframes sharing a
# direction set it (avg confidence); a directional 4h opposed by a lower TF is
# vetoed to NEUTRAL. Disable to fall back to the weighted-sum + override path.
FRIDAY_MTF_QUORUM=true
`

// New loads friday's config and builds the PRD-003 multi-agent
// orchestrator (Analyst → Risk Manager → Executor), returning it together
// with the resolved *config.Config. The emitter (the TUI sink) receives
// role-tagged events from all three agents.
func New(emitter orchestrator.RoleEmitter) (*orchestrator.Orchestrator, *config.Config, error) {
	home, _ := os.UserHomeDir()

	// One declarative LoadOptions block carries every env-driven
	// behaviour: alias promotion, provider credentials, named
	// post-Load overrides, and the first-run .env seed.
	cfg, err := config.Load(config.LoadOptions{
		AppName: "friday",
		AppHome: filepath.Join(home, ".friday"),

		// Friendlier env-var spellings → evva canonical names.
		EnvAliases: map[string]string{
			"LOGDIR":   "LOG_DIR",
			"LOGLEVEL": "LOG_LEVEL",
			"APIKEY":   "DEEPSEEK_API_KEY",
		},

		// Declarative provider creds — replaces the post-Load
		// shim friday used to carry. Read DEEPSEEK_API_KEY (or its
		// APIKEY alias above) and install via cfg.SetProviderCredentials.
		ProviderCredentials: map[string]config.ProviderCredsFromEnv{
			"deepseek": {
				APIKeyEnv:     "DEEPSEEK_API_KEY",
				APIURLDefault: constant.DEEPSEEK.ApiUrl,
			},
		},

		// Named overrides for env vars without a YAML hook. The
		// Name field surfaces in the wrapped error if any of these
		// fail, so a multi-override host can identify the culprit.
		EnvOverrides: []config.EnvOverride{
			{Name: "max_iters_from_env", Fn: applyMaxItersFromEnv},
		},

		// First-launch ~/.friday/.env seed. Never overwrites an
		// existing file.
		SeedEnvTemplate: envTemplate,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("config.Load: %w", err)
	}

	// Friendly diagnostic — agents fail loudly on first Run() if the
	// API key is empty, but a one-line hint here saves the user that
	// round-trip.
	if cfg.LLMProviderConfig["deepseek"].ApiSecret == "" {
		fmt.Fprintln(os.Stderr,
			"friday: DEEPSEEK_API_KEY is empty — set it in ~/.friday/.env and try again.")
	}

	// PRD-005: session circuit breaker. Thresholds come from env (with
	// documented defaults). Install it on the tool package (binance_order
	// consults it; log_trade feeds it) and hand the same pointer to the
	// orchestrator so it can Observe/Tick each round.
	breaker := risk.NewCircuitBreaker(
		envFloat("FRIDAY_DAILY_LOSS_PCT", 0.10),
		envInt("FRIDAY_MAX_CONSEC_LOSSES", 5),
		envFloat("FRIDAY_DRAWDOWN_HALT_PCT", 0.20),
		envInt("FRIDAY_COOLDOWN_CYCLES", 20),
	)
	fridaytool.SetCircuitBreaker(breaker)

	// Resolve the active trading pairs from FRIDAY_SYMBOLS and validate them
	// against the venue's exchangeInfo (testnet or mainnet) — symbols the
	// endpoint does not list as TRADING are logged and dropped here, so the
	// per-round pipeline only ever iterates markets that actually exist.
	symbols := resolveSymbols()
	if len(symbols) == 0 {
		return nil, nil, fmt.Errorf(
			"no tradable symbols resolved from FRIDAY_SYMBOLS — set it to symbols listed on %s", binanceBaseURL())
	}

	// PRD-021 §4: paper-trading mode. A virtual book replaces real order
	// placement; market data stays live. Install BEFORE the orchestrator so the
	// trading tools intercept from round one. Printed prominently below.
	paper := strings.EqualFold(os.Getenv("FRIDAY_PAPER"), "true")
	if paper {
		pp := risk.NewPaperPortfolio(envFloat("FRIDAY_PAPER_BALANCE", 1000))
		fridaytool.SetPaperPortfolio(pp)
		fmt.Fprintf(os.Stderr,
			"\n=== PAPER TRADING MODE ===\nNo real orders will be placed. Virtual balance %.2f USDT. Market data is live.\n==========================\n\n",
			pp.Balance())
	}

	// PRD-021 §3: external notifications (Discord/Telegram). nil when none
	// configured. The tool layer fires large-PnL close alerts; the orchestrator
	// fires session + breaker-transition alerts.
	notifier := notify.NewFromEnv()
	fridaytool.SetNotifier(notifier, envFloat("FRIDAY_NOTIFY_PNL_PCT", 0.05))

	// PRD-020 §3: fee-budget guardrail (rolling-window anti-overtrading). Install
	// on the tool package (binance_order checks it, log_trade feeds it).
	feeBudget := risk.NewFeeBudget(
		risk.DefaultFeeWindow,
		envFloat("FRIDAY_FEE_BUDGET_PCT", risk.DefaultMaxFeePct),
	)
	fridaytool.SetFeeBudget(feeBudget)

	// PRD-020 §4: portfolio correlation-group caps. The SAME GroupLimits feeds
	// both the binance_order validator AND the Risk Manager prompt, so the cap
	// the model is told about is exactly the cap the code enforces.
	groups := risk.ParseGroupLimits(os.Getenv("FRIDAY_GROUP_LIMITS"))
	portfolioValidator := risk.NewPortfolioGroupValidator(groups)
	fridaytool.SetPortfolioValidator(&portfolioValidator)
	orchestrator.SetPortfolioGroupsHint(groups.PromptHint())

	// PRD-015: calibrate strategy confidences from a startup backtest sweep over
	// recent 4h candles, so each strategy votes with its real per-symbol win rate
	// this session. Best-effort: on failure the hardcoded confidences stand.
	calibrateStrategies(symbols)

	// PRD-020 §5: keep calibration fresh — re-run the sweep every
	// FRIDAY_RECALIBRATE_HOURS (default 4) on a background goroutine so stale
	// confidences don't persist all session as the regime shifts. 0 disables.
	startRecalibrator(symbols)

	// PRD-003: build the three-agent orchestrator (Analyst → Risk
	// Manager → Executor). Tool wiring, profiles, and the round loop all
	// live in internal/orchestrator now; bootstrap only loads config and
	// hands the emitter (TUI sink) in for role-tagged events.
	orch, err := orchestrator.New(cfg, emitter, breaker, symbols)
	if err != nil {
		return nil, nil, fmt.Errorf("orchestrator.New: %w", err)
	}
	orch.SetFeeBudget(feeBudget)
	orch.SetNotifier(notifier)
	orch.SetPaper(paper)
	orch.SetEndpoint(binanceBaseURL())
	orch.SetRegimeSource(fridaytool.RegimeFor)

	// Per-round analysis log: append each round's full Analyst→Risk→Executor
	// outcome to ~/.friday/memory/rounds.jsonl (alongside the trade log) for
	// offline analysis. Same append-only JSONL format as trades.jsonl.
	roundLogPath := filepath.Join(home, ".friday", "memory", "rounds.jsonl")
	orch.SetRoundRecorder(orchestrator.NewRoundRecorder(roundLogPath))

	return orch, cfg, nil
}

// envFloat reads a float env var, returning def when unset or unparsable.
func envFloat(key string, def float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "friday: %s=%q ignored: %v\n", key, v, err)
		return def
	}
	return f
}

// envInt reads an int env var, returning def when unset or unparsable.
func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		fmt.Fprintf(os.Stderr, "friday: %s=%q ignored: %v\n", key, v, err)
		return def
	}
	return n
}

// applyMaxItersFromEnv lets users tweak the loop cap from a one-line
// .env edit without touching the YAML. Wired through
// LoadOptions.EnvOverrides so any failure surfaces in the
// "config: EnvOverrides[max_iters_from_env]: ..." form.
func applyMaxItersFromEnv(cfg *config.Config) error {
	v := os.Getenv("MAX_ITERS")
	if v == "" {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		fmt.Fprintf(os.Stderr, "friday: MAX_ITERS=%q ignored: %v\n", v, err)
		return nil
	}
	return cfg.SetMaxIterations(n)
}
