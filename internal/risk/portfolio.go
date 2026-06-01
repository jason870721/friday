package risk

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Portfolio-level correlation-aware sizing (PRD-020 §4). The per-position 15%
// margin cap treats every symbol independently, but BTC/ETH/SOL move together
// and the US-stock perps (NVDA/GOOGL/AMZN/META) share tech-sector beta — so
// "7 positions at 14% each" can be only TWO real bets at ~50% margin apiece. A
// broad risk-off move then hits the whole book at once. GroupLimits caps the
// COMBINED margin of correlated symbols; the tighter of (group cap, per-position
// cap) wins.

// GroupConfig is one correlated group's cap and members.
type GroupConfig struct {
	MaxMarginPct float64  // combined margin ceiling as a fraction of balance, e.g. 0.30
	Symbols      []string // member symbols, e.g. ["BTCUSDT","ETHUSDT","SOLUSDT"]
}

// GroupLimits maps a group name → its config. Symbols absent from every group
// are unconstrained (only the per-position cap applies).
type GroupLimits map[string]GroupConfig

// DefaultGroupLimits is friday's out-of-the-box correlation map: crypto majors
// capped at 30% combined margin, US-stock perps at 40%.
func DefaultGroupLimits() GroupLimits {
	return GroupLimits{
		"crypto": {MaxMarginPct: 0.30, Symbols: []string{"BTCUSDT", "ETHUSDT", "SOLUSDT"}},
		"stocks": {MaxMarginPct: 0.40, Symbols: []string{"NVDAUSDT", "GOOGLUSDT", "AMZNUSDT", "METAUSDT"}},
	}
}

// ParseGroupLimits parses the FRIDAY_GROUP_LIMITS env format into GroupLimits,
// falling back to DefaultGroupLimits when raw is empty or unparseable. The
// format is semicolon-separated groups, each "name:pct:SYM1,SYM2,…" where pct is
// a percent (e.g. 30 → 0.30):
//
//	crypto:30:BTCUSDT,ETHUSDT,SOLUSDT;stocks:40:NVDAUSDT,GOOGLUSDT
func ParseGroupLimits(raw string) GroupLimits {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultGroupLimits()
	}
	out := GroupLimits{}
	for _, grp := range strings.Split(raw, ";") {
		grp = strings.TrimSpace(grp)
		if grp == "" {
			continue
		}
		parts := strings.SplitN(grp, ":", 3)
		if len(parts) != 3 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		pct, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
		if err != nil || pct <= 0 {
			continue
		}
		var syms []string
		for _, s := range strings.Split(parts[2], ",") {
			if s = strings.ToUpper(strings.TrimSpace(s)); s != "" {
				syms = append(syms, s)
			}
		}
		if name == "" || len(syms) == 0 {
			continue
		}
		out[name] = GroupConfig{MaxMarginPct: pct / 100.0, Symbols: syms}
	}
	if len(out) == 0 {
		return DefaultGroupLimits()
	}
	return out
}

// GroupFor returns the name and config of the group a symbol belongs to, and
// whether one was found. A symbol may appear in at most one group (first match
// in deterministic name order wins).
func (g GroupLimits) GroupFor(symbol string) (string, GroupConfig, bool) {
	for _, name := range g.names() {
		cfg := g[name]
		for _, s := range cfg.Symbols {
			if s == symbol {
				return name, cfg, true
			}
		}
	}
	return "", GroupConfig{}, false
}

// Members returns the group's member symbols excluding `exclude` — the OTHER
// symbols whose open margin counts toward the group's used exposure.
func (c GroupConfig) Members(exclude string) []string {
	out := make([]string, 0, len(c.Symbols))
	for _, s := range c.Symbols {
		if s != exclude {
			out = append(out, s)
		}
	}
	return out
}

// names returns the group names in deterministic (sorted) order.
func (g GroupLimits) names() []string {
	out := make([]string, 0, len(g))
	for n := range g {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// PromptHint renders the group caps for the Risk Manager prompt, e.g.
// "crypto ≤30% combined (BTCUSDT/ETHUSDT/SOLUSDT); stocks ≤40% combined (…)".
func (g GroupLimits) PromptHint() string {
	if len(g) == 0 {
		return "no correlated-group caps configured"
	}
	parts := make([]string, 0, len(g))
	for _, name := range g.names() {
		cfg := g[name]
		parts = append(parts, fmt.Sprintf("%s ≤%.0f%% combined margin (%s)",
			name, cfg.MaxMarginPct*100, strings.Join(cfg.Symbols, "/")))
	}
	return strings.Join(parts, "; ")
}

// GroupCapExceededError is returned when an opening order would push its group's
// combined margin over the group cap. Its message names the numbers and tells
// the model how to recover.
type GroupCapExceededError struct {
	Group      string
	NewMargin  float64
	UsedMargin float64
	Cap        float64
	Balance    float64
	CapPct     float64
}

func (e *GroupCapExceededError) Error() string {
	return fmt.Sprintf(
		"PORTFOLIO GROUP BLOCKED: opening this position adds $%.2f margin to group %q, which already uses $%.2f — total $%.2f exceeds the group's %.0f%%-of-balance cap of $%.2f (balance $%.2f). The %s symbols are correlated; reduce size, close another %s position, or trade an uncorrelated symbol.",
		e.NewMargin, e.Group, e.UsedMargin, e.NewMargin+e.UsedMargin, e.CapPct*100, e.Cap, e.Balance, e.Group, e.Group)
}

// PortfolioGroupValidator blocks an OPENING order whose group's combined margin
// (this order + already-open members) would exceed the group cap (PRD-020 §4).
// Reduce-only closes always pass; symbols in no group always pass; an
// indeterminate snapshot (zero balance/price) passes (the caller's other gates
// apply). Implements Validator.
type PortfolioGroupValidator struct {
	Limits GroupLimits
}

// NewPortfolioGroupValidator returns a validator over the given limits.
func NewPortfolioGroupValidator(limits GroupLimits) PortfolioGroupValidator {
	return PortfolioGroupValidator{Limits: limits}
}

// Validate satisfies Validator. It computes THIS order's margin (notional ÷
// leverage) and adds Account.GroupUsedMargin (the margin already committed by
// the group's other open positions, supplied by the caller).
func (v PortfolioGroupValidator) Validate(o Order, a Account) error {
	if o.ReduceOnly {
		return nil
	}
	if a.WalletBalance <= 0 || a.MarkPrice <= 0 {
		return nil
	}
	name, cfg, ok := v.Limits.GroupFor(o.Symbol)
	if !ok {
		return nil // symbol not in any group
	}

	notional := o.Quantity * a.MarkPrice
	newMargin := notional
	if a.Leverage > 0 {
		newMargin = notional / a.Leverage
	}

	limit := a.WalletBalance * cfg.MaxMarginPct
	if newMargin+a.GroupUsedMargin > limit {
		return &GroupCapExceededError{
			Group:      name,
			NewMargin:  newMargin,
			UsedMargin: a.GroupUsedMargin,
			Cap:        limit,
			Balance:    a.WalletBalance,
			CapPct:     cfg.MaxMarginPct,
		}
	}
	return nil
}
