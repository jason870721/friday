// Command analyze is friday's post-mortem session analyser (PRD-021 §2). It
// reads the two structured logs friday writes — rounds.jsonl (every round's full
// Analyst→Risk→Executor pipeline) and trades.jsonl (every closed trade) — and
// prints a report the operator can use to answer "did momentum make money
// today?", "was the Analyst's bias accurate?", and "when did the breaker trip?".
//
//	go run ./cmd/analyze                 # text report from ~/.friday/memory/*.jsonl
//	go run ./cmd/analyze -json           # structured JSON
//	go run ./cmd/analyze -trades x.jsonl -rounds y.jsonl
//
// It places no orders and touches no network — it only reads local logs.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/johnny1110/friday/internal/memory"
	"github.com/johnny1110/friday/internal/orchestrator"
)

func main() {
	home, _ := os.UserHomeDir()
	defRounds := filepath.Join(home, ".friday", "memory", "rounds.jsonl")
	defTrades := filepath.Join(home, ".friday", "memory", "trades.jsonl")

	roundsPath := flag.String("rounds", defRounds, "path to rounds.jsonl")
	tradesPath := flag.String("trades", defTrades, "path to trades.jsonl")
	asJSON := flag.Bool("json", false, "emit structured JSON instead of a text report")
	flag.Parse()

	rounds := loadRounds(*roundsPath)
	trades := loadTrades(*tradesPath)

	rep := buildReport(rounds, trades)

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			fmt.Fprintln(os.Stderr, "analyze: encode:", err)
			os.Exit(1)
		}
		return
	}
	fmt.Print(rep.text())
}

// --- loading (graceful: missing/empty/malformed → skip, never crash) ---

func loadRounds(path string) []orchestrator.RoundRecord {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []orchestrator.RoundRecord
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 8*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r orchestrator.RoundRecord
		if json.Unmarshal([]byte(line), &r) == nil {
			out = append(out, r)
		}
	}
	return out
}

func loadTrades(path string) []memory.TradeRecord {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []memory.TradeRecord
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 8*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var t memory.TradeRecord
		if json.Unmarshal([]byte(line), &t) == nil && t.Symbol != "" {
			t.DeriveOutcome() // ensure Outcome is set for older records
			out = append(out, t)
		}
	}
	return out
}

// --- report model ---

// Stats is the win/PnL summary shared by the per-strategy / per-symbol / regime
// breakdowns.
type Stats struct {
	Trades          int     `json:"trades"`
	Wins            int     `json:"wins"`
	Losses          int     `json:"losses"`
	WinRate         float64 `json:"win_rate"`
	TotalPnL        float64 `json:"total_pnl"`
	AvgPnL          float64 `json:"avg_pnl"` // = per-trade expectancy in USDT (WR×avgWin − LR×|avgLoss|)
	AvgWin          float64 `json:"avg_win"`
	AvgLoss         float64 `json:"avg_loss"`      // negative (mean of losing trades)
	Payoff          float64 `json:"payoff"`        // avgWin / |avgLoss|; -1 ⇒ ∞ (no losses)
	ProfitFactor    float64 `json:"profit_factor"` // gross wins / abs(gross losses); -1 ⇒ ∞ (no losses)
	Sharpe          float64 `json:"sharpe"`        // per-trade mean(PnL)/stddev(PnL), NOT annualised
	MaxConsecLosses int     `json:"max_consec_losses"`
}

// Report is the full structured post-mortem (also the -json payload).
type Report struct {
	Overview struct {
		Rounds          int     `json:"rounds"`
		Trades          int     `json:"trades"`
		Paper           bool    `json:"paper"`
		FirstTrade      string  `json:"first_trade,omitempty"`
		LastTrade       string  `json:"last_trade,omitempty"`
		TotalPnL        float64 `json:"total_pnl"`
		TotalFees       float64 `json:"total_fees"`
		MaxDrawdown     float64 `json:"max_drawdown"`
		WinRate         float64 `json:"win_rate"`
		Expectancy      float64 `json:"expectancy"` // per-trade avg net PnL (USDT)
		AvgWin          float64 `json:"avg_win"`
		AvgLoss         float64 `json:"avg_loss"`
		Payoff          float64 `json:"payoff"`
		ProfitFactor    float64 `json:"profit_factor"`
		Sharpe          float64 `json:"sharpe"`
		MaxConsecLosses int     `json:"max_consec_losses"`
	} `json:"overview"`
	PerStrategy      map[string]Stats            `json:"per_strategy"`
	PerSymbol        map[string]Stats            `json:"per_symbol"`
	PerRegime        map[string]Stats            `json:"per_regime"`
	RegimeByStrategy map[string]map[string]Stats `json:"regime_by_strategy"`
	AnalystAccuracy  struct {
		Evaluated int     `json:"evaluated"`
		Correct   int     `json:"correct"`
		Accuracy  float64 `json:"accuracy"`
	} `json:"analyst_accuracy"`
	BreakerTimeline []BreakerEvent `json:"breaker_timeline"`
}

// BreakerEvent is one PAUSED/HALTED span in the breaker timeline.
type BreakerEvent struct {
	State     string `json:"state"`
	FromRound int    `json:"from_round"`
	ToRound   int    `json:"to_round"`
	Rounds    int    `json:"rounds"`
	Reason    string `json:"reason"`
}

func buildReport(rounds []orchestrator.RoundRecord, trades []memory.TradeRecord) Report {
	var rep Report
	rep.PerStrategy = statsByKey(trades, func(t memory.TradeRecord) string { return orElse(t.Strategy, "unknown") })
	rep.PerSymbol = statsByKey(trades, func(t memory.TradeRecord) string { return t.Symbol })

	// Overview.
	rep.Overview.Rounds = roundCount(rounds)
	rep.Overview.Trades = len(trades)
	rep.Overview.Paper = anyPaper(rounds, trades)
	rep.Overview.TotalPnL = sumPnL(trades)
	rep.Overview.TotalFees = sumFees(trades)
	rep.Overview.MaxDrawdown = maxDrawdown(trades)

	// Aggregate risk/edge metrics over ALL trades (same maths as the per-group
	// tables) so the overview answers "is there a positive expectancy, and how
	// steady is it?" at a glance.
	ov := statsOf(trades)
	rep.Overview.WinRate = ov.WinRate
	rep.Overview.Expectancy = ov.AvgPnL
	rep.Overview.AvgWin = ov.AvgWin
	rep.Overview.AvgLoss = ov.AvgLoss
	rep.Overview.Payoff = ov.Payoff
	rep.Overview.ProfitFactor = ov.ProfitFactor
	rep.Overview.Sharpe = ov.Sharpe
	rep.Overview.MaxConsecLosses = ov.MaxConsecLosses
	if len(trades) > 0 {
		sorted := append([]memory.TradeRecord(nil), trades...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].Time < sorted[j].Time })
		rep.Overview.FirstTrade = unixToStr(sorted[0].Time)
		rep.Overview.LastTrade = unixToStr(sorted[len(sorted)-1].Time)
	}

	// Per-regime (attribute each trade to the regime of its opening round).
	regimeOf := regimeAttributor(rounds)
	rep.PerRegime = statsByKey(trades, func(t memory.TradeRecord) string { return orElse(regimeOf(t), "UNKNOWN") })
	rep.RegimeByStrategy = regimeByStrategy(trades, regimeOf)

	// Analyst accuracy + breaker timeline.
	rep.AnalystAccuracy.Evaluated, rep.AnalystAccuracy.Correct, rep.AnalystAccuracy.Accuracy = analystAccuracy(rounds, trades)
	rep.BreakerTimeline = breakerTimeline(rounds)
	return rep
}

// --- stats ---

func statsByKey(trades []memory.TradeRecord, key func(memory.TradeRecord) string) map[string]Stats {
	groups := map[string][]memory.TradeRecord{}
	for _, t := range trades {
		k := key(t)
		groups[k] = append(groups[k], t)
	}
	out := make(map[string]Stats, len(groups))
	for k, g := range groups {
		out[k] = statsOf(g)
	}
	return out
}

func statsOf(trades []memory.TradeRecord) Stats {
	var s Stats
	var grossWin, grossLoss float64

	// Sort by time so the consecutive-loss streak is chronological.
	sorted := append([]memory.TradeRecord(nil), trades...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Time < sorted[j].Time })
	pnls := make([]float64, 0, len(sorted))
	consec := 0
	for _, t := range sorted {
		p := t.EffectivePnL()
		pnls = append(pnls, p)
		s.Trades++
		s.TotalPnL += p
		switch {
		case p > 0:
			s.Wins++
			grossWin += p
			consec = 0
		case p < 0:
			s.Losses++
			grossLoss += p
			consec++
			if consec > s.MaxConsecLosses {
				s.MaxConsecLosses = consec
			}
		}
	}
	if s.Trades > 0 {
		s.AvgPnL = s.TotalPnL / float64(s.Trades) // dollar expectancy per trade
	}
	if s.Wins+s.Losses > 0 {
		s.WinRate = float64(s.Wins) / float64(s.Wins+s.Losses)
	}
	if s.Wins > 0 {
		s.AvgWin = grossWin / float64(s.Wins)
	}
	if s.Losses > 0 {
		s.AvgLoss = grossLoss / float64(s.Losses)
	}
	s.Payoff = ratioOrInf(s.AvgWin, math.Abs(s.AvgLoss))
	s.ProfitFactor = ratioOrInf(grossWin, math.Abs(grossLoss))

	// Per-trade Sharpe: mean(PnL)/stddev(PnL). Not annualised — a unitless
	// consistency read (higher = steadier edge), comparable across strategies.
	if len(pnls) > 1 {
		m := meanF(pnls)
		if sd := stddevF(pnls, m); sd > 0 {
			s.Sharpe = m / sd
		}
	}
	return s
}

// ratioOrInf returns num/den, or -1 as a sentinel for ∞ when den is 0 but num is
// positive (a profitable group with no losing side), or 0 when both are 0.
func ratioOrInf(num, den float64) float64 {
	switch {
	case den == 0 && num > 0:
		return -1
	case den == 0:
		return 0
	default:
		return num / den
	}
}

func meanF(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	var sum float64
	for _, x := range v {
		sum += x
	}
	return sum / float64(len(v))
}

func stddevF(v []float64, mean float64) float64 {
	if len(v) < 2 {
		return 0
	}
	var sum float64
	for _, x := range v {
		d := x - mean
		sum += d * d
	}
	return math.Sqrt(sum / float64(len(v)-1))
}

func regimeByStrategy(trades []memory.TradeRecord, regimeOf func(memory.TradeRecord) string) map[string]map[string]Stats {
	byRegime := map[string][]memory.TradeRecord{}
	for _, t := range trades {
		r := orElse(regimeOf(t), "UNKNOWN")
		byRegime[r] = append(byRegime[r], t)
	}
	out := make(map[string]map[string]Stats, len(byRegime))
	for r, g := range byRegime {
		out[r] = statsByKey(g, func(t memory.TradeRecord) string { return orElse(t.Strategy, "unknown") })
	}
	return out
}

// --- overview helpers ---

func roundCount(rounds []orchestrator.RoundRecord) int {
	max := 0
	for _, r := range rounds {
		if r.Round > max {
			max = r.Round
		}
	}
	if max == 0 {
		return len(rounds)
	}
	return max
}

func sumPnL(trades []memory.TradeRecord) float64 {
	var s float64
	for _, t := range trades {
		s += t.EffectivePnL()
	}
	return s
}

func sumFees(trades []memory.TradeRecord) float64 {
	var s float64
	for _, t := range trades {
		// Commission/Funding are signed costs (negative when paid); fee SPEND is
		// the positive magnitude. Only exchange-reconciled trades carry them.
		s += -(t.Commission + t.Funding)
	}
	return s
}

func maxDrawdown(trades []memory.TradeRecord) float64 {
	sorted := append([]memory.TradeRecord(nil), trades...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Time < sorted[j].Time })
	var cum, peak, maxDD float64
	for _, t := range sorted {
		cum += t.EffectivePnL()
		if cum > peak {
			peak = cum
		}
		if dd := peak - cum; dd > maxDD {
			maxDD = dd
		}
	}
	return maxDD
}

func anyPaper(rounds []orchestrator.RoundRecord, trades []memory.TradeRecord) bool {
	for _, r := range rounds {
		if r.Paper {
			return true
		}
	}
	for _, t := range trades {
		if t.Paper {
			return true
		}
	}
	return false
}

// --- regime attribution: each trade → the regime of its opening round ---

// regimeAttributor returns a function mapping a trade to the market regime it
// was OPENED under: the latest round at/before the trade's time that both names
// a regime for the symbol AND opened a position on it. Falls back to the latest
// round with any regime for the symbol before the trade.
func regimeAttributor(rounds []orchestrator.RoundRecord) func(memory.TradeRecord) string {
	type rr struct {
		t      int64
		regime string
		opened bool
	}
	bySym := map[string][]rr{}
	for _, r := range rounds {
		ts := parseRFC(r.Time)
		opened := map[string]bool{}
		for _, d := range r.Decisions {
			if d.Action == "OPEN_LONG" || d.Action == "OPEN_SHORT" {
				opened[d.Symbol] = true
			}
		}
		for sym, regime := range r.Regimes {
			bySym[sym] = append(bySym[sym], rr{t: ts, regime: regime, opened: opened[sym]})
		}
	}
	for sym := range bySym {
		sort.Slice(bySym[sym], func(i, j int) bool { return bySym[sym][i].t < bySym[sym][j].t })
	}
	return func(tr memory.TradeRecord) string {
		recs := bySym[tr.Symbol]
		openRegime, latest := "", ""
		for _, r := range recs {
			if r.t > tr.Time {
				break
			}
			latest = r.regime // latest regime at/before the trade
			if r.opened {
				openRegime = r.regime // regime of the round that OPENED the position
			}
		}
		if openRegime != "" {
			return openRegime // prefer the opening round's regime
		}
		return latest
	}
}

// --- analyst accuracy ---

// analystAccuracy joins each trade to its opening round and checks whether the
// Analyst's directional bias on that symbol matched a winning outcome (PRD-021
// §2). Only rounds with a directional bias AND an OPEN decision count.
func analystAccuracy(rounds []orchestrator.RoundRecord, trades []memory.TradeRecord) (evaluated, correct int, accuracy float64) {
	type rr struct {
		t      int64
		bias   string // BULLISH/BEARISH/NEUTRAL
		opened bool
	}
	bySym := map[string][]rr{}
	for _, r := range rounds {
		ts := parseRFC(r.Time)
		opened := map[string]bool{}
		for _, d := range r.Decisions {
			if d.Action == "OPEN_LONG" || d.Action == "OPEN_SHORT" {
				opened[d.Symbol] = true
			}
		}
		for _, a := range r.Analysis {
			bySym[a.Symbol] = append(bySym[a.Symbol], rr{t: ts, bias: a.Bias, opened: opened[a.Symbol]})
		}
	}
	for sym := range bySym {
		sort.Slice(bySym[sym], func(i, j int) bool { return bySym[sym][i].t < bySym[sym][j].t })
	}

	for _, tr := range trades {
		// Find the latest opening round at/before the trade with a directional bias.
		var matched *rr
		for i := range bySym[tr.Symbol] {
			r := bySym[tr.Symbol][i]
			if r.t > tr.Time {
				break
			}
			if r.opened && (r.bias == "BULLISH" || r.bias == "BEARISH") {
				rr := r
				matched = &rr
			}
		}
		if matched == nil {
			continue
		}
		evaluated++
		// The Analyst's bias was "accurate" when the trade it drove WON.
		if tr.EffectivePnL() > 0 {
			correct++
		}
	}
	if evaluated > 0 {
		accuracy = float64(correct) / float64(evaluated)
	}
	return evaluated, correct, accuracy
}

// --- breaker timeline ---

func breakerTimeline(rounds []orchestrator.RoundRecord) []BreakerEvent {
	sorted := append([]orchestrator.RoundRecord(nil), rounds...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Round < sorted[j].Round })

	var events []BreakerEvent
	var cur *BreakerEvent
	flush := func(toRound int) {
		if cur != nil {
			cur.ToRound = toRound
			cur.Rounds = cur.ToRound - cur.FromRound + 1
			events = append(events, *cur)
			cur = nil
		}
	}
	for _, r := range sorted {
		state := breakerWord(r.Breaker)
		switch state {
		case "PAUSED", "HALTED":
			if cur == nil {
				cur = &BreakerEvent{State: state, FromRound: r.Round, Reason: r.Breaker}
			} else if cur.State != state {
				flush(r.Round - 1)
				cur = &BreakerEvent{State: state, FromRound: r.Round, Reason: r.Breaker}
			}
		default: // NORMAL / unknown → close any open span
			flush(r.Round - 1)
		}
	}
	if cur != nil && len(sorted) > 0 {
		flush(sorted[len(sorted)-1].Round)
	}
	return events
}

func breakerWord(s string) string {
	s = strings.TrimSpace(s)
	switch {
	case strings.HasPrefix(s, "PAUSED"):
		return "PAUSED"
	case strings.HasPrefix(s, "HALTED"):
		return "HALTED"
	case strings.HasPrefix(s, "NORMAL"):
		return "NORMAL"
	default:
		return ""
	}
}

// --- text rendering ---

func (rep Report) text() string {
	var b strings.Builder
	hr := strings.Repeat("─", 60)

	fmt.Fprintf(&b, "%s\nFRIDAY SESSION POST-MORTEM%s\n%s\n", hr, paperBadge(rep.Overview.Paper), hr)

	// 1. Overview.
	fmt.Fprintf(&b, "\n[1] SESSION OVERVIEW\n")
	fmt.Fprintf(&b, "  Rounds run     : %d\n", rep.Overview.Rounds)
	fmt.Fprintf(&b, "  Trades taken   : %d\n", rep.Overview.Trades)
	if rep.Overview.FirstTrade != "" {
		fmt.Fprintf(&b, "  Time range     : %s → %s\n", rep.Overview.FirstTrade, rep.Overview.LastTrade)
	}
	fmt.Fprintf(&b, "  Total net PnL  : %+.4f USDT\n", rep.Overview.TotalPnL)
	fmt.Fprintf(&b, "  Total fees     : %.4f USDT\n", rep.Overview.TotalFees)
	fmt.Fprintf(&b, "  Max drawdown   : %.4f USDT\n", rep.Overview.MaxDrawdown)
	fmt.Fprintf(&b, "  Win rate       : %.1f%%\n", rep.Overview.WinRate*100)
	fmt.Fprintf(&b, "  Expectancy/trade: %+.4f USDT\n", rep.Overview.Expectancy)
	fmt.Fprintf(&b, "  Avg win / loss : %+.4f / %+.4f  (payoff %s, PF %s)\n",
		rep.Overview.AvgWin, rep.Overview.AvgLoss, pfStr(rep.Overview.Payoff), pfStr(rep.Overview.ProfitFactor))
	fmt.Fprintf(&b, "  Sharpe (/trade): %.2f\n", rep.Overview.Sharpe)
	fmt.Fprintf(&b, "  Max consec loss: %d\n", rep.Overview.MaxConsecLosses)

	// 2. Per-strategy.
	fmt.Fprintf(&b, "\n[2] PER-STRATEGY\n")
	writeStatsTable(&b, rep.PerStrategy)

	// 3. Per-symbol.
	fmt.Fprintf(&b, "\n[3] PER-SYMBOL\n")
	writeStatsTable(&b, rep.PerSymbol)

	// 4. Per-regime.
	fmt.Fprintf(&b, "\n[4] PER-REGIME\n")
	writeStatsTable(&b, rep.PerRegime)
	for _, regime := range sortedKeys(rep.RegimeByStrategy) {
		fmt.Fprintf(&b, "  · %s by strategy:\n", regime)
		for _, s := range sortedKeys(rep.RegimeByStrategy[regime]) {
			st := rep.RegimeByStrategy[regime][s]
			fmt.Fprintf(&b, "      %-14s %s\n", s, oneLine(st))
		}
	}

	// 5. Analyst accuracy.
	fmt.Fprintf(&b, "\n[5] ANALYST ACCURACY (directional bias → winning trade)\n")
	if rep.AnalystAccuracy.Evaluated == 0 {
		fmt.Fprintf(&b, "  no bias-driven trades to evaluate\n")
	} else {
		fmt.Fprintf(&b, "  %d/%d correct (%.1f%%)\n",
			rep.AnalystAccuracy.Correct, rep.AnalystAccuracy.Evaluated, rep.AnalystAccuracy.Accuracy*100)
	}

	// 6. Breaker timeline.
	fmt.Fprintf(&b, "\n[6] BREAKER TIMELINE\n")
	if len(rep.BreakerTimeline) == 0 {
		fmt.Fprintf(&b, "  breaker stayed NORMAL all session\n")
	} else {
		for _, e := range rep.BreakerTimeline {
			fmt.Fprintf(&b, "  %s rounds %d–%d (%d round(s)): %s\n",
				e.State, e.FromRound, e.ToRound, e.Rounds, e.Reason)
		}
	}
	fmt.Fprintf(&b, "%s\n", hr)
	return b.String()
}

func writeStatsTable(b *strings.Builder, m map[string]Stats) {
	keys := sortedKeys(m)
	if len(keys) == 0 {
		fmt.Fprintf(b, "  (no trades)\n")
		return
	}
	fmt.Fprintf(b, "  %-14s %6s %7s %12s %11s %8s %7s %6s\n", "key", "trades", "win%", "totalPnL", "expect", "PF", "payoff", "maxCL")
	for _, k := range keys {
		fmt.Fprintf(b, "  %-14s %s\n", k, oneLine(m[k]))
	}
}

func oneLine(s Stats) string {
	return fmt.Sprintf("%6d %6.1f%% %+12.4f %+11.4f %8s %7s %6d",
		s.Trades, s.WinRate*100, s.TotalPnL, s.AvgPnL, pfStr(s.ProfitFactor), pfStr(s.Payoff), s.MaxConsecLosses)
}

func pfStr(pf float64) string {
	if pf < 0 {
		return "∞"
	}
	return fmt.Sprintf("%.2f", pf)
}

func paperBadge(paper bool) string {
	if paper {
		return "  [PAPER]"
	}
	return ""
}

// --- small helpers ---

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func orElse(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

func parseRFC(s string) int64 {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return 0
	}
	return t.Unix()
}

func unixToStr(sec int64) string {
	if sec == 0 {
		return ""
	}
	return time.Unix(sec, 0).UTC().Format("2006-01-02 15:04:05Z")
}
