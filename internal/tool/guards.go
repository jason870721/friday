package tool

import (
	"github.com/johnny1110/friday/internal/notify"
	"github.com/johnny1110/friday/internal/risk"
)

// Process-wide pre-trade guards installed by bootstrap (PRD-020). Like
// globalBreaker, they are consulted by binance_order before an OPENING order
// and fed by log_trade. Nil when unset (tests / no config) → the gate is a
// no-op.
var (
	// globalFeeBudget caps fee spend over a rolling window (PRD-020 §3).
	globalFeeBudget *risk.FeeBudget

	// globalPortfolioValidator caps combined margin per correlated group
	// (PRD-020 §4).
	globalPortfolioValidator *risk.PortfolioGroupValidator

	// globalPaper is the virtual book for paper-trading mode (PRD-021 §4). When
	// non-nil, the trading tools intercept and update it instead of hitting the
	// exchange; market-data tools are unaffected.
	globalPaper *risk.PaperPortfolio

	// globalNotifier pushes significant events externally (PRD-021 §3). nil → no
	// notifications. notifyPnLPct is the |netPnL|/balance threshold for a
	// large-PnL close alert.
	globalNotifier notify.Notifier
	notifyPnLPct   = 0.05
)

// SetFeeBudget installs the shared fee-budget guard. Called once at bootstrap.
func SetFeeBudget(fb *risk.FeeBudget) { globalFeeBudget = fb }

// SetPortfolioValidator installs the shared portfolio-group guard. Called once
// at bootstrap.
func SetPortfolioValidator(v *risk.PortfolioGroupValidator) { globalPortfolioValidator = v }

// SetPaperPortfolio installs the virtual book and switches the trading tools
// into paper mode (PRD-021 §4). Called once at bootstrap when FRIDAY_PAPER=true.
func SetPaperPortfolio(p *risk.PaperPortfolio) { globalPaper = p }

// PaperEnabled reports whether paper-trading mode is active.
func PaperEnabled() bool { return globalPaper != nil }

// SetNotifier installs the external notifier and the large-PnL alert threshold
// (PRD-021 §3). Called once at bootstrap.
func SetNotifier(n notify.Notifier, pnlPct float64) {
	globalNotifier = n
	if pnlPct > 0 {
		notifyPnLPct = pnlPct
	}
}
