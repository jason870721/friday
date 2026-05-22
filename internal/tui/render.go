package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/johnny1110/evva/pkg/event"
)

// Render styles used across the transcript. lipgloss prints ANSI even
// when output isn't a TTY; bubbletea handles the alt-screen / cursor
// teardown for us.
var (
	stylePrompt     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214"))
	styleAssistant  = lipgloss.NewStyle().Foreground(lipgloss.Color("48"))
	styleThinking   = lipgloss.NewStyle().Faint(true).Italic(true)
	styleToolHead   = lipgloss.NewStyle().Foreground(lipgloss.Color("75"))
	styleToolResult = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	styleError      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("196"))
	styleNotice     = lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("245"))
)

// renderEvent maps an evva Event into the transcript lines we want
// the user to see. Returns an empty string when the event is silent
// (the status footer covers it, or it's a v1-dropped kind).
//
// Phase 19a refactor: switches on the typed pointer returned by
// e.Payload() instead of e.Kind + the matching field. Same outcome,
// less boilerplate — no need to remember which field on Event goes
// with which Kind.
//
// Kinds friday silently drops in v1:
//   - *TextPayload with Kind=text_chunk / thinking_chunk: profile.Stream
//     is false, never fire.
//   - *ApprovalNeededPayload / *QuestionNeededPayload: permission mode
//     is bypass.
//   - *StoreUpdatePayload / *ModeChangedPayload / *Compacting*: no UI
//     surface yet.
//   - *TurnPayload / *RunStartPayload / *RunResumePayload: noisy;
//     the prompt line we already drew at startRun is enough.
func renderEvent(e event.Event) string {
	switch p := e.Payload().(type) {
	case *event.TextPayload:
		if p.Text == "" {
			return ""
		}
		if e.Kind == event.KindThinking {
			return styleThinking.Render("… " + indent(p.Text, "  "))
		}
		return styleAssistant.Render("friday: ") + p.Text

	case *event.ToolUseStartPayload:
		summary := summariseToolInput(p.Input)
		return styleToolHead.Render(fmt.Sprintf("⚙ %s", p.Name)) +
			styleNotice.Render(" "+summary)

	case *event.ToolUseResultPayload:
		body := truncateLines(p.Content, 6, 240)
		if p.IsError {
			return styleError.Render("  ✗ " + body)
		}
		return styleToolResult.Render(indent(body, "  "))

	case *event.ErrorPayload:
		// Message is the Phase 19a stringified field — already
		// populated at emit time, no nil-check / .Error() dance.
		return styleError.Render(fmt.Sprintf("! %s: %s", p.Stage, p.Message))

	case *event.IterLimitPayload:
		return styleNotice.Render(fmt.Sprintf(
			"⏸ paused at iteration limit (%d). Send a follow-up prompt to continue.", p.Iters))
	}

	// Kinds without a payload (or payloads we drop).
	if e.Kind == event.KindRunCancelled {
		return styleNotice.Render("⨯ run cancelled.")
	}
	return ""
}

// summariseToolInput pulls the highest-signal field out of the tool's
// raw JSON input for the transcript line. We don't have per-tool
// knowledge here — fall back to "first non-trivial string value" which
// happens to work for `bash.command`, `read.path`, `edit.file_path`,
// `grep.pattern`, etc.
func summariseToolInput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	// Preferred keys in priority order; fall through to the first
	// short-ish string value found.
	for _, k := range []string{"command", "path", "file_path", "pattern", "query", "url"} {
		if v, ok := m[k].(string); ok && v != "" {
			return ellipsize(v, 80)
		}
	}
	for _, v := range m {
		if s, ok := v.(string); ok && s != "" && len(s) < 120 {
			return ellipsize(s, 80)
		}
	}
	return ""
}

func ellipsize(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func indent(s, prefix string) string {
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

func truncateLines(s string, maxLines, maxBytes int) string {
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > maxLines {
		lines = append(lines[:maxLines], fmt.Sprintf("… (%d more lines)", len(lines)-maxLines))
	}
	out := strings.Join(lines, "\n")
	if len(out) > maxBytes {
		out = out[:maxBytes-1] + "…"
	}
	return out
}
