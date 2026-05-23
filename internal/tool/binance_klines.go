package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/johnny1110/evva/pkg/tools"
)

const BinanceKlinesToolName tools.ToolName = "binance_klines"

const binanceKlinesDescription = `Get recent candlestick (OHLCV) data for a Binance Futures symbol.

Returns up to 'limit' candles for the given interval, formatted as one
candle per line: time | open | high | low | close | volume.

Typical use:
- interval=5m, limit=20 — last 100 minutes for a quick trend read
- interval=1h, limit=24 — last day for higher-timeframe context

Intervals: 1m, 3m, 5m, 15m, 30m, 1h, 2h, 4h, 6h, 8h, 12h, 1d.`

const binanceKlinesSchema = `{
	"type": "object",
	"additionalProperties": false,
	"required": ["symbol", "interval"],
	"properties": {
		"symbol":   {"type": "string", "description": "Binance Futures symbol, e.g. BTCUSDT."},
		"interval": {"type": "string", "description": "Candle interval: 1m, 5m, 15m, 1h, 4h, 1d, etc."},
		"limit":    {"type": "integer", "minimum": 1, "maximum": 500, "default": 20, "description": "Number of candles (1-500). Default 20."}
	}
}`

type BinanceKlinesTool struct{}

func NewBinanceKlines() *BinanceKlinesTool { return &BinanceKlinesTool{} }

func (BinanceKlinesTool) Name() string            { return string(BinanceKlinesToolName) }
func (BinanceKlinesTool) Description() string     { return binanceKlinesDescription }
func (BinanceKlinesTool) Schema() json.RawMessage { return json.RawMessage(binanceKlinesSchema) }

type binanceKlinesInput struct {
	Symbol   string `json:"symbol"`
	Interval string `json:"interval"`
	Limit    *int   `json:"limit,omitempty"`
}

func (BinanceKlinesTool) Execute(ctx context.Context, logger *slog.Logger, raw json.RawMessage) (tools.Result, error) {
	var in binanceKlinesInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("binance_klines: decode input: %v", err)}, nil
	}
	if in.Symbol == "" || in.Interval == "" {
		return tools.Result{IsError: true, Content: "binance_klines: symbol and interval are required"}, nil
	}
	limit := 20
	if in.Limit != nil {
		limit = *in.Limit
	}
	if limit < 1 || limit > 500 {
		return tools.Result{IsError: true, Content: fmt.Sprintf("binance_klines: limit=%d out of range [1,500]", limit)}, nil
	}

	cli, err := sharedBinanceClient()
	if err != nil {
		return tools.Result{IsError: true, Content: err.Error()}, nil
	}

	logger.Debug("binance_klines.dispatch", "symbol", in.Symbol, "interval", in.Interval, "limit", limit)

	ks, err := cli.Klines(ctx, in.Symbol, in.Interval, limit)
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("binance_klines: %v", err)}, nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s %s (%d candles)\n", in.Symbol, in.Interval, len(ks))
	fmt.Fprintln(&b, "openTime | open | high | low | close | volume")
	for _, k := range ks {
		fmt.Fprintf(&b, "%d | %.4f | %.4f | %.4f | %.4f | %.4f\n",
			k.OpenTime, k.Open, k.High, k.Low, k.Close, k.Volume)
	}
	return tools.Result{Content: b.String()}, nil
}
