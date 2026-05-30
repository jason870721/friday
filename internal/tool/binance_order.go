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
	} else if verr := orderValidator.Validate(risk.Order{
		Symbol:     in.Symbol,
		Side:       in.Side,
		Quantity:   in.Quantity,
		ReduceOnly: in.ReduceOnly,
	}, acct); verr != nil {
		logger.Info("binance_order.guardrail_blocked", "symbol", in.Symbol, "reason", verr.Error())
		return tools.Result{IsError: true, Content: verr.Error()}, nil
	}

	ord, err := cli.MarketOrder(ctx, in.Symbol, side, in.Quantity, in.ReduceOnly)
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("binance_order: %v", err)}, nil
	}
	return tools.Result{Content: guardNote + binance.FormatOrder(ord)}, nil
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
