package binance

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSignTradFiPerpsAgreement_Code200IsSuccess(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		// The agreement endpoint signals SUCCESS with code 200 (not 0) — the
		// client must NOT treat that as an error.
		w.Write([]byte(`{"code":200,"msg":"success"}`))
	}))
	defer srv.Close()

	cli := New(srv.URL, "key", "secret")
	if err := cli.SignTradFiPerpsAgreement(context.Background()); err != nil {
		t.Fatalf("SignTradFiPerpsAgreement: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s; want POST", gotMethod)
	}
	if gotPath != "/fapi/v1/stock/contract" {
		t.Errorf("path = %s; want /fapi/v1/stock/contract", gotPath)
	}
}

func TestSignTradFiPerpsAgreement_PropagatesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":-4411,"msg":"Please sign TradFi-Perps agreement contract fapi."}`))
	}))
	defer srv.Close()

	cli := New(srv.URL, "key", "secret")
	if err := cli.SignTradFiPerpsAgreement(context.Background()); err == nil {
		t.Fatal("expected error from a negative-code envelope")
	}
}
