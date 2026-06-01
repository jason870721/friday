package risk

import (
	"errors"
	"testing"
)

func TestGroupFor_IdentifiesGroup(t *testing.T) {
	g := DefaultGroupLimits()
	if name, _, ok := g.GroupFor("BTCUSDT"); !ok || name != "crypto" {
		t.Errorf("BTCUSDT → (%q,%v); want crypto,true", name, ok)
	}
	if name, _, ok := g.GroupFor("NVDAUSDT"); !ok || name != "stocks" {
		t.Errorf("NVDAUSDT → (%q,%v); want stocks,true", name, ok)
	}
	if _, _, ok := g.GroupFor("DOGEUSDT"); ok {
		t.Error("DOGEUSDT is in no group; want ok=false")
	}
}

func TestParseGroupLimits(t *testing.T) {
	g := ParseGroupLimits("crypto:25:BTCUSDT,ETHUSDT;stocks:50:NVDAUSDT")
	if cfg := g["crypto"]; cfg.MaxMarginPct != 0.25 || len(cfg.Symbols) != 2 {
		t.Errorf("crypto parsed wrong: %+v", cfg)
	}
	if cfg := g["stocks"]; cfg.MaxMarginPct != 0.50 || cfg.Symbols[0] != "NVDAUSDT" {
		t.Errorf("stocks parsed wrong: %+v", cfg)
	}
	// Empty / garbage → defaults.
	if g := ParseGroupLimits(""); len(g) != 2 {
		t.Errorf("empty → default groups, got %d", len(g))
	}
	if g := ParseGroupLimits("nonsense"); len(g) != 2 {
		t.Errorf("unparseable → default groups, got %d", len(g))
	}
}

func TestPortfolioGroupValidator_BlocksOverGroupCap(t *testing.T) {
	v := NewPortfolioGroupValidator(DefaultGroupLimits())
	// crypto cap = 30% of $1000 = $300. ETH already uses $250; this BTC order
	// adds margin = (0.1 × $1000) / 10 = $10 notional... compute: qty 0.5 × mark
	// 1000 = $500 notional ÷ 10x = $50 margin. $250 + $50 = $300 = cap → ok.
	a := Account{WalletBalance: 1000, MarkPrice: 1000, Leverage: 10, GroupUsedMargin: 250}
	o := Order{Symbol: "BTCUSDT", Side: "BUY", Quantity: 0.5}
	if err := v.Validate(o, a); err != nil {
		t.Errorf("exactly at cap should pass, got %v", err)
	}

	// Push used margin up so the same order breaches the cap.
	a.GroupUsedMargin = 260 // 260 + 50 = 310 > 300
	err := v.Validate(o, a)
	if err == nil {
		t.Fatal("over the group cap should error")
	}
	var ce *GroupCapExceededError
	if !errors.As(err, &ce) || ce.Group != "crypto" {
		t.Errorf("want GroupCapExceededError for crypto, got %v", err)
	}
}

func TestPortfolioGroupValidator_PassThroughCases(t *testing.T) {
	v := NewPortfolioGroupValidator(DefaultGroupLimits())
	a := Account{WalletBalance: 1000, MarkPrice: 1000, Leverage: 1, GroupUsedMargin: 999}

	// Reduce-only always bypasses.
	if err := v.Validate(Order{Symbol: "BTCUSDT", Quantity: 100, ReduceOnly: true}, a); err != nil {
		t.Errorf("reduce-only should bypass, got %v", err)
	}
	// Symbol in no group passes.
	if err := v.Validate(Order{Symbol: "DOGEUSDT", Quantity: 100}, a); err != nil {
		t.Errorf("ungrouped symbol should pass, got %v", err)
	}
	// Indeterminate snapshot passes.
	if err := v.Validate(Order{Symbol: "BTCUSDT", Quantity: 100}, Account{}); err != nil {
		t.Errorf("indeterminate snapshot should pass, got %v", err)
	}
}
