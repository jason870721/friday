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

	"github.com/johnny1110/evva/pkg/agent"
	"github.com/johnny1110/evva/pkg/config"
	"github.com/johnny1110/evva/pkg/constant"
	"github.com/johnny1110/evva/pkg/event"
	_ "github.com/johnny1110/evva/pkg/llm/builtins"
	pkgtools "github.com/johnny1110/evva/pkg/tools"
	"github.com/johnny1110/evva/pkg/tools/kits"

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
# MAX_ITERS=30
`

// New builds a ready-to-Run friday agent and returns it together with
// the resolved *config.Config (the TUI reads provider/model from it
// for the status footer).
func New(sink event.Sink) (agent.Agent, *config.Config, error) {
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

	// Canonical general-purpose tool kit. (active includes
	// tool_search because we're using the deferred companion.)
	active, deferred := kits.GeneralPurposeKit()
	// Append friday's own custom tools. Echo is wired below via
	// WithCustomTool; the name must also appear in active so the
	// LLM sees it in the tool catalog from turn one.

	prof, err := agent.NewProfile(
		"friday",
		SystemPrompt,
		active,
		"deepseek",
		constant.DEEPSEEK_V4_PRO,
		agent.ProfileOptions{
			DeferredTools: deferred,
			Stream:        false, // buffered — keeps the TUI simple
		},
	)
	if err != nil {
		return nil, nil, fmt.Errorf("agent.NewProfile: %w", err)
	}

	ag, err := agent.NewWithProfile(prof,
		agent.WithConfig(cfg),
		agent.WithSink(sink),
		agent.WithMaxIterations(maxIters(cfg)),
		agent.WithHeadlessBypass(),
		agent.WithName("friday"),
		// Friday's own custom tools. The factory receives tools.State
		// (Config + Workdir + Logger) at build time; EchoTool ignores
		// state but real friday tools can reach for cfg or workdir
		// from here.
		agent.WithCustomTool(fridaytool.EchoToolName, func(pkgtools.State) (pkgtools.Tool, error) {
			return fridaytool.NewEcho(), nil
		}),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("agent.NewWithProfile: %w", err)
	}

	for _, act := range active {
		ag.Logger().Info("ExposeTool", act)
	}

	return ag, cfg, nil
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

func maxIters(cfg *config.Config) int {
	if cfg.DefaultMaxIterations > 0 {
		return cfg.DefaultMaxIterations
	}
	return 30
}
