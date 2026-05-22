// Command friday is a ReAct chat agent built on the evva SDK. Persona
// is F.R.I.D.A.Y. — a brisk, mildly witty engineering assistant. LLM is
// DeepSeek v4 Pro by default; configuration lives in ~/.friday/.env
// (DEEPSEEK_API_KEY, LOG_LEVEL, MAX_ITERS, …).
//
// Phase 15 of the evva roadmap: friday's only purpose is to prove
// evva's pkg/* surface is usable for a real downstream consumer. The
// findings live in /mnt/friday/docs/sdk-feedback.md and feed back into
// evva's next round of polish.
package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/johnny1110/evva/pkg/version"
	"github.com/johnny1110/friday/internal/bootstrap"
	"github.com/johnny1110/friday/internal/tui"
)

func main() {
	// Log the evva SDK version friday is bound to. Useful when the
	// user files a bug — pkg/version is stable surface so this is
	// safe to query at any startup.
	// Bare() drops the leading "v" — composes cleanly into our own
	// "evva 0.2.4-alpha.3" log format without the awkward double-v.
	fmt.Fprintf(os.Stderr, "friday: built on evva %s\n", version.Bare())

	// 1. Build the event sink first. The bubbletea program isn't ready
	//    yet, so the sink starts unattached; it gets wired below once
	//    we've constructed the program.
	sink := tui.NewSink()

	// 2. Bootstrap loads ~/.friday/.env + ~/.friday/config/friday-config.yml,
	//    registers DeepSeek credentials, builds the F.R.I.D.A.Y. Profile,
	//    and returns a ready agent.Agent.
	ag, cfg, err := bootstrap.New(sink)
	if err != nil {
		fmt.Fprintln(os.Stderr, "friday: bootstrap failed:", err)
		os.Exit(1)
	}

	// 3. Bubbletea program. Alt-screen + mouse support is plenty for v1;
	//    the textinput handles its own focus.
	model := tui.New(ag, cfg, sink)
	prog := tea.NewProgram(model,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)

	// 4. Attach the sink — every subsequent agent event reaches the
	//    bubbletea Update loop as an AgentEventMsg.
	sink.Attach(prog)

	if _, err := prog.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "friday: tui exited:", err)
		os.Exit(1)
	}
}
