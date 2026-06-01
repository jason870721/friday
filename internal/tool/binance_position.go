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
	"github.com/johnny1110/friday/internal/risk"
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

	// Paper-trading mode (PRD-021 §4): report virtual positions (with uPnL from
	// the live mark when a client is available), never the real exchange account.
	if globalPaper != nil {
		return paperPositions(ctx, in.Symbol), nil
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

// paperPositions renders the virtual book's positions, computing uPnL from the
// live mark price when a market-data client is available (best-effort — falls
// back to the entry price as the mark when not).
func paperPositions(ctx context.Context, symbol string) tools.Result {
	cli, _ := sharedBinanceClient() // market data only; nil tolerated below
	all := globalPaper.Positions()
	var rows []risk.PaperPosition
	for _, p := range all {
		if symbol == "" || p.Symbol == symbol {
			rows = append(rows, p)
		}
	}
	if len(rows) == 0 {
		if symbol != "" {
			return tools.Result{Content: fmt.Sprintf("%s: no position (paper)", symbol)}
		}
		return tools.Result{Content: "no open positions (paper)"}
	}
	var b strings.Builder
	for i, p := range rows {
		if i > 0 {
			b.WriteString("\n")
		}
		mark := p.Entry
		if cli != nil {
			if mp, err := cli.Price(ctx, p.Symbol); err == nil {
				if m, _ := strconv.ParseFloat(mp.MarkPrice, 64); m > 0 {
					mark = m
				}
			}
		}
		upnl := (mark - p.Entry) * p.Amt // signed Amt makes this correct for shorts
		fmt.Fprintf(&b, "%s %s size=%g entry=%.4f mark=%.4f uPnL=%+.4f lev=%gx [PAPER]",
			p.Symbol, p.Side(), absf(p.Amt), p.Entry, mark, upnl, p.Leverage)
	}
	return tools.Result{Content: b.String()}
}

func absf(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
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
