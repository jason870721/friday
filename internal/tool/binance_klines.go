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
	"github.com/johnny1110/friday/internal/strategy"
)

// hintLeverage is the illustrative leverage used only for the sizing hint's
// margin figure; the Risk Manager recomputes with the leverage it actually
// sets. Quantity itself is risk-based and leverage-independent.
const hintLeverage = 20.0

const BinanceKlinesToolName tools.ToolName = "binance_klines"

const binanceKlinesDescription = `Get recent candlestick (OHLCV) data for a Binance Futures symbol.

Returns up to 'limit' candles for the given interval, formatted as one
candle per line: time | open | high | low | close | volume. A trailing
"Summary:" line gives a natural-language read of the series — price vs
MA20, RSI(14) with its zone, and short-term momentum.

Typical use:
- interval=5m, limit=20 — last 100 minutes for a quick trend read
- interval=1h, limit=24 — last day for higher-timeframe context

Intervals: 1m, 3m, 5m, 15m, 30m, 1h, 2h, 4h, 6h, 8h, 12h, 1d.`

const binanceKlinesSchema = `{
	"type": "object",
	"additionalProperties": false,
	"required": ["symbol", "interval"],
	"properties": {
		"symbol":   {"type": "string", "description": "Binance Futures symbol, e.g. BTCUSDT."},
		"interval": {"type": "string", "description": "Candle interval: 1m, 5m, 15m, 1h, 4h, 1d, etc."},
		"limit":    {"type": "integer", "minimum": 1, "maximum": 500, "default": 20, "description": "Number of candles (1-500). Default 20."}
	}
}`

type BinanceKlinesTool struct{}

func NewBinanceKlines() *BinanceKlinesTool { return &BinanceKlinesTool{} }

func (BinanceKlinesTool) Name() string            { return string(BinanceKlinesToolName) }
func (BinanceKlinesTool) Description() string     { return binanceKlinesDescription }
func (BinanceKlinesTool) Schema() json.RawMessage { return json.RawMessage(binanceKlinesSchema) }

type binanceKlinesInput struct {
	Symbol   string `json:"symbol"`
	Interval string `json:"interval"`
	Limit    *int   `json:"limit,omitempty"`
}

func (BinanceKlinesTool) Execute(ctx context.Context, logger *slog.Logger, raw json.RawMessage) (tools.Result, error) {
	var in binanceKlinesInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("binance_klines: decode input: %v", err)}, nil
	}
	if in.Symbol == "" || in.Interval == "" {
		return tools.Result{IsError: true, Content: "binance_klines: symbol and interval are required"}, nil
	}
	limit := 20
	if in.Limit != nil {
		limit = *in.Limit
	}
	if limit < 1 || limit > 500 {
		return tools.Result{IsError: true, Content: fmt.Sprintf("binance_klines: limit=%d out of range [1,500]", limit)}, nil
	}

	cli, err := sharedBinanceClient()
	if err != nil {
		return tools.Result{IsError: true, Content: err.Error()}, nil
	}

	logger.Debug("binance_klines.dispatch", "symbol", in.Symbol, "interval", in.Interval, "limit", limit)

	ks, err := cli.Klines(ctx, in.Symbol, in.Interval, limit)
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("binance_klines: %v", err)}, nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s %s (%d candles)\n", in.Symbol, in.Interval, len(ks))
	fmt.Fprintln(&b, "openTime | open | high | low | close | volume")
	for _, k := range ks {
		fmt.Fprintf(&b, "%d | %.4f | %.4f | %.4f | %.4f | %.4f\n",
			k.OpenTime, k.Open, k.High, k.Low, k.Close, k.Volume)
	}
	// PRD-001: a natural-language read of the same candles — MA20, RSI(14),
	// and short-term momentum. PRD-006: append the deterministic strategy
	// consensus (momentum / breakout / mean-reversion) so the Analyst reads
	// pre-computed signals instead of inventing direction. The candle table
	// above is retained alongside both.
	summary := binance.SemanticSummary(ks)
	consensus := strategy.ConsensusFor(in.Symbol, ks)
	fmt.Fprintf(&b, "\nSummary: %s\n", strategy.FormatSummary(summary, consensus))

	// PRD-007: append a volatility-based sizing hint (risk ÷ ATR stop) so the
	// Analyst can carry ATR + the suggested stop into its report for the Risk
	// Manager. Best-effort: needs the live balance, so silently omit it if the
	// (signed) balance read is unavailable.
	if bal, berr := cli.USDTBalance(ctx); berr == nil {
		if hint := atrSizingHint(ks, parseBalance(bal.Balance)); hint != "" {
			fmt.Fprintf(&b, "%s\n", hint)
		}
	}
	return tools.Result{Content: b.String()}, nil
}

// atrSizingHint renders the PRD-007 risk-based sizing suggestion for the series,
// or "" when ATR or the balance is unavailable.
func atrSizingHint(ks []binance.Kline, balance float64) string {
	atr, ok := binance.ATR(ks, 14)
	if !ok || balance <= 0 || len(ks) == 0 {
		return ""
	}
	entry := ks[len(ks)-1].Close
	sz := risk.SuggestedSize(risk.DirLong, risk.SizeParams{
		Balance:        balance,
		EntryPrice:     entry,
		ATR:            atr,
		Leverage:       hintLeverage,
		RiskPerTrade:   risk.DefaultRiskPerTrade,
		StopMultiplier: risk.DefaultStopMultiplier,
		MaxMarginPct:   guardrailMaxMarginPct,
	})
	if sz.Quantity <= 0 {
		return ""
	}
	shortStop := entry + risk.DefaultStopMultiplier*atr
	capNote := ""
	if sz.CappedByLimit {
		capNote = " [risk target exceeds cap → clamped to it]"
	}
	return fmt.Sprintf(
		"Sizing hint (%.0f%% risk, %.0f×ATR stop, @%.0fx illustrative): ~%.4f units, margin ~$%.2f, stop long ~%.4f / short ~%.4f; %.0f%% margin cap = $%.2f.%s",
		risk.DefaultRiskPerTrade*100, risk.DefaultStopMultiplier, hintLeverage,
		sz.Quantity, sz.Margin, sz.StopPrice, shortStop,
		guardrailMaxMarginPct*100, balance*guardrailMaxMarginPct, capNote)
}

func parseBalance(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}
