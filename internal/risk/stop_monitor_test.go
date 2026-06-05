package risk

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

type closeCall struct {
	symbol string
	qty    float64
	side   string
}

type mockBroker struct {
	price    float64
	priceErr error
	closes   []closeCall
}

func (m *mockBroker) MarkPrice(_ context.Context, _ string) (float64, error) {
	return m.price, m.priceErr
}

func (m *mockBroker) CloseReduceOnly(_ context.Context, symbol string, qty float64, side string) error {
	m.closes = append(m.closes, closeCall{symbol, qty, side})
	return nil
}

func newTestMonitor(b StopBroker) *StopMonitor {
	return NewStopMonitor(b, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
}

func TestStopMonitor_LongStopFires(t *testing.T) {
	b := &mockBroker{price: 99}
	m := newTestMonitor(b)
	m.SetLevels("BTCUSDT", StopLevels{StopPrice: 100, PositionQty: 1, PositionSide: DirLong})
	m.check(context.Background())

	if len(b.closes) != 1 || b.closes[0].side != DirLong || b.closes[0].qty != 1 {
		t.Fatalf("expected one LONG close qty 1, got %+v", b.closes)
	}
	if m.Active() != 0 {
		t.Errorf("levels should clear after firing; active=%d", m.Active())
	}
}

func TestStopMonitor_ShortStopFires(t *testing.T) {
	b := &mockBroker{price: 101}
	m := newTestMonitor(b)
	m.SetLevels("ETHUSDT", StopLevels{StopPrice: 100, PositionQty: 2, PositionSide: DirShort})
	m.check(context.Background())
	if len(b.closes) != 1 || b.closes[0].side != DirShort {
		t.Fatalf("expected one SHORT close, got %+v", b.closes)
	}
}

func TestStopMonitor_PeakTracking(t *testing.T) {
	b := &mockBroker{price: 102}
	m := newTestMonitor(b)
	// LONG entry 100, qty 2, stop far below so it never fires across polls.
	m.SetLevels("BTCUSDT", StopLevels{StopPrice: 90, PositionQty: 2, PositionSide: DirLong, EntryPrice: 100})

	if _, ok := m.PeakPnL("BTCUSDT"); ok {
		t.Fatal("no observation yet → PeakPnL should report ok=false")
	}
	b.price = 102 // uPnL = (102-100)*2 = +4
	m.check(context.Background())
	b.price = 105 // uPnL = +10  (new peak)
	m.check(context.Background())
	b.price = 103 // uPnL = +6   (below peak — peak must hold)
	m.check(context.Background())

	if peak, ok := m.PeakPnL("BTCUSDT"); !ok || peak != 10 {
		t.Errorf("peak = %v (ok=%v); want 10", peak, ok)
	}

	// A new position (different entry) resets the peak.
	m.SetLevels("BTCUSDT", StopLevels{StopPrice: 95, PositionQty: 2, PositionSide: DirLong, EntryPrice: 110})
	if _, ok := m.PeakPnL("BTCUSDT"); ok {
		t.Error("re-arm with a new entry should reset the peak (ok=false until next poll)")
	}

	// Closing the position clears the peak.
	m.SetLevels("BTCUSDT", StopLevels{})
	if _, ok := m.PeakPnL("BTCUSDT"); ok {
		t.Error("clearing levels should drop the peak")
	}
}

func TestStopMonitor_TakeProfitFires(t *testing.T) {
	b := &mockBroker{price: 110}
	m := newTestMonitor(b)
	m.SetLevels("BTCUSDT", StopLevels{TakeProfit: 109, PositionQty: 1, PositionSide: DirLong})
	m.check(context.Background())
	if len(b.closes) != 1 {
		t.Fatalf("LONG TP at 110≥109 should fire, got %+v", b.closes)
	}
}

func TestStopMonitor_NoTriggerInRange(t *testing.T) {
	b := &mockBroker{price: 105}
	m := newTestMonitor(b)
	m.SetLevels("BTCUSDT", StopLevels{StopPrice: 100, TakeProfit: 110, PositionQty: 1, PositionSide: DirLong})
	m.check(context.Background())
	if len(b.closes) != 0 {
		t.Errorf("price in range should not fire, got %+v", b.closes)
	}
	if m.Active() != 1 {
		t.Errorf("levels should persist when not breached; active=%d", m.Active())
	}
}

func TestStopMonitor_MidFlightClear(t *testing.T) {
	b := &mockBroker{price: 99} // would breach if still armed
	m := newTestMonitor(b)
	m.SetLevels("BTCUSDT", StopLevels{StopPrice: 100, PositionQty: 1, PositionSide: DirLong})
	m.SetLevels("BTCUSDT", StopLevels{}) // clear
	if m.Active() != 0 {
		t.Fatalf("expected cleared levels, active=%d", m.Active())
	}
	m.check(context.Background())
	if len(b.closes) != 0 {
		t.Errorf("cleared level must not fire, got %+v", b.closes)
	}
}

func TestStopMonitor_SetLevelsIdempotent(t *testing.T) {
	m := newTestMonitor(&mockBroker{price: 105})
	lv := StopLevels{StopPrice: 100, PositionQty: 1, PositionSide: DirLong}
	m.SetLevels("BTCUSDT", lv)
	m.SetLevels("BTCUSDT", lv) // duplicate
	if m.Active() != 1 {
		t.Errorf("duplicate SetLevels should keep one entry; active=%d", m.Active())
	}
}

func TestStopMonitor_PriceErrorSkips(t *testing.T) {
	b := &mockBroker{priceErr: errors.New("boom")}
	m := newTestMonitor(b)
	m.SetLevels("BTCUSDT", StopLevels{StopPrice: 100, PositionQty: 1, PositionSide: DirLong})
	m.check(context.Background())
	if len(b.closes) != 0 {
		t.Errorf("price error should skip, got %+v", b.closes)
	}
	if m.Active() != 1 {
		t.Errorf("levels should persist after a price error; active=%d", m.Active())
	}
}

func TestStopMonitor_StartStopsOnContextCancel(t *testing.T) {
	m := NewStopMonitor(&mockBroker{price: 105}, time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Start(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Start did not return after context cancel")
	}
}
