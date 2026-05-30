package tool

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/johnny1110/evva/pkg/tools"
)

func TestParseFearGreed_HappyPath(t *testing.T) {
	body := []byte(`{"name":"Fear and Greed Index","data":[{"value":"72","value_classification":"Greed","timestamp":"1551157200"}]}`)
	got, err := parseFearGreed(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "72") || !strings.Contains(got, "Greed") {
		t.Errorf("parsed line = %q; want value 72 and classification Greed", got)
	}
}

func TestParseFearGreed_EmptyData(t *testing.T) {
	if _, err := parseFearGreed([]byte(`{"data":[]}`)); err == nil {
		t.Error("expected error on empty data array")
	}
}

func TestParseFearGreed_BadJSON(t *testing.T) {
	if _, err := parseFearGreed([]byte(`{not json`)); err == nil {
		t.Error("expected decode error on malformed JSON")
	}
}

// TestFearGreed_Execute drives the tool end-to-end against a stub server,
// exercising the HTTP path without hitting the live API.
func TestFearGreed_Execute(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"data":[{"value":"15","value_classification":"Extreme Fear","timestamp":"1551157200"}]}`))
	}))
	defer srv.Close()

	orig := fearGreedURL
	fearGreedURL = srv.URL
	defer func() { fearGreedURL = orig }()

	res, err := NewFearGreedIndex().Execute(context.Background(), nopLogger(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected go error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected IsError; content=%q", res.Content)
	}
	if !strings.Contains(res.Content, "15") || !strings.Contains(res.Content, "Extreme Fear") {
		t.Errorf("content = %q; want value 15 and Extreme Fear", res.Content)
	}
}

// Compile-time assertion that FearGreedIndexTool satisfies tools.Tool.
var _ tools.Tool = (*FearGreedIndexTool)(nil)
