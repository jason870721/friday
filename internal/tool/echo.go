// Package tool hosts friday's own custom tools — tools that ship as
// part of friday (not evva). Wired into the agent at construction via
// agent.WithCustomTool. The factory pattern lets us reach friday-local
// state (config, logger, future stores) without leaking internals into
// the LLM-facing surface.
//
// Convention: each tool lives in its own file (echo.go, weather.go,
// …), exports a New<X>() constructor plus the <X>Tool struct that
// satisfies pkg/tools.Tool, and gets registered by name in
// internal/bootstrap/bootstrap.go.
package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/johnny1110/evva/pkg/tools"
)

// EchoToolName is the wire name the LLM sees. Kept exported so the
// bootstrap layer can register the tool without typing the string
// twice (drift-free).
const EchoToolName tools.ToolName = "echo"

// echoToolDescription is what the model reads when deciding whether
// to invoke. Kept terse — the schema below already declares the
// inputs.
const echoToolDescription = `Echo the given text back unchanged.

Useful for:
- Testing tool plumbing — does the agent invoke a custom tool correctly?
- Surfacing a literal string back to the user without paraphrasing.
- Mirroring user input in a multi-step plan when the agent needs to
  quote the original prompt exactly.

The tool returns the text verbatim. If 'times' is set, repeats the
text that many times separated by a newline.`

// echoToolSchema constrains the input. JSON Schema draft-7 syntax;
// matches how evva's bundled tools declare their inputs.
const echoToolSchema = `{
	"type": "object",
	"additionalProperties": false,
	"required": ["text"],
	"properties": {
		"text":  {"type": "string", "description": "The text to echo back."},
		"times": {"type": "integer", "minimum": 1, "maximum": 10, "default": 1, "description": "Repeat count (1-10). Defaults to 1."}
	}
}`

// EchoTool is the simplest possible custom tool — takes a string,
// returns it. Serves as friday's reference example for downstream
// authors building their own tools.
type EchoTool struct{}

// NewEcho constructs an EchoTool. The factory (in
// internal/bootstrap/bootstrap.go) wraps this so agent.WithCustomTool
// can call it with a tools.State even though EchoTool itself ignores
// state.
func NewEcho() *EchoTool { return &EchoTool{} }

// Name satisfies tools.Tool.
func (EchoTool) Name() string { return string(EchoToolName) }

// Description satisfies tools.Tool.
func (EchoTool) Description() string { return echoToolDescription }

// Schema satisfies tools.Tool.
func (EchoTool) Schema() json.RawMessage { return json.RawMessage(echoToolSchema) }

// echoInput is the parsed shape of the JSON the LLM sends. Times is
// a pointer so we can distinguish "model omitted" (use default 1)
// from "model sent 0" (which the schema's minimum=1 would reject
// anyway, but defence-in-depth is cheap).
type echoInput struct {
	Text  string `json:"text"`
	Times *int   `json:"times,omitempty"`
}

// Execute satisfies tools.Tool. Returns Result.Content = the echoed
// text, or Result.IsError = true with a Content describing the
// validation failure.
func (EchoTool) Execute(_ context.Context, logger *slog.Logger, raw json.RawMessage) (tools.Result, error) {
	var in echoInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("echo: decode input: %v", err)}, nil
	}
	if in.Text == "" {
		return tools.Result{IsError: true, Content: "echo: text is required"}, nil
	}
	times := 1
	if in.Times != nil {
		times = *in.Times
	}
	if times < 1 || times > 10 {
		return tools.Result{IsError: true, Content: fmt.Sprintf("echo: times=%d out of range [1,10]", times)}, nil
	}

	logger.Debug("echo.dispatch", "text", in.Text, "times", times)

	if times == 1 {
		return tools.Result{Content: in.Text}, nil
	}
	lines := make([]string, times)
	for i := range lines {
		lines[i] = in.Text
	}
	return tools.Result{Content: strings.Join(lines, "\n")}, nil
}
