package tool

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/johnny1110/friday/internal/memory"
	"github.com/johnny1110/friday/internal/notify"
	"github.com/johnny1110/friday/internal/risk"
)

// HasOpenPositions reports whether any non-zero position is currently held —
// a cheap Go-side check the orchestrator uses to decide if it can skip the Risk
// Manager on an all-NEUTRAL round (the idle short-circuit). Paper mode reads the
// virtual book (instant); live mode does one /fapi/v2/positionRisk call. On a
// query error it returns true (fail safe — never skip the risk checks when the
// position state is unknown).
func HasOpenPositions(ctx context.Context) bool {
	if globalPaper != nil {
		return len(globalPaper.Positions()) > 0
	}
	cli, err := sharedBinanceClient()
	if err != nil {
		return true
	}
	open, err := cli.OpenPositions(ctx)
	if err != nil {
		return true
	}
	return len(open) > 0
}

// OpenPositionsBySymbol returns a ground-truth snapshot of the currently-held
// positions keyed by symbol (e.g. "SHORT 0.4 @ 880.5000 uPnL +2.30"). The
// orchestrator injects this authoritative state into the role prompts so the
// Analyst/Risk Manager stop trusting the LLM-authored carry, which drifts when a
// position is closed OUT-OF-BAND by the StopMonitor (the carry then keeps
// asserting a holding that no longer exists). Paper mode reads the virtual book;
// live mode does one positionRisk call. The bool is false when the real state
// could NOT be determined (no client / query error) — the caller must then NOT
// assert "flat" and should fall back to the carry.
func OpenPositionsBySymbol(ctx context.Context) (map[string]string, bool) {
	out := map[string]string{}
	if globalPaper != nil {
		for _, p := range globalPaper.Positions() {
			out[p.Symbol] = fmt.Sprintf("%s %g @ %.4f", p.Side(), absf(p.Amt), p.Entry)
		}
		return out, true
	}
	cli, err := sharedBinanceClient()
	if err != nil {
		return nil, false
	}
	open, err := cli.OpenPositions(ctx)
	if err != nil {
		return nil, false
	}
	for _, p := range open {
		amt, _ := strconv.ParseFloat(p.PositionAmt, 64)
		dir := "LONG"
		if amt < 0 {
			dir = "SHORT"
		}
		entry, _ := strconv.ParseFloat(p.EntryPrice, 64)
		upnl, _ := strconv.ParseFloat(p.UnRealizedProfit, 64)
		out[p.Symbol] = fmt.Sprintf("%s %g @ %.4f uPnL %+.2f", dir, absf(amt), entry, upnl)
	}
	return out, true
}

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

// estRoundTripFeeRate is the round-trip taker fee assumed when estimating a
// StopMonitor close's net PnL (~2 × 0.04% taker = 0.08% of notional). It is a
// deliberate over-estimate: for a safety breaker, UNDER-reporting a loss is the
// dangerous direction, so we'd rather the daily-loss gate trip slightly early.
const estRoundTripFeeRate = 0.0008

// LogStopClose records a position closed by the StopMonitor (outside the normal
// round loop) into the trade memory, feeds the circuit breaker, and fires a
// notification — the same path a round-based close takes through log_trade.
//
// The PnL is ESTIMATED from entry vs mark minus an assumed round-trip taker fee
// (the income ledger isn't queried here — it lags a market close by seconds), so
// the record is marked "reported", not "exchange"; cmd/reconcile-memory backfills
// the true net later. Feeding the breaker matters for safety: the daily-loss and
// consecutive-loss gates would otherwise MISS monitor-triggered losses entirely
// (drawdown/HALT keys off the true wallet balance via Observe, so it is exact).
func LogStopClose(event risk.StopCloseEvent) {
	store, err := sharedTradeStore()
	if err != nil {
		return
	}

	var pnl float64
	bias := "LONG"
	if event.PositionSide == risk.DirShort {
		bias = "SHORT"
	}
	if event.EntryPrice > 0 && event.MarkPrice > 0 {
		switch event.PositionSide {
		case risk.DirLong:
			pnl = (event.MarkPrice - event.EntryPrice) * event.PositionQty
		case risk.DirShort:
			pnl = (event.EntryPrice - event.MarkPrice) * event.PositionQty
		}
		// Deduct the estimated round-trip fee so the breaker's daily-loss
		// accumulator isn't systematically under-counting, and a marginal gross
		// gain that doesn't cover fees is correctly classified as a net LOSS.
		pnl -= estRoundTripFeeRate * event.MarkPrice * event.PositionQty
	}

	rec := memory.TradeRecord{
		Symbol:      event.Symbol,
		Time:        time.Now().Unix(),
		EntryReason: fmt.Sprintf("StopMonitor: %s at %.4f", event.Reason, event.MarkPrice),
		Bias:        bias,
		PnL:         pnl,
		PnLSource:   "reported",
		Outcome:     "LOSS",
		Paper:       globalPaper != nil,
	}
	if pnl > 0 {
		rec.Outcome = "WIN"
	}

	if err := store.Log(rec); err != nil {
		return
	}

	// Feed the circuit breaker with the estimated PnL.
	if globalBreaker != nil {
		globalBreaker.RecordTrade(pnl)
	}

	// Fire a notification for every StopMonitor close.
	if globalNotifier != nil {
		outcomeWord := "虧損"
		if pnl > 0 {
			outcomeWord = "獲利"
		}
		tag := ""
		if rec.Paper {
			tag = " [PAPER]"
		}
		reason := "止損"
		if event.Reason == "take-profit" {
			reason = "停利"
		}
		title := fmt.Sprintf("🛑 Friday StopMonitor: %s %s%s", event.Symbol, reason, tag)
		body := fmt.Sprintf("%s %s %s約 %+.2f USDT，平倉價 %.4f",
			bias, event.Symbol, outcomeWord, pnl, event.MarkPrice)
		if nerr := globalNotifier.Notify(title, body); nerr != nil {
			// best-effort; don't fail the close for a notify error
		}
	}
}
