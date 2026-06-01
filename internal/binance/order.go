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

// StopMarketOrder places a server-side STOP_MARKET order (PRD-020 §2) — a
// crash-survivable stop-loss that executes on the exchange even if friday is
// killed, OOMs, or hangs (unlike the in-memory StopMonitor). stopPrice is the
// trigger (mark) price; on trigger a MARKET order of `quantity` fills at
// side. timeInForce=GTC so it survives until explicitly cancelled. reduceOnly
// guarantees it can only flatten, never flip. Used for the stop-loss leg.
func (c *Client) StopMarketOrder(ctx context.Context, symbol string, side OrderSide, quantity, stopPrice float64, reduceOnly bool) (*OrderResponse, error) {
	return c.triggerOrder(ctx, symbol, side, "STOP_MARKET", quantity, stopPrice, reduceOnly)
}

// TakeProfitMarketOrder places a server-side TAKE_PROFIT_MARKET order (PRD-020
// §2) — the profit-side twin of StopMarketOrder. The distinct type is required
// because the trigger fires from the FAVOURABLE side of the mark price (a
// STOP_MARKET on the same side would be rejected with -2021 "would immediately
// trigger"). Used for the take-profit leg.
func (c *Client) TakeProfitMarketOrder(ctx context.Context, symbol string, side OrderSide, quantity, stopPrice float64, reduceOnly bool) (*OrderResponse, error) {
	return c.triggerOrder(ctx, symbol, side, "TAKE_PROFIT_MARKET", quantity, stopPrice, reduceOnly)
}

// triggerOrder is the shared POST for STOP_MARKET / TAKE_PROFIT_MARKET orders.
func (c *Client) triggerOrder(ctx context.Context, symbol string, side OrderSide, orderType string, quantity, stopPrice float64, reduceOnly bool) (*OrderResponse, error) {
	params := url.Values{
		"symbol":      {symbol},
		"side":        {string(side)},
		"type":        {orderType},
		"timeInForce": {"GTC"},
		"quantity":    {strconv.FormatFloat(quantity, 'f', -1, 64)},
		"stopPrice":   {strconv.FormatFloat(stopPrice, 'f', -1, 64)},
		"workingType": {"MARK_PRICE"},
	}
	if reduceOnly {
		params.Set("reduceOnly", "true")
	}
	var out OrderResponse
	if err := c.postSigned(ctx, "/fapi/v1/order", params, &out); err != nil {
		return nil, fmt.Errorf("%s: %w", strings.ToLower(orderType), err)
	}
	return &out, nil
}

// CancelOrder cancels a single open order by ID on a symbol (PRD-020 §2) —
// used to clear a native STOP_MARKET / TAKE_PROFIT_MARKET once its position is
// closed or replaced.
func (c *Client) CancelOrder(ctx context.Context, symbol string, orderID int64) error {
	params := url.Values{
		"symbol":  {symbol},
		"orderId": {strconv.FormatInt(orderID, 10)},
	}
	if err := c.do(ctx, "DELETE", "/fapi/v1/order", params, true, nil); err != nil {
		return fmt.Errorf("cancelOrder %s #%d: %w", symbol, orderID, err)
	}
	return nil
}

// OpenOrder is one row of the open-orders endpoint — the fields friday needs to
// reconcile orphaned native stops at startup (PRD-020 §2 R5).
type OpenOrder struct {
	OrderID       int64  `json:"orderId"`
	Symbol        string `json:"symbol"`
	Type          string `json:"type"` // STOP_MARKET, TAKE_PROFIT_MARKET, LIMIT, …
	Side          string `json:"side"`
	StopPrice     string `json:"stopPrice"`
	ReduceOnly    bool   `json:"reduceOnly"`
	ClosePosition bool   `json:"closePosition"`
}

// OpenOrders lists the account's currently-open (unfilled) orders. When symbol
// is "" every symbol's open orders are returned (the form startup orphan
// discovery uses); otherwise only the given symbol's.
func (c *Client) OpenOrders(ctx context.Context, symbol string) ([]OpenOrder, error) {
	params := url.Values{}
	if symbol != "" {
		params.Set("symbol", symbol)
	}
	var out []OpenOrder
	if err := c.getSigned(ctx, "/fapi/v1/openOrders", params, &out); err != nil {
		return nil, fmt.Errorf("openOrders: %w", err)
	}
	return out, nil
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
