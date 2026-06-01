package binance

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMaxLeverages_PicksHighestBracket(t *testing.T) {
	const body = `[
		{"symbol":"BTCUSDT","brackets":[
			{"bracket":1,"initialLeverage":125},
			{"bracket":2,"initialLeverage":100},
			{"bracket":3,"initialLeverage":50}
		]},
		{"symbol":"NVDAUSDT","brackets":[
			{"bracket":1,"initialLeverage":10},
			{"bracket":2,"initialLeverage":5}
		]},
		{"symbol":"WEIRD","brackets":[]}
	]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fapi/v1/leverageBracket" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Write([]byte(body))
	}))
	defer srv.Close()

	got, err := New(srv.URL, "k", "s").MaxLeverages(context.Background())
	if err != nil {
		t.Fatalf("MaxLeverages: %v", err)
	}
	if got["BTCUSDT"] != 125 {
		t.Errorf("BTCUSDT = %d; want 125", got["BTCUSDT"])
	}
	if got["NVDAUSDT"] != 10 {
		t.Errorf("NVDAUSDT = %d; want 10", got["NVDAUSDT"])
	}
	if _, ok := got["WEIRD"]; ok {
		t.Error("symbol with no brackets should be omitted")
	}
}

func TestLeverageBrackets_ParsesAndSortsByNotionalCap(t *testing.T) {
	// Tiers deliberately out of order in the payload; we expect ascending by cap.
	const body = `[
		{"symbol":"AMZNUSDT","brackets":[
			{"bracket":2,"initialLeverage":5,"notionalCap":25000},
			{"bracket":1,"initialLeverage":10,"notionalCap":5000},
			{"bracket":3,"initialLeverage":2,"notionalCap":100000}
		]}
	]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	defer srv.Close()

	got, err := New(srv.URL, "k", "s").LeverageBrackets(context.Background())
	if err != nil {
		t.Fatalf("LeverageBrackets: %v", err)
	}
	bs := got["AMZNUSDT"]
	if len(bs) != 3 {
		t.Fatalf("AMZNUSDT brackets = %d; want 3", len(bs))
	}
	if bs[0].NotionalCap != 5000 || bs[0].InitialLeverage != 10 {
		t.Errorf("first tier = %+v; want cap 5000 / lev 10 (ascending sort)", bs[0])
	}
}

func TestMaxLeverageForNotional(t *testing.T) {
	// AMZN-style nested tiers: highest leverage only for the smallest notional.
	bs := []LeverageBracket{
		{NotionalCap: 5000, InitialLeverage: 10},
		{NotionalCap: 25000, InitialLeverage: 5},
		{NotionalCap: 100000, InitialLeverage: 2},
	}
	cases := []struct {
		notional float64
		want     int
	}{
		{4000, 10},   // inside the top-leverage tier
		{5000, 10},   // exactly on the tier cap → still the top tier
		{5000.01, 5}, // just over → drops to the next tier
		{20000, 5},
		{60000, 2},
		{500000, 2}, // beyond every cap → best-effort floor (lowest leverage)
	}
	for _, c := range cases {
		got, ok := MaxLeverageForNotional(bs, c.notional)
		if !ok || got != c.want {
			t.Errorf("MaxLeverageForNotional($%.2f) = %d,%v; want %d,true", c.notional, got, ok, c.want)
		}
	}
	if _, ok := MaxLeverageForNotional(nil, 1000); ok {
		t.Error("empty bracket table should return ok=false")
	}
}
