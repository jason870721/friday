package tool

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/johnny1110/evva/pkg/tools"
)

// nopLogger returns a logger that discards everything — keeps test
// output clean.
func nopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardWriter{}, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestEcho_Metadata(t *testing.T) {
	tool := NewEcho()
	if tool.Name() != "echo" {
		t.Errorf("Name() = %q; want %q", tool.Name(), "echo")
	}
	if tool.Description() == "" {
		t.Error("Description() is empty")
	}
	var schema map[string]any
	if err := json.Unmarshal(tool.Schema(), &schema); err != nil {
		t.Fatalf("Schema is not valid JSON: %v", err)
	}
	// Spot-check the required field is declared.
	req, ok := schema["required"].([]any)
	if !ok || len(req) != 1 || req[0] != "text" {
		t.Errorf("Schema.required: got %v; want [text]", schema["required"])
	}
}

func TestEcho_HappyPath(t *testing.T) {
	tool := NewEcho()
	res, err := tool.Execute(context.Background(), nopLogger(),
		json.RawMessage(`{"text":"hello, friday"}`))
	if err != nil {
		t.Fatalf("unexpected go error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected IsError; content=%q", res.Content)
	}
	if res.Content != "hello, friday" {
		t.Errorf("Content = %q; want %q", res.Content, "hello, friday")
	}
}

func TestEcho_Repeats(t *testing.T) {
	tool := NewEcho()
	res, _ := tool.Execute(context.Background(), nopLogger(),
		json.RawMessage(`{"text":"hi","times":3}`))
	if res.IsError {
		t.Fatalf("unexpected IsError; content=%q", res.Content)
	}
	want := "hi\nhi\nhi"
	if res.Content != want {
		t.Errorf("Content = %q; want %q", res.Content, want)
	}
	// And spot-check the newline count.
	if got := strings.Count(res.Content, "\n"); got != 2 {
		t.Errorf("newline count = %d; want 2", got)
	}
}

func TestEcho_RejectsEmptyText(t *testing.T) {
	tool := NewEcho()
	res, _ := tool.Execute(context.Background(), nopLogger(),
		json.RawMessage(`{"text":""}`))
	if !res.IsError || !strings.Contains(res.Content, "required") {
		t.Errorf("expected required-text error; got isErr=%v content=%q", res.IsError, res.Content)
	}
}

func TestEcho_RejectsOutOfRangeTimes(t *testing.T) {
	tool := NewEcho()
	for _, n := range []int{0, 11, 999} {
		input := []byte(`{"text":"x","times":`)
		input = append(input, []byte(itoa(n))...)
		input = append(input, '}')
		res, _ := tool.Execute(context.Background(), nopLogger(), json.RawMessage(input))
		if !res.IsError {
			t.Errorf("times=%d: expected IsError, got success content=%q", n, res.Content)
		}
	}
}

func TestEcho_DecodeError(t *testing.T) {
	tool := NewEcho()
	res, _ := tool.Execute(context.Background(), nopLogger(),
		json.RawMessage(`{not json`))
	if !res.IsError || !strings.Contains(res.Content, "decode") {
		t.Errorf("expected decode error; got isErr=%v content=%q", res.IsError, res.Content)
	}
}

// Compile-time assertion that EchoTool satisfies tools.Tool.
var _ tools.Tool = (*EchoTool)(nil)

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
