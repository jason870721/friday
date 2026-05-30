package tui

import (
	"context"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/johnny1110/evva/pkg/config"
)

// Runner is the slice of behaviour the TUI needs from whatever drives a
// prompt. Both a single agent.Agent and the multi-agent
// orchestrator.Orchestrator satisfy it, so the TUI is agnostic to which
// is wired in.
type Runner interface {
	Run(ctx context.Context, prompt string) (string, error)
}

// Model is friday's bubbletea Model. Three stacked regions: a viewport
// holding the transcript, a textinput for the prompt, a one-line status
// footer. The agent runs on a separate goroutine spawned from Update;
// events stream back through Sink → program.Send → AgentEventMsg.
type Model struct {
	runner  Runner
	cfg     *config.Config
	sink    *Sink
	model   string // provider/model label cached for the footer
	persona string

	view  viewport.Model
	input textinput.Model

	// transcript accumulates every rendered line. We rebuild
	// view.SetContent on every change so wrapping recomputes when the
	// terminal resizes.
	transcript []string

	width  int
	height int

	busy      bool
	runCancel context.CancelFunc

	// pendingPrompts buffers user input typed while the agent is busy.
	// Each Enter-while-busy appends one entry; RunDoneMsg pops the head
	// and starts a new Run. Cleared on Ctrl+C-while-busy. The queue is
	// FIFO and unbounded (charLimit on the textinput is the only
	// per-entry guardrail).
	pendingPrompts []string

	// Cumulative token usage — driven by KindUsage events.
	inputTokens, outputTokens int
	messages                  int

	ready bool
}

// New constructs the Model. The Sink should already exist; main attaches
// it to the program after the program is created. modelLabel is the
// provider/model string shown in the footer.
func New(runner Runner, modelLabel string, cfg *config.Config, sink *Sink) Model {
	ti := textinput.New()
	ti.Placeholder = "ask friday…"
	ti.Focus()
	ti.CharLimit = 4096
	ti.Prompt = "› "
	ti.PromptStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)

	if modelLabel == "" {
		modelLabel = "deepseek/?"
	}

	m := Model{
		runner:     runner,
		cfg:        cfg,
		sink:       sink,
		model:      modelLabel,
		persona:    "analyst→risk→executor",
		input:      ti,
		transcript: []string{welcomeBanner()},
	}
	return m
}

// Init is bubbletea's startup hook; we just blink the textinput cursor.
func (m Model) Init() tea.Cmd {
	return textinput.Blink
}

// appendLines pushes one or more lines into the transcript and
// refreshes the viewport content. Each call ends pinned to the bottom
// so the user sees the latest line without scrolling.
func (m *Model) appendLines(lines ...string) {
	for _, l := range lines {
		if l == "" {
			continue
		}
		m.transcript = append(m.transcript, l)
	}
	m.refreshTranscript()
}

func (m *Model) refreshTranscript() {
	if !m.ready {
		return
	}
	m.view.SetContent(strings.Join(m.transcript, "\n"))
	m.view.GotoBottom()
}

func welcomeBanner() string {
	style := lipgloss.NewStyle().
		Foreground(lipgloss.Color("214")).
		Bold(true)
	return style.Render("friday online. Ask me something, boss.")
}
