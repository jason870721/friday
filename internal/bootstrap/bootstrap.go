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

	"github.com/johnny1110/evva/pkg/config"
	"github.com/johnny1110/evva/pkg/constant"
	_ "github.com/johnny1110/evva/pkg/llm/builtins"

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

	// PRD-003: build the three-agent orchestrator (Analyst → Risk
	// Manager → Executor). Tool wiring, profiles, and the round loop all
	// live in internal/orchestrator now; bootstrap only loads config and
	// hands the emitter (TUI sink) in for role-tagged events.
	orch, err := orchestrator.New(cfg, emitter, breaker)
	if err != nil {
		return nil, nil, fmt.Errorf("orchestrator.New: %w", err)
	}

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
