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
		"strategy":    {"type": "string", "description": "Restrict to trades triggered by this strategy (e.g. momentum / breakout / mean_reversion / ema_cross / divergence). Omit to recall all strategies."},
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
func (RecallTradesTool) Description() string     { return recallTradesDescription }
func (RecallTradesTool) Schema() json.RawMessage { return json.RawMessage(recallTradesSchema) }

type recallTradesInput struct {
	Symbol    string  `json:"symbol,omitempty"`
	Strategy  string  `json:"strategy,omitempty"`
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
	// PRD-014: an optional strategy filter — "how did momentum specifically do
	// in conditions like these?". Omitted → all strategies (the original behaviour).
	// PRD-023: conclusive is false when fewer than ConclusiveMinSamples comparable
	// trades exist — a thin, likely all-loss sample the Analyst must NOT veto on.
	matches, conclusive := store.SimilarConclusive(in.Symbol, in.Strategy, f, k)

	logger.Debug("recall_trades.query", "symbol", in.Symbol, "strategy", in.Strategy, "k", k, "hits", len(matches), "conclusive", conclusive)

	if len(matches) == 0 {
		return tools.Result{Content: "No past trades logged yet — trade memory is empty. Decide on this round's data alone."}, nil
	}

	var b strings.Builder
	scope := "all symbols"
	if in.Symbol != "" {
		scope = in.Symbol
	}
	if in.Strategy != "" {
		scope += ", " + in.Strategy + " only"
	}
	fmt.Fprintf(&b, "%d most similar past trades (%s):\n", len(matches), scope)
	for _, m := range matches {
		r := m.Record
		strat := ""
		if r.Strategy != "" {
			strat = " | strat: " + r.Strategy
		}
		fmt.Fprintf(&b, "- %s %s | %s PnL %+.2f | RSI %.0f, price-vs-MA %+.2f%%, sentiment %.0f | sim %.2f%s | reason: %s\n",
			r.Symbol, r.Bias, r.Outcome, r.EffectivePnL(), r.Features.RSI, r.Features.PriceVsMA, r.Features.Sentiment, m.Similarity, strat, r.EntryReason)
	}

	// PRD-023: a thin sample (<5 comparable trades) is not statistically
	// meaningful — present it as non-informative so the Analyst won't cite an
	// all-loss 2-3-trade recall to veto a setup (the negative feedback loop).
	if !conclusive {
		fmt.Fprintf(&b, "Outcome: insufficient data (<%d similar trades) — do not use this to veto.\n", memory.ConclusiveMinSamples)
		return tools.Result{Content: strings.TrimRight(b.String(), "\n")}, nil
	}

	// PRD-014: outcome breakdown across the returned trades, with a per-strategy
	// split when more than one strategy is represented.
	fmt.Fprintf(&b, "Outcome: %s\n", formatOutcome(memory.OutcomeStatsOf(matches)))
	if perStrat := strategyBreakdown(matches); len(perStrat) > 1 {
		for _, line := range perStrat {
			fmt.Fprintf(&b, "  - %s\n", line)
		}
	}
	return tools.Result{Content: strings.TrimRight(b.String(), "\n")}, nil
}

// formatOutcome renders an OutcomeStats as a one-liner:
// "2 wins, 1 loss (avg win +$3.40, avg loss -$1.20)". Flats are shown only when
// present; averages are omitted for an absent side.
func formatOutcome(st memory.OutcomeStats) string {
	head := fmt.Sprintf("%d %s, %d %s", st.Wins, plural(st.Wins, "win", "wins"), st.Losses, plural(st.Losses, "loss", "losses"))
	if st.Flats > 0 {
		head += fmt.Sprintf(", %d flat", st.Flats)
	}
	var avgs []string
	if st.Wins > 0 {
		avgs = append(avgs, fmt.Sprintf("avg win %+.2f", st.AvgWin))
	}
	if st.Losses > 0 {
		avgs = append(avgs, fmt.Sprintf("avg loss %+.2f", st.AvgLoss))
	}
	if len(avgs) > 0 {
		head += " (" + strings.Join(avgs, ", ") + ")"
	}
	return head
}

// strategyBreakdown returns one "name: <outcome>" line per distinct strategy in
// the matches, ordered by appearance. Records with no strategy are grouped under
// "(unattributed)". Returns the lines so the caller can decide whether to show
// them (only when >1 strategy is present).
func strategyBreakdown(matches []memory.Scored) []string {
	order := make([]string, 0, 4)
	groups := make(map[string][]memory.Scored)
	for _, m := range matches {
		key := m.Record.Strategy
		if key == "" {
			key = "(unattributed)"
		}
		if _, seen := groups[key]; !seen {
			order = append(order, key)
		}
		groups[key] = append(groups[key], m)
	}
	out := make([]string, 0, len(order))
	for _, key := range order {
		out = append(out, fmt.Sprintf("%s: %s", key, formatOutcome(memory.OutcomeStatsOf(groups[key]))))
	}
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
