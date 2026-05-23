package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/johnny1110/evva/pkg/tools"
)

const BinanceLeverageToolName tools.ToolName = "binance_leverage"

const binanceLeverageDescription = `Set leverage for a Binance Futures symbol.

Call before opening a position when you want a leverage different from
your current setting on that symbol. Setting leverage on a symbol with
an open position changes the margin requirement immediately — be careful.

Range: 1x to 125x depending on symbol. For this experiment, stay 1x-100x.
Higher leverage tightens the liquidation distance proportionally.`

const binanceLeverageSchema = `{
	"type": "object",
	"additionalProperties": false,
	"required": ["symbol", "leverage"],
	"properties": {
		"symbol":   {"type": "string", "description": "Binance Futures symbol, e.g. BTCUSDT."},
		"leverage": {"type": "integer", "minimum": 1, "maximum": 125, "description": "Leverage multiplier (1-125). For this experiment, 1-100."}
	}
}`

type BinanceLeverageTool struct{}

func NewBinanceLeverage() *BinanceLeverageTool { return &BinanceLeverageTool{} }

func (BinanceLeverageTool) Name() string            { return string(BinanceLeverageToolName) }
func (BinanceLeverageTool) Description() string     { return binanceLeverageDescription }
func (BinanceLeverageTool) Schema() json.RawMessage { return json.RawMessage(binanceLeverageSchema) }

type binanceLeverageInput struct {
	Symbol   string `json:"symbol"`
	Leverage int    `json:"leverage"`
}

func (BinanceLeverageTool) Execute(ctx context.Context, logger *slog.Logger, raw json.RawMessage) (tools.Result, error) {
	var in binanceLeverageInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("binance_leverage: decode input: %v", err)}, nil
	}
	if in.Symbol == "" {
		return tools.Result{IsError: true, Content: "binance_leverage: symbol is required"}, nil
	}
	if in.Leverage < 1 || in.Leverage > 125 {
		return tools.Result{IsError: true, Content: fmt.Sprintf("binance_leverage: leverage=%d out of range [1,125]", in.Leverage)}, nil
	}

	cli, err := sharedBinanceClient()
	if err != nil {
		return tools.Result{IsError: true, Content: err.Error()}, nil
	}

	logger.Debug("binance_leverage.dispatch", "symbol", in.Symbol, "leverage", in.Leverage)

	r, err := cli.SetLeverage(ctx, in.Symbol, in.Leverage)
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("binance_leverage: %v", err)}, nil
	}
	return tools.Result{Content: fmt.Sprintf("%s leverage set to %dx (maxNotional=%s)", r.Symbol, r.Leverage, r.MaxNotionalValue)}, nil
}
