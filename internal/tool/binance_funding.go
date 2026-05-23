package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/johnny1110/evva/pkg/tools"
)

const BinanceFundingToolName tools.ToolName = "binance_funding"

const binanceFundingDescription = `Get the most recent funding rate for a Binance Futures symbol.

Funding is paid periodically (every 8h) between longs and shorts. The rate
is a useful sentiment / cost-of-carry signal:
- Strongly positive → too many longs; longs pay shorts; expensive to hold long
- Strongly negative → too many shorts; shorts pay longs; expensive to hold short

A value like "0.0001" means 0.01%. Returns the rate and the funding
timestamp it applied to.`

const binanceFundingSchema = `{
	"type": "object",
	"additionalProperties": false,
	"required": ["symbol"],
	"properties": {
		"symbol": {"type": "string", "description": "Binance Futures symbol, e.g. BTCUSDT."}
	}
}`

type BinanceFundingTool struct{}

func NewBinanceFunding() *BinanceFundingTool { return &BinanceFundingTool{} }

func (BinanceFundingTool) Name() string            { return string(BinanceFundingToolName) }
func (BinanceFundingTool) Description() string     { return binanceFundingDescription }
func (BinanceFundingTool) Schema() json.RawMessage { return json.RawMessage(binanceFundingSchema) }

type binanceFundingInput struct {
	Symbol string `json:"symbol"`
}

func (BinanceFundingTool) Execute(ctx context.Context, logger *slog.Logger, raw json.RawMessage) (tools.Result, error) {
	var in binanceFundingInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("binance_funding: decode input: %v", err)}, nil
	}
	if in.Symbol == "" {
		return tools.Result{IsError: true, Content: "binance_funding: symbol is required"}, nil
	}

	cli, err := sharedBinanceClient()
	if err != nil {
		return tools.Result{IsError: true, Content: err.Error()}, nil
	}

	logger.Debug("binance_funding.dispatch", "symbol", in.Symbol)

	fr, err := cli.FundingRate(ctx, in.Symbol)
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("binance_funding: %v", err)}, nil
	}
	ts := time.UnixMilli(fr.FundingTime).UTC().Format(time.RFC3339)
	return tools.Result{Content: fmt.Sprintf("%s fundingRate=%s at=%s", fr.Symbol, fr.FundingRate, ts)}, nil
}
