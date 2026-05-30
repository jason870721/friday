package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/johnny1110/evva/pkg/event"
)

// AgentEventMsg wraps an evva event for the bubbletea Update loop.
// Sink.Emit is called from the agent's emit goroutine; bubbletea's
// program.Send is goroutine-safe and pushes onto the program's msg
// channel, so the Model only ever sees events serialised through
// Update().
//
// Source carries the role tag (Analyst / Risk / Executor / Pipeline) so
// the transcript can prefix the line. Empty for untagged events.
type AgentEventMsg struct {
	Event  event.Event
	Source string
}

// RunDoneMsg is delivered when an agent.Run() goroutine finishes (or
// errors). The Update handler unlocks the input on receipt.
type RunDoneMsg struct {
	Result string
	Err    error
}

// Sink implements event.Sink. The bubbletea program isn't known at
// construction time (we need the sink to wire the agent BEFORE we can
// build the program that holds the Model), so Attach is a two-step
// dance: build sink → build agent with WithSink(sink) → build program
// with the agent on the model → sink.Attach(program).
type Sink struct{ program *tea.Program }

// NewSink returns an unattached Sink. Calls to Emit before Attach are
// dropped on the floor — useful when an agent might emit something
// during construction (the toolset registry init logs an info event,
// for example).
func NewSink() *Sink { return &Sink{} }

// Attach plugs the bubbletea program into the sink. After this every
// Emit forwards through program.Send.
func (s *Sink) Attach(p *tea.Program) { s.program = p }

// Emit satisfies event.Sink — untagged events (Source "").
func (s *Sink) Emit(e event.Event) {
	s.EmitRole("", e)
}

// EmitRole satisfies orchestrator.RoleEmitter: forwards an event tagged
// with the producing role so the transcript can prefix the line.
func (s *Sink) EmitRole(role string, e event.Event) {
	if s.program == nil {
		return
	}
	s.program.Send(AgentEventMsg{Event: e, Source: role})
}
