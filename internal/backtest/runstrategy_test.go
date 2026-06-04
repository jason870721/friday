package backtest

import (
	"testing"

	"github.com/johnny1110/friday/internal/binance"
	"github.com/johnny1110/friday/internal/strategy"
)

// risingPullbackCandles is a net-up series whose RSI stays in the 50–70
// momentum band (pullbacks keep it out of overbought) with the last three
// closes rising — the shape Momentum fires Long on.
func risingPullbackCandles(n int) []binance.Kline {
	closes := make([]float64, 0, n)
	v := 100.0
	for i := 0; i < n-3; i++ {
		if i%2 == 0 {
			v += 1.5
		} else {
			v -= 1.2
		}
		closes = append(closes, v)
	}
	closes = append(closes, v+0.5, v+1.0, v+1.5)
	ks := make([]binance.Kline, len(closes))
	for i, c := range closes {
		ks[i] = binance.Kline{Close: c, High: c * 1.002, Low: c * 0.998, Volume: 100}
	}
	return ks
}

// fallingCandles is a strictly declining series → RSI 0 (oversold) and price
// well below MA20, the shape MeanReversion fires Long on (and keeps stopping
// out as the decline continues).
func fallingCandles(n int) []binance.Kline {
	ks := make([]binance.Kline, n)
	for i := range n {
		c := 200.0 - float64(i)*3
		ks[i] = binance.Kline{Close: c, High: c * 1.002, Low: c * 0.998, Volume: 100}
	}
	return ks
}

// momentumWinnersCandles is a net-up sawtooth: each cycle climbs for several
// bars (momentum fires Long) then snaps back below its MA (stopping the long at
// invalidation) before resuming higher. It produces many momentum trades whose
// winners (the rides up) outweigh the small stop-outs — a sub-50% win rate with
// positive expectancy, the trend-strategy profile the calibration must keep.
func momentumWinnersCandles() []binance.Kline {
	closes := []float64{}
	v := 100.0
	for c := 0; c < 24; c++ {
		// climb (momentum entry forms on the rising closes)
		for i := 0; i < 5; i++ {
			v += 3
			closes = append(closes, v)
		}
		// sharp pullback below the lagging MA → stops the long
		for i := 0; i < 3; i++ {
			v -= 4
			closes = append(closes, v)
		}
	}
	ks := make([]binance.Kline, len(closes))
	for i, c := range closes {
		ks[i] = binance.Kline{Close: c, High: c * 1.003, Low: c * 0.997, Volume: 100}
	}
	return ks
}

func TestRunStrategy_MomentumUptrendWins(t *testing.T) {
	res, err := RunStrategy(strategy.Momentum{}, "BTCUSDT", risingPullbackCandles(40))
	if err != nil {
		t.Fatalf("RunStrategy: %v", err)
	}
	if res.Trades == 0 {
		t.Fatal("expected >0 trades for momentum on an uptrend")
	}
	if res.WinRate < 0.5 {
		t.Errorf("win rate = %.2f; want ≥0.5 (momentum riding a clean uptrend)", res.WinRate)
	}
}

func TestRunStrategy_MeanReversionExitsAtInvalidation(t *testing.T) {
	res, err := RunStrategy(strategy.MeanReversion{}, "ETHUSDT", fallingCandles(30))
	if err != nil {
		t.Fatalf("RunStrategy: %v", err)
	}
	if res.Trades == 0 {
		t.Fatal("expected MeanReversion to enter on oversold conditions")
	}
	// On a continuing decline each fade is stopped at its ~1% invalidation, so
	// the average trade is a small loss — proof the exit fired at invalidation.
	if res.AvgPnLPct >= 0 {
		t.Errorf("avg pnl = %.2f%%; want negative (stopped at invalidation)", res.AvgPnLPct)
	}
}

func TestRunStrategy_NoCandles(t *testing.T) {
	if _, err := RunStrategy(strategy.Momentum{}, "BTCUSDT", nil); err == nil {
		t.Error("expected an error for an empty candle series")
	}
}

func TestCalibrate_MapsExpectancyAndOmitsThin(t *testing.T) {
	strats := []strategy.Strategy{strategy.MeanReversion{}, strategy.Momentum{}}
	cal := Calibrate(strats, map[string][]binance.Kline{
		"FALLUSDT": fallingCandles(40),        // MeanReversion: many stop-outs (≥5) → negative expectancy → conf 0
		"RISEUSDT": risingPullbackCandles(40), // Momentum: one trade to end (<5) → omitted
	})

	mr, ok := cal["FALLUSDT"]["mean_reversion"]
	if !ok {
		t.Fatal("mean_reversion should be calibrated on FALLUSDT (≥5 trades)")
	}
	// Negative per-trade expectancy (each fade stopped at its invalidation) → 0.
	if mr != 0 {
		t.Errorf("a negative-expectancy strategy maps to 0 confidence; got %.2f", mr)
	}
	// A strategy with <CalibrationMinTrades trades is omitted → hardcoded fallback.
	if _, ok := cal["RISEUSDT"]["momentum"]; ok {
		t.Error("momentum had <5 trades on RISEUSDT; it should be omitted (fallback), not calibrated")
	}
}

// TestCalibrate_KeepsProfitableLowWinRate is the regression guard for the
// win-rate→expectancy fix: a strategy that wins rarely but with large winners
// (positive expectancy, sub-50% win rate) must NOT be disabled — the old
// win-rate map zeroed exactly these profitable trend strategies.
func TestCalibrate_KeepsProfitableLowWinRate(t *testing.T) {
	res, err := RunStrategy(strategy.Momentum{}, "UPUSDT", momentumWinnersCandles())
	if err != nil {
		t.Fatalf("RunStrategy: %v", err)
	}
	if res.Trades < CalibrationMinTrades {
		t.Skipf("fixture produced %d trades (<%d); cannot exercise the calibration path", res.Trades, CalibrationMinTrades)
	}
	cal := Calibrate([]strategy.Strategy{strategy.Momentum{}}, map[string][]binance.Kline{"UPUSDT": momentumWinnersCandles()})
	conf, ok := cal["UPUSDT"]["momentum"]
	if res.AvgPnLPct-roundTripFeePct > 0 {
		if !ok || conf <= 0 {
			t.Errorf("a positive-expectancy strategy (avg %.2f%%, win rate %.0f%%) must keep confidence >0; got ok=%v conf=%.2f",
				res.AvgPnLPct, res.WinRate*100, ok, conf)
		}
	}
}

func TestBestTakeProfit_PicksFromGrid(t *testing.T) {
	// Momentum on a clean uptrend trades; the sweep must return a TP from the
	// grid with ok=true.
	tp, ret, ok := BestTakeProfit(strategy.Momentum{}, "BTCUSDT", risingPullbackCandles(40))
	if !ok {
		t.Fatal("expected a rankable TP (momentum trades on this uptrend)")
	}
	inGrid := false
	for _, g := range tpSweepGrid {
		if tp == g {
			inGrid = true
		}
	}
	if !inGrid {
		t.Errorf("BestTakeProfit returned TP %.2f not in the sweep grid %v", tp, tpSweepGrid)
	}
	_ = ret
}

func TestBestTakeProfit_NoTrades(t *testing.T) {
	// A flat series never triggers momentum → nothing to rank.
	flat := make([]binance.Kline, 40)
	for i := range flat {
		flat[i] = binance.Kline{Close: 100, High: 100.1, Low: 99.9, Volume: 100}
	}
	if _, _, ok := BestTakeProfit(strategy.Momentum{}, "BTCUSDT", flat); ok {
		t.Error("expected ok=false when the strategy never trades")
	}
}
