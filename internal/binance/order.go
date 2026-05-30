package binance

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// OrderSide is "BUY" or "SELL". BUY opens a long (or closes a short);
// SELL opens a short (or closes a long).
type OrderSide string

const (
	SideBuy  OrderSide = "BUY"
	SideSell OrderSide = "SELL"
)

// LeverageResponse echoes back the new leverage setting.
type LeverageResponse struct {
	Symbol           string `json:"symbol"`
	Leverage         int    `json:"leverage"`
	MaxNotionalValue string `json:"maxNotionalValue"`
}

// SetLeverage configures leverage for a symbol. Must be called before
// opening a position when changing from the default.
func (c *Client) SetLeverage(ctx context.Context, symbol string, leverage int) (*LeverageResponse, error) {
	params := url.Values{
		"symbol":   {symbol},
		"leverage": {strconv.Itoa(leverage)},
	}
	var out LeverageResponse
	if err := c.postSigned(ctx, "/fapi/v1/leverage", params, &out); err != nil {
		return nil, fmt.Errorf("setLeverage: %w", err)
	}
	return &out, nil
}

// OrderResponse is the relevant subset of Binance's order-ack payload.
type OrderResponse struct {
	OrderID       int64  `json:"orderId"`
	Symbol        string `json:"symbol"`
	Status        string `json:"status"`
	ClientOrderID string `json:"clientOrderId"`
	Side          string `json:"side"`
	Type          string `json:"type"`
	OrigQty       string `json:"origQty"`
	ExecutedQty   string `json:"executedQty"`
	AvgPrice      string `json:"avgPrice"`
	ReduceOnly    bool   `json:"reduceOnly"`
}

// MarketOrder places a MARKET order. quantity is the contract size in
// the base asset (e.g. 0.002 BTC). reduceOnly = true means the order can
// only reduce an existing position (used for closes).
func (c *Client) MarketOrder(ctx context.Context, symbol string, side OrderSide, quantity float64, reduceOnly bool) (*OrderResponse, error) {
	params := url.Values{
		"symbol":   {symbol},
		"side":     {string(side)},
		"type":     {"MARKET"},
		"quantity": {strconv.FormatFloat(quantity, 'f', -1, 64)},
	}
	if reduceOnly {
		params.Set("reduceOnly", "true")
	}
	var out OrderResponse
	if err := c.postSigned(ctx, "/fapi/v1/order", params, &out); err != nil {
		return nil, fmt.Errorf("marketOrder: %w", err)
	}
	return &out, nil
}

// CloseAllPositions cancels every open order on the account and then
// places a reduce-only market order to flatten each non-zero position.
// Returns a slice of human-readable lines describing what happened.
func (c *Client) CloseAllPositions(ctx context.Context) ([]string, error) {
	var lines []string

	// 1. Query active positions first so we know which symbols matter.
	positions, err := c.Positions(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("closeAll: list positions: %w", err)
	}

	// 2. For each symbol with a non-zero position, cancel that symbol's
	//    open orders. Binance's deleteAllOpenOrders is per-symbol.
	for _, p := range positions {
		amt, _ := strconv.ParseFloat(p.PositionAmt, 64)
		if amt == 0 {
			continue
		}
		if err := c.cancelAllOpenOrders(ctx, p.Symbol); err != nil {
			lines = append(lines, fmt.Sprintf("%s: cancel orders failed: %v", p.Symbol, err))
		} else {
			lines = append(lines, fmt.Sprintf("%s: cancelled open orders", p.Symbol))
		}
	}

	// 3. Flatten each open position with a reduce-only market order.
	for _, p := range positions {
		amt, _ := strconv.ParseFloat(p.PositionAmt, 64)
		if amt == 0 {
			continue
		}
		side := SideSell
		qty := amt
		if amt < 0 {
			side = SideBuy
			qty = -amt
		}
		ord, err := c.MarketOrder(ctx, p.Symbol, side, qty, true)
		if err != nil {
			lines = append(lines, fmt.Sprintf("%s: close failed: %v", p.Symbol, err))
			continue
		}
		lines = append(lines, fmt.Sprintf("%s: closed %s %s @ avg %s (orderId=%d)",
			p.Symbol, ord.Side, ord.ExecutedQty, ord.AvgPrice, ord.OrderID))
	}

	if len(lines) == 0 {
		lines = append(lines, "no open positions to close")
	}
	return lines, nil
}

func (c *Client) cancelAllOpenOrders(ctx context.Context, symbol string) error {
	params := url.Values{"symbol": {symbol}}
	return c.do(ctx, "DELETE", "/fapi/v1/allOpenOrders", params, true, nil)
}

// FormatOrder returns a single-line summary of an order response,
// useful for tool-result content.
func FormatOrder(o *OrderResponse) string {
	var b strings.Builder
	// A just-acked MARKET order frequently reports executedQty=0 because
	// the fill settles asynchronously — showing "qty=0" then misleads the
	// reader (and the agent) into thinking nothing traded. Fall back to the
	// requested origQty so the line reflects the order size; the status
	// field already distinguishes NEW from FILLED.
	qty := o.ExecutedQty
	if isZeroQty(qty) && !isZeroQty(o.OrigQty) {
		qty = o.OrigQty
	}
	fmt.Fprintf(&b, "%s %s %s qty=%s", o.Symbol, o.Side, o.Type, qty)
	if o.AvgPrice != "" && o.AvgPrice != "0" && o.AvgPrice != "0.00" {
		fmt.Fprintf(&b, " @ %s", o.AvgPrice)
	}
	fmt.Fprintf(&b, " status=%s orderId=%d", o.Status, o.OrderID)
	if o.ReduceOnly {
		b.WriteString(" reduceOnly")
	}
	return b.String()
}

// isZeroQty reports whether a Binance quantity string is empty or
// numerically zero (e.g. "", "0", "0.000").
func isZeroQty(s string) bool {
	if s == "" {
		return true
	}
	f, err := strconv.ParseFloat(s, 64)
	return err == nil && f == 0
}
