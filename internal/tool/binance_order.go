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

// guardrailMaxMarginPct is the hard ceiling the pre-trade guardrail
// enforces: an opening order's margin may not exceed this fraction of the
// wallet balance. 15% mirrors the per-position cap documented in the
// system prompt — enforced here in code so the model can't talk past it.
const guardrailMaxMarginPct = 0.15

// orderValidator is the process-wide pre-trade guardrail (PRD-002).
var orderValidator = risk.NewMarginCapValidator(guardrailMaxMarginPct)

const BinanceOrderToolName tools.ToolName = "binance_order"

const binanceOrderDescription = `Place a MARKET order on Binance USDⓈ-M Futures.

side = BUY  → opens or increases a LONG position (or closes a SHORT)
side = SELL → opens or increases a SHORT position (or closes a LONG)

quantity is in the base asset (e.g. 0.002 BTC, 0.1 SOL). Round DOWN to
the symbol's step size before calling. A quantity of 0 will fail.

Optionally pass reduce_only=true to guarantee the order can only close
or reduce an existing position — useful when you want to flatten without
risk of accidentally flipping direction.

Position-sizing formula:
  quantity = (margin_usdt × leverage) / mark_price

Always call binance_leverage first if changing leverage on this symbol.`

const binanceOrderSchema = `{
	"type": "object",
	"additionalProperties": false,
	"required": ["symbol", "side", "quantity"],
	"properties": {
		"symbol":      {"type": "string", "description": "Binance Futures symbol, e.g. BTCUSDT."},
		"side":        {"type": "string", "enum": ["BUY", "SELL"], "description": "BUY = long / close short; SELL = short / close long."},
		"quantity":    {"type": "number", "exclusiveMinimum": 0, "description": "Quantity in base asset, e.g. 0.002. Must respect symbol step size."},
		"reduce_only": {"type": "boolean", "default": false, "description": "If true, order can only reduce/close an existing position."}
	}
}`

type BinanceOrderTool struct{}

func NewBinanceOrder() *BinanceOrderTool { return &BinanceOrderTool{} }

func (BinanceOrderTool) Name() string            { return string(BinanceOrderToolName) }
func (BinanceOrderTool) Description() string     { return binanceOrderDescription }
func (BinanceOrderTool) Schema() json.RawMessage { return json.RawMessage(binanceOrderSchema) }

type binanceOrderInput struct {
	Symbol     string  `json:"symbol"`
	Side       string  `json:"side"`
	Quantity   float64 `json:"quantity"`
	ReduceOnly bool    `json:"reduce_only,omitempty"`
}

func (BinanceOrderTool) Execute(ctx context.Context, logger *slog.Logger, raw json.RawMessage) (tools.Result, error) {
	var in binanceOrderInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("binance_order: decode input: %v", err)}, nil
	}
	if in.Symbol == "" {
		return tools.Result{IsError: true, Content: "binance_order: symbol is required"}, nil
	}
	side := binance.OrderSide(strings.ToUpper(in.Side))
	if side != binance.SideBuy && side != binance.SideSell {
		return tools.Result{IsError: true, Content: fmt.Sprintf("binance_order: side=%q must be BUY or SELL", in.Side)}, nil
	}
	if in.Quantity <= 0 {
		return tools.Result{IsError: true, Content: fmt.Sprintf("binance_order: quantity=%g must be > 0", in.Quantity)}, nil
	}

	// Paper-trading mode (PRD-021 §4): no real order. Fetch the mark (real
	// market data), update the virtual book, and report what WOULD have happened.
	// The exchange's account endpoints are never touched.
	if globalPaper != nil {
		return paperOrder(ctx, logger, in, side)
	}

	// Circuit breaker (PRD-005): session-level gate, BEFORE the per-trade
	// margin guardrail. Blocks new entries when the session is paused/halted.
	// Reduce-only closes always bypass — flattening risk is never blocked.
	if !in.ReduceOnly && globalBreaker != nil {
		if berr := globalBreaker.Check(); berr != nil {
			logger.Info("binance_order.breaker_blocked", "symbol", in.Symbol, "reason", berr.Error())
			return tools.Result{IsError: true, Content: berr.Error()}, nil
		}
	}

	cli, err := sharedBinanceClient()
	if err != nil {
		return tools.Result{IsError: true, Content: err.Error()}, nil
	}

	logger.Debug("binance_order.dispatch",
		"symbol", in.Symbol, "side", side, "quantity", in.Quantity, "reduce_only", in.ReduceOnly)

	// Pre-trade guardrail (PRD-002): pull the live snapshot and run the
	// margin-cap validator BEFORE the order reaches Binance. A breach
	// blocks the order and tells the model to recalculate. If the snapshot
	// can't be fetched we fail open (the LLM-level caps still apply) but
	// note the degradation in the result.
	var guardNote string
	acct, snapErr := orderGuardSnapshot(ctx, cli, in.Symbol)
	if snapErr != nil {
		logger.Warn("binance_order.guardrail_snapshot_failed", "symbol", in.Symbol, "err", snapErr)
		guardNote = fmt.Sprintf("[guardrail skipped: could not fetch snapshot: %v] ", snapErr)
	}

	// Notional leverage clamp (PRD-019): Binance caps a position's notional by
	// leverage tier — the highest leverage only covers the smallest notional.
	// An OPEN whose notional (qty × mark) exceeds the tier the currently-set
	// leverage allows is rejected with -2027. The PRD-012 clamp only stopped
	// over-cap leverage (-4028); it can't see notional. Here we know it, so we
	// auto-LOWER leverage to the tier this notional fits BEFORE the order — the
	// position then opens at a valid leverage instead of failing. Reduce-only
	// closes are exempt (they shrink the position, never trip the cap). The
	// guardrail below re-validates margin at the lowered leverage, so a position
	// too big to fit both the tier and the 15% margin cap is cleanly rejected.
	if !in.ReduceOnly && snapErr == nil && acct.MarkPrice > 0 {
		notional := in.Quantity * acct.MarkPrice
		if maxLev, ok := maxLeverageForNotional(in.Symbol, notional); ok && acct.Leverage > float64(maxLev) {
			logger.Info("binance_order.leverage_lowered_for_notional",
				"symbol", in.Symbol, "notional", notional, "from", acct.Leverage, "to", maxLev)
			if _, lerr := cli.SetLeverage(ctx, in.Symbol, maxLev); lerr != nil {
				logger.Warn("binance_order.leverage_lower_failed", "symbol", in.Symbol, "err", lerr)
				guardNote += fmt.Sprintf("[warn: could not lower leverage to %dx for $%.0f notional: %v] ", maxLev, notional, lerr)
			} else {
				guardNote += fmt.Sprintf("[leverage auto-lowered to %dx so $%.0f notional fits the bracket cap (avoids -2027)] ", maxLev, notional)
				acct.Leverage = float64(maxLev)
			}
		}
	}

	// Fee-budget guardrail (PRD-020 §3): block a new OPENING when fee spend over
	// the rolling window has exceeded the cap (anti-overtrading). Needs the live
	// balance, so it runs after the snapshot. Reduce-only closes bypass.
	if !in.ReduceOnly && snapErr == nil && globalFeeBudget != nil {
		if ferr := globalFeeBudget.Check(acct.WalletBalance); ferr != nil {
			logger.Info("binance_order.fee_budget_blocked", "symbol", in.Symbol, "reason", ferr.Error())
			return tools.Result{IsError: true, Content: ferr.Error()}, nil
		}
	}

	ro := risk.Order{
		Symbol:     in.Symbol,
		Side:       in.Side,
		Quantity:   in.Quantity,
		ReduceOnly: in.ReduceOnly,
	}

	// Portfolio-group guardrail (PRD-020 §4): block an OPENING that pushes a
	// correlated group's COMBINED margin over its cap. Needs the margin already
	// committed by the group's OTHER open positions, so fetch them and sum.
	if !in.ReduceOnly && snapErr == nil && globalPortfolioValidator != nil {
		if _, cfg, ok := globalPortfolioValidator.Limits.GroupFor(in.Symbol); ok {
			acct.GroupUsedMargin = groupUsedMargin(ctx, cli, cfg, in.Symbol, logger)
			if perr := globalPortfolioValidator.Validate(ro, acct); perr != nil {
				logger.Info("binance_order.portfolio_blocked", "symbol", in.Symbol, "reason", perr.Error())
				return tools.Result{IsError: true, Content: perr.Error()}, nil
			}
		}
	}

	if snapErr == nil {
		if verr := orderValidator.Validate(ro, acct); verr != nil {
			logger.Info("binance_order.guardrail_blocked", "symbol", in.Symbol, "reason", verr.Error())
			return tools.Result{IsError: true, Content: verr.Error()}, nil
		}
	}

	ord, err := cli.MarketOrder(ctx, in.Symbol, side, in.Quantity, in.ReduceOnly)
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("binance_order: %v", err)}, nil
	}
	return tools.Result{Content: guardNote + binance.FormatOrder(ord)}, nil
}

// groupUsedMargin sums the margin (|notional| ÷ leverage) committed by the
// OTHER open positions in a correlated group (PRD-020 §4) — the exposure the
// PortfolioGroupValidator adds to this order's margin before checking the group
// cap. The order's own symbol is excluded (its fresh margin is added by the
// validator). Best-effort: a fetch failure logs and returns 0 (the group gate
// then only sees this order, never over-counting).
func groupUsedMargin(ctx context.Context, cli *binance.Client, cfg risk.GroupConfig, excludeSymbol string, logger *slog.Logger) float64 {
	members := make(map[string]bool, len(cfg.Symbols))
	for _, s := range cfg.Members(excludeSymbol) {
		members[s] = true
	}
	if len(members) == 0 {
		return 0
	}
	open, err := cli.OpenPositions(ctx)
	if err != nil {
		logger.Warn("binance_order.group_exposure_failed", "err", err)
		return 0
	}
	var used float64
	for _, p := range open {
		if !members[p.Symbol] {
			continue
		}
		amt, _ := strconv.ParseFloat(p.PositionAmt, 64)
		mark, _ := strconv.ParseFloat(p.MarkPrice, 64)
		lev, _ := strconv.ParseFloat(p.Leverage, 64)
		notional := abs(amt) * mark
		if lev > 0 {
			used += notional / lev
		} else {
			used += notional
		}
	}
	return used
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// paperOrder applies a market order to the virtual book at the live mark price
// (PRD-021 §4) — market data is real, the fill is virtual. Reduce-only reduces
// the position (realising virtual PnL); otherwise it opens/adds.
func paperOrder(ctx context.Context, logger *slog.Logger, in binanceOrderInput, side binance.OrderSide) (tools.Result, error) {
	cli, err := sharedBinanceClient()
	if err != nil {
		return tools.Result{IsError: true, Content: err.Error()}, nil
	}
	mp, err := cli.Price(ctx, in.Symbol)
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("binance_order (paper): price: %v", err)}, nil
	}
	mark, _ := strconv.ParseFloat(mp.MarkPrice, 64)
	if mark <= 0 {
		return tools.Result{IsError: true, Content: fmt.Sprintf("binance_order (paper): no mark price for %s", in.Symbol)}, nil
	}
	realised := globalPaper.Trade(in.Symbol, string(side), in.Quantity, mark, in.ReduceOnly)
	logger.Info("binance_order.paper", "symbol", in.Symbol, "side", side, "qty", in.Quantity,
		"mark", mark, "reduce_only", in.ReduceOnly, "realised", realised)
	msg := fmt.Sprintf("PAPER: would have placed %s %s qty=%g @ ~%.4f (no real order).", in.Symbol, side, in.Quantity, mark)
	if in.ReduceOnly {
		msg += fmt.Sprintf(" Virtual realised PnL %+.4f USDT. Virtual balance now %.2f.", realised, globalPaper.Balance())
	}
	return tools.Result{Content: msg}, nil
}

// orderGuardSnapshot pulls the live wallet balance, mark price, and
// configured leverage the pre-trade guardrail needs to judge an order.
// Any fetch failure is returned so the caller can fail open and flag it.
func orderGuardSnapshot(ctx context.Context, cli *binance.Client, symbol string) (risk.Account, error) {
	bal, err := cli.USDTBalance(ctx)
	if err != nil {
		return risk.Account{}, fmt.Errorf("balance: %w", err)
	}
	walletBalance, _ := strconv.ParseFloat(bal.Balance, 64)

	mp, err := cli.Price(ctx, symbol)
	if err != nil {
		return risk.Account{}, fmt.Errorf("price: %w", err)
	}
	markPrice, _ := strconv.ParseFloat(mp.MarkPrice, 64)

	// Leverage is best-effort: positionRisk returns the configured
	// leverage per symbol even when flat. A miss leaves Leverage=0, which
	// makes the validator treat margin as full notional (conservative).
	var leverage float64
	if pos, perr := cli.Positions(ctx, symbol); perr == nil && len(pos) > 0 {
		leverage, _ = strconv.ParseFloat(pos[0].Leverage, 64)
	}

	return risk.Account{
		WalletBalance: walletBalance,
		MarkPrice:     markPrice,
		Leverage:      leverage,
	}, nil
}
