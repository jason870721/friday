package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/johnny1110/evva/pkg/tools"
)

const BinanceTickerToolName tools.ToolName = "binance_ticker"

const binanceTickerDescription = `Get 24-hour rolling-window statistics for a Binance Futures symbol.

Returns priceChangePercent, high, low, last price, and volume. Use this for
quick context beyond raw price:
- priceChangePercent → trending vs ranging today
- high / low → where current price sits in today's range
- volume / quoteVolume → real participation vs thin tape

Call in parallel for multiple symbols.`

const binanceTickerSchema = `{
	"type": "object",
	"additionalProperties": false,
	"required": ["symbol"],
	"properties": {
		"symbol": {"type": "string", "description": "Binance Futures symbol, e.g. BTCUSDT."}
	}
}`

type BinanceTickerTool struct{}

func NewBinanceTicker() *BinanceTickerTool { return &BinanceTickerTool{} }

func (BinanceTickerTool) Name() string            { return string(BinanceTickerToolName) }
func (BinanceTickerTool) Description() string     { return binanceTickerDescription }
func (BinanceTickerTool) Schema() json.RawMessage { return json.RawMessage(binanceTickerSchema) }

type binanceTickerInput struct {
	Symbol string `json:"symbol"`
}

func (BinanceTickerTool) Execute(ctx context.Context, logger *slog.Logger, raw json.RawMessage) (tools.Result, error) {
	var in binanceTickerInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("binance_ticker: decode input: %v", err)}, nil
	}
	if in.Symbol == "" {
		return tools.Result{IsError: true, Content: "binance_ticker: symbol is required"}, nil
	}

	cli, err := sharedBinanceClient()
	if err != nil {
		return tools.Result{IsError: true, Content: err.Error()}, nil
	}

	logger.Debug("binance_ticker.dispatch", "symbol", in.Symbol)

	t, err := cli.Ticker24hr(ctx, in.Symbol)
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("binance_ticker: %v", err)}, nil
	}
	content := fmt.Sprintf(
		"%s 24h: last=%s change=%s%% high=%s low=%s vol=%s quoteVol=%s",
		t.Symbol, t.LastPrice, t.PriceChangePercent, t.HighPrice, t.LowPrice, t.Volume, t.QuoteVolume,
	)
	return tools.Result{Content: content}, nil
}
