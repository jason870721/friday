package risk

import (
	"fmt"
	"sync"
	"time"
)

// FeeBudget is a session-level anti-overtrading guardrail (PRD-020 §3). The
// circuit breaker catches large/consecutive losses, but neither it nor the
// per-trade margin cap captures death-by-a-thousand-cuts: PRD-011's
// reconciliation showed fees consumed ~36% of total losses when the system
// over-traded. FeeBudget tracks fee spend over a rolling window and blocks new
// OPENINGS once the spend exceeds a fraction of the wallet balance.
//
// It complements the prompt's "only open when the move clears ≥3× round-trip
// fee" rule with a hard, code-enforced ceiling the LLM cannot reason past.
//
// Feeding:
//   - Record(fee, balance) — called by log_trade after exchange reconciliation,
//     with the magnitude of commission + funding paid on the close.
//   - Check(balance)       — called by binance_order before an OPENING order.
//
// All methods are safe for concurrent use.
type FeeBudget struct {
	mu sync.Mutex

	window    time.Duration
	maxFeePct float64 // e.g. 0.005 → block at 0.5% of balance spent on fees in the window
	entries   []feeEntry
	now       func() time.Time // injectable clock for tests; nil → time.Now
}

type feeEntry struct {
	at      time.Time
	fee     float64 // positive fee cost in USDT
	balance float64
}

// DefaultFeeWindow / DefaultMaxFeePct are the documented defaults: at a ~0.08%
// round-trip taker fee, ~6 round-trips in 30 min hit the 0.5% ceiling.
const (
	DefaultFeeWindow = 30 * time.Minute
	DefaultMaxFeePct = 0.005
)

// NewFeeBudget builds a budget. Non-positive arguments fall back to the
// documented defaults.
func NewFeeBudget(window time.Duration, maxFeePct float64) *FeeBudget {
	if window <= 0 {
		window = DefaultFeeWindow
	}
	if maxFeePct <= 0 {
		maxFeePct = DefaultMaxFeePct
	}
	return &FeeBudget{window: window, maxFeePct: maxFeePct}
}

func (fb *FeeBudget) clock() time.Time {
	if fb.now != nil {
		return fb.now()
	}
	return time.Now()
}

// Record folds one closed trade's fee spend into the window. fee is the POSITIVE
// magnitude of fees paid (commission + funding cost); a non-positive fee (e.g.
// funding received exceeded commission) records nothing — it is not "spend".
func (fb *FeeBudget) Record(fee, balance float64) {
	if fee <= 0 {
		return
	}
	fb.mu.Lock()
	defer fb.mu.Unlock()
	now := fb.clock()
	fb.entries = append(fb.entries, feeEntry{at: now, fee: fee, balance: balance})
	fb.expire(now)
}

// expire drops entries older than the window. Caller holds the lock.
func (fb *FeeBudget) expire(now time.Time) {
	cut := now.Add(-fb.window)
	i := 0
	for i < len(fb.entries) && fb.entries[i].at.Before(cut) {
		i++
	}
	if i > 0 {
		fb.entries = append(fb.entries[:0], fb.entries[i:]...)
	}
}

// windowedFee sums the fee spend still inside the window and returns it with the
// most-recent recorded balance. Caller holds the lock.
func (fb *FeeBudget) windowedFee(now time.Time) (sum, balance float64) {
	fb.expire(now)
	for _, e := range fb.entries {
		sum += e.fee
		balance = e.balance
	}
	return sum, balance
}

// Check returns a non-nil, actionable error when the windowed fee spend exceeds
// maxFeePct of the balance — blocking a new OPENING order. Reduce-only closes
// must bypass this at the call site. A non-positive balance is indeterminate →
// allow (the caller's other gates still apply).
func (fb *FeeBudget) Check(balance float64) error {
	if balance <= 0 {
		return nil
	}
	fb.mu.Lock()
	defer fb.mu.Unlock()
	sum, _ := fb.windowedFee(fb.clock())
	limit := balance * fb.maxFeePct
	if sum > limit {
		return fmt.Errorf(
			"FEE BUDGET EXCEEDED: $%.4f in fees over the last %s exceeds the %.2f%%-of-balance cap of $%.4f (balance $%.2f) — stop opening new positions and let the window roll off (anti-overtrading)",
			sum, fb.window, fb.maxFeePct*100, limit, balance)
	}
	return nil
}

// Status is a one-line summary for the round prompt, plus whether the budget is
// "near" the limit (≥50% spent) so the orchestrator only surfaces it when it
// matters. It uses the most recently recorded balance (no live fetch).
func (fb *FeeBudget) Status() (line string, near bool) {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	sum, balance := fb.windowedFee(fb.clock())
	if balance <= 0 {
		return "", false
	}
	limit := balance * fb.maxFeePct
	pct := 0.0
	if limit > 0 {
		pct = sum / limit * 100
	}
	line = fmt.Sprintf("fee budget: $%.4f spent in last %s (%.0f%% of the %.2f%%-of-balance cap)",
		sum, fb.window, pct, fb.maxFeePct*100)
	return line, pct >= 50
}
