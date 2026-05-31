package binance

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestSummarizeRealized_SplitsByType(t *testing.T) {
	rows := []IncomeEntry{
		{IncomeType: "REALIZED_PNL", Income: "-4.3904"},
		{IncomeType: "REALIZED_PNL", Income: "0.0102"},
		{IncomeType: "COMMISSION", Income: "-0.4267"},
		{IncomeType: "FUNDING_FEE", Income: "-1.8698"},
		{IncomeType: "TRANSFER", Income: "5000"},           // must be ignored
		{IncomeType: "COMMISSION", Income: "not-a-number"}, // skipped, not fatal
	}
	s := SummarizeRealized(rows)
	if s.RealizedRows != 2 {
		t.Errorf("RealizedRows = %d; want 2", s.RealizedRows)
	}
	if !approx(s.RealizedPnL, -4.3802) {
		t.Errorf("RealizedPnL = %v; want -4.3802", s.RealizedPnL)
	}
	if !approx(s.Commission, -0.4267) {
		t.Errorf("Commission = %v; want -0.4267", s.Commission)
	}
	if !approx(s.Funding, -1.8698) {
		t.Errorf("Funding = %v; want -1.8698", s.Funding)
	}
	if want := -4.3802 - 0.4267 - 1.8698; !approx(s.Net(), want) {
		t.Errorf("Net = %v; want %v", s.Net(), want)
	}
}

func TestSummarizeRealized_NoRealizedRows(t *testing.T) {
	// Only fees/funding (e.g. an open with no close yet) — RealizedRows 0 tells
	// the caller to fall back rather than record a fake 0 PnL.
	s := SummarizeRealized([]IncomeEntry{{IncomeType: "COMMISSION", Income: "-0.5"}})
	if s.RealizedRows != 0 {
		t.Errorf("RealizedRows = %d; want 0", s.RealizedRows)
	}
}

func TestIncome_ParsesAndScopesToSymbol(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("symbol"); got != "NVDAUSDT" {
			t.Errorf("symbol param = %q; want NVDAUSDT", got)
		}
		if r.Header.Get("X-MBX-APIKEY") == "" {
			t.Error("missing API key header on signed request")
		}
		w.Write([]byte(`[
			{"symbol":"NVDAUSDT","incomeType":"REALIZED_PNL","income":"-4.4856","time":1780224490000,"tradeId":"1"},
			{"symbol":"NVDAUSDT","incomeType":"COMMISSION","income":"-0.4360","time":1780224490000,"tradeId":"1"}
		]`))
	}))
	defer srv.Close()

	cli := New(srv.URL, "key", "secret")
	rows, err := cli.Income(context.Background(), "NVDAUSDT", 1780224000000, 1780225000000, 100)
	if err != nil {
		t.Fatalf("Income: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d; want 2", len(rows))
	}
	if s := SummarizeRealized(rows); !approx(s.Net(), -4.9216) {
		t.Errorf("net = %v; want -4.9216", s.Net())
	}
}
