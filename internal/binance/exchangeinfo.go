package binance

import (
	"context"
	"fmt"
)

// SymbolInfo is the subset of /fapi/v1/exchangeInfo friday needs: a symbol's
// tradability status and its LOT_SIZE step (the quantity increment orders must
// round to).
type SymbolInfo struct {
	Symbol   string
	Status   string // "TRADING", "PENDING_TRADING", "SETTLING", "CLOSE", ...
	StepSize string // from the LOT_SIZE filter, e.g. "0.001"
}

// exchangeInfoResponse mirrors only the fields we read from the (large)
// exchangeInfo payload.
type exchangeInfoResponse struct {
	Symbols []struct {
		Symbol  string `json:"symbol"`
		Status  string `json:"status"`
		Filters []struct {
			FilterType string `json:"filterType"`
			StepSize   string `json:"stepSize"`
		} `json:"filters"`
	} `json:"symbols"`
}

// ExchangeInfo returns the per-symbol trading rules the venue publishes. It is
// an UNSIGNED public endpoint, so it works without API credentials — friday
// calls it once at startup to validate the configured symbol list against what
// the endpoint (testnet or mainnet) actually lists, rather than discovering an
// "invalid symbol" mid-round on every cycle.
func (c *Client) ExchangeInfo(ctx context.Context) ([]SymbolInfo, error) {
	var resp exchangeInfoResponse
	if err := c.get(ctx, "/fapi/v1/exchangeInfo", nil, &resp); err != nil {
		return nil, fmt.Errorf("exchangeInfo: %w", err)
	}
	out := make([]SymbolInfo, 0, len(resp.Symbols))
	for _, s := range resp.Symbols {
		info := SymbolInfo{Symbol: s.Symbol, Status: s.Status}
		for _, f := range s.Filters {
			if f.FilterType == "LOT_SIZE" {
				info.StepSize = f.StepSize
				break
			}
		}
		out = append(out, info)
	}
	return out, nil
}
