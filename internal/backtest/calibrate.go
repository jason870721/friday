package backtest

import (
	"github.com/johnny1110/friday/internal/binance"
	"github.com/johnny1110/friday/internal/strategy"
)

// CalibrationMinTrades is the minimum number of backtest trades a strategy must
// produce on a symbol for its expectancy to be trusted. Below this, the result is
// noise — the strategy is omitted from the calibration map and falls back to its
// hardcoded confidence (PRD-015).
const CalibrationMinTrades = 5

// roundTripFeePct is the approximate taker round-trip cost (~0.04% each way)
// netted out of a strategy's per-trade expectancy before it earns any
// confidence: an average move that doesn't clear its own fees is not an edge.
const roundTripFeePct = 0.08

// fullConfidenceEdgePct is the fee-netted per-trade expectancy (average PnL %, at
// 1× leverage) that earns full confidence (1.0); smaller edges scale linearly.
const fullConfidenceEdgePct = 1.0

// Calibrate backtests every strategy against every symbol's candles and maps each
// one's per-trade EXPECTANCY (average PnL %, fee-netted) to a confidence score
// (PRD-015), returning a nested symbol → strategy → direction → confidence map.
// A strategy with fewer than CalibrationMinTrades in the window is omitted (the
// caller then keeps the hardcoded default for it).
//
// Direction-split (PRD-022): LONG and SHORT confidences are computed separately
// because trend-following strategies often excel in one direction and bleed in
// the other (e.g. momentum shorts win in a downtrend while momentum longs get
// stopped out). A 0-confidence direction means the strategy abstains from voting
// on that side only, rather than being entirely silenced.
//
// Expectancy, NOT win rate. The original win-rate map (confidence = (WR−0.5)×2)
// was backwards for trend strategies: momentum/breakout/ema_cross win rarely but
// big (e.g. 12% win rate yet +1.4%/trade, strongly profitable) — a win-rate map
// disabled exactly those money-makers while crowning a 100%-win-rate Bollinger
// fade whose +0.10%/trade doesn't even clear the ~0.08% round-trip fee. With only
// that thin strategy left voting, the ≥2-aligned consensus could never form and
// the engine sat in perpetual NEUTRAL. Expectancy reflects what actually earns:
//
//	netEdge    = avgPnLPct − roundTripFeePct
//	confidence = clamp(0, 1, netEdge / fullConfidenceEdgePct)
//	  netEdge ≤ 0 → 0 (no edge after fees → the strategy abstains)
//
// Candles are supplied by the caller (bootstrap fetches 5m×1500 per symbol) so
// Calibrate is pure and testable — no network, no clock.
func Calibrate(strategies []strategy.Strategy, candlesBySymbol map[string][]binance.Kline) map[string]map[string]map[string]float64 {
	out := make(map[string]map[string]map[string]float64, len(candlesBySymbol))
	for symbol, candles := range candlesBySymbol {
		for _, s := range strategies {
			res, err := RunStrategy(s, symbol, candles)
			if err != nil || res.Trades < CalibrationMinTrades {
				continue // insufficient data → fall back to the hardcoded default
			}
			if out[symbol] == nil {
				out[symbol] = make(map[string]map[string]float64, len(strategies))
			}
			dirs := make(map[string]float64, 2)
			if res.LongTrades >= CalibrationMinTrades {
				conf := (res.LongAvgPnLPct - roundTripFeePct) / fullConfidenceEdgePct
				if conf < 0 {
					conf = 0
				}
				if conf > 1 {
					conf = 1
				}
				dirs["LONG"] = conf
			}
			if res.ShortTrades >= CalibrationMinTrades {
				conf := (res.ShortAvgPnLPct - roundTripFeePct) / fullConfidenceEdgePct
				if conf < 0 {
					conf = 0
				}
				if conf > 1 {
					conf = 1
				}
				dirs["SHORT"] = conf
			}
			if len(dirs) > 0 {
				out[symbol][s.Name()] = dirs
			}
		}
	}
	return out
}
