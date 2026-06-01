package tool

import (
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
