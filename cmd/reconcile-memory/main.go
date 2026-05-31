// Command reconcile-memory repairs friday's trade memory (trades.jsonl) by
// replacing each record's agent-reported PnL with the exchange's ground truth.
//
// The Executor used to write a self-reported PnL into memory, which proved
// unreliable (losing closes logged as WINs). This tool re-derives each trade's
// realised PnL / commission / funding from the Binance income ledger, matches
// it to the logged trade by symbol and time, and rewrites the record's
// PnL / NetPnL / Outcome. The original entry_reason and market features (the
// learning signal) are preserved — only the corrupted outcome figures change.
//
// Dry-run by default; pass -write to apply (the original is backed up first).
//
//	go run ./cmd/reconcile-memory                 # preview against ~/.friday/memory/trades.jsonl
//	go run ./cmd/reconcile-memory -write          # apply, backing up to trades.jsonl.bak
//
// Credentials come from the same env as the app: BINANCE_API_KEY,
// BINANCE_SECRET_KEY, BINANCE_BASE_URL.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/johnny1110/friday/internal/binance"
	"github.com/johnny1110/friday/internal/memory"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "reconcile-memory:", err)
		os.Exit(1)
	}
}

func run() error {
	home, _ := os.UserHomeDir()
	defaultPath := filepath.Join(home, ".friday", "memory", "trades.jsonl")

	path := flag.String("path", defaultPath, "trades.jsonl to reconcile")
	toleranceSec := flag.Int("tolerance", 600, "max seconds between a logged trade and its exchange close to consider them the same trade")
	write := flag.Bool("write", false, "apply changes (default: dry-run preview)")
	flag.Parse()

	records, err := loadRecords(*path)
	if err != nil {
		return err
	}
	if len(records) == 0 {
		fmt.Printf("no records in %s — nothing to do\n", *path)
		return nil
	}

	apiKey, secret := os.Getenv("BINANCE_API_KEY"), os.Getenv("BINANCE_SECRET_KEY")
	if apiKey == "" || secret == "" {
		return fmt.Errorf("BINANCE_API_KEY/BINANCE_SECRET_KEY must be set to query the income ledger")
	}
	cli := binance.New(baseURL(), apiKey, secret)

	// Pull the income ledger per symbol over the full span of logged trades and
	// group it into discrete close events.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	eventsBySymbol, err := loadCloseEvents(ctx, cli, records)
	if err != nil {
		return err
	}

	tol := int64(*toleranceSec)
	matched, unverified := 0, 0

	fmt.Printf("%-10s %-19s %-8s  %-22s  %s\n", "symbol", "time", "old", "exchange (realised/net)", "outcome")
	fmt.Println(rule(94))

	for i := range records {
		r := &records[i]
		ev := takeNearestEvent(eventsBySymbol[r.Symbol], r.Time, tol)
		old := fmt.Sprintf("%+.2f %s", r.PnL, r.Outcome)
		if ev == nil {
			unverified++
			fmt.Printf("%-10s %-19s %-8s  %-22s  %s\n",
				r.Symbol, tsStr(r.Time), old, "(no matching close)", "UNVERIFIED — kept as-is")
			continue
		}
		matched++
		r.PnL = ev.RealizedPnL
		r.Commission = ev.Commission
		r.Funding = ev.Funding
		r.NetPnL = ev.Net()
		r.PnLSource = "exchange"
		r.Outcome = "" // force re-derivation from the net wallet impact
		r.DeriveOutcome()
		fmt.Printf("%-10s %-19s %-8s  %-22s  %s\n",
			r.Symbol, tsStr(r.Time), old,
			fmt.Sprintf("%+.4f / %+.4f", r.PnL, r.NetPnL), r.Outcome)
	}

	fmt.Println(rule(94))
	fmt.Printf("%d record(s): %d reconciled from exchange, %d unverified\n", len(records), matched, unverified)

	if !*write {
		fmt.Println("\ndry-run — no file written. Re-run with -write to apply (original backed up to .bak).")
		return nil
	}
	if matched == 0 {
		fmt.Println("\nnothing reconciled — not writing.")
		return nil
	}
	if err := backup(*path); err != nil {
		return err
	}
	if err := writeRecords(*path, records); err != nil {
		return err
	}
	fmt.Printf("\nwrote %d records to %s (backup: %s.bak)\n", len(records), *path, *path)
	return nil
}

// closeEvent is one reconstructed close: the realised P&L and costs of a single
// position-closing event on the exchange, with the time it occurred.
type closeEvent struct {
	binance.RealizedSummary
	TimeMs int64
	used   bool
}

// loadCloseEvents fetches the income ledger for every symbol present in the
// records and clusters it into discrete close events.
func loadCloseEvents(ctx context.Context, cli *binance.Client, records []memory.TradeRecord) (map[string][]*closeEvent, error) {
	minT, maxT := records[0].Time, records[0].Time
	symbols := map[string]bool{}
	for _, r := range records {
		symbols[r.Symbol] = true
		if r.Time < minT {
			minT = r.Time
		}
		if r.Time > maxT {
			maxT = r.Time
		}
	}
	// Pad the window generously so a close logged slightly after the fill is
	// still inside the queried range.
	startMs := (minT - 3600) * 1000
	endMs := (maxT + 3600) * 1000

	out := map[string][]*closeEvent{}
	for sym := range symbols {
		rows, err := cli.Income(ctx, sym, startMs, endMs, 1000)
		if err != nil {
			return nil, fmt.Errorf("income %s: %w", sym, err)
		}
		out[sym] = clusterEvents(rows)
	}
	return out, nil
}

// clusterEvents groups income rows into close events: rows within clusterGapMs
// of each other form one event. Only clusters that contain at least one
// REALIZED_PNL row count as a close.
func clusterEvents(rows []binance.IncomeEntry) []*closeEvent {
	const clusterGapMs int64 = 5000
	sort.Slice(rows, func(i, j int) bool { return rows[i].Time < rows[j].Time })

	var events []*closeEvent
	var batch []binance.IncomeEntry
	flush := func() {
		if len(batch) == 0 {
			return
		}
		s := binance.SummarizeRealized(batch)
		if s.RealizedRows > 0 {
			events = append(events, &closeEvent{RealizedSummary: s, TimeMs: batch[0].Time})
		}
		batch = nil
	}
	var last int64
	for _, r := range rows {
		if len(batch) > 0 && r.Time-last > clusterGapMs {
			flush()
		}
		batch = append(batch, r)
		last = r.Time
	}
	flush()
	return events
}

// takeNearestEvent returns (and marks used) the unused close event nearest to
// tradeTimeSec, within tolSec. Greedy nearest-match prevents two logged trades
// from claiming the same exchange close.
func takeNearestEvent(events []*closeEvent, tradeTimeSec, tolSec int64) *closeEvent {
	var best *closeEvent
	var bestDiff int64
	for _, ev := range events {
		if ev.used {
			continue
		}
		diff := tradeTimeSec - ev.TimeMs/1000
		if diff < 0 {
			diff = -diff
		}
		if diff > tolSec {
			continue
		}
		if best == nil || diff < bestDiff {
			best, bestDiff = ev, diff
		}
	}
	if best != nil {
		best.used = true
	}
	return best
}

func loadRecords(path string) ([]memory.TradeRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	var out []memory.TradeRecord
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var r memory.TradeRecord
		if err := json.Unmarshal(line, &r); err != nil {
			return nil, fmt.Errorf("parse line: %w", err)
		}
		out = append(out, r)
	}
	return out, sc.Err()
}

func writeRecords(path string, records []memory.TradeRecord) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, r := range records {
		if err := enc.Encode(r); err != nil {
			return err
		}
	}
	return nil
}

func backup(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return os.WriteFile(path+".bak", data, 0o644)
}

func baseURL() string {
	if v := os.Getenv("BINANCE_BASE_URL"); v != "" {
		return v
	}
	return "https://testnet.binancefuture.com"
}

func tsStr(sec int64) string { return time.Unix(sec, 0).Format("2006-01-02 15:04:05") }

func rule(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = '-'
	}
	return string(b)
}
