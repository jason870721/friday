package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/johnny1110/evva/pkg/tools"
)

const BinanceBalanceToolName tools.ToolName = "binance_balance"

const binanceBalanceDescription = `Get USDT wallet balance on Binance USDⓈ-M Futures.

Returns:
- balance: total wallet balance (USDT)
- availableBalance: free USDT available for new margin
- crossWalletBalance: cross-margin wallet
- crossUnPnl: total unrealized PnL across all open positions

Use on the first round to confirm starting capital, and any time you want
to verify available margin before opening a position.`

const binanceBalanceSchema = `{
	"type": "object",
	"additionalProperties": false,
	"properties": {}
}`

type BinanceBalanceTool struct{}

func NewBinanceBalance() *BinanceBalanceTool { return &BinanceBalanceTool{} }

func (BinanceBalanceTool) Name() string            { return string(BinanceBalanceToolName) }
func (BinanceBalanceTool) Description() string     { return binanceBalanceDescription }
func (BinanceBalanceTool) Schema() json.RawMessage { return json.RawMessage(binanceBalanceSchema) }

func (BinanceBalanceTool) Execute(ctx context.Context, logger *slog.Logger, _ json.RawMessage) (tools.Result, error) {
	cli, err := sharedBinanceClient()
	if err != nil {
		return tools.Result{IsError: true, Content: err.Error()}, nil
	}

	logger.Debug("binance_balance.dispatch")

	b, err := cli.USDTBalance(ctx)
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("binance_balance: %v", err)}, nil
	}
	content := fmt.Sprintf(
		"USDT balance=%s available=%s crossWallet=%s crossUnPnl=%s",
		b.Balance, b.AvailableBalance, b.CrossWalletBalance, b.CrossUnPnl,
	)
	return tools.Result{Content: content}, nil
}
