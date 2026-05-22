package tui

import (
	"context"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/johnny1110/evva/pkg/agent"
	"github.com/johnny1110/evva/pkg/config"
)

// Model is friday's bubbletea Model. Three stacked regions: a viewport
// holding the transcript, a textinput for the prompt, a one-line status
// footer. The agent runs on a separate goroutine spawned from Update;
// events stream back through Sink → program.Send → AgentEventMsg.
type Model struct {
	agent   agent.Agent
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

	// Cumulative token usage — driven by KindUsage events.
	inputTokens, outputTokens int
	messages                  int

	ready bool
}

// New constructs the Model. The Sink should already exist; main attaches
// it to the program after the program is created.
func New(ag agent.Agent, cfg *config.Config, sink *Sink) Model {
	ti := textinput.New()
	ti.Placeholder = "ask friday…"
	ti.Focus()
	ti.CharLimit = 4096
	ti.Prompt = "› "
	ti.PromptStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)

	model := ag.Model()
	if model == "" {
		model = "deepseek/?"
	}

	m := Model{
		agent:      ag,
		cfg:        cfg,
		sink:       sink,
		model:      model,
		persona:    "friday",
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
