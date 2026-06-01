// Package risk holds friday's hard-coded, deterministic pre-trade
// guardrails — the code-level backstop between the agent's decision and
// the live exchange. PRD-002 (Phase 2).
//
// Unlike the prompt's seven risk checks (which the LLM self-enforces and
// could, in principle, reason its way past), these validators run in Go
// and cannot be overridden by the model. They are pure and deterministic
// so they can be unit-tested without touching the network.
package risk

import "fmt"

// Order is the proposed trade handed to a Validator. It mirrors the
// fields binance_order acts on.
type Order struct {
	Symbol     string
	Side       string
	Quantity   float64
	ReduceOnly bool
}

// Account is the live snapshot a Validator needs to judge an order. All
// values are USDT terms except Leverage, which is a multiple (e.g. 25).
type Account struct {
	WalletBalance float64 // total USDT wallet balance
	MarkPrice     float64 // current mark price for Order.Symbol
	Leverage      float64 // configured leverage for Order.Symbol (0 = unknown)

	// GroupUsedMargin is the margin (USDT) already committed by OTHER open
	// positions in the same portfolio group as Order.Symbol (PRD-020 §4). The
	// caller computes it; PortfolioGroupValidator adds this order's margin and
	// checks the group cap. 0 when no group applies or no other position is open.
	GroupUsedMargin float64
}

// Validator is the middleware contract: given a proposed order and the
// live account snapshot, return nil to allow it or a non-nil error to
// block it. Implementations must be pure and deterministic.
type Validator interface {
	Validate(Order, Account) error
}

// CapExceededError is returned when an order breaches a guardrail. Its
// message is written to be read by the agent — it names the numbers and
// tells the model exactly how to recover.
type CapExceededError struct {
	Margin   float64
	Notional float64
	Leverage float64
	Cap      float64
	Balance  float64
	CapPct   float64
}

func (e *CapExceededError) Error() string {
	return fmt.Sprintf(
		"GUARDRAIL BLOCKED: order margin $%.2f (notional $%.2f ÷ %.0fx leverage) exceeds the %.0f%%-of-balance cap of $%.2f (wallet balance $%.2f). Recalculate with a smaller quantity or lower leverage, then retry.",
		e.Margin, e.Notional, e.Leverage, e.CapPct*100, e.Cap, e.Balance)
}

// MarginCapValidator blocks any OPENING order whose margin (notional ÷
// leverage) exceeds MaxMarginPct of the wallet balance. This matches the
// per-position hard cap documented in the system prompt (15%), enforced
// here in code as a fat-finger backstop.
//
// Reduce-only orders (closes) are always allowed — flattening risk must
// never be blocked. When the snapshot is indeterminate (zero balance or
// price), Validate allows the order and leaves degradation handling to
// the caller.
type MarginCapValidator struct {
	MaxMarginPct float64 // e.g. 0.15 for 15%
}

// NewMarginCapValidator returns a validator capping order margin at
// maxMarginPct of wallet balance.
func NewMarginCapValidator(maxMarginPct float64) MarginCapValidator {
	return MarginCapValidator{MaxMarginPct: maxMarginPct}
}

// Validate satisfies Validator.
func (v MarginCapValidator) Validate(o Order, a Account) error {
	if o.ReduceOnly {
		return nil // closes always allowed
	}
	if a.WalletBalance <= 0 || a.MarkPrice <= 0 {
		return nil // indeterminate — caller decides whether to flag it
	}

	notional := o.Quantity * a.MarkPrice
	margin := notional
	if a.Leverage > 0 {
		margin = notional / a.Leverage
	}

	limit := a.WalletBalance * v.MaxMarginPct
	if margin > limit {
		return &CapExceededError{
			Margin:   margin,
			Notional: notional,
			Leverage: a.Leverage,
			Cap:      limit,
			Balance:  a.WalletBalance,
			CapPct:   v.MaxMarginPct,
		}
	}
	return nil
}
