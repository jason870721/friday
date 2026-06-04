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
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/johnny1110/evva/pkg/version"
	"github.com/johnny1110/friday/internal/bootstrap"
	"github.com/johnny1110/friday/internal/risk"
	"github.com/johnny1110/friday/internal/tool"
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
	//    registers DeepSeek credentials, and builds the PRD-003 three-agent
	//    orchestrator (Analyst → Risk Manager → Executor). The sink doubles
	//    as the orchestrator's RoleEmitter for role-tagged events.
	orch, cfg, err := bootstrap.New(sink)
	if err != nil {
		fmt.Fprintln(os.Stderr, "friday: bootstrap failed:", err)
		os.Exit(1)
	}

	// 2b. PRD-009: start the stop-loss/TP monitor — a goroutine that polls
	//     mark price ~every second and fires reduce-only closes the instant a
	//     registered level is breached, independent of the agents' 15s loop.
	//     It shares the tools' Binance client and lives for the whole session
	//     (cancelled on exit). Skipped if Binance credentials are absent.
	monitorCtx, cancelMonitor := context.WithCancel(context.Background())
	defer cancelMonitor()
	if cli, err := tool.SharedBinanceClient(); err != nil {
		fmt.Fprintf(os.Stderr, "friday: stop monitor disabled (%v)\n", err)
	} else {
		monitor := risk.NewStopMonitor(tool.NewBinanceStopBroker(cli), time.Second, slog.Default(), tool.LogStopClose)
		tool.SetStopMonitor(monitor)
		go monitor.Start(monitorCtx)

		// PRD-020 §2 R5: cancel any server-side STOP_MARKET / TAKE_PROFIT_MARKET
		// orders orphaned by a previous session (position no longer exists), so a
		// stale stop can't fire against a position friday no longer holds. Skipped
		// in paper mode — it queries real account endpoints (PRD-021 §4).
		if !tool.PaperEnabled() {
			go tool.CleanupOrphanStops(monitorCtx, slog.Default())
		}
	}

	// 3. Bubbletea program. Alt-screen + mouse support is plenty for v1;
	//    the textinput handles its own focus.
	model := tui.New(orch, "deepseek · 3-agent pipeline", cfg, sink)
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
