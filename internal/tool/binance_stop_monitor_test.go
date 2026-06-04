package tool

import (
	"context"
	"testing"

	"github.com/johnny1110/friday/internal/binance"
	"github.com/johnny1110/friday/internal/risk"
)

func TestCloseSideFor(t *testing.T) {
	// A LONG is flattened by SELL; a SHORT by BUY (PRD-020 §2 native stops).
	if got := closeSideFor(risk.DirLong); got != binance.SideSell {
		t.Errorf("closeSideFor(LONG) = %v; want SELL", got)
	}
	if got := closeSideFor(risk.DirShort); got != binance.SideBuy {
		t.Errorf("closeSideFor(SHORT) = %v; want BUY", got)
	}
}

// TestStopEntryPrice_Paper verifies the StopMonitor's entry-price capture reads
// the virtual book in paper mode. Without this the levels carry EntryPrice=0 and
// every monitor close estimates +0.00 PnL (the bug this fixes).
func TestStopEntryPrice_Paper(t *testing.T) {
	prev := globalPaper
	pp := risk.NewPaperPortfolio(1000)
	pp.Trade("BTCUSDT", "BUY", 0.5, 27000, false)
	globalPaper = pp
	t.Cleanup(func() { globalPaper = prev })

	if got := stopEntryPrice(context.Background(), "BTCUSDT"); got != 27000 {
		t.Errorf("stopEntryPrice(BTCUSDT) = %g; want 27000", got)
	}
	if got := stopEntryPrice(context.Background(), "ETHUSDT"); got != 0 {
		t.Errorf("stopEntryPrice(ETHUSDT, flat) = %g; want 0", got)
	}
}
