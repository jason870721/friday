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

const BinanceCloseAllToolName tools.ToolName = "binance_close_all"

const binanceCloseAllDescription = `EMERGENCY: cancel every open order and flatten every open position immediately.

Internally:
  1. List all positions across all symbols
  2. Cancel any open orders on symbols with non-zero positions
  3. Place a reduce-only MARKET order to close each open position

No parameters — the tool figures out what's open. Use when:
- Total PnL hits the -$10 stop
- You want a clean slate before changing strategy
- Something looks wrong and you want out of the market

Returns a per-symbol summary of what was cancelled and closed.`

const binanceCloseAllSchema = `{
	"type": "object",
	"additionalProperties": false,
	"properties": {}
}`

type BinanceCloseAllTool struct{}

func NewBinanceCloseAll() *BinanceCloseAllTool { return &BinanceCloseAllTool{} }

func (BinanceCloseAllTool) Name() string            { return string(BinanceCloseAllToolName) }
func (BinanceCloseAllTool) Description() string     { return binanceCloseAllDescription }
func (BinanceCloseAllTool) Schema() json.RawMessage { return json.RawMessage(binanceCloseAllSchema) }

func (BinanceCloseAllTool) Execute(ctx context.Context, logger *slog.Logger, _ json.RawMessage) (tools.Result, error) {
	cli, err := sharedBinanceClient()
	if err != nil {
		return tools.Result{IsError: true, Content: err.Error()}, nil
	}

	// Paper-trading mode (PRD-021 §4): flatten the virtual book at live marks.
	if globalPaper != nil {
		return paperCloseAll(ctx, logger, cli), nil
	}

	logger.Warn("binance_close_all.dispatch")

	lines, err := cli.CloseAllPositions(ctx)
	if err != nil {
		return tools.Result{IsError: true, Content: "binance_close_all: " + err.Error()}, nil
	}
	return tools.Result{Content: strings.Join(lines, "\n")}, nil
}

// paperCloseAll flattens every virtual position at its live mark price.
func paperCloseAll(ctx context.Context, logger *slog.Logger, cli *binance.Client) tools.Result {
	logger.Warn("binance_close_all.paper")
	positions := globalPaper.Positions()
	if len(positions) == 0 {
		return tools.Result{Content: "PAPER: no virtual positions to close."}
	}
	var lines []string
	for _, p := range positions {
		mark := p.Entry
		if mp, err := cli.Price(ctx, p.Symbol); err == nil {
			if m, _ := strconv.ParseFloat(mp.MarkPrice, 64); m > 0 {
				mark = m
			}
		}
		realised, _ := globalPaper.CloseAt(p.Symbol, mark)
		lines = append(lines, fmt.Sprintf("PAPER: closed %s %s size=%g @ ~%.4f → realised %+.4f",
			p.Symbol, p.Side(), abs(p.Amt), mark, realised))
	}
	lines = append(lines, fmt.Sprintf("PAPER: virtual balance now %.2f USDT.", globalPaper.Balance()))
	return tools.Result{Content: strings.Join(lines, "\n")}
}
