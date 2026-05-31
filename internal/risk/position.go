package risk

// PRD-007: volatility-aware position sizing. Instead of a flat percentage of
// balance, derive quantity from a fixed RISK budget (a fraction of balance you
// accept losing if the stop is hit) divided by an ATR-based stop distance. A
// low-volatility symbol (BTC) and a high-volatility one (SOL) then carry
// comparable risk per trade. This produces a TARGET that still sits under the
// 15% hard margin cap enforced by MarginCapValidator — it does not replace it.

// Direction labels for SuggestedSize (match the rest of friday's LONG/SHORT).
const (
	DirLong  = "LONG"
	DirShort = "SHORT"
)

// Default ATR-sizing parameters.
const (
	DefaultRiskPerTrade   = 0.01 // risk 1% of balance per trade
	DefaultStopMultiplier = 2.0  // stop sits 2×ATR from entry
)

// SizeParams are the inputs to SuggestedSize. All prices/balances are USDT;
// Leverage is a multiple (e.g. 25).
type SizeParams struct {
	Balance        float64 // wallet balance
	EntryPrice     float64 // intended entry / current mark price
	ATR            float64 // ATR(14) in price units
	Leverage       float64 // configured leverage
	RiskPerTrade   float64 // fraction of balance risked at the stop (e.g. 0.01)
	StopMultiplier float64 // ATR multiples to the stop (e.g. 2.0)
	MaxMarginPct   float64 // hard margin cap fraction (e.g. 0.15); 0 disables the clamp
}

// SizeResult is the volatility-calibrated suggestion.
type SizeResult struct {
	Quantity      float64 // base-asset quantity
	Notional      float64 // Quantity × EntryPrice
	Margin        float64 // Notional ÷ Leverage
	StopPrice     float64 // entry ∓ StopMultiplier×ATR (below for long, above for short)
	CappedByLimit bool    // true when the risk-based size was clamped to MaxMarginPct
}

// SuggestedSize computes a risk-based position size for dir (DirLong/DirShort).
// quantity = (Balance × RiskPerTrade) / (StopMultiplier × ATR), then clamped so
// margin never exceeds MaxMarginPct of balance. Returns the zero value when any
// input is non-positive (nothing sensible to size).
func SuggestedSize(dir string, p SizeParams) SizeResult {
	if p.Balance <= 0 || p.EntryPrice <= 0 || p.ATR <= 0 ||
		p.Leverage <= 0 || p.RiskPerTrade <= 0 || p.StopMultiplier <= 0 {
		return SizeResult{}
	}

	stopDist := p.StopMultiplier * p.ATR
	riskBudget := p.Balance * p.RiskPerTrade

	qty := riskBudget / stopDist
	notional := qty * p.EntryPrice
	margin := notional / p.Leverage

	capped := false
	if p.MaxMarginPct > 0 {
		maxMargin := p.Balance * p.MaxMarginPct
		if margin > maxMargin {
			capped = true
			margin = maxMargin
			notional = margin * p.Leverage
			qty = notional / p.EntryPrice
		}
	}

	stop := p.EntryPrice - stopDist
	if dir == DirShort {
		stop = p.EntryPrice + stopDist
	}

	return SizeResult{
		Quantity:      qty,
		Notional:      notional,
		Margin:        margin,
		StopPrice:     stop,
		CappedByLimit: capped,
	}
}
