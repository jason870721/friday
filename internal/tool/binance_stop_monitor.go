package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"

	"github.com/johnny1110/evva/pkg/tools"
	"github.com/johnny1110/friday/internal/binance"
	"github.com/johnny1110/friday/internal/risk"
)

// nativeStopOrders tracks the server-side STOP_MARKET / TAKE_PROFIT_MARKET order
// IDs friday placed per symbol (PRD-020 §2), so they can be cancelled when the
// position closes or the level is replaced. In-memory — paired with the
// exchange-side orders, which DO survive a restart (startup orphan cleanup in
// main reconciles any left behind).
type nativeStopPair struct {
	stopOrderID int64
	tpOrderID   int64
}

var (
	nativeStopMu     sync.Mutex
	nativeStopOrders = map[string]nativeStopPair{}
)

// globalStopMonitor is the process-wide StopMonitor (PRD-009), installed at
// startup by main via SetStopMonitor. nil when the monitor isn't running
// (e.g. no Binance credentials).
var globalStopMonitor *risk.StopMonitor

// SetStopMonitor installs the shared monitor the binance_stop_monitor tool
// registers levels with.
func SetStopMonitor(m *risk.StopMonitor) { globalStopMonitor = m }

// SharedBinanceClient exposes the process-wide Binance client so main can build
// the StopMonitor on the SAME client the trading tools use.
func SharedBinanceClient() (*binance.Client, error) { return sharedBinanceClient() }

// binanceStopBroker adapts *binance.Client to risk.StopBroker.
type binanceStopBroker struct{ cli *binance.Client }

// NewBinanceStopBroker wraps a client as the broker the StopMonitor drives. In
// paper-trading mode (PRD-021 §4) it returns a broker that reads the real mark
// price but flattens against the virtual book instead of the exchange.
func NewBinanceStopBroker(cli *binance.Client) risk.StopBroker {
	if globalPaper != nil {
		return paperStopBroker{cli: cli, paper: globalPaper}
	}
	return binanceStopBroker{cli: cli}
}

// paperStopBroker drives the StopMonitor in paper mode: real mark price, virtual
// close.
type paperStopBroker struct {
	cli   *binance.Client
	paper *risk.PaperPortfolio
}

func (b paperStopBroker) MarkPrice(ctx context.Context, symbol string) (float64, error) {
	mp, err := b.cli.Price(ctx, symbol)
	if err != nil {
		return 0, err
	}
	return strconv.ParseFloat(mp.MarkPrice, 64)
}

func (b paperStopBroker) CloseReduceOnly(ctx context.Context, symbol string, qty float64, positionSide string) error {
	// Close at the live mark so the virtual realised PnL is realistic.
	mark := 0.0
	if mp, err := b.cli.Price(ctx, symbol); err == nil {
		mark, _ = strconv.ParseFloat(mp.MarkPrice, 64)
	}
	side := "SELL"
	if positionSide == risk.DirShort {
		side = "BUY"
	}
	if mark > 0 {
		b.paper.Trade(symbol, side, qty, mark, true)
		return nil
	}
	return b.paper.CloseReduceOnly(ctx, symbol, qty, positionSide)
}

func (b binanceStopBroker) MarkPrice(ctx context.Context, symbol string) (float64, error) {
	mp, err := b.cli.Price(ctx, symbol)
	if err != nil {
		return 0, err
	}
	return strconv.ParseFloat(mp.MarkPrice, 64)
}

func (b binanceStopBroker) CloseReduceOnly(ctx context.Context, symbol string, qty float64, positionSide string) error {
	side := binance.SideSell // flatten a long
	if positionSide == risk.DirShort {
		side = binance.SideBuy // flatten a short
	}
	_, err := b.cli.MarketOrder(ctx, symbol, side, qty, true)
	return err
}

const BinanceStopMonitorToolName tools.ToolName = "binance_stop_monitor"

const binanceStopMonitorDescription = `Register (or clear) a stop-loss / take-profit level for a symbol — DUAL protection (PRD-009 + PRD-020 §2).

This does TWO things at once:
  1. Places server-side native STOP_MARKET (and TAKE_PROFIT_MARKET) orders on
     Binance — these execute on the exchange and SURVIVE a friday crash/restart.
  2. Registers the same levels with the in-memory StopMonitor, which polls mark
     price ~every second and fires a reduce-only close within ~1s — a fast
     backstop in case the native order was rejected for a transient reason.

Call this RIGHT AFTER an OPEN fills, using the stop_loss the Risk Manager
computed (and take_profit if given). Calling again for the same symbol REPLACES
the prior native orders (the old ones are cancelled first). To clear manually
(e.g. after you close a position yourself), call again with both stop_price and
take_profit_price set to 0 — this cancels the native orders and clears the level.`

const binanceStopMonitorSchema = `{
	"type": "object",
	"additionalProperties": false,
	"required": ["symbol", "position_side", "quantity"],
	"properties": {
		"symbol":            {"type": "string", "description": "Symbol of the open position, e.g. BTCUSDT."},
		"position_side":     {"type": "string", "enum": ["LONG", "SHORT"], "description": "Side of the position to protect."},
		"quantity":          {"type": "number", "description": "Position size to close on breach (base asset)."},
		"stop_price":        {"type": "number", "description": "Stop-loss mark price (0 = none)."},
		"take_profit_price": {"type": "number", "description": "Take-profit mark price (0 = none)."}
	}
}`

type BinanceStopMonitorTool struct{}

func NewBinanceStopMonitor() *BinanceStopMonitorTool { return &BinanceStopMonitorTool{} }

func (BinanceStopMonitorTool) Name() string        { return string(BinanceStopMonitorToolName) }
func (BinanceStopMonitorTool) Description() string { return binanceStopMonitorDescription }
func (BinanceStopMonitorTool) Schema() json.RawMessage {
	return json.RawMessage(binanceStopMonitorSchema)
}

type binanceStopMonitorInput struct {
	Symbol          string  `json:"symbol"`
	PositionSide    string  `json:"position_side"`
	Quantity        float64 `json:"quantity"`
	StopPrice       float64 `json:"stop_price"`
	TakeProfitPrice float64 `json:"take_profit_price"`
}

func (BinanceStopMonitorTool) Execute(ctx context.Context, logger *slog.Logger, raw json.RawMessage) (tools.Result, error) {
	var in binanceStopMonitorInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("binance_stop_monitor: decode input: %v", err)}, nil
	}
	if in.Symbol == "" {
		return tools.Result{IsError: true, Content: "binance_stop_monitor: symbol is required"}, nil
	}
	if globalStopMonitor == nil {
		return tools.Result{IsError: true, Content: "binance_stop_monitor: stop monitor is not running (no Binance credentials?)"}, nil
	}

	levels := risk.StopLevels{
		StopPrice:    in.StopPrice,
		TakeProfit:   in.TakeProfitPrice,
		PositionQty:  in.Quantity,
		PositionSide: in.PositionSide,
	}
	globalStopMonitor.SetLevels(in.Symbol, levels)

	// Mirror StopMonitor.active(): a level with no size or no stop/TP clears.
	arming := in.Quantity > 0 && (in.StopPrice > 0 || in.TakeProfitPrice > 0) &&
		(in.PositionSide == risk.DirLong || in.PositionSide == risk.DirShort)

	// PRD-020 §2: always cancel any prior native orders first (replace-or-clear),
	// then place fresh ones when arming. Best-effort — a native failure leaves the
	// local monitor as the backstop; surface it as a note, not a hard error.
	// PRD-021 §4: in paper mode there is no exchange order — the local monitor
	// (driven by the paper broker) is the only stop; skip the native legs.
	var nativeNote string
	if globalPaper != nil {
		nativeNote = " [PAPER: virtual stop — local monitor only, no exchange order]"
	} else if cli, cerr := sharedBinanceClient(); cerr != nil {
		nativeNote = fmt.Sprintf(" [native stops unavailable: %v]", cerr)
	} else {
		cancelNativeStops(ctx, cli, logger, in.Symbol)
		if arming {
			nativeNote = placeNativeStops(ctx, cli, logger, in)
		}
	}

	if !arming {
		logger.Debug("binance_stop_monitor.cleared", "symbol", in.Symbol)
		return tools.Result{Content: fmt.Sprintf("Cleared stop/TP levels for %s (monitor now watching %d symbol(s)).%s", in.Symbol, globalStopMonitor.Active(), nativeNote)}, nil
	}
	logger.Debug("binance_stop_monitor.armed", "symbol", in.Symbol, "side", in.PositionSide,
		"stop", in.StopPrice, "tp", in.TakeProfitPrice, "qty", in.Quantity)
	return tools.Result{Content: fmt.Sprintf(
		"Monitoring %s %s qty=%g: stop=%g take_profit=%g. Native exchange orders placed (survive restarts) + the background monitor will close within ~1s of a breach.%s",
		in.Symbol, in.PositionSide, in.Quantity, in.StopPrice, in.TakeProfitPrice, nativeNote)}, nil
}

// closeSideFor returns the order side that FLATTENS a position of the given
// side: a LONG is closed by SELL, a SHORT by BUY.
func closeSideFor(positionSide string) binance.OrderSide {
	if positionSide == risk.DirShort {
		return binance.SideBuy
	}
	return binance.SideSell
}

// placeNativeStops places the server-side STOP_MARKET (and TAKE_PROFIT_MARKET)
// reduce-only orders for an armed position and records their IDs so they can be
// cancelled later. Returns a human-readable note (errors included, non-fatal).
func placeNativeStops(ctx context.Context, cli *binance.Client, logger *slog.Logger, in binanceStopMonitorInput) string {
	side := closeSideFor(in.PositionSide)
	var pair nativeStopPair
	var notes []string

	if in.StopPrice > 0 {
		if ord, err := cli.StopMarketOrder(ctx, in.Symbol, side, in.Quantity, in.StopPrice, true); err != nil {
			logger.Warn("binance_stop_monitor.native_stop_failed", "symbol", in.Symbol, "err", err)
			notes = append(notes, fmt.Sprintf("native STOP_MARKET rejected (%v) — local monitor still armed", err))
		} else {
			pair.stopOrderID = ord.OrderID
		}
	}
	if in.TakeProfitPrice > 0 {
		if ord, err := cli.TakeProfitMarketOrder(ctx, in.Symbol, side, in.Quantity, in.TakeProfitPrice, true); err != nil {
			logger.Warn("binance_stop_monitor.native_tp_failed", "symbol", in.Symbol, "err", err)
			notes = append(notes, fmt.Sprintf("native TAKE_PROFIT_MARKET rejected (%v)", err))
		} else {
			pair.tpOrderID = ord.OrderID
		}
	}

	nativeStopMu.Lock()
	nativeStopOrders[in.Symbol] = pair
	nativeStopMu.Unlock()

	if len(notes) > 0 {
		return " [" + strings.Join(notes, "; ") + "]"
	}
	return ""
}

// cancelNativeStops cancels and forgets any native orders friday recorded for a
// symbol. Best-effort: a cancel failure (e.g. the order already filled) is
// logged and ignored — the order is gone either way.
func cancelNativeStops(ctx context.Context, cli *binance.Client, logger *slog.Logger, symbol string) {
	nativeStopMu.Lock()
	pair, ok := nativeStopOrders[symbol]
	delete(nativeStopOrders, symbol)
	nativeStopMu.Unlock()
	if !ok {
		return
	}
	for _, id := range []int64{pair.stopOrderID, pair.tpOrderID} {
		if id == 0 {
			continue
		}
		if err := cli.CancelOrder(ctx, symbol, id); err != nil {
			logger.Debug("binance_stop_monitor.native_cancel_failed", "symbol", symbol, "orderId", id, "err", err)
		}
	}
}

// CleanupOrphanStops cancels any server-side STOP_MARKET / TAKE_PROFIT_MARKET
// orders left over from a previous session whose position no longer exists
// (PRD-020 §2 R5). Called once at startup. Best-effort and fully logged; never
// fatal — market data and trading work regardless.
func CleanupOrphanStops(ctx context.Context, logger *slog.Logger) {
	cli, err := sharedBinanceClient()
	if err != nil {
		return // no credentials → nothing to reconcile
	}
	orders, err := cli.OpenOrders(ctx, "")
	if err != nil {
		logger.Warn("stop_monitor.orphan_scan_failed", "err", err)
		return
	}
	open, err := cli.OpenPositions(ctx)
	if err != nil {
		logger.Warn("stop_monitor.orphan_positions_failed", "err", err)
		return
	}
	hasPosition := make(map[string]bool, len(open))
	for _, p := range open {
		hasPosition[p.Symbol] = true
	}
	cancelled := 0
	for _, o := range orders {
		t := strings.ToUpper(o.Type)
		if t != "STOP_MARKET" && t != "TAKE_PROFIT_MARKET" {
			continue
		}
		if hasPosition[o.Symbol] {
			continue // a live position still wants this stop
		}
		if err := cli.CancelOrder(ctx, o.Symbol, o.OrderID); err != nil {
			logger.Warn("stop_monitor.orphan_cancel_failed", "symbol", o.Symbol, "orderId", o.OrderID, "err", err)
			continue
		}
		logger.Info("stop_monitor.orphan_cancelled", "symbol", o.Symbol, "orderId", o.OrderID, "type", t)
		cancelled++
	}
	if cancelled > 0 {
		logger.Info("stop_monitor.orphan_cleanup_done", "cancelled", cancelled)
	}
}
