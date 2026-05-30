package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/johnny1110/evva/pkg/tools"
)

func TestRunBacktest_InvalidRuleRejectedBeforeNetwork(t *testing.T) {
	// An unknown indicator fails rule.Validate() before any klines fetch,
	// so this never touches the network.
	res, err := NewRunBacktest().Execute(context.Background(), nopLogger(),
		json.RawMessage(`{"symbol":"BTCUSDT","indicator":"MACD","op":"<","value":1,"direction":"LONG","take_profit_pct":1,"stop_loss_pct":1}`))
	if err != nil {
		t.Fatalf("unexpected go error: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "invalid rule") {
		t.Errorf("expected invalid-rule error; got isErr=%v content=%q", res.IsError, res.Content)
	}
}

func TestRunBacktest_RejectsMissingSymbol(t *testing.T) {
	res, _ := NewRunBacktest().Execute(context.Background(), nopLogger(),
		json.RawMessage(`{"indicator":"RSI","op":"<","value":30,"direction":"LONG","take_profit_pct":1,"stop_loss_pct":1}`))
	if !res.IsError || !strings.Contains(res.Content, "symbol") {
		t.Errorf("expected symbol-required error; got isErr=%v content=%q", res.IsError, res.Content)
	}
}

func TestLogTrade_DecodeError(t *testing.T) {
	res, _ := NewLogTrade().Execute(context.Background(), nopLogger(), json.RawMessage(`{not json`))
	if !res.IsError || !strings.Contains(res.Content, "decode") {
		t.Errorf("expected decode error; got isErr=%v content=%q", res.IsError, res.Content)
	}
}

// Compile-time assertions that the PRD-004 tools satisfy tools.Tool.
var (
	_ tools.Tool = (*RunBacktestTool)(nil)
	_ tools.Tool = (*LogTradeTool)(nil)
	_ tools.Tool = (*RecallTradesTool)(nil)
)
