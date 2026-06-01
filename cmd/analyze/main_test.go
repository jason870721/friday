package main

import (
	"testing"

	"github.com/johnny1110/friday/internal/memory"
	"github.com/johnny1110/friday/internal/orchestrator"
)

func tr(symbol, strategy string, net float64) memory.TradeRecord {
	return memory.TradeRecord{Symbol: symbol, Strategy: strategy, NetPnL: net, PnLSource: "exchange"}
}

func TestStatsOf(t *testing.T) {
	s := statsOf([]memory.TradeRecord{
		tr("BTCUSDT", "momentum", 10),
		tr("BTCUSDT", "momentum", -4),
		tr("BTCUSDT", "momentum", 6),
	})
	if s.Trades != 3 || s.Wins != 2 || s.Losses != 1 {
		t.Errorf("counts wrong: %+v", s)
	}
	if s.TotalPnL != 12 {
		t.Errorf("total = %v; want 12", s.TotalPnL)
	}
	// Profit factor = gross wins (16) / abs(gross losses) (4) = 4.0.
	if s.ProfitFactor != 4 {
		t.Errorf("profit factor = %v; want 4", s.ProfitFactor)
	}

	// No losses → ∞ sentinel (-1).
	allWins := statsOf([]memory.TradeRecord{tr("X", "a", 5), tr("X", "a", 5)})
	if allWins.ProfitFactor != -1 {
		t.Errorf("all-wins PF should be -1 (∞ sentinel), got %v", allWins.ProfitFactor)
	}
}

func TestBuildReport_EmptyIsZeros(t *testing.T) {
	rep := buildReport(nil, nil)
	if rep.Overview.Trades != 0 || rep.Overview.Rounds != 0 || rep.Overview.TotalPnL != 0 {
		t.Errorf("empty report should be zeros: %+v", rep.Overview)
	}
	// text() must not panic on empty input.
	_ = rep.text()
}

func TestRegimeAttribution_PrefersOpeningRound(t *testing.T) {
	rounds := []orchestrator.RoundRecord{
		{Round: 1, Time: "2026-06-01T10:00:00Z", Regimes: map[string]string{"BTCUSDT": "TRENDING"},
			Decisions: []orchestrator.RiskDecision{{Symbol: "BTCUSDT", Action: "OPEN_LONG"}}},
		{Round: 2, Time: "2026-06-01T10:00:15Z", Regimes: map[string]string{"BTCUSDT": "RANGING"},
			Decisions: []orchestrator.RiskDecision{{Symbol: "BTCUSDT", Action: "WAIT"}}},
	}
	regimeOf := regimeAttributor(rounds)
	// A trade closed after both rounds attributes to the OPENING round (TRENDING),
	// not the latest regime (RANGING).
	got := regimeOf(memory.TradeRecord{Symbol: "BTCUSDT", Time: parseRFC("2026-06-01T10:00:20Z")})
	if got != "TRENDING" {
		t.Errorf("regime = %q; want TRENDING (opening round)", got)
	}
}

func TestAnalystAccuracy(t *testing.T) {
	rounds := []orchestrator.RoundRecord{
		{Round: 1, Time: "2026-06-01T10:00:00Z",
			Analysis:  []orchestrator.SymbolAnalysis{{Symbol: "BTCUSDT", Bias: "BULLISH"}},
			Decisions: []orchestrator.RiskDecision{{Symbol: "BTCUSDT", Action: "OPEN_LONG"}}},
	}
	trades := []memory.TradeRecord{
		{Symbol: "BTCUSDT", Bias: "LONG", NetPnL: 5, PnLSource: "exchange", Time: parseRFC("2026-06-01T10:00:10Z")},
	}
	ev, correct, acc := analystAccuracy(rounds, trades)
	if ev != 1 || correct != 1 || acc != 1 {
		t.Errorf("accuracy = (%d,%d,%.2f); want (1,1,1.0)", ev, correct, acc)
	}
}

func TestBreakerTimeline(t *testing.T) {
	rounds := []orchestrator.RoundRecord{
		{Round: 1, Breaker: "NORMAL (…)"},
		{Round: 2, Breaker: "PAUSED (5 consecutive losses) — CLOSE/WAIT only"},
		{Round: 3, Breaker: "PAUSED (…) "},
		{Round: 4, Breaker: "NORMAL (…)"},
	}
	ev := breakerTimeline(rounds)
	if len(ev) != 1 {
		t.Fatalf("want 1 PAUSED span, got %d (%+v)", len(ev), ev)
	}
	if ev[0].State != "PAUSED" || ev[0].FromRound != 2 || ev[0].ToRound != 3 || ev[0].Rounds != 2 {
		t.Errorf("span wrong: %+v", ev[0])
	}
}
