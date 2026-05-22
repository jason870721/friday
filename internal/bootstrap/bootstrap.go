// Package bootstrap wires friday's runtime: load config, fold in
// ~/.friday/.env overrides, install DeepSeek credentials, build the
// Profile, construct the agent.
//
// Nothing in this package imports evva's internal/* — the entire
// surface is pkg/agent + pkg/config + pkg/tools + pkg/event +
// pkg/tools/kits + pkg/llm/builtins. That last blank import
// side-effect-registers Anthropic, DeepSeek, and Ollama into
// pkg/llm.DefaultRegistry so the Profile's "deepseek" name resolves
// at agent construction.
//
// Phase 19 (evva v0.2.4-alpha.2) revamp: every multi-line helper here
// used to be a hand-rolled shim around an evva surface that lacked an
// ergonomic accessor. The whole bootstrap is now ~60 lines, half the
// size of the previous round. See docs/sdk-feedback.md for the full
// round-2 report.
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
	"github.com/johnny1110/evva/pkg/tools/kits"
)

// New builds a ready-to-Run friday agent and returns it together with
// the resolved *config.Config (the TUI reads provider/model from it
// for the status footer).
//
// Failure modes:
//   - config.Load returns an error (filesystem write-back on first run
//     hit a permission problem, AppHome is unwriteable, etc.)
//   - agent.NewWithProfile fails to build the LLM client (typically a
//     missing API key; we print a hint above so the user can self-heal)
func New(sink event.Sink) (agent.Agent, *config.Config, error) {
	home, _ := os.UserHomeDir()

	// config.Load auto-loads ~/.friday/.env via godotenv. EnvAliases
	// translate friday-flavoured names → evva canonicals before that
	// happens; EnvOverrides fold any vars that don't have a YAML hook
	// (MAX_ITERS, APIKEY → deepseek creds) into the populated cfg.
	cfg, err := config.Load(config.LoadOptions{
		AppName: "friday",
		AppHome: filepath.Join(home, ".friday"),
		EnvAliases: map[string]string{
			"LOGDIR":   "LOG_DIR",
			"LOGLEVEL": "LOG_LEVEL",
			"APIKEY":   "DEEPSEEK_API_KEY",
		},
		EnvOverrides: []func(*config.Config) error{
			applyMaxItersFromEnv,
			applyDeepSeekCreds,
		},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("config.Load: %w", err)
	}

	// Canonical general-purpose tool kit. Replaces the
	// fs.Names()+shell.Names()+todo.Names()+util.Names() chain friday
	// used to assemble by hand.
	active, deferred := kits.GeneralPurposeKit()

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
	)
	if err != nil {
		return nil, nil, fmt.Errorf("agent.NewWithProfile: %w", err)
	}
	return ag, cfg, nil
}

// applyDeepSeekCreds installs the DeepSeek API key on cfg from
// DEEPSEEK_API_KEY (canonical) or APIKEY (the friday-flavoured alias
// promoted by EnvAliases above).
//
// Runs as a LoadOptions.EnvOverrides callback so cfg.mu is the only
// thing touched.
func applyDeepSeekCreds(cfg *config.Config) error {
	apiKey := os.Getenv("DEEPSEEK_API_KEY")
	if apiKey == "" {
		// Soft-fail with a hint — let the user reach the TUI before
		// failing on the first Run.
		fmt.Fprintln(os.Stderr,
			"friday: DEEPSEEK_API_KEY is empty — set it in ~/.friday/.env and try again.")
	}
	return cfg.SetProviderCredentials("deepseek", constant.DEEPSEEK.ApiUrl, apiKey)
}

// applyMaxItersFromEnv lets users tweak the loop cap from a one-line
// .env edit without touching the YAML.
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
