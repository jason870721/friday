package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/johnny1110/evva/pkg/tools"
)

const BinancePriceToolName tools.ToolName = "binance_price"

const binancePriceDescription = `Get the current mark price for a Binance USDⓈ-M Futures symbol.

Returns the symbol and its current mark price. Use this to read the latest
price before sizing a position or computing PnL. The mark price (not last
trade price) is what funding and liquidation calculations key on.

Call in parallel for multiple symbols when surveying the market.`

const binancePriceSchema = `{
	"type": "object",
	"additionalProperties": false,
	"required": ["symbol"],
	"properties": {
		"symbol": {"type": "string", "description": "Binance Futures symbol, e.g. BTCUSDT, ETHUSDT, SOLUSDT."}
	}
}`

type BinancePriceTool struct{}

func NewBinancePrice() *BinancePriceTool { return &BinancePriceTool{} }

func (BinancePriceTool) Name() string                { return string(BinancePriceToolName) }
func (BinancePriceTool) Description() string         { return binancePriceDescription }
func (BinancePriceTool) Schema() json.RawMessage     { return json.RawMessage(binancePriceSchema) }

type binancePriceInput struct {
	Symbol string `json:"symbol"`
}

func (BinancePriceTool) Execute(ctx context.Context, logger *slog.Logger, raw json.RawMessage) (tools.Result, error) {
	var in binancePriceInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("binance_price: decode input: %v", err)}, nil
	}
	if in.Symbol == "" {
		return tools.Result{IsError: true, Content: "binance_price: symbol is required"}, nil
	}

	cli, err := sharedBinanceClient()
	if err != nil {
		return tools.Result{IsError: true, Content: err.Error()}, nil
	}

	logger.Debug("binance_price.dispatch", "symbol", in.Symbol)

	mp, err := cli.Price(ctx, in.Symbol)
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("binance_price: %v", err)}, nil
	}
	return tools.Result{Content: fmt.Sprintf("%s mark=%s", mp.Symbol, mp.MarkPrice)}, nil
}
