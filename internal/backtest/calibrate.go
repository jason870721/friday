package backtest

import (
	"github.com/johnny1110/friday/internal/binance"
	"github.com/johnny1110/friday/internal/strategy"
)

// CalibrationMinTrades is the minimum number of backtest trades a strategy must
// produce on a symbol for its win rate to be trusted. Below this, the result is
// noise — the strategy is omitted from the calibration map and falls back to its
// hardcoded confidence (PRD-015).
const CalibrationMinTrades = 5

// Calibrate backtests every strategy against every symbol's candles and maps the
// observed win rate to a confidence score (PRD-015), returning a nested
// symbol → strategy → confidence map. A strategy with fewer than
// CalibrationMinTrades in the window is omitted (the caller then keeps the
// hardcoded default for it).
//
// The win-rate → confidence map is deliberately linear and simple:
//
//	confidence = max(0, (winRate − 0.5) × 2)
//	  50% → 0.0 (coin flip, no edge)   75% → 0.5   100% → 1.0   <50% → 0.0
//
// Candles are supplied by the caller (bootstrap fetches 4h×200 per symbol) so
// Calibrate is pure and testable — no network, no clock.
func Calibrate(strategies []strategy.Strategy, candlesBySymbol map[string][]binance.Kline) map[string]map[string]float64 {
	out := make(map[string]map[string]float64, len(candlesBySymbol))
	for symbol, candles := range candlesBySymbol {
		for _, s := range strategies {
			res, err := RunStrategy(s, symbol, candles)
			if err != nil || res.Trades < CalibrationMinTrades {
				continue // insufficient data → fall back to the hardcoded default
			}
			conf := (res.WinRate - 0.5) * 2
			if conf < 0 {
				conf = 0
			}
			if out[symbol] == nil {
				out[symbol] = make(map[string]float64, len(strategies))
			}
			out[symbol][s.Name()] = conf
		}
	}
	return out
}
