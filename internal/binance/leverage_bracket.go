package binance

import (
	"context"
	"fmt"
	"net/url"
)

// leverageBracketEntry mirrors one symbol's notional leverage brackets from
// /fapi/v1/leverageBracket. We only read initialLeverage.
type leverageBracketEntry struct {
	Symbol   string `json:"symbol"`
	Brackets []struct {
		InitialLeverage int `json:"initialLeverage"`
	} `json:"brackets"`
}

// MaxLeverages returns the maximum allowed leverage per symbol, derived from
// the account's leverage brackets (/fapi/v1/leverageBracket — SIGNED). A
// symbol's max is the highest initialLeverage across its notional brackets
// (e.g. BTCUSDT 125, a TradFi stock perp 10). Symbols requesting leverage above
// their max are rejected by Binance with code -4028.
func (c *Client) MaxLeverages(ctx context.Context) (map[string]int, error) {
	var raw []leverageBracketEntry
	if err := c.getSigned(ctx, "/fapi/v1/leverageBracket", url.Values{}, &raw); err != nil {
		return nil, fmt.Errorf("leverageBracket: %w", err)
	}
	out := make(map[string]int, len(raw))
	for _, e := range raw {
		mx := 0
		for _, b := range e.Brackets {
			if b.InitialLeverage > mx {
				mx = b.InitialLeverage
			}
		}
		if mx > 0 {
			out[e.Symbol] = mx
		}
	}
	return out, nil
}
