package binance

import (
	"context"
	"fmt"
	"net/url"
	"sort"
)

// leverageBracketEntry mirrors one symbol's notional leverage brackets from
// /fapi/v1/leverageBracket. We read initialLeverage and notionalCap — the two
// fields that together describe "the most leverage allowed for a position of
// this notional size".
type leverageBracketEntry struct {
	Symbol   string `json:"symbol"`
	Brackets []struct {
		InitialLeverage int     `json:"initialLeverage"`
		NotionalCap     float64 `json:"notionalCap"`
	} `json:"brackets"`
}

// LeverageBracket is one notional tier for a symbol: a position whose notional
// is at or below NotionalCap may use up to InitialLeverage. Brackets are
// nested/contiguous (tier 1 covers [0, cap1] at the HIGHEST leverage, tier 2
// covers [cap1, cap2] at a lower leverage, …), so the highest leverage is only
// available for the SMALLEST notional. Asking for more leverage than the tier a
// notional falls into permits is what Binance rejects with code -2027
// ("Exceeded the maximum allowable position at current leverage").
type LeverageBracket struct {
	NotionalCap     float64
	InitialLeverage int
}

// LeverageBrackets returns each symbol's full notional→leverage tier table
// (/fapi/v1/leverageBracket — SIGNED), sorted ascending by NotionalCap. This is
// the data the per-order leverage clamp (PRD-019) needs to keep a position's
// notional inside the tier its leverage allows, so an order never trips -2027.
func (c *Client) LeverageBrackets(ctx context.Context) (map[string][]LeverageBracket, error) {
	var raw []leverageBracketEntry
	if err := c.getSigned(ctx, "/fapi/v1/leverageBracket", url.Values{}, &raw); err != nil {
		return nil, fmt.Errorf("leverageBracket: %w", err)
	}
	out := make(map[string][]LeverageBracket, len(raw))
	for _, e := range raw {
		if len(e.Brackets) == 0 {
			continue
		}
		bs := make([]LeverageBracket, 0, len(e.Brackets))
		for _, b := range e.Brackets {
			bs = append(bs, LeverageBracket{NotionalCap: b.NotionalCap, InitialLeverage: b.InitialLeverage})
		}
		sort.Slice(bs, func(i, j int) bool { return bs[i].NotionalCap < bs[j].NotionalCap })
		out[e.Symbol] = bs
	}
	return out, nil
}

// MaxLeverages returns the maximum allowed leverage per symbol, derived from
// the bracket table — the highest initialLeverage across a symbol's tiers (e.g.
// BTCUSDT 125, a TradFi stock perp 10). Symbols requesting leverage above their
// max are rejected by Binance with code -4028. NOTE: this max is only usable for
// the smallest notional tier; for larger positions the usable leverage is lower
// (see LeverageBrackets / MaxLeverageForNotional).
func (c *Client) MaxLeverages(ctx context.Context) (map[string]int, error) {
	brackets, err := c.LeverageBrackets(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]int, len(brackets))
	for sym, bs := range brackets {
		mx := 0
		for _, b := range bs {
			if b.InitialLeverage > mx {
				mx = b.InitialLeverage
			}
		}
		if mx > 0 {
			out[sym] = mx
		}
	}
	return out, nil
}

// MaxLeverageForNotional returns the highest leverage permitted for a position
// of the given notional, given a symbol's bracket table (ascending by cap): the
// initialLeverage of the smallest tier whose cap still covers the notional. If
// the notional exceeds every tier's cap, the lowest-leverage (largest) tier is
// returned as the best-effort floor. Returns (0,false) when the table is empty.
func MaxLeverageForNotional(brackets []LeverageBracket, notional float64) (int, bool) {
	if len(brackets) == 0 {
		return 0, false
	}
	for _, b := range brackets {
		if notional <= b.NotionalCap {
			return b.InitialLeverage, true
		}
	}
	return brackets[len(brackets)-1].InitialLeverage, true
}
