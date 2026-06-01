package tool

import (
	"strings"
	"testing"

	"github.com/johnny1110/friday/internal/memory"
)

func scored(strategy string, pnl float64) memory.Scored {
	return memory.Scored{Record: memory.TradeRecord{Strategy: strategy, PnL: pnl, PnLSource: "reported"}}
}

func TestFormatOutcome(t *testing.T) {
	got := formatOutcome(memory.OutcomeStatsOf([]memory.Scored{
		scored("momentum", 10), scored("momentum", 4), scored("breakout", -6),
	}))
	for _, want := range []string{"2 wins", "1 loss", "avg win +7.00", "avg loss -6.00"} {
		if !strings.Contains(got, want) {
			t.Errorf("formatOutcome = %q; missing %q", got, want)
		}
	}
	// Singular forms and flats.
	one := formatOutcome(memory.OutcomeStatsOf([]memory.Scored{scored("x", 1), scored("x", 0)}))
	if !strings.Contains(one, "1 win, 0 losses, 1 flat") {
		t.Errorf("formatOutcome singular/flat = %q", one)
	}
}

func TestStrategyBreakdown(t *testing.T) {
	matches := []memory.Scored{
		scored("momentum", 10),
		scored("breakout", -6),
		scored("momentum", -2),
		scored("", 3), // unattributed
	}
	lines := strategyBreakdown(matches)
	if len(lines) != 3 {
		t.Fatalf("breakdown lines = %d; want 3 (momentum, breakout, unattributed)", len(lines))
	}
	// Order preserved by first appearance; momentum aggregates both its trades.
	if !strings.HasPrefix(lines[0], "momentum: ") || !strings.Contains(lines[0], "1 win, 1 loss") {
		t.Errorf("momentum line = %q", lines[0])
	}
	if !strings.HasPrefix(lines[2], "(unattributed): ") {
		t.Errorf("third line = %q; want unattributed", lines[2])
	}
}
