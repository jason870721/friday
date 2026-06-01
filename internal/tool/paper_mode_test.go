package tool

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/johnny1110/friday/internal/risk"
)

// TestPaperBalance_ReturnsVirtualState verifies binance_balance reports the
// virtual wallet (never a real account call) when paper mode is active.
func TestPaperBalance_ReturnsVirtualState(t *testing.T) {
	prev := globalPaper
	globalPaper = risk.NewPaperPortfolio(2500)
	t.Cleanup(func() { globalPaper = prev })

	res, _ := BinanceBalanceTool{}.Execute(context.Background(), slog.Default(), nil)
	if res.IsError {
		t.Fatalf("paper balance errored: %s", res.Content)
	}
	if !strings.Contains(res.Content, "2500") || !strings.Contains(res.Content, "[PAPER]") {
		t.Errorf("paper balance content = %q; want virtual 2500 + [PAPER]", res.Content)
	}
}

// TestPaperPosition_ReportsVirtual verifies binance_position reports the virtual
// book, not the exchange.
func TestPaperPosition_ReportsVirtual(t *testing.T) {
	prev := globalPaper
	pp := risk.NewPaperPortfolio(1000)
	pp.Trade("BTCUSDT", "BUY", 0.5, 100, false)
	globalPaper = pp
	t.Cleanup(func() { globalPaper = prev })

	// No mark fetch path is exercised for entry display because Price would need a
	// client; paperPositions tolerates a price error and falls back to entry.
	raw, _ := json.Marshal(map[string]string{"symbol": "ETHUSDT"})
	res, _ := BinancePositionTool{}.Execute(context.Background(), slog.Default(), raw)
	if !strings.Contains(res.Content, "no position (paper)") {
		t.Errorf("ETHUSDT (flat) should report no paper position, got %q", res.Content)
	}
}

func TestPaperEnabled(t *testing.T) {
	prev := globalPaper
	t.Cleanup(func() { globalPaper = prev })
	globalPaper = nil
	if PaperEnabled() {
		t.Error("PaperEnabled should be false when unset")
	}
	globalPaper = risk.NewPaperPortfolio(1000)
	if !PaperEnabled() {
		t.Error("PaperEnabled should be true when set")
	}
}
