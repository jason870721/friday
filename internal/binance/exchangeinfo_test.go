package binance

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestExchangeInfo_ParsesStatusAndStepSize(t *testing.T) {
	const body = `{
		"symbols": [
			{"symbol":"BTCUSDT","status":"TRADING","filters":[
				{"filterType":"PRICE_FILTER","tickSize":"0.10"},
				{"filterType":"LOT_SIZE","stepSize":"0.001"}
			]},
			{"symbol":"GOOGLUSDT","status":"PENDING_TRADING","filters":[
				{"filterType":"LOT_SIZE","stepSize":"1"}
			]},
			{"symbol":"NOLOT","status":"TRADING","filters":[
				{"filterType":"PRICE_FILTER","tickSize":"0.01"}
			]}
		]
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fapi/v1/exchangeInfo" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Write([]byte(body))
	}))
	defer srv.Close()

	cli := New(srv.URL, "", "")
	got, err := cli.ExchangeInfo(context.Background())
	if err != nil {
		t.Fatalf("ExchangeInfo: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d; want 3", len(got))
	}

	byName := map[string]SymbolInfo{}
	for _, s := range got {
		byName[s.Symbol] = s
	}
	if s := byName["BTCUSDT"]; s.Status != "TRADING" || s.StepSize != "0.001" {
		t.Errorf("BTCUSDT = %+v; want TRADING / 0.001", s)
	}
	if s := byName["GOOGLUSDT"]; s.Status != "PENDING_TRADING" || s.StepSize != "1" {
		t.Errorf("GOOGLUSDT = %+v; want PENDING_TRADING / 1", s)
	}
	// A symbol without a LOT_SIZE filter parses with an empty StepSize rather
	// than failing the whole call.
	if s := byName["NOLOT"]; s.StepSize != "" {
		t.Errorf("NOLOT StepSize = %q; want empty", s.StepSize)
	}
}

func TestExchangeInfo_PropagatesAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Binance returns its error envelope on HTTP 200.
		w.Write([]byte(`{"code":-1121,"msg":"Invalid symbol."}`))
	}))
	defer srv.Close()

	cli := New(srv.URL, "", "")
	if _, err := cli.ExchangeInfo(context.Background()); err == nil {
		t.Fatal("expected error from API error envelope")
	}
}
