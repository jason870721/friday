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

func TestCalibrate_MapsWinRateAndOmitsThin(t *testing.T) {
	strats := []strategy.Strategy{strategy.MeanReversion{}, strategy.Momentum{}}
	cal := Calibrate(strats, map[string][]binance.Kline{
		"FALLUSDT": fallingCandles(40),        // MeanReversion: many stop-outs (≥5) → 0% win → conf 0
		"RISEUSDT": risingPullbackCandles(40), // Momentum: one trade to end (<5) → omitted
	})

	mr, ok := cal["FALLUSDT"]["mean_reversion"]
	if !ok {
		t.Fatal("mean_reversion should be calibrated on FALLUSDT (≥5 trades)")
	}
	if mr != 0 {
		t.Errorf("a 0%% win rate maps to 0 confidence; got %.2f", mr)
	}
	// A strategy with <CalibrationMinTrades trades is omitted → hardcoded fallback.
	if _, ok := cal["RISEUSDT"]["momentum"]; ok {
		t.Error("momentum had <5 trades on RISEUSDT; it should be omitted (fallback), not calibrated")
	}
}
