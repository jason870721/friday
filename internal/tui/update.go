package tui

import (
	"context"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/johnny1110/evva/pkg/event"
)

// Update is bubbletea's per-tick handler. It dispatches on the message
// type:
//   - tea.WindowSizeMsg: lay out the viewport + footer + input
//   - tea.KeyMsg: input editing, submit, ctrl-c, ctrl-l, esc
//   - AgentEventMsg: render an event line; update usage footer
//   - RunDoneMsg: unlock the input
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var (
		inputCmd tea.Cmd
		viewCmd  tea.Cmd
	)

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		if !m.ready {
			m.view = viewport.New(msg.Width, m.transcriptHeight())
			m.view.SetContent(strings.Join(m.transcript, "\n"))
			m.ready = true
		} else {
			m.view.Width = msg.Width
			m.view.Height = m.transcriptHeight()
		}
		m.input.Width = msg.Width - 4
		m.refreshTranscript()
		return m, nil

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC:
			if m.busy && m.runCancel != nil {
				m.runCancel()
				m.appendLines(styleNotice.Render("⨯ cancel requested"))
				return m, nil
			}
			return m, tea.Quit
		case tea.KeyEsc:
			return m, tea.Quit
		case tea.KeyCtrlL:
			m.transcript = []string{welcomeBanner()}
			m.refreshTranscript()
			return m, nil
		case tea.KeyEnter:
			prompt := strings.TrimSpace(m.input.Value())
			if prompt == "" || m.busy {
				return m, nil
			}
			return m.startRun(prompt)
		}

	case AgentEventMsg:
		return m.handleAgentEvent(msg.Event), nil

	case RunDoneMsg:
		m.busy = false
		if msg.Err != nil {
			m.appendLines(styleError.Render("! run failed: " + msg.Err.Error()))
		}
		// agent.Run's return string is also surfaced via KindText, so we
		// don't re-print msg.Result here.
		m.input.Reset()
		m.input.Focus()
		return m, nil
	}

	// Forward to the focused subcomponents.
	m.input, inputCmd = m.input.Update(msg)
	if m.ready {
		m.view, viewCmd = m.view.Update(msg)
	}
	return m, tea.Batch(inputCmd, viewCmd)
}

// startRun kicks off agent.Run() in a goroutine, drops a "> prompt"
// line into the transcript, locks input, and returns a tea.Cmd that
// will deliver a RunDoneMsg when the agent finishes.
func (m Model) startRun(prompt string) (tea.Model, tea.Cmd) {
	m.appendLines(stylePrompt.Render("> ") + prompt)
	m.busy = true
	m.input.Reset()

	ctx, cancel := context.WithCancel(context.Background())
	m.runCancel = cancel

	ag := m.agent
	cmd := func() tea.Msg {
		resp, err := ag.Run(ctx, prompt)
		return RunDoneMsg{Result: resp, Err: err}
	}
	return m, cmd
}

// handleAgentEvent renders one evva event into a transcript line and
// folds usage data into the cumulative counters.
func (m Model) handleAgentEvent(e event.Event) tea.Model {
	if e.Kind == event.KindUsage && e.Usage != nil {
		m.inputTokens = e.Usage.Cumulative.InputTokens
		m.outputTokens = e.Usage.Cumulative.OutputTokens
		return m
	}

	if line := renderEvent(e); line != "" {
		m.appendLines(line)
	}

	// Track approximate message count from session info — cheap, no
	// extra LLM calls. Pull on RunEnd so the footer updates exactly
	// once per turn.
	if e.Kind == event.KindRunEnd {
		s := m.agent.Session()
		m.messages = s.MessageCount
	}
	return m
}

// transcriptHeight returns the viewport height after reserving rows for
// the input line + footer + margins.
func (m Model) transcriptHeight() int {
	const reserved = 4 // input (1) + footer (1) + margins (2)
	h := m.height - reserved
	if h < 3 {
		h = 3
	}
	return h
}
