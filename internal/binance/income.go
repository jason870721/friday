package binance

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
)

// IncomeEntry is one row of the futures account ledger (/fapi/v1/income):
// realized PnL, commission, funding, transfers, etc. Income is a SIGNED
// decimal string (e.g. "-4.3904" for a fee or loss).
type IncomeEntry struct {
	Symbol     string `json:"symbol"`
	IncomeType string `json:"incomeType"` // REALIZED_PNL, COMMISSION, FUNDING_FEE, TRANSFER, ...
	Income     string `json:"income"`
	Time       int64  `json:"time"` // unix millis
	TradeID    string `json:"tradeId"`
}

// Income returns account-ledger rows, optionally scoped to one symbol and a
// [startTimeMs, endTimeMs] window (0 = unbounded). Signed endpoint.
func (c *Client) Income(ctx context.Context, symbol string, startTimeMs, endTimeMs int64, limit int) ([]IncomeEntry, error) {
	params := url.Values{}
	if symbol != "" {
		params.Set("symbol", symbol)
	}
	if startTimeMs > 0 {
		params.Set("startTime", strconv.FormatInt(startTimeMs, 10))
	}
	if endTimeMs > 0 {
		params.Set("endTime", strconv.FormatInt(endTimeMs, 10))
	}
	if limit > 0 {
		params.Set("limit", strconv.Itoa(limit))
	}
	var out []IncomeEntry
	if err := c.getSigned(ctx, "/fapi/v1/income", params, &out); err != nil {
		return nil, fmt.Errorf("income: %w", err)
	}
	return out, nil
}

// RealizedSummary aggregates a closed position's wallet impact, split into the
// exchange's own income categories. Commission and Funding are typically
// negative (costs); RealizedPnL is the price-difference P&L.
type RealizedSummary struct {
	RealizedPnL  float64
	Commission   float64
	Funding      float64
	RealizedRows int // count of REALIZED_PNL rows summed; 0 => nothing matched
}

// Net is the true wallet impact: realized PnL net of fees and funding.
func (s RealizedSummary) Net() float64 { return s.RealizedPnL + s.Commission + s.Funding }

// RecentRealized sums REALIZED_PNL / COMMISSION / FUNDING_FEE for a symbol
// within [startTimeMs, endTimeMs] — the authoritative wallet impact of a
// just-closed position, replacing any agent-reported PnL guess. A zero
// RealizedRows means the window held no realised P&L (e.g. the close has not
// posted yet, or the window missed it) and the caller should fall back.
func (c *Client) RecentRealized(ctx context.Context, symbol string, startTimeMs, endTimeMs int64) (RealizedSummary, error) {
	rows, err := c.Income(ctx, symbol, startTimeMs, endTimeMs, 1000)
	if err != nil {
		return RealizedSummary{}, err
	}
	return SummarizeRealized(rows), nil
}

// SummarizeRealized folds income rows into a RealizedSummary. Exposed so
// offline tooling (memory reconciliation) can reuse the same accounting.
func SummarizeRealized(rows []IncomeEntry) RealizedSummary {
	var s RealizedSummary
	for _, r := range rows {
		v, err := strconv.ParseFloat(r.Income, 64)
		if err != nil {
			continue
		}
		switch r.IncomeType {
		case "REALIZED_PNL":
			s.RealizedPnL += v
			s.RealizedRows++
		case "COMMISSION":
			s.Commission += v
		case "FUNDING_FEE":
			s.Funding += v
		}
	}
	return s
}
