package backtest

import (
	"github.com/johnny1110/friday/internal/binance"
	"github.com/johnny1110/friday/internal/strategy"
)

// tpSweepGrid is the coarse take-profit grid (% price move in favour) the
// PRD-020 §6 sweep evaluates per strategy.
var tpSweepGrid = []float64{0.5, 1.0, 1.5, 2.0, 3.0}

// BestTakeProfit sweeps the coarse take-profit grid for a strategy over a candle
// series and returns the TP% with the best total return (and that return). It
// replays the strategy's own entries, exiting whichever level the subsequent
// candles touch first: the TP% in favour, or the strategy's invalidation level
// against (the stop). Ties keep the SMALLER TP (bank the winner sooner). ok is
// false when the strategy never trades on these candles (no TP can be ranked).
//
// Pure and deterministic (no network/clock) so it is unit-testable, mirroring
// RunStrategy. The chosen TP% is informational — a calibration aid for choosing
// per-strategy targets — and is not fed back into the live signal in this PRD.
func BestTakeProfit(s strategy.Strategy, symbol string, candles []binance.Kline) (bestTP, bestReturn float64, ok bool) {
	for _, tp := range tpSweepGrid {
		res := runStrategyTP(s, symbol, candles, tp)
		if res.Trades == 0 {
			continue
		}
		if !ok || res.TotalReturnPct > bestReturn {
			ok = true
			bestTP = tp
			bestReturn = res.TotalReturnPct
		}
	}
	return bestTP, bestReturn, ok
}

// runStrategyTP is RunStrategy with a fixed take-profit overlay: a position
// exits at the FIRST of (a) the strategy's invalidation level against it, or
// (b) tpPct in favour of it, else marks to the last close. Leverage is 1.
func runStrategyTP(s strategy.Strategy, symbol string, candles []binance.Kline, tpPct float64) Result {
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
		pnlPct, exitIdx := simulateDirectionalTP(candles, i, entry, long, sig.Invalidation, tpPct)
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
	return res
}

// simulateDirectionalTP exits at the first candle whose range touches either the
// invalidation level (stop, checked first — conservative) or the take-profit
// (entry × (1 ± tpPct/100)), else marks to the last close. Unlevered PnL%.
func simulateDirectionalTP(candles []binance.Kline, entryIdx int, entry float64, long bool, invalidation, tpPct float64) (float64, int) {
	tp := tpPct / 100.0
	for j := entryIdx + 1; j < len(candles); j++ {
		hi, lo := candles[j].High, candles[j].Low
		if long {
			if invalidation > 0 && lo <= invalidation {
				return (invalidation - entry) / entry * 100, j
			}
			if hi >= entry*(1+tp) {
				return tp * 100, j
			}
		} else {
			if invalidation > 0 && hi >= invalidation {
				return (entry - invalidation) / entry * 100, j
			}
			if lo <= entry*(1-tp) {
				return tp * 100, j
			}
		}
	}
	last := candles[len(candles)-1].Close
	move := (last - entry) / entry
	if !long {
		move = -move
	}
	return move * 100, len(candles) - 1
}
