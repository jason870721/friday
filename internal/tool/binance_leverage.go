package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"github.com/johnny1110/evva/pkg/tools"
	"github.com/johnny1110/friday/internal/binance"
)

// maxLeverages holds the per-symbol max leverage (from leverageBracket),
// installed at startup by SetMaxLeverages. binance_leverage clamps requested
// leverage to this so an over-cap request can't be rejected with -4028
// (PRD-012). leverageBrackets holds the full notional→leverage tier table for
// the per-order notional clamp (PRD-019). Both empty when unset → no clamp.
var (
	maxLevMu         sync.RWMutex
	maxLeverages     = map[string]int{}
	leverageBrackets = map[string][]binance.LeverageBracket{}
)

// SetMaxLeverages installs the per-symbol leverage caps (called from bootstrap
// after the leverageBracket preflight).
func SetMaxLeverages(m map[string]int) {
	maxLevMu.Lock()
	defer maxLevMu.Unlock()
	maxLeverages = m
}

// SetLeverageBrackets installs the per-symbol notional→leverage tier tables
// (called from bootstrap after the leverageBracket preflight). These let
// binance_order keep a position's notional inside the tier its leverage allows,
// so an order can't be rejected with -2027 (PRD-019).
func SetLeverageBrackets(m map[string][]binance.LeverageBracket) {
	maxLevMu.Lock()
	defer maxLevMu.Unlock()
	leverageBrackets = m
}

// maxLeverageFor returns the cap for a symbol and whether one is known.
func maxLeverageFor(symbol string) (int, bool) {
	maxLevMu.RLock()
	defer maxLevMu.RUnlock()
	v, ok := maxLeverages[symbol]
	return v, ok
}

// maxLeverageForNotional returns the highest leverage permitted for a position
// of the given notional on the symbol (the tier its notional falls into), and
// whether the symbol's bracket table is known.
func maxLeverageForNotional(symbol string, notional float64) (int, bool) {
	maxLevMu.RLock()
	defer maxLevMu.RUnlock()
	bs, ok := leverageBrackets[symbol]
	if !ok {
		return 0, false
	}
	return binance.MaxLeverageForNotional(bs, notional)
}

const BinanceLeverageToolName tools.ToolName = "binance_leverage"

const binanceLeverageDescription = `Set leverage for a Binance Futures symbol.

Call before opening a position when you want a leverage different from
your current setting on that symbol. Setting leverage on a symbol with
an open position changes the margin requirement immediately — be careful.

Max leverage is PER-SYMBOL (BTC/ETH allow 100x+, TradFi stock perps cap at
~10x). A request above a symbol's max is automatically clamped down to that max
(so it never fails with -4028). Higher leverage tightens the liquidation
distance proportionally.`

const binanceLeverageSchema = `{
	"type": "object",
	"additionalProperties": false,
	"required": ["symbol", "leverage"],
	"properties": {
		"symbol":   {"type": "string", "description": "Binance Futures symbol, e.g. BTCUSDT."},
		"leverage": {"type": "integer", "minimum": 1, "maximum": 125, "description": "Leverage multiplier (1-125). For this experiment, 1-100."}
	}
}`

type BinanceLeverageTool struct{}

func NewBinanceLeverage() *BinanceLeverageTool { return &BinanceLeverageTool{} }

func (BinanceLeverageTool) Name() string            { return string(BinanceLeverageToolName) }
func (BinanceLeverageTool) Description() string     { return binanceLeverageDescription }
func (BinanceLeverageTool) Schema() json.RawMessage { return json.RawMessage(binanceLeverageSchema) }

type binanceLeverageInput struct {
	Symbol   string `json:"symbol"`
	Leverage int    `json:"leverage"`
}

func (BinanceLeverageTool) Execute(ctx context.Context, logger *slog.Logger, raw json.RawMessage) (tools.Result, error) {
	var in binanceLeverageInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("binance_leverage: decode input: %v", err)}, nil
	}
	if in.Symbol == "" {
		return tools.Result{IsError: true, Content: "binance_leverage: symbol is required"}, nil
	}
	if in.Leverage < 1 || in.Leverage > 125 {
		return tools.Result{IsError: true, Content: fmt.Sprintf("binance_leverage: leverage=%d out of range [1,125]", in.Leverage)}, nil
	}

	// Paper-trading mode (PRD-021 §4): record on the virtual book, no API call.
	if globalPaper != nil {
		lev := in.Leverage
		if max, ok := maxLeverageFor(in.Symbol); ok && lev > max {
			lev = max
		}
		globalPaper.SetLeverage(in.Symbol, float64(lev))
		logger.Info("binance_leverage.paper", "symbol", in.Symbol, "leverage", lev)
		return tools.Result{Content: fmt.Sprintf("PAPER: would have set %s leverage to %dx (no real change).", in.Symbol, lev)}, nil
	}

	cli, err := sharedBinanceClient()
	if err != nil {
		return tools.Result{IsError: true, Content: err.Error()}, nil
	}

	// PRD-012: clamp to the symbol's known max so an over-cap request (e.g. 100x
	// on a 10x stock perp) is corrected instead of rejected with -4028.
	lev := in.Leverage
	var clampNote string
	if max, ok := maxLeverageFor(in.Symbol); ok && lev > max {
		logger.Info("binance_leverage.clamped", "symbol", in.Symbol, "requested", in.Leverage, "max", max)
		clampNote = fmt.Sprintf(" (clamped from %dx — %s max is %dx)", in.Leverage, in.Symbol, max)
		lev = max
	}

	logger.Debug("binance_leverage.dispatch", "symbol", in.Symbol, "leverage", lev)

	r, err := cli.SetLeverage(ctx, in.Symbol, lev)
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("binance_leverage: %v", err)}, nil
	}
	return tools.Result{Content: fmt.Sprintf("%s leverage set to %dx%s (maxNotional=%s)", r.Symbol, r.Leverage, clampNote, r.MaxNotionalValue)}, nil
}
