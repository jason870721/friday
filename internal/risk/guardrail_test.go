package risk

import (
	"errors"
	"testing"
)

func TestMarginCapValidator_BlocksOversized(t *testing.T) {
	v := NewMarginCapValidator(0.15)
	// Balance $5,000 → cap = $750 margin. Notional $18,750 at 25x →
	// margin $750 (exactly at the cap, allowed). Push leverage down to
	// 10x → margin $1,875 > $750 → blocked.
	err := v.Validate(
		Order{Symbol: "SOLUSDT", Side: "BUY", Quantity: 227.1},
		Account{WalletBalance: 5000, MarkPrice: 82.55, Leverage: 10},
	)
	if err == nil {
		t.Fatal("expected oversized order to be blocked, got nil")
	}
	var capErr *CapExceededError
	if !errors.As(err, &capErr) {
		t.Fatalf("expected *CapExceededError, got %T: %v", err, err)
	}
	if capErr.Cap != 750 {
		t.Errorf("cap = %.2f; want 750", capErr.Cap)
	}
}

func TestMarginCapValidator_AllowsWithinCap(t *testing.T) {
	v := NewMarginCapValidator(0.15)
	// Balance $5,000 → cap $750. Notional $18,750 at 25x → margin $750,
	// not over the cap → allowed.
	err := v.Validate(
		Order{Symbol: "SOLUSDT", Side: "BUY", Quantity: 227.1},
		Account{WalletBalance: 5000, MarkPrice: 82.55, Leverage: 25},
	)
	if err != nil {
		t.Errorf("expected within-cap order to pass, got %v", err)
	}
}

func TestMarginCapValidator_ReduceOnlyExempt(t *testing.T) {
	v := NewMarginCapValidator(0.15)
	// A huge reduce-only close must never be blocked, even far over cap.
	err := v.Validate(
		Order{Symbol: "BTCUSDT", Side: "SELL", Quantity: 100, ReduceOnly: true},
		Account{WalletBalance: 5000, MarkPrice: 73000, Leverage: 1},
	)
	if err != nil {
		t.Errorf("reduce-only close should be exempt, got %v", err)
	}
}

func TestMarginCapValidator_NoLeverageUsesNotional(t *testing.T) {
	v := NewMarginCapValidator(0.15)
	// Leverage 0 (unknown) → margin == notional. $1,000 notional on a
	// $5,000 balance → $1,000 > $750 cap → blocked (conservative).
	err := v.Validate(
		Order{Symbol: "BTCUSDT", Side: "BUY", Quantity: 1},
		Account{WalletBalance: 5000, MarkPrice: 1000, Leverage: 0},
	)
	if err == nil {
		t.Error("expected block when leverage unknown and notional over cap")
	}
}

func TestMarginCapValidator_IndeterminateAllows(t *testing.T) {
	v := NewMarginCapValidator(0.15)
	// Zero balance / price → can't compute a cap → allow (fail-open;
	// the tool layer flags the degradation).
	if err := v.Validate(
		Order{Symbol: "BTCUSDT", Side: "BUY", Quantity: 1},
		Account{WalletBalance: 0, MarkPrice: 0},
	); err != nil {
		t.Errorf("indeterminate snapshot should allow, got %v", err)
	}
}

// Compile-time assertion that MarginCapValidator satisfies Validator.
var _ Validator = MarginCapValidator{}
