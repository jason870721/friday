package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/johnny1110/evva/pkg/tools"
	"github.com/johnny1110/friday/internal/binance"
	"github.com/johnny1110/friday/internal/risk"
)

// globalStopMonitor is the process-wide StopMonitor (PRD-009), installed at
// startup by main via SetStopMonitor. nil when the monitor isn't running
// (e.g. no Binance credentials).
var globalStopMonitor *risk.StopMonitor

// SetStopMonitor installs the shared monitor the binance_stop_monitor tool
// registers levels with.
func SetStopMonitor(m *risk.StopMonitor) { globalStopMonitor = m }

// SharedBinanceClient exposes the process-wide Binance client so main can build
// the StopMonitor on the SAME client the trading tools use.
func SharedBinanceClient() (*binance.Client, error) { return sharedBinanceClient() }

// binanceStopBroker adapts *binance.Client to risk.StopBroker.
type binanceStopBroker struct{ cli *binance.Client }

// NewBinanceStopBroker wraps a client as the broker the StopMonitor drives.
func NewBinanceStopBroker(cli *binance.Client) risk.StopBroker { return binanceStopBroker{cli: cli} }

func (b binanceStopBroker) MarkPrice(ctx context.Context, symbol string) (float64, error) {
	mp, err := b.cli.Price(ctx, symbol)
	if err != nil {
		return 0, err
	}
	return strconv.ParseFloat(mp.MarkPrice, 64)
}

func (b binanceStopBroker) CloseReduceOnly(ctx context.Context, symbol string, qty float64, positionSide string) error {
	side := binance.SideSell // flatten a long
	if positionSide == risk.DirShort {
		side = binance.SideBuy // flatten a short
	}
	_, err := b.cli.MarketOrder(ctx, symbol, side, qty, true)
	return err
}

const BinanceStopMonitorToolName tools.ToolName = "binance_stop_monitor"

const binanceStopMonitorDescription = `Register (or clear) a stop-loss / take-profit level for a symbol with the background StopMonitor (PRD-009).

The monitor polls mark price ~every second and fires a reduce-only market close
the instant a level is crossed — a fast safety net independent of the 15s round
loop. Call this RIGHT AFTER an OPEN fills, using the stop_loss the Risk Manager
computed (and take_profit if given). Levels auto-clear once they fire. To clear
manually (e.g. after you close a position yourself), call again with both
stop_price and take_profit_price set to 0.`

const binanceStopMonitorSchema = `{
	"type": "object",
	"additionalProperties": false,
	"required": ["symbol", "position_side", "quantity"],
	"properties": {
		"symbol":            {"type": "string", "description": "Symbol of the open position, e.g. BTCUSDT."},
		"position_side":     {"type": "string", "enum": ["LONG", "SHORT"], "description": "Side of the position to protect."},
		"quantity":          {"type": "number", "description": "Position size to close on breach (base asset)."},
		"stop_price":        {"type": "number", "description": "Stop-loss mark price (0 = none)."},
		"take_profit_price": {"type": "number", "description": "Take-profit mark price (0 = none)."}
	}
}`

type BinanceStopMonitorTool struct{}

func NewBinanceStopMonitor() *BinanceStopMonitorTool { return &BinanceStopMonitorTool{} }

func (BinanceStopMonitorTool) Name() string        { return string(BinanceStopMonitorToolName) }
func (BinanceStopMonitorTool) Description() string { return binanceStopMonitorDescription }
func (BinanceStopMonitorTool) Schema() json.RawMessage {
	return json.RawMessage(binanceStopMonitorSchema)
}

type binanceStopMonitorInput struct {
	Symbol          string  `json:"symbol"`
	PositionSide    string  `json:"position_side"`
	Quantity        float64 `json:"quantity"`
	StopPrice       float64 `json:"stop_price"`
	TakeProfitPrice float64 `json:"take_profit_price"`
}

func (BinanceStopMonitorTool) Execute(_ context.Context, logger *slog.Logger, raw json.RawMessage) (tools.Result, error) {
	var in binanceStopMonitorInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("binance_stop_monitor: decode input: %v", err)}, nil
	}
	if in.Symbol == "" {
		return tools.Result{IsError: true, Content: "binance_stop_monitor: symbol is required"}, nil
	}
	if globalStopMonitor == nil {
		return tools.Result{IsError: true, Content: "binance_stop_monitor: stop monitor is not running (no Binance credentials?)"}, nil
	}

	levels := risk.StopLevels{
		StopPrice:    in.StopPrice,
		TakeProfit:   in.TakeProfitPrice,
		PositionQty:  in.Quantity,
		PositionSide: in.PositionSide,
	}
	globalStopMonitor.SetLevels(in.Symbol, levels)

	// Mirror StopMonitor.active(): a level with no size or no stop/TP clears.
	arming := in.Quantity > 0 && (in.StopPrice > 0 || in.TakeProfitPrice > 0) &&
		(in.PositionSide == risk.DirLong || in.PositionSide == risk.DirShort)
	if !arming {
		logger.Debug("binance_stop_monitor.cleared", "symbol", in.Symbol)
		return tools.Result{Content: fmt.Sprintf("Cleared stop/TP levels for %s (monitor now watching %d symbol(s)).", in.Symbol, globalStopMonitor.Active())}, nil
	}
	logger.Debug("binance_stop_monitor.armed", "symbol", in.Symbol, "side", in.PositionSide,
		"stop", in.StopPrice, "tp", in.TakeProfitPrice, "qty", in.Quantity)
	return tools.Result{Content: fmt.Sprintf(
		"Monitoring %s %s qty=%g: stop=%g take_profit=%g. The background monitor will close it within ~1s of a breach.",
		in.Symbol, in.PositionSide, in.Quantity, in.StopPrice, in.TakeProfitPrice)}, nil
}
