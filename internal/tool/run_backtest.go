package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/johnny1110/evva/pkg/tools"
	"github.com/johnny1110/friday/internal/backtest"
)

const RunBacktestToolName tools.ToolName = "run_backtest"

const runBacktestDescription = `Backtest a simple strategy rule on historical
candles before risking it live (PRD-004 sandbox).

Fetches recent klines for the symbol and replays the rule: when the entry
condition holds, open in 'direction'; exit at take_profit_pct or
stop_loss_pct (whichever the following candles hit first). Returns win rate,
average PnL %, total return %, and max drawdown. Places NO orders and
touches no account — it is purely a what-if simulation.

Indicators: RSI (RSI(14), 0-100) or PRICE_VS_MA ((close-MA20)/MA20 percent).`

const runBacktestSchema = `{
	"type": "object",
	"additionalProperties": false,
	"required": ["symbol", "indicator", "op", "value", "direction", "take_profit_pct", "stop_loss_pct"],
	"properties": {
		"symbol":          {"type": "string", "description": "BTCUSDT / ETHUSDT / SOLUSDT."},
		"interval":        {"type": "string", "default": "5m", "description": "Candle interval (default 5m)."},
		"limit":           {"type": "integer", "minimum": 30, "maximum": 500, "default": 200, "description": "Candles to simulate over (30-500, default 200)."},
		"indicator":       {"type": "string", "enum": ["RSI", "PRICE_VS_MA"], "description": "Entry signal."},
		"op":              {"type": "string", "enum": ["<", ">"], "description": "Comparison: indicator OP value."},
		"value":           {"type": "number", "description": "Threshold, e.g. 30 for 'RSI < 30'."},
		"direction":       {"type": "string", "enum": ["LONG", "SHORT"], "description": "Trade direction on entry."},
		"take_profit_pct": {"type": "number", "exclusiveMinimum": 0, "description": "Take-profit as a price-move percent, e.g. 1.5."},
		"stop_loss_pct":   {"type": "number", "exclusiveMinimum": 0, "description": "Stop-loss as a price-move percent, e.g. 1.0."},
		"leverage":        {"type": "number", "minimum": 1, "default": 1, "description": "Leverage multiplier applied to PnL%."}
	}
}`

type RunBacktestTool struct{}

func NewRunBacktest() *RunBacktestTool { return &RunBacktestTool{} }

func (RunBacktestTool) Name() string            { return string(RunBacktestToolName) }
func (RunBacktestTool) Description() string      { return runBacktestDescription }
func (RunBacktestTool) Schema() json.RawMessage { return json.RawMessage(runBacktestSchema) }

type runBacktestInput struct {
	Symbol        string  `json:"symbol"`
	Interval      string  `json:"interval,omitempty"`
	Limit         *int    `json:"limit,omitempty"`
	Indicator     string  `json:"indicator"`
	Op            string  `json:"op"`
	Value         float64 `json:"value"`
	Direction     string  `json:"direction"`
	TakeProfitPct float64 `json:"take_profit_pct"`
	StopLossPct   float64 `json:"stop_loss_pct"`
	Leverage      float64 `json:"leverage,omitempty"`
}

func (RunBacktestTool) Execute(ctx context.Context, logger *slog.Logger, raw json.RawMessage) (tools.Result, error) {
	var in runBacktestInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("run_backtest: decode input: %v", err)}, nil
	}
	if in.Symbol == "" {
		return tools.Result{IsError: true, Content: "run_backtest: symbol is required"}, nil
	}
	interval := in.Interval
	if interval == "" {
		interval = "5m"
	}
	limit := 200
	if in.Limit != nil {
		limit = *in.Limit
	}
	leverage := in.Leverage
	if leverage <= 0 {
		leverage = 1
	}

	rule := backtest.Rule{
		Indicator:     backtest.Indicator(in.Indicator),
		Op:            backtest.Op(in.Op),
		Value:         in.Value,
		Direction:     in.Direction,
		TakeProfitPct: in.TakeProfitPct,
		StopLossPct:   in.StopLossPct,
		Leverage:      leverage,
	}
	if err := rule.Validate(); err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("run_backtest: invalid rule: %v", err)}, nil
	}

	cli, err := sharedBinanceClient()
	if err != nil {
		return tools.Result{IsError: true, Content: err.Error()}, nil
	}
	candles, err := cli.Klines(ctx, in.Symbol, interval, limit)
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("run_backtest: fetch klines: %v", err)}, nil
	}

	res, err := backtest.Run(rule, candles)
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("run_backtest: %v", err)}, nil
	}

	logger.Debug("run_backtest.done", "symbol", in.Symbol, "trades", res.Trades, "winRate", res.WinRate)
	return tools.Result{Content: fmt.Sprintf(
		"Backtest %s %s [%s %s %.2f → %s, TP %.2f%% / SL %.2f%% @ %.0fx] over %d candles:\n"+
			"  trades=%d  wins=%d  winRate=%.0f%%  avgPnL=%+.2f%%  totalReturn=%+.2f%%  maxDrawdown=%.2f%%\n"+
			"  (simulation only — no orders placed)",
		in.Symbol, interval, in.Indicator, in.Op, in.Value, in.Direction,
		in.TakeProfitPct, in.StopLossPct, leverage, len(candles),
		res.Trades, res.Wins, res.WinRate*100, res.AvgPnLPct, res.TotalReturnPct, res.MaxDrawdownPct)}, nil
}
