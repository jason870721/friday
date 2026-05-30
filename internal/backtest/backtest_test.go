package backtest

import (
	"testing"

	"github.com/johnny1110/friday/internal/binance"
)

// risingCandles builds an uptrend: closes climb linearly, each candle
// spanning ±a couple percent so TP/SL levels are reachable on the next bar.
func risingCandles(n int) []binance.Kline {
	ks := make([]binance.Kline, n)
	for i := range n {
		c := 100.0 + float64(i)*0.5
		ks[i] = binance.Kline{
			Close: c,
			High:  c * 1.02,
			Low:   c * 0.99,
		}
	}
	return ks
}

func TestRun_LongInUptrendWins(t *testing.T) {
	rule := Rule{
		Indicator: IndicatorPriceVsMA, Op: OpGreater, Value: 0,
		Direction: "LONG", TakeProfitPct: 1.0, StopLossPct: 1.0, Leverage: 1,
	}
	res, err := Run(rule, risingCandles(40))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Trades == 0 {
		t.Fatal("expected at least one trade")
	}
	// Nearly all trades win; the entry on the final candle has no later
	// bar to reach TP, so it marks flat rather than winning.
	if res.WinRate < 0.8 {
		t.Errorf("win rate = %.2f; want > 0.8 (LONG in a clean uptrend)", res.WinRate)
	}
	if res.AvgPnLPct <= 0 {
		t.Errorf("avg pnl = %.2f; want > 0", res.AvgPnLPct)
	}
}

func TestRun_ShortInUptrendLoses(t *testing.T) {
	rule := Rule{
		Indicator: IndicatorPriceVsMA, Op: OpGreater, Value: 0,
		Direction: "SHORT", TakeProfitPct: 1.0, StopLossPct: 1.0, Leverage: 1,
	}
	res, err := Run(rule, risingCandles(40))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Trades == 0 {
		t.Fatal("expected at least one trade")
	}
	if res.WinRate != 0.0 {
		t.Errorf("win rate = %.2f; want 0.00 (SHORT into an uptrend)", res.WinRate)
	}
	if res.MaxDrawdownPct <= 0 {
		t.Errorf("max drawdown = %.2f; want > 0 for a losing series", res.MaxDrawdownPct)
	}
}

func TestRun_LeverageScalesPnL(t *testing.T) {
	base := Rule{Indicator: IndicatorPriceVsMA, Op: OpGreater, Value: 0,
		Direction: "LONG", TakeProfitPct: 1.0, StopLossPct: 1.0, Leverage: 1}
	lev := base
	lev.Leverage = 10

	r1, _ := Run(base, risingCandles(40))
	r10, _ := Run(lev, risingCandles(40))
	if r1.AvgPnLPct <= 0 || r10.AvgPnLPct <= 0 {
		t.Fatalf("expected positive pnl, got %.2f and %.2f", r1.AvgPnLPct, r10.AvgPnLPct)
	}
	if got := r10.AvgPnLPct / r1.AvgPnLPct; got < 9.5 || got > 10.5 {
		t.Errorf("10x leverage scaled pnl by %.2f; want ~10", got)
	}
}

func TestRule_Validate(t *testing.T) {
	bad := []Rule{
		{Indicator: "MACD", Op: OpLess, Value: 1, Direction: "LONG", TakeProfitPct: 1, StopLossPct: 1},
		{Indicator: IndicatorRSI, Op: "==", Value: 1, Direction: "LONG", TakeProfitPct: 1, StopLossPct: 1},
		{Indicator: IndicatorRSI, Op: OpLess, Value: 1, Direction: "SIDEWAYS", TakeProfitPct: 1, StopLossPct: 1},
		{Indicator: IndicatorRSI, Op: OpLess, Value: 1, Direction: "LONG", TakeProfitPct: 0, StopLossPct: 1},
	}
	for i, r := range bad {
		if err := r.Validate(); err == nil {
			t.Errorf("bad rule %d passed validation", i)
		}
	}
	good := Rule{Indicator: IndicatorRSI, Op: OpLess, Value: 30, Direction: "LONG", TakeProfitPct: 2, StopLossPct: 1, Leverage: 5}
	if err := good.Validate(); err != nil {
		t.Errorf("good rule rejected: %v", err)
	}
}
