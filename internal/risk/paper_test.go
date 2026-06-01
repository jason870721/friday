package risk

import (
	"context"
	"testing"
)

func TestPaperPortfolio_OpenAndCloseRealisesPnL(t *testing.T) {
	p := NewPaperPortfolio(1000)

	// Open long 1 BTC @ 100.
	p.Trade("BTCUSDT", "BUY", 1, 100, false)
	pos, ok := p.Position("BTCUSDT")
	if !ok || pos.Amt != 1 || pos.Entry != 100 {
		t.Fatalf("after open: %+v ok=%v", pos, ok)
	}
	if p.Balance() != 1000 {
		t.Errorf("balance should be unchanged before close, got %v", p.Balance())
	}

	// Close 1 BTC @ 110 → +10 realised.
	realised, closed := p.CloseAt("BTCUSDT", 110)
	if realised != 10 {
		t.Errorf("realised = %v; want 10", realised)
	}
	if closed != 1 {
		t.Errorf("closed amt = %v; want 1", closed)
	}
	if p.Balance() != 1010 {
		t.Errorf("balance = %v; want 1010", p.Balance())
	}
	if _, ok := p.Position("BTCUSDT"); ok {
		t.Error("position should be flat after full close")
	}
}

func TestPaperPortfolio_ShortAndAverageEntry(t *testing.T) {
	p := NewPaperPortfolio(1000)
	// Short 2 @ 50, add 2 @ 70 → avg entry 60, size -4.
	p.Trade("ETHUSDT", "SELL", 2, 50, false)
	p.Trade("ETHUSDT", "SELL", 2, 70, false)
	pos, _ := p.Position("ETHUSDT")
	if pos.Amt != -4 || pos.Entry != 60 {
		t.Fatalf("avg short: %+v; want amt=-4 entry=60", pos)
	}
	// Cover all @ 55 → short profit (60-55)*4 = 20.
	realised, _ := p.CloseAt("ETHUSDT", 55)
	if realised != 20 {
		t.Errorf("short realised = %v; want 20", realised)
	}
}

func TestPaperPortfolio_ReduceOnlyDoesNotFlip(t *testing.T) {
	p := NewPaperPortfolio(1000)
	p.Trade("BTCUSDT", "BUY", 1, 100, false)
	// Reduce-only SELL of 3 caps at the 1 held — no flip into a short.
	p.Trade("BTCUSDT", "SELL", 3, 110, true)
	if _, ok := p.Position("BTCUSDT"); ok {
		t.Error("reduce-only over-close should flatten, not flip")
	}
	if p.Balance() != 1010 {
		t.Errorf("balance = %v; want 1010 (only the 1 held realised)", p.Balance())
	}
}

func TestPaperPortfolio_CloseReduceOnlySatisfiesBroker(t *testing.T) {
	p := NewPaperPortfolio(1000)
	p.Trade("SOLUSDT", "BUY", 5, 20, false)
	// StopMonitor path: flatten a LONG (→ SELL). markFallback uses entry, so PnL≈0.
	if err := p.CloseReduceOnly(context.Background(), "SOLUSDT", 5, DirLong); err != nil {
		t.Fatalf("CloseReduceOnly: %v", err)
	}
	if _, ok := p.Position("SOLUSDT"); ok {
		t.Error("position should be flat after CloseReduceOnly")
	}
}
