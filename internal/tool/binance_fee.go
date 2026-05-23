package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/johnny1110/evva/pkg/tools"
)

const BinanceFeeToolName tools.ToolName = "binance_fee"

const binanceFeeDescription = `Get the account's maker and taker commission rates for a symbol.

Rates are account-specific (VIP tier, BNB discount, referral) — call this
before sizing any trade. Returned as decimal strings; multiply by 100 to
get the percentage. A taker rate of "0.0004" = 0.04% = 4 bps per fill.

Round-trip cost on a MARKET-in/MARKET-out trade is roughly:
  2 × takerRate × position_value

Use this to compute breakeven before entering — if expected move doesn't
clear ~3× the round-trip fee, skip the trade.`

const binanceFeeSchema = `{
	"type": "object",
	"additionalProperties": false,
	"required": ["symbol"],
	"properties": {
		"symbol": {"type": "string", "description": "Binance Futures symbol, e.g. BTCUSDT."}
	}
}`

type BinanceFeeTool struct{}

func NewBinanceFee() *BinanceFeeTool { return &BinanceFeeTool{} }

func (BinanceFeeTool) Name() string            { return string(BinanceFeeToolName) }
func (BinanceFeeTool) Description() string     { return binanceFeeDescription }
func (BinanceFeeTool) Schema() json.RawMessage { return json.RawMessage(binanceFeeSchema) }

type binanceFeeInput struct {
	Symbol string `json:"symbol"`
}

func (BinanceFeeTool) Execute(ctx context.Context, logger *slog.Logger, raw json.RawMessage) (tools.Result, error) {
	var in binanceFeeInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("binance_fee: decode input: %v", err)}, nil
	}
	if in.Symbol == "" {
		return tools.Result{IsError: true, Content: "binance_fee: symbol is required"}, nil
	}

	cli, err := sharedBinanceClient()
	if err != nil {
		return tools.Result{IsError: true, Content: err.Error()}, nil
	}

	logger.Debug("binance_fee.dispatch", "symbol", in.Symbol)

	r, err := cli.CommissionRate(ctx, in.Symbol)
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("binance_fee: %v", err)}, nil
	}

	maker, _ := strconv.ParseFloat(r.MakerCommissionRate, 64)
	taker, _ := strconv.ParseFloat(r.TakerCommissionRate, 64)
	content := fmt.Sprintf(
		"%s maker=%s (%.4f%%) taker=%s (%.4f%%) round-trip≈%.4f%%",
		r.Symbol, r.MakerCommissionRate, maker*100, r.TakerCommissionRate, taker*100, taker*2*100,
	)
	return tools.Result{Content: content}, nil
}
