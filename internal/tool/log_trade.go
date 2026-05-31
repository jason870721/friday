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
indicators the Analyst reads), the entry reason, and the direction.

The 'pnl' you pass is only a hint: log_trade RECONCILES it against the Binance
income ledger (realised PnL − commission − funding) and records the exchange's
true net, so you do not need to compute fees yourself. recall_trades later
retrieves the most similar past trades and their REAL outcomes.`

const logTradeSchema = `{
	"type": "object",
	"additionalProperties": false,
	"required": ["symbol", "bias", "pnl", "entry_reason", "rsi", "price_vs_ma", "momentum", "funding", "sentiment"],
	"properties": {
		"symbol":      {"type": "string", "description": "The closed position's symbol, e.g. BTCUSDT."},
		"bias":        {"type": "string", "enum": ["LONG", "SHORT"], "description": "Direction the trade was taken."},
		"strategy":    {"type": "string", "description": "The strategy that triggered this trade, e.g. momentum / breakout / mean_reversion / ema_cross / divergence (from the Risk Manager's decision reason). Optional but enables per-strategy win/loss tracking."},
		"pnl":         {"type": "number", "description": "Your best estimate of realised PnL in USDT (a hint; reconciled against the exchange ledger)."},
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
func (LogTradeTool) Description() string     { return logTradeDescription }
func (LogTradeTool) Schema() json.RawMessage { return json.RawMessage(logTradeSchema) }

type logTradeInput struct {
	Symbol      string  `json:"symbol"`
	Bias        string  `json:"bias"`
	Strategy    string  `json:"strategy,omitempty"`
	PnL         float64 `json:"pnl"`
	EntryReason string  `json:"entry_reason"`
	RSI         float64 `json:"rsi"`
	PriceVsMA   float64 `json:"price_vs_ma"`
	Momentum    float64 `json:"momentum"`
	Funding     float64 `json:"funding"`
	Sentiment   float64 `json:"sentiment"`
}

// reconcileWindow is how far back log_trade looks in the income ledger for the
// close it is recording. log_trade is called right after the close, so a short
// window captures it while limiting the chance of folding in an unrelated
// earlier close of the same symbol.
const reconcileWindow = 90 * time.Second

func (LogTradeTool) Execute(ctx context.Context, logger *slog.Logger, raw json.RawMessage) (tools.Result, error) {
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
		Strategy:    in.Strategy,
		PnL:         in.PnL,
		PnLSource:   "reported",
		Features: memory.Features{
			RSI:       in.RSI,
			PriceVsMA: in.PriceVsMA,
			Momentum:  in.Momentum,
			Funding:   in.Funding,
			Sentiment: in.Sentiment,
		},
	}

	// Reconcile against the exchange ledger — the agent-reported pnl has proven
	// unreliable (it logged WINs on losing closes). The income endpoint is
	// ground truth: realised PnL net of commission and funding. Fall back to
	// the reported value only if reconciliation is unavailable.
	if cli, cerr := sharedBinanceClient(); cerr == nil {
		end := time.Now()
		start := end.Add(-reconcileWindow)
		if sum, rerr := cli.RecentRealized(ctx, in.Symbol, start.UnixMilli(), end.UnixMilli()); rerr != nil {
			logger.Warn("log_trade.reconcile_failed", "symbol", in.Symbol, "err", rerr)
		} else if sum.RealizedRows > 0 {
			rec.PnL = sum.RealizedPnL
			rec.Commission = sum.Commission
			rec.Funding = sum.Funding
			rec.NetPnL = sum.Net()
			rec.PnLSource = "exchange"
		}
	}

	if err := store.Log(rec); err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("log_trade: %v", err)}, nil
	}

	// Feed the session circuit breaker (PRD-005) the TRUE net wallet impact —
	// daily-loss and consecutive-loss tracking must reflect reality, not a
	// reported figure that may be wrong.
	effective := rec.PnL
	if rec.PnLSource == "exchange" {
		effective = rec.NetPnL
	}
	if globalBreaker != nil {
		globalBreaker.RecordTrade(effective)
	}

	logger.Debug("log_trade.stored", "symbol", in.Symbol, "pnl", rec.PnL,
		"net", rec.NetPnL, "source", rec.PnLSource, "total", store.Len())

	if rec.PnLSource == "exchange" {
		return tools.Result{Content: fmt.Sprintf(
			"Logged %s %s — exchange realised %+.4f, fees %+.4f, funding %+.4f → NET %+.4f USDT (you reported %+.2f). Memory now holds %d records.",
			in.Symbol, in.Bias, rec.PnL, rec.Commission, rec.Funding, rec.NetPnL, in.PnL, store.Len())}, nil
	}
	return tools.Result{Content: fmt.Sprintf(
		"Logged %s %s trade (reported PnL %+.2f; exchange reconciliation unavailable). Memory now holds %d records.",
		in.Symbol, in.Bias, in.PnL, store.Len())}, nil
}
