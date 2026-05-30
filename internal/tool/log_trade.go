package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/johnny1110/evva/pkg/tools"
	"github.com/johnny1110/friday/internal/memory"
)

const LogTradeToolName tools.ToolName = "log_trade"

const logTradeDescription = `Record a CLOSED trade into friday's trade memory (PRD-004).

Call this right after a position is closed, so future rounds can learn from
how this setup resolved. Store the market context AT THE TRADE (the same
indicators the Analyst reads), the entry reason, the direction, and the
realised PnL. recall_trades later retrieves the most similar past trades.`

const logTradeSchema = `{
	"type": "object",
	"additionalProperties": false,
	"required": ["symbol", "bias", "pnl", "entry_reason", "rsi", "price_vs_ma", "momentum", "funding", "sentiment"],
	"properties": {
		"symbol":      {"type": "string", "description": "BTCUSDT / ETHUSDT / SOLUSDT."},
		"bias":        {"type": "string", "enum": ["LONG", "SHORT"], "description": "Direction the trade was taken."},
		"pnl":         {"type": "number", "description": "Realised PnL in USDT (negative for a loss)."},
		"entry_reason":{"type": "string", "description": "Why the trade was opened (the setup)."},
		"rsi":         {"type": "number", "description": "RSI(14) at the trade, 0-100."},
		"price_vs_ma": {"type": "number", "description": "(price-MA20)/MA20 as a percent, e.g. 0.3."},
		"momentum":    {"type": "number", "description": "-1 falling, 0 mixed, +1 rising."},
		"funding":     {"type": "number", "description": "Funding rate as a percent, e.g. 0.01."},
		"sentiment":   {"type": "number", "description": "Fear & Greed index, 0-100."}
	}
}`

type LogTradeTool struct{}

func NewLogTrade() *LogTradeTool { return &LogTradeTool{} }

func (LogTradeTool) Name() string            { return string(LogTradeToolName) }
func (LogTradeTool) Description() string      { return logTradeDescription }
func (LogTradeTool) Schema() json.RawMessage { return json.RawMessage(logTradeSchema) }

type logTradeInput struct {
	Symbol      string  `json:"symbol"`
	Bias        string  `json:"bias"`
	PnL         float64 `json:"pnl"`
	EntryReason string  `json:"entry_reason"`
	RSI         float64 `json:"rsi"`
	PriceVsMA   float64 `json:"price_vs_ma"`
	Momentum    float64 `json:"momentum"`
	Funding     float64 `json:"funding"`
	Sentiment   float64 `json:"sentiment"`
}

func (LogTradeTool) Execute(_ context.Context, logger *slog.Logger, raw json.RawMessage) (tools.Result, error) {
	var in logTradeInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("log_trade: decode input: %v", err)}, nil
	}
	if in.Symbol == "" {
		return tools.Result{IsError: true, Content: "log_trade: symbol is required"}, nil
	}

	store, err := sharedTradeStore()
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("log_trade: %v", err)}, nil
	}

	rec := memory.TradeRecord{
		Symbol:      in.Symbol,
		Time:        time.Now().Unix(),
		EntryReason: in.EntryReason,
		Bias:        in.Bias,
		PnL:         in.PnL,
		Features: memory.Features{
			RSI:       in.RSI,
			PriceVsMA: in.PriceVsMA,
			Momentum:  in.Momentum,
			Funding:   in.Funding,
			Sentiment: in.Sentiment,
		},
	}
	if err := store.Log(rec); err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("log_trade: %v", err)}, nil
	}

	// Feed the session circuit breaker (PRD-005): a closed trade's realised
	// PnL drives the consecutive-loss and daily-loss tracking.
	if globalBreaker != nil {
		globalBreaker.RecordTrade(in.PnL)
	}

	logger.Debug("log_trade.stored", "symbol", in.Symbol, "pnl", in.PnL, "total", store.Len())
	return tools.Result{Content: fmt.Sprintf(
		"Logged %s %s trade (PnL %+.2f). Trade memory now holds %d records.",
		in.Symbol, in.Bias, in.PnL, store.Len())}, nil
}
