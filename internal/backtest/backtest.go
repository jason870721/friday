// Package backtest is friday's sandbox simulator (PRD-004). It replays a
// simple, structured strategy rule over historical candles and reports
// win rate / PnL / drawdown — so the agent can sanity-check a hypothesis
// before risking it live. It places NO orders and touches no account.
package backtest

import (
	"fmt"

	"github.com/johnny1110/friday/internal/binance"
	"github.com/johnny1110/friday/internal/strategy"
)

// Indicator selects the signal a Rule's entry condition tests.
type Indicator string

const (
	IndicatorRSI       Indicator = "RSI"        // RSI(14), 0-100
	IndicatorPriceVsMA Indicator = "PRICE_VS_MA" // (close-MA20)/MA20 as a percent
)

// Op is the comparison applied to the indicator value.
type Op string

const (
	OpLess    Op = "<"
	OpGreater Op = ">"
)

// Rule is a single deterministic strategy: when the entry condition holds
// and we are flat, open Direction; exit when price moves TakeProfitPct in
// favour or StopLossPct against (whichever the next candles hit first).
type Rule struct {
	Indicator     Indicator `json:"indicator"`
	Op            Op        `json:"op"`
	Value         float64   `json:"value"`
	Direction     string    `json:"direction"`       // LONG or SHORT
	TakeProfitPct float64   `json:"take_profit_pct"` // e.g. 1.5 = +1.5% price move
	StopLossPct   float64   `json:"stop_loss_pct"`   // e.g. 1.0 = -1.0% price move
	Leverage      float64   `json:"leverage"`        // multiplies the % move into PnL%
}

// Result is the summary of a backtest run.
type Result struct {
	Trades         int     `json:"trades"`
	Wins           int     `json:"wins"`
	WinRate        float64 `json:"win_rate"`         // 0-1
	AvgPnLPct      float64 `json:"avg_pnl_pct"`      // per-trade, leverage-applied
	TotalReturnPct float64 `json:"total_return_pct"` // sum of trade PnL%
	MaxDrawdownPct float64 `json:"max_drawdown_pct"` // worst peak-to-trough of cumulative return
}

// Validate checks the rule is runnable.
func (r Rule) Validate() error {
	switch r.Indicator {
	case IndicatorRSI, IndicatorPriceVsMA:
	default:
		return fmt.Errorf("unknown indicator %q", r.Indicator)
	}
	switch r.Op {
	case OpLess, OpGreater:
	default:
		return fmt.Errorf("unknown op %q", r.Op)
	}
	if r.Direction != "LONG" && r.Direction != "SHORT" {
		return fmt.Errorf("direction must be LONG or SHORT, got %q", r.Direction)
	}
	if r.TakeProfitPct <= 0 || r.StopLossPct <= 0 {
		return fmt.Errorf("take_profit_pct and stop_loss_pct must be > 0")
	}
	if r.Leverage <= 0 {
		r.Leverage = 1
	}
	return nil
}

// Run simulates rule over candles (oldest-first) and returns the summary.
// The indicator is evaluated on the close of each candle; an entry fills
// at that close, and the outcome is decided by scanning subsequent candle
// highs/lows for the take-profit or stop-loss level (stop checked first on
// any given candle — the conservative assumption).
func Run(rule Rule, candles []binance.Kline) (Result, error) {
	if err := rule.Validate(); err != nil {
		return Result{}, err
	}
	lev := rule.Leverage
	if lev <= 0 {
		lev = 1
	}

	var res Result
	var cumulative, peak, maxDD float64

	i := 0
	for i < len(candles) {
		sig, ok := indicatorAt(rule.Indicator, candles, i)
		if !ok || !triggered(rule.Op, sig, rule.Value) {
			i++
			continue
		}

		entry := candles[i].Close
		pnlPct, exitIdx := simulateTrade(rule, candles, i, entry, lev)
		res.Trades++
		if pnlPct > 0 {
			res.Wins++
		}
		res.TotalReturnPct += pnlPct

		cumulative += pnlPct
		if cumulative > peak {
			peak = cumulative
		}
		if dd := peak - cumulative; dd > maxDD {
			maxDD = dd
		}

		// Resume scanning after the trade closed (no overlapping trades).
		if exitIdx > i {
			i = exitIdx + 1
		} else {
			i++
		}
	}

	if res.Trades > 0 {
		res.WinRate = float64(res.Wins) / float64(res.Trades)
		res.AvgPnLPct = res.TotalReturnPct / float64(res.Trades)
	}
	res.MaxDrawdownPct = maxDD
	return res, nil
}

// RunStrategy replays a deterministic strategy.Strategy over a candle series
// (oldest-first) and reports its signal quality — win rate, avg/total PnL%, max
// drawdown (PRD-015). Unlike Run (which tests a rule with fixed TP/SL), it uses
// the STRATEGY'S OWN logic for entries and the strategy's Invalidation level as
// the exit/stop; a position with no invalidation hit is marked to the last
// close. Leverage is fixed at 1 — calibration measures raw signal quality, not
// levered returns. No overlapping trades (re-entry only after the prior exit).
func RunStrategy(s strategy.Strategy, symbol string, candles []binance.Kline) (Result, error) {
	if len(candles) == 0 {
		return Result{}, fmt.Errorf("RunStrategy: no candles")
	}

	var res Result
	var cumulative, peak, maxDD float64

	i := 0
	for i < len(candles) {
		sig := s.Analyze(symbol, candles[:i+1])
		if sig.Direction == strategy.Neutral {
			i++
			continue
		}

		entry := candles[i].Close
		long := sig.Direction == strategy.Long
		pnlPct, exitIdx := simulateDirectional(candles, i, entry, long, sig.Invalidation)
		res.Trades++
		if pnlPct > 0 {
			res.Wins++
		}
		res.TotalReturnPct += pnlPct

		cumulative += pnlPct
		if cumulative > peak {
			peak = cumulative
		}
		if dd := peak - cumulative; dd > maxDD {
			maxDD = dd
		}

		if exitIdx > i {
			i = exitIdx + 1
		} else {
			i++
		}
	}

	if res.Trades > 0 {
		res.WinRate = float64(res.Wins) / float64(res.Trades)
		res.AvgPnLPct = res.TotalReturnPct / float64(res.Trades)
	}
	res.MaxDrawdownPct = maxDD
	return res, nil
}

// simulateDirectional exits a position the first candle whose range touches the
// strategy's Invalidation level (stop), else marks to the last close. PnL% is
// unlevered (leverage = 1). An invalidation of 0 means "no stop" → always marks
// to the last close. Mirrors simulateTrade's conservative intrabar assumption.
func simulateDirectional(candles []binance.Kline, entryIdx int, entry float64, long bool, invalidation float64) (float64, int) {
	for j := entryIdx + 1; j < len(candles); j++ {
		if invalidation <= 0 {
			break
		}
		hi, lo := candles[j].High, candles[j].Low
		if long && lo <= invalidation {
			return (invalidation - entry) / entry * 100, j
		}
		if !long && hi >= invalidation {
			return (entry - invalidation) / entry * 100, j
		}
	}

	last := candles[len(candles)-1].Close
	move := (last - entry) / entry
	if !long {
		move = -move
	}
	return move * 100, len(candles) - 1
}

// simulateTrade returns the leverage-applied PnL% and the candle index the
// trade exited on. If neither TP nor SL is hit, the position is marked to
// the last candle's close.
func simulateTrade(rule Rule, candles []binance.Kline, entryIdx int, entry, lev float64) (float64, int) {
	tp := rule.TakeProfitPct / 100.0
	sl := rule.StopLossPct / 100.0
	long := rule.Direction == "LONG"

	for j := entryIdx + 1; j < len(candles); j++ {
		hi, lo := candles[j].High, candles[j].Low
		if long {
			// Stop checked first (conservative).
			if lo <= entry*(1-sl) {
				return -sl * 100 * lev, j
			}
			if hi >= entry*(1+tp) {
				return tp * 100 * lev, j
			}
		} else {
			if hi >= entry*(1+sl) {
				return -sl * 100 * lev, j
			}
			if lo <= entry*(1-tp) {
				return tp * 100 * lev, j
			}
		}
	}

	// Open at the end of data → mark to last close.
	last := candles[len(candles)-1].Close
	move := (last - entry) / entry
	if !long {
		move = -move
	}
	return move * 100 * lev, len(candles) - 1
}

func triggered(op Op, value, threshold float64) bool {
	switch op {
	case OpLess:
		return value < threshold
	case OpGreater:
		return value > threshold
	default:
		return false
	}
}

// indicatorAt computes the rule's indicator using candles up to and
// including index i. ok is false when there's insufficient history.
func indicatorAt(ind Indicator, candles []binance.Kline, i int) (float64, bool) {
	closes := closesUpTo(candles, i)
	switch ind {
	case IndicatorRSI:
		return binance.RSI(closes, 14)
	case IndicatorPriceVsMA:
		ma, ok := binance.SMA(closes, 20)
		if !ok || ma == 0 {
			return 0, false
		}
		return (candles[i].Close - ma) / ma * 100, true
	default:
		return 0, false
	}
}

func closesUpTo(candles []binance.Kline, i int) []float64 {
	out := make([]float64, 0, i+1)
	for j := 0; j <= i && j < len(candles); j++ {
		out = append(out, candles[j].Close)
	}
	return out
}
