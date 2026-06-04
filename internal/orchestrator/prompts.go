package orchestrator

import (
	"fmt"
	"strconv"
	"strings"
)

// Prompt rendering. Each role agent (Analyst → Risk Manager → Executor) gets a
// single-responsibility system prompt built from a template by renderPrompt.
//
// The mandate TEXT lives in prompt_templates.go (the authoritative source of
// trading logic); this file is just the machinery that fills its per-session
// tokens — {{SYMBOLS}} / {{COUNT}} / {{STEPS}} / {{GROUPS}} — so the covered
// markets and correlated-group caps stay config concerns, not hardcoded prompt
// text. What used to be the agent's own "never stop / schedule_wakeup" loop is
// owned by the Go orchestrator, so the prompts say nothing about looping.

// --- entry points: a finished system prompt per role ---

// analystSystemPrompt takes the submit tool's NAME so the parallel fleet can
// give each per-symbol agent a UNIQUE submit tool (e.g. submit_analysis_BTCUSDT)
// — evva dedups custom tools by name across agents, so a shared name would make
// the whole fleet collide on one capture. The single-agent path passes
// submitAnalysisName.
func analystSystemPrompt(syms []MarketSymbol, submitName string) string {
	return renderPrompt(analystSystemTmpl, syms, submitName)
}
func riskSystemPrompt(syms []MarketSymbol) string     { return renderPrompt(riskSystemTmpl, syms, submitRiskName) }
func executorSystemPrompt(syms []MarketSymbol) string { return renderPrompt(executorSystemTmpl, syms, submitExecName) }

// renderPrompt substitutes the per-session tokens in a prompt template. A
// string replacer (not fmt) is used because the prompts are dense with literal
// '%' signs (15%, RSI zones, funding thresholds) that fmt would misread.
// {{SUBMIT}} is the role's submit tool name (only the Analyst template uses it;
// a no-op for the risk/exec templates, which name their submit tools inline).
func renderPrompt(tmpl string, syms []MarketSymbol, submitName string) string {
	return strings.NewReplacer(
		"{{SYMBOLS}}", symbolNames(syms),
		"{{COUNT}}", strconv.Itoa(len(syms)),
		"{{STEPS}}", stepSizeHint(syms),
		"{{GROUPS}}", portfolioGroupsHint(),
		"{{SUBMIT}}", submitName,
	).Replace(tmpl)
}

// --- token helpers ---

// symbolNames renders the list as "BTCUSDT, ETHUSDT, SOLUSDT".
func symbolNames(syms []MarketSymbol) string {
	names := make([]string, len(syms))
	for i, s := range syms {
		names[i] = s.Name
	}
	return strings.Join(names, ", ")
}

// stepSizeHint renders the per-symbol quantity step the Risk Manager rounds to,
// its max leverage (PRD-012), AND the notional ceiling at that max leverage
// (PRD-019), e.g.
// "BTCUSDT 0.001 (≤125x), NVDAUSDT 0.01 (≤10x, ≤$5k @max-lev)". A symbol with no
// known step is skipped; an unknown max leverage omits the "(≤Nx…)" suffix; an
// unknown notional cap omits just the "@max-lev" part.
func stepSizeHint(syms []MarketSymbol) string {
	parts := make([]string, 0, len(syms))
	for _, s := range syms {
		if s.StepSize == "" {
			continue
		}
		p := fmt.Sprintf("%s %s", s.Name, s.StepSize)
		if s.MaxLeverage > 0 {
			if s.MaxNotional > 0 {
				p += fmt.Sprintf(" (≤%dx, ≤%s @max-lev)", s.MaxLeverage, shortUSD(s.MaxNotional))
			} else {
				p += fmt.Sprintf(" (≤%dx)", s.MaxLeverage)
			}
		}
		parts = append(parts, p)
	}
	if len(parts) == 0 {
		return "each symbol's exchangeInfo LOT_SIZE step"
	}
	return strings.Join(parts, ", ")
}

// shortUSD renders a dollar amount compactly for the prompt: "$5k", "$25k",
// "$1.2M", "$300" — enough precision for the Risk Manager to size against the
// notional tier without flooding the prompt with digits.
func shortUSD(v float64) string {
	switch {
	case v >= 1_000_000:
		return shortNum(v/1_000_000, "M")
	case v >= 1_000:
		return shortNum(v/1_000, "k")
	default:
		return fmt.Sprintf("$%.0f", v)
	}
}

// shortNum drops a trailing ".0" so 5.0k renders "$5k" but 1.2M stays "$1.2M".
func shortNum(n float64, unit string) string {
	if n == float64(int64(n)) {
		return fmt.Sprintf("$%d%s", int64(n), unit)
	}
	return fmt.Sprintf("$%.1f%s", n, unit)
}

// --- {{GROUPS}} injectable state (PRD-020 §4) ---

// portfolioGroupsString holds the correlated-group caps for the Risk Manager
// prompt, installed by bootstrap from the same risk.GroupLimits the
// binance_order validator enforces — so the prompt and the code agree. Empty
// until set (e.g. in tests) → a neutral note.
var portfolioGroupsString string

// SetPortfolioGroupsHint installs the rendered group-cap hint the Risk Manager
// prompt's {{GROUPS}} token expands to. Called once at bootstrap, before New.
func SetPortfolioGroupsHint(s string) { portfolioGroupsString = s }

func portfolioGroupsHint() string {
	if strings.TrimSpace(portfolioGroupsString) == "" {
		return "no correlated-group caps configured this session"
	}
	return portfolioGroupsString
}
