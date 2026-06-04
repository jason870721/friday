package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/johnny1110/evva/pkg/tools"
	"github.com/johnny1110/friday/internal/binance"
	"github.com/johnny1110/friday/internal/strategy"
)

const BinanceMTFKlinesToolName tools.ToolName = "binance_mtf_klines"

const binanceMTFKlinesDescription = `Multi-timeframe market read for a symbol in ONE call (PRD-008).

Fetches three timeframes concurrently — 5m (last 8 hours), 1h (last day), and
4h (last 4 days) — and returns, per timeframe, a natural-language Summary
(price vs MA20, RSI, momentum, ATR) and a coarse direction (BULLISH / BEARISH /
NEUTRAL). It then gives a cross-timeframe verdict:

- ALIGNED   — the timeframes agree; trade with the trend.
- CONFLICT  — lower vs higher timeframe disagree; the HIGHER timeframe
              dominates (a 5m long against a 4h downtrend is a trap).
- NO-EDGE   — no clear direction on any timeframe.

Use this as your PRIMARY market read for each symbol; fall back to
binance_klines only when you need an extra interval.`

const binanceMTFKlinesSchema = `{
	"type": "object",
	"additionalProperties": false,
	"required": ["symbol"],
	"properties": {
		"symbol": {"type": "string", "description": "Binance Futures symbol, e.g. BTCUSDT."}
	}
}`

type BinanceMTFKlinesTool struct{}

func NewBinanceMTFKlines() *BinanceMTFKlinesTool { return &BinanceMTFKlinesTool{} }

func (BinanceMTFKlinesTool) Name() string            { return string(BinanceMTFKlinesToolName) }
func (BinanceMTFKlinesTool) Description() string     { return binanceMTFKlinesDescription }
func (BinanceMTFKlinesTool) Schema() json.RawMessage { return json.RawMessage(binanceMTFKlinesSchema) }

type binanceMTFKlinesInput struct {
	Symbol string `json:"symbol"`
}

// mtfFrames are the timeframes fetched, ordered LOW → HIGH. The cross-TF
// verdict treats later (higher) frames as dominant on conflict.
var mtfFrames = []struct {
	Interval string
	Limit    int
}{
	{"5m", 96}, // PRD-024 R1: 96×5m = 8h so all 5 strategies (EMACross needs 50) can vote on the entry TF
	{"1h", 24},
	{"4h", 48}, // PRD-016: ≥29 candles so ADX(14) → regime detection is computable
}

type tfRead struct {
	interval string
	summary  string
	dir      string
	ks       []binance.Kline // raw candles (kept for the 5m frame's divergence hint, PRD-013)
	err      error
}

func (BinanceMTFKlinesTool) Execute(ctx context.Context, logger *slog.Logger, raw json.RawMessage) (tools.Result, error) {
	var in binanceMTFKlinesInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("binance_mtf_klines: decode input: %v", err)}, nil
	}
	if in.Symbol == "" {
		return tools.Result{IsError: true, Content: "binance_mtf_klines: symbol is required"}, nil
	}

	cli, err := sharedBinanceClient()
	if err != nil {
		return tools.Result{IsError: true, Content: err.Error()}, nil
	}

	// Fetch every timeframe concurrently on the shared client (one assistant
	// turn instead of three sequential round-trips).
	reads := make([]tfRead, len(mtfFrames))
	var wg sync.WaitGroup
	for i, f := range mtfFrames {
		wg.Add(1)
		go func(i int, interval string, limit int) {
			defer wg.Done()
			ks, err := cli.Klines(ctx, in.Symbol, interval, limit)
			if err != nil {
				reads[i] = tfRead{interval: interval, err: err}
				return
			}
			reads[i] = tfRead{
				interval: interval,
				summary:  binance.SemanticSummary(ks),
				dir:      binance.ClassifyDirection(ks),
				ks:       ks,
			}
		}(i, f.Interval, f.Limit)
	}
	wg.Wait()

	logger.Debug("binance_mtf_klines.dispatch", "symbol", in.Symbol)

	// All timeframes failed → surface an error; otherwise degrade gracefully
	// (a failed TF is reported and treated as NEUTRAL for the verdict).
	ok := 0
	for _, r := range reads {
		if r.err == nil {
			ok++
		}
	}
	if ok == 0 {
		return tools.Result{IsError: true, Content: fmt.Sprintf(
			"binance_mtf_klines: all timeframes failed for %s (e.g. %v)", in.Symbol, reads[0].err)}, nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s multi-timeframe read:\n", in.Symbol)
	for _, r := range reads {
		if r.err != nil {
			fmt.Fprintf(&b, "[%s] data unavailable (%v) — treated as NEUTRAL\n", r.interval, r.err)
			continue
		}
		fmt.Fprintf(&b, "[%s] %s → %s\n", r.interval, r.summary, r.dir)
	}
	fmt.Fprintf(&b, "Cross-TF: %s\n", crossTimeframeVerdict(reads))

	// PRD-016: classify the market regime from the 4h candles (slow-moving,
	// structural) so the Analyst knows which strategy type the regime favours.
	// PRD-021 §2: stash the regime per symbol so the orchestrator can record it
	// in the round log (the post-mortem tool attributes trades to their regime).
	if fourH := reads[2]; fourH.err == nil && fourH.interval == "4h" && len(fourH.ks) > 0 {
		if _, ok := binance.ADX(fourH.ks, 14); ok {
			recordRegime(in.Symbol, strategy.DetectRegime(fourH.ks).String())
		}
		if line := regimeLine(fourH.ks); line != "" {
			fmt.Fprintf(&b, "%s\n", line)
		}
	}

	// PRD-017: run the full (calibrated + regime-weighted) strategy engine on
	// EVERY timeframe's candles, then combine into one cross-timeframe vote where
	// higher timeframes dominate (5m×1.0, 1h×1.5, 4h×2.0). This replaces the old
	// 5m-only consensus with a quantitative MTF signal the Analyst treats as
	// primary. (Only 4h has enough candles for ADX, so lower TFs are calibrated
	// but regime-transitional — see PRD-016.)
	consensusByTF := make(map[string]strategy.Consensus, len(reads))
	for _, r := range reads {
		if r.err == nil && len(r.ks) > 0 {
			consensusByTF[r.interval] = strategy.ConsensusForWithRegime(in.Symbol, r.ks)
		}
	}
	if len(consensusByTF) > 0 {
		mtf := strategy.AggregateMTF(consensusByTF)
		fmt.Fprintf(&b, "MTF Strategy: %s %s\n", mtf.Direction, mtf.Summary)
	}

	// PRD-013: cache the 5m frame (the divergence strategy's reference series)
	// and append a cross-symbol divergence hint when this symbol is moving
	// decisively against a flat BTC anchor.
	if fiveMin := reads[0]; fiveMin.err == nil && fiveMin.interval == "5m" {
		cacheKlines(in.Symbol, fiveMin.ks)
		if hint := divergenceHint(in.Symbol, fiveMin.ks); hint != "" {
			fmt.Fprintf(&b, "%s\n", hint)
		}
	}
	return tools.Result{Content: b.String()}, nil
}

// FetchMTF runs the multi-timeframe read for one symbol and returns the
// formatted output string (the same text the LLM would see from the tool).
// The orchestrator calls this for every symbol before the Analyst runs so the
// LLM never needs to call binance_mtf_klines — the data is pre-loaded into
// the round prompt, cutting 7 of the most expensive tool calls per round.
func FetchMTF(ctx context.Context, symbol string) (string, error) {
	var t BinanceMTFKlinesTool
	raw, _ := json.Marshal(binanceMTFKlinesInput{Symbol: symbol})
	res, err := t.Execute(ctx, slog.Default(), raw)
	if err != nil {
		return "", err
	}
	if res.IsError {
		return "", fmt.Errorf("mtf_klines: %s", res.Content)
	}
	return res.Content, nil
}

// regimeLine renders the PRD-016 regime classification for a candle series:
// "Regime: TRENDING (ADX 34.2) → favoring momentum/breakout/ema_cross." Returns
// "" when there are too few candles for ADX (regime is then undetermined).
func regimeLine(ks []binance.Kline) string {
	adx, ok := binance.ADX(ks, 14)
	if !ok {
		return ""
	}
	r := strategy.DetectRegime(ks)
	return fmt.Sprintf("Regime: %s (ADX %.1f) → %s.", r, adx, r.Favors())
}

// crossTimeframeVerdict reduces the per-timeframe directions (ordered LOW →
// HIGH) to an ALIGNED / CONFLICT / NO-EDGE verdict. On conflict the highest
// timeframe with a non-neutral direction dominates.
func crossTimeframeVerdict(reads []tfRead) string {
	hasBull, hasBear := false, false
	for _, r := range reads {
		switch r.dir {
		case binance.DirectionBullish:
			hasBull = true
		case binance.DirectionBearish:
			hasBear = true
		}
	}

	switch {
	case hasBull && hasBear:
		// Dominant = highest timeframe (last in LOW→HIGH order) that is non-neutral.
		dom := binance.DirectionNeutral
		domTF := ""
		for _, r := range reads {
			if r.dir == binance.DirectionBullish || r.dir == binance.DirectionBearish {
				dom, domTF = r.dir, r.interval
			}
		}
		return fmt.Sprintf("CONFLICT — timeframes disagree; defer to the higher timeframe (%s %s dominates). Treat lower-TF setups against it as traps.", domTF, dom)
	case hasBull:
		return "ALIGNED BULLISH — timeframes agree; trade with the uptrend."
	case hasBear:
		return "ALIGNED BEARISH — timeframes agree; trade with the downtrend."
	default:
		return "NO-EDGE — no clear direction on any timeframe; prefer to wait."
	}
}
