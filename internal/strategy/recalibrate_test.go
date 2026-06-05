package strategy

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/johnny1110/friday/internal/binance"
)

func TestRecalibrator_RunOnceUpdatesStore(t *testing.T) {
	t.Cleanup(func() { SetDefaultCalibration(nil) })

	rc := &Recalibrator{
		Symbols:    []string{"BTCUSDT"},
		Strategies: DefaultStrategies(),
		Interval:   time.Hour,
		Fetch: func(_ context.Context, sym, _ string, _ int) ([]binance.Kline, error) {
			return candlesFromCloses(repeat(100, 50)...), nil
		},
		CalibrateFn: func(_ []Strategy, _ map[string][]binance.Kline) map[string]map[string]map[string]float64 {
			return map[string]map[string]map[string]float64{"BTCUSDT": {"momentum": {"LONG": 0.42, "SHORT": 0.42}}}
		},
	}
	rc.runOnce(context.Background(), slog.Default())

	if got := calibrationFor("BTCUSDT")["momentum"]["LONG"]; got != 0.42 {
		t.Errorf("calibration not installed: momentum = %v; want 0.42", got)
	}
}

func TestRecalibrator_FailedRunKeepsExisting(t *testing.T) {
	t.Cleanup(func() { SetDefaultCalibration(nil) })
	SetDefaultCalibration(map[string]map[string]map[string]float64{"BTCUSDT": {"momentum": {"LONG": 0.7, "SHORT": 0.7}}})

	rc := &Recalibrator{
		Symbols:    []string{"BTCUSDT"},
		Strategies: DefaultStrategies(),
		Interval:   time.Hour,
		Fetch: func(_ context.Context, _, _ string, _ int) ([]binance.Kline, error) {
			return nil, errors.New("network down")
		},
		CalibrateFn: func(_ []Strategy, _ map[string][]binance.Kline) map[string]map[string]map[string]float64 {
			t.Fatal("CalibrateFn must not run when no candles were fetched")
			return map[string]map[string]map[string]float64{}
		},
	}
	rc.runOnce(context.Background(), slog.Default())

	if got := calibrationFor("BTCUSDT")["momentum"]["LONG"]; got != 0.7 {
		t.Errorf("a failed run must keep the prior confidences; momentum = %v; want 0.7", got)
	}
}

func TestRecalibrator_DisabledReturnsImmediately(t *testing.T) {
	// Interval 0 → Run must return at once (would otherwise block forever).
	done := make(chan struct{})
	rc := &Recalibrator{Interval: 0}
	go func() { rc.Run(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run with Interval=0 should return immediately (disabled)")
	}
}
