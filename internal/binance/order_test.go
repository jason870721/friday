package binance

import (
	"strings"
	"testing"
)

func TestFormatOrder_FallsBackToOrigQtyWhenUnfilled(t *testing.T) {
	// A just-acked MARKET order: executedQty=0, status NEW. The summary
	// should report the requested origQty, not "0".
	o := &OrderResponse{
		Symbol:      "BTCUSDT",
		Side:        "BUY",
		Type:        "MARKET",
		OrigQty:     "0.101",
		ExecutedQty: "0.000",
		Status:      "NEW",
		OrderID:     13485845074,
	}
	got := FormatOrder(o)
	if !strings.Contains(got, "qty=0.101") {
		t.Errorf("FormatOrder = %q; want qty=0.101 (origQty fallback)", got)
	}
	if strings.Contains(got, "qty=0.000") {
		t.Errorf("FormatOrder = %q; should not show the zero executedQty", got)
	}
}

func TestFormatOrder_PrefersExecutedQtyWhenFilled(t *testing.T) {
	// A filled order: executedQty is the source of truth.
	o := &OrderResponse{
		Symbol:      "SOLUSDT",
		Side:        "SELL",
		Type:        "MARKET",
		OrigQty:     "227.1",
		ExecutedQty: "227.1",
		AvgPrice:    "82.55",
		Status:      "FILLED",
		OrderID:     42,
		ReduceOnly:  true,
	}
	got := FormatOrder(o)
	if !strings.Contains(got, "qty=227.1") {
		t.Errorf("FormatOrder = %q; want qty=227.1", got)
	}
	if !strings.Contains(got, "@ 82.55") || !strings.Contains(got, "status=FILLED") || !strings.Contains(got, "reduceOnly") {
		t.Errorf("FormatOrder = %q; missing avg price / status / reduceOnly", got)
	}
}

func TestIsZeroQty(t *testing.T) {
	for _, s := range []string{"", "0", "0.000", "0.0"} {
		if !isZeroQty(s) {
			t.Errorf("isZeroQty(%q) = false; want true", s)
		}
	}
	for _, s := range []string{"0.101", "227.1", "1"} {
		if isZeroQty(s) {
			t.Errorf("isZeroQty(%q) = true; want false", s)
		}
	}
}
