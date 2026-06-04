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
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/johnny1110/evva/pkg/event"
	"github.com/johnny1110/evva/pkg/version"
	"github.com/johnny1110/friday/internal/bootstrap"
	"github.com/johnny1110/friday/internal/risk"
	"github.com/johnny1110/friday/internal/tool"
	"github.com/johnny1110/friday/internal/tui"
)

// kickoffPrompt is the standard start instruction (mirrors .friday/skills/start).
const kickoffPrompt = "開始交易。立即分析所有已設定的市場（見啟動時印出的清單），依授權執行。分析與報告請以中文回覆。"

// startStopMonitor wires the PRD-009 stop-loss/TP monitor (shared by the TUI and
// headless paths). Orphan-stop cleanup is skipped in paper mode (real endpoints).
func startStopMonitor(ctx context.Context) {
	cli, err := tool.SharedBinanceClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "friday: stop monitor disabled (%v)\n", err)
		return
	}
	monitor := risk.NewStopMonitor(tool.NewBinanceStopBroker(cli), time.Second, slog.Default(), tool.LogStopClose)
	tool.SetStopMonitor(monitor)
	go monitor.Start(ctx)
	if !tool.PaperEnabled() {
		go tool.CleanupOrphanStops(ctx, slog.Default())
	}
}

// stdoutEmitter is a headless orchestrator.RoleEmitter: it prints the pipeline's
// text narration to stdout (no TUI), so a batch run is observable in a log.
type stdoutEmitter struct{}

func (stdoutEmitter) EmitRole(role string, e event.Event) {
	if e.Kind != event.KindText || e.Text == nil {
		return
	}
	if role == "" {
		fmt.Println(e.Text.Text)
		return
	}
	fmt.Printf("[%s] %s\n", role, e.Text.Text)
}

// runHeadless runs the orchestrator for a bounded number of rounds without the
// TUI, then exits — for paper-mode batch validation (FRIDAY_PAPER=true). Ctrl+C
// stops early. After it returns, run `go run ./cmd/analyze` for the post-mortem.
func runHeadless(rounds int, fast bool) {
	orch, _, err := bootstrap.New(stdoutEmitter{})
	if err != nil {
		fmt.Fprintln(os.Stderr, "friday: bootstrap failed:", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	startStopMonitor(ctx)

	orch.SetMaxRounds(rounds)
	if fast {
		orch.SetInterval(0) // back-to-back rounds, no 15s live cadence
	}

	mode := "LIVE"
	if tool.PaperEnabled() {
		mode = "PAPER"
	}
	fmt.Fprintf(os.Stderr, "friday: headless %s run — %d round(s), fast=%v\n", mode, rounds, fast)
	if _, err := orch.Run(ctx, kickoffPrompt); err != nil {
		fmt.Fprintln(os.Stderr, "friday: run error:", err)
	}
	fmt.Fprintln(os.Stderr, "friday: headless run complete — `go run ./cmd/analyze` for the post-mortem.")
}

func main() {
	headless := flag.Bool("headless", false, "run N rounds without the TUI then exit (for FRIDAY_PAPER batch validation)")
	rounds := flag.Int("rounds", 30, "headless: number of rounds to run before exiting")
	fast := flag.Bool("fast", false, "headless: run rounds back-to-back with no inter-round delay")
	flag.Parse()

	fmt.Fprintf(os.Stderr, "friday: built on evva %s\n", version.Bare())

	if *headless {
		runHeadless(*rounds, *fast)
		return
	}

	mainTUI()
}

func mainTUI() {
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
	startStopMonitor(monitorCtx)

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
