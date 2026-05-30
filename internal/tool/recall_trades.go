package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/johnny1110/evva/pkg/tools"
	"github.com/johnny1110/friday/internal/memory"
)

const RecallTradesToolName tools.ToolName = "recall_trades"

const recallTradesDescription = `Retrieve past trades whose market conditions
most resemble the current setup (PRD-004 self-reflection).

Before committing to a bias on a symbol, call this with the CURRENT
indicators. It returns the most similar previously-logged trades and how
they resolved (WIN/LOSS, PnL) — use them as evidence, not gospel. If the
memory is empty, it says so.`

const recallTradesSchema = `{
	"type": "object",
	"additionalProperties": false,
	"required": ["rsi", "price_vs_ma", "momentum", "funding", "sentiment"],
	"properties": {
		"symbol":      {"type": "string", "description": "Restrict to this symbol; omit to search all symbols."},
		"k":           {"type": "integer", "minimum": 1, "maximum": 10, "default": 3, "description": "How many similar trades to return (1-10, default 3)."},
		"rsi":         {"type": "number", "description": "Current RSI(14), 0-100."},
		"price_vs_ma": {"type": "number", "description": "Current (price-MA20)/MA20 as a percent."},
		"momentum":    {"type": "number", "description": "-1 falling, 0 mixed, +1 rising."},
		"funding":     {"type": "number", "description": "Current funding rate as a percent."},
		"sentiment":   {"type": "number", "description": "Current Fear & Greed index, 0-100."}
	}
}`

type RecallTradesTool struct{}

func NewRecallTrades() *RecallTradesTool { return &RecallTradesTool{} }

func (RecallTradesTool) Name() string            { return string(RecallTradesToolName) }
func (RecallTradesTool) Description() string      { return recallTradesDescription }
func (RecallTradesTool) Schema() json.RawMessage { return json.RawMessage(recallTradesSchema) }

type recallTradesInput struct {
	Symbol    string  `json:"symbol,omitempty"`
	K         *int    `json:"k,omitempty"`
	RSI       float64 `json:"rsi"`
	PriceVsMA float64 `json:"price_vs_ma"`
	Momentum  float64 `json:"momentum"`
	Funding   float64 `json:"funding"`
	Sentiment float64 `json:"sentiment"`
}

func (RecallTradesTool) Execute(_ context.Context, logger *slog.Logger, raw json.RawMessage) (tools.Result, error) {
	var in recallTradesInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("recall_trades: decode input: %v", err)}, nil
	}
	k := 3
	if in.K != nil {
		k = *in.K
	}

	store, err := sharedTradeStore()
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("recall_trades: %v", err)}, nil
	}

	f := memory.Features{
		RSI:       in.RSI,
		PriceVsMA: in.PriceVsMA,
		Momentum:  in.Momentum,
		Funding:   in.Funding,
		Sentiment: in.Sentiment,
	}
	matches := store.Similar(in.Symbol, f, k)

	logger.Debug("recall_trades.query", "symbol", in.Symbol, "k", k, "hits", len(matches))

	if len(matches) == 0 {
		return tools.Result{Content: "No past trades logged yet — trade memory is empty. Decide on this round's data alone."}, nil
	}

	var b strings.Builder
	scope := "all symbols"
	if in.Symbol != "" {
		scope = in.Symbol
	}
	fmt.Fprintf(&b, "%d most similar past trades (%s):\n", len(matches), scope)
	for _, m := range matches {
		r := m.Record
		fmt.Fprintf(&b, "- %s %s | %s PnL %+.2f | RSI %.0f, price-vs-MA %+.2f%%, sentiment %.0f | sim %.2f | reason: %s\n",
			r.Symbol, r.Bias, r.Outcome, r.PnL, r.Features.RSI, r.Features.PriceVsMA, r.Features.Sentiment, m.Similarity, r.EntryReason)
	}
	return tools.Result{Content: strings.TrimRight(b.String(), "\n")}, nil
}
