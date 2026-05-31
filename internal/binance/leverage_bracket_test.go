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
