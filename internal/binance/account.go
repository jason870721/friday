package binance

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
)

// BalanceEntry is one row of the balance endpoint — one per asset.
type BalanceEntry struct {
	AccountAlias       string `json:"accountAlias"`
	Asset              string `json:"asset"`
	Balance            string `json:"balance"`
	CrossWalletBalance string `json:"crossWalletBalance"`
	CrossUnPnl         string `json:"crossUnPnl"`
	AvailableBalance   string `json:"availableBalance"`
	MaxWithdrawAmount  string `json:"maxWithdrawAmount"`
}

// Balances returns every asset balance on the futures account.
func (c *Client) Balances(ctx context.Context) ([]BalanceEntry, error) {
	var out []BalanceEntry
	if err := c.getSigned(ctx, "/fapi/v2/balance", nil, &out); err != nil {
		return nil, fmt.Errorf("balance: %w", err)
	}
	return out, nil
}

// USDTBalance is the convenience accessor for the USDT row — the only
// asset friday's experiment cares about.
func (c *Client) USDTBalance(ctx context.Context) (*BalanceEntry, error) {
	bs, err := c.Balances(ctx)
	if err != nil {
		return nil, err
	}
	for i := range bs {
		if bs[i].Asset == "USDT" {
			return &bs[i], nil
		}
	}
	return nil, fmt.Errorf("balance: USDT not present in account")
}

// PositionEntry is one row from /fapi/v2/positionRisk. Binance returns
// a row per symbol whether or not a position is open — callers must
// filter on PositionAmt != 0 when they only want active positions.
type PositionEntry struct {
	Symbol           string `json:"symbol"`
	PositionAmt      string `json:"positionAmt"`
	EntryPrice       string `json:"entryPrice"`
	MarkPrice        string `json:"markPrice"`
	UnRealizedProfit string `json:"unRealizedProfit"`
	LiquidationPrice string `json:"liquidationPrice"`
	Leverage         string `json:"leverage"`
	PositionSide     string `json:"positionSide"`
}

// Positions returns the position-risk rows. When symbol is "", every
// symbol on the account is returned; otherwise only the matching row.
func (c *Client) Positions(ctx context.Context, symbol string) ([]PositionEntry, error) {
	params := url.Values{}
	if symbol != "" {
		params.Set("symbol", symbol)
	}
	var out []PositionEntry
	if err := c.getSigned(ctx, "/fapi/v2/positionRisk", params, &out); err != nil {
		return nil, fmt.Errorf("positions: %w", err)
	}
	return out, nil
}

// OpenPositions returns only the position rows with a non-zero size.
func (c *Client) OpenPositions(ctx context.Context) ([]PositionEntry, error) {
	all, err := c.Positions(ctx, "")
	if err != nil {
		return nil, err
	}
	open := make([]PositionEntry, 0, len(all))
	for _, p := range all {
		amt, _ := strconv.ParseFloat(p.PositionAmt, 64)
		if amt != 0 {
			open = append(open, p)
		}
	}
	return open, nil
}
