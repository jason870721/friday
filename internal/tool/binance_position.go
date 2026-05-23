package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/johnny1110/evva/pkg/tools"
	"github.com/johnny1110/friday/internal/binance"
)

const BinancePositionToolName tools.ToolName = "binance_position"

const binancePositionDescription = `Get current open position(s) on Binance Futures.

Without a symbol: returns every position with non-zero size across the
account (one line per symbol).
With a symbol: returns just that symbol's row (including a "no position"
indicator if size is zero).

Each row shows:
- direction (LONG/SHORT) and size in base asset
- entry price, current mark price
- unrealized PnL in USDT
- liquidation price
- leverage

When closing a position with binance_order, use the absolute value of
positionAmt from here as the order quantity — partial closes leave dust.`

const binancePositionSchema = `{
	"type": "object",
	"additionalProperties": false,
	"properties": {
		"symbol": {"type": "string", "description": "Optional Binance Futures symbol. If omitted, returns all open positions."}
	}
}`

type BinancePositionTool struct{}

func NewBinancePosition() *BinancePositionTool { return &BinancePositionTool{} }

func (BinancePositionTool) Name() string            { return string(BinancePositionToolName) }
func (BinancePositionTool) Description() string     { return binancePositionDescription }
func (BinancePositionTool) Schema() json.RawMessage { return json.RawMessage(binancePositionSchema) }

type binancePositionInput struct {
	Symbol string `json:"symbol,omitempty"`
}

func (BinancePositionTool) Execute(ctx context.Context, logger *slog.Logger, raw json.RawMessage) (tools.Result, error) {
	var in binancePositionInput
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return tools.Result{IsError: true, Content: fmt.Sprintf("binance_position: decode input: %v", err)}, nil
		}
	}

	cli, err := sharedBinanceClient()
	if err != nil {
		return tools.Result{IsError: true, Content: err.Error()}, nil
	}

	logger.Debug("binance_position.dispatch", "symbol", in.Symbol)

	rows, err := cli.Positions(ctx, in.Symbol)
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("binance_position: %v", err)}, nil
	}

	var b strings.Builder
	if in.Symbol != "" {
		// Single-symbol query: if no row or zero size, say so explicitly.
		if len(rows) == 0 {
			return tools.Result{Content: fmt.Sprintf("%s: no position", in.Symbol)}, nil
		}
		p := rows[0]
		amt, _ := strconv.ParseFloat(p.PositionAmt, 64)
		if amt == 0 {
			return tools.Result{Content: fmt.Sprintf("%s: no position", p.Symbol)}, nil
		}
		b.WriteString(formatPositionLine(p, amt))
		return tools.Result{Content: b.String()}, nil
	}

	// All-symbols query: filter to non-zero rows.
	any := false
	for _, p := range rows {
		amt, _ := strconv.ParseFloat(p.PositionAmt, 64)
		if amt == 0 {
			continue
		}
		if any {
			b.WriteString("\n")
		}
		b.WriteString(formatPositionLine(p, amt))
		any = true
	}
	if !any {
		return tools.Result{Content: "no open positions"}, nil
	}
	return tools.Result{Content: b.String()}, nil
}

func formatPositionLine(p binance.PositionEntry, amt float64) string {
	dir := "LONG"
	size := amt
	if amt < 0 {
		dir = "SHORT"
		size = -amt
	}
	return fmt.Sprintf(
		"%s %s size=%g entry=%s mark=%s pnl=%s liq=%s lev=%sx",
		p.Symbol, dir, size, p.EntryPrice, p.MarkPrice,
		p.UnRealizedProfit, p.LiquidationPrice, p.Leverage,
	)
}
