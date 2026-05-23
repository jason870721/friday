package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/johnny1110/evva/pkg/tools"
	"github.com/johnny1110/friday/internal/binance"
)

const BinanceOrderToolName tools.ToolName = "binance_order"

const binanceOrderDescription = `Place a MARKET order on Binance USDⓈ-M Futures.

side = BUY  → opens or increases a LONG position (or closes a SHORT)
side = SELL → opens or increases a SHORT position (or closes a LONG)

quantity is in the base asset (e.g. 0.002 BTC, 0.1 SOL). Round DOWN to
the symbol's step size before calling. A quantity of 0 will fail.

Optionally pass reduce_only=true to guarantee the order can only close
or reduce an existing position — useful when you want to flatten without
risk of accidentally flipping direction.

Position-sizing formula:
  quantity = (margin_usdt × leverage) / mark_price

Always call binance_leverage first if changing leverage on this symbol.`

const binanceOrderSchema = `{
	"type": "object",
	"additionalProperties": false,
	"required": ["symbol", "side", "quantity"],
	"properties": {
		"symbol":      {"type": "string", "description": "Binance Futures symbol, e.g. BTCUSDT."},
		"side":        {"type": "string", "enum": ["BUY", "SELL"], "description": "BUY = long / close short; SELL = short / close long."},
		"quantity":    {"type": "number", "exclusiveMinimum": 0, "description": "Quantity in base asset, e.g. 0.002. Must respect symbol step size."},
		"reduce_only": {"type": "boolean", "default": false, "description": "If true, order can only reduce/close an existing position."}
	}
}`

type BinanceOrderTool struct{}

func NewBinanceOrder() *BinanceOrderTool { return &BinanceOrderTool{} }

func (BinanceOrderTool) Name() string            { return string(BinanceOrderToolName) }
func (BinanceOrderTool) Description() string     { return binanceOrderDescription }
func (BinanceOrderTool) Schema() json.RawMessage { return json.RawMessage(binanceOrderSchema) }

type binanceOrderInput struct {
	Symbol     string  `json:"symbol"`
	Side       string  `json:"side"`
	Quantity   float64 `json:"quantity"`
	ReduceOnly bool    `json:"reduce_only,omitempty"`
}

func (BinanceOrderTool) Execute(ctx context.Context, logger *slog.Logger, raw json.RawMessage) (tools.Result, error) {
	var in binanceOrderInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("binance_order: decode input: %v", err)}, nil
	}
	if in.Symbol == "" {
		return tools.Result{IsError: true, Content: "binance_order: symbol is required"}, nil
	}
	side := binance.OrderSide(strings.ToUpper(in.Side))
	if side != binance.SideBuy && side != binance.SideSell {
		return tools.Result{IsError: true, Content: fmt.Sprintf("binance_order: side=%q must be BUY or SELL", in.Side)}, nil
	}
	if in.Quantity <= 0 {
		return tools.Result{IsError: true, Content: fmt.Sprintf("binance_order: quantity=%g must be > 0", in.Quantity)}, nil
	}

	cli, err := sharedBinanceClient()
	if err != nil {
		return tools.Result{IsError: true, Content: err.Error()}, nil
	}

	logger.Debug("binance_order.dispatch",
		"symbol", in.Symbol, "side", side, "quantity", in.Quantity, "reduce_only", in.ReduceOnly)

	ord, err := cli.MarketOrder(ctx, in.Symbol, side, in.Quantity, in.ReduceOnly)
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("binance_order: %v", err)}, nil
	}
	return tools.Result{Content: binance.FormatOrder(ord)}, nil
}
