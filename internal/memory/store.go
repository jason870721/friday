// Package memory is friday's trade-log vector memory (PRD-004). It is a
// lightweight, embedded, file-backed vector store — no external server,
// no embedding model. Each closed trade is logged with a numeric
// market-feature vector; before evaluating a new setup the Analyst
// retrieves the most similar past trades and their outcomes.
//
// Market-feature vectors (RSI, price-vs-MA20, momentum, funding,
// sentiment) are a more faithful notion of "similar market conditions"
// than text embeddings would be, and they need no embedding API.
package memory

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// Features is the numeric characterisation of a market context at the time
// of a trade. The same fields are supplied when logging and when
// recalling, so similarity compares like with like.
type Features struct {
	RSI       float64 `json:"rsi"`         // 0-100
	PriceVsMA float64 `json:"price_vs_ma"` // (price-MA20)/MA20 as a percent, e.g. +0.3
	Momentum  float64 `json:"momentum"`    // -1 falling / 0 mixed / +1 rising
	Funding   float64 `json:"funding"`     // funding rate as a percent, e.g. +0.01
	Sentiment float64 `json:"sentiment"`   // Fear & Greed index, 0-100
}

// vec maps Features to a normalised vector. A constant leading dimension
// keeps every vector non-degenerate (no zero vectors), so cosine
// similarity is always well-defined. Each remaining dimension is scaled to
// roughly [-1,1] so no single feature dominates the angle.
func (f Features) vec() []float64 {
	return []float64{
		1.0,
		f.RSI / 100.0,
		clamp(f.PriceVsMA/10.0, -1, 1),
		clamp(f.Momentum, -1, 1),
		clamp(f.Funding*10.0, -1, 1),
		f.Sentiment / 100.0,
	}
}

// TradeRecord is one logged closed trade.
//
// PnL is the realised price-difference P&L. When the trade was reconciled
// against the exchange ledger (PnLSource == "exchange"), Commission, Funding
// and NetPnL are also populated and Outcome reflects the true wallet impact
// (NetPnL); otherwise the figures are the agent's best-effort report.
type TradeRecord struct {
	Symbol      string   `json:"symbol"`
	Time        int64    `json:"time"` // unix seconds
	Features    Features `json:"features"`
	EntryReason string   `json:"entry_reason"`
	Bias        string   `json:"bias"`                  // LONG / SHORT
	Strategy    string   `json:"strategy,omitempty"`    // triggering strategy, e.g. "momentum" (PRD-014); "" for pre-PRD-014 records
	PnL         float64  `json:"pnl"`                   // realised price PnL in USDT
	Commission  float64  `json:"commission,omitempty"`  // trading fees for the trade (negative)
	Funding     float64  `json:"funding_fee,omitempty"` // funding paid (−) / received (+)
	NetPnL      float64  `json:"net_pnl,omitempty"`     // PnL + Commission + Funding (true wallet impact)
	PnLSource   string   `json:"pnl_source,omitempty"`  // "exchange" (reconciled), "reported" (agent), or "paper"
	Paper       bool     `json:"paper,omitempty"`       // true when logged in paper-trading mode (PRD-021 §4)
	Outcome     string   `json:"outcome"`               // WIN / LOSS / FLAT (derived if empty)
}

// EffectivePnL is the most authoritative PnL figure: the net wallet impact when
// reconciled against the exchange (PRD-011), else the raw realised PnL. This is
// the basis DeriveOutcome and OutcomeStats key off, so WIN/LOSS and the
// per-strategy stats all reflect the true wallet impact.
func (r TradeRecord) EffectivePnL() float64 {
	if r.PnLSource == "exchange" {
		return r.NetPnL
	}
	return r.PnL
}

// DeriveOutcome sets Outcome from the most authoritative figure available: the
// net wallet impact when reconciled against the exchange, else the raw PnL.
func (r *TradeRecord) DeriveOutcome() {
	switch basis := r.EffectivePnL(); {
	case basis > 0:
		r.Outcome = "WIN"
	case basis < 0:
		r.Outcome = "LOSS"
	default:
		r.Outcome = "FLAT"
	}
}

// OutcomeStats summarises the WIN/LOSS record of a set of trades (PRD-014).
// AvgWin is the mean EffectivePnL of the winners (≥0); AvgLoss the mean of the
// losers (≤0, so it reads as a negative number).
type OutcomeStats struct {
	Wins, Losses, Flats int
	AvgWin, AvgLoss     float64
}

// OutcomeStatsOf tallies wins/losses/flats and the average win/loss across the
// given scored trades, keying off each record's EffectivePnL (so the breakdown
// reflects true wallet impact, consistent with DeriveOutcome — PRD-011/014).
func OutcomeStatsOf(scored []Scored) OutcomeStats {
	var st OutcomeStats
	var winSum, lossSum float64
	for _, m := range scored {
		switch p := m.Record.EffectivePnL(); {
		case p > 0:
			st.Wins++
			winSum += p
		case p < 0:
			st.Losses++
			lossSum += p
		default:
			st.Flats++
		}
	}
	if st.Wins > 0 {
		st.AvgWin = winSum / float64(st.Wins)
	}
	if st.Losses > 0 {
		st.AvgLoss = lossSum / float64(st.Losses)
	}
	return st
}

// Scored pairs a record with its similarity to a query (1 = identical
// direction in feature space, 0 = orthogonal).
type Scored struct {
	Record     TradeRecord
	Similarity float64
}

// Store is a process-wide, file-backed collection of trade records. It
// loads the whole file into memory on Open (trade logs are small) and
// appends one JSON line per Log.
type Store struct {
	mu      sync.Mutex
	path    string
	records []TradeRecord
}

// Open loads (or creates) a store backed by path. A missing file is not an
// error — it yields an empty store that the first Log will create.
func Open(path string) (*Store, error) {
	s := &Store{path: path}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("memory: open %s: %w", path, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec TradeRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue // skip a corrupt line rather than fail the whole store
		}
		s.records = append(s.records, rec)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("memory: read %s: %w", path, err)
	}
	return s, nil
}

// Len reports how many records the store holds.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.records)
}

// Log appends a record and persists it. Outcome is derived (from the net
// wallet impact, or raw PnL) when not already set.
func (s *Store) Log(rec TradeRecord) error {
	if rec.Outcome == "" {
		rec.DeriveOutcome()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, rec)

	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("memory: mkdir: %w", err)
	}
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("memory: open for append: %w", err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	if err := enc.Encode(rec); err != nil {
		return fmt.Errorf("memory: encode: %w", err)
	}
	return nil
}

// ConclusiveMinSamples is the number of comparable trades a recall must have for
// its WIN/LOSS record to be treated as statistically meaningful (PRD-023). Below
// this, a (likely all-loss) 2-3-trade sample is noise — the Analyst is told not
// to veto on it, which would otherwise create a losses→never-trade feedback loop.
const ConclusiveMinSamples = 5

// Similar returns the top-k records most similar to f, highest similarity
// first. When symbol is non-empty, only records for that symbol are
// considered. Ties and fewer-than-k cases are handled gracefully.
func (s *Store) Similar(symbol string, f Features, k int) []Scored {
	top, _ := s.similar(symbol, "", f, k)
	return top
}

// SimilarByStrategy is Similar restricted to records attributed to the given
// strategy (PRD-014) — e.g. "how did momentum specifically do in conditions
// like these?". An empty strategy matches everything (same as Similar).
func (s *Store) SimilarByStrategy(symbol, strategy string, f Features, k int) []Scored {
	top, _ := s.similar(symbol, strategy, f, k)
	return top
}

// SimilarConclusive returns the top-k similar records AND whether the recall is
// statistically conclusive — true only when at least ConclusiveMinSamples
// comparable trades exist for the symbol/strategy filter (PRD-023 R5). The
// conclusiveness reflects the full candidate pool, not the k-capped slice, so a
// small k can't make a rich history look thin. recall_trades uses the flag to
// avoid presenting a thin sample as a veto-worthy result.
func (s *Store) SimilarConclusive(symbol, strategy string, f Features, k int) ([]Scored, bool) {
	top, pool := s.similar(symbol, strategy, f, k)
	return top, pool >= ConclusiveMinSamples
}

// similar is the shared scoring core. symbol/strategy are optional filters
// (empty = no filter). It returns the top-k scored records and the pool size —
// the number of records that matched the filters BEFORE the k-cap (used to judge
// conclusiveness).
func (s *Store) similar(symbol, strategy string, f Features, k int) (top []Scored, pool int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	q := f.vec()
	scored := make([]Scored, 0, len(s.records))
	for _, rec := range s.records {
		if symbol != "" && rec.Symbol != symbol {
			continue
		}
		if strategy != "" && rec.Strategy != strategy {
			continue
		}
		scored = append(scored, Scored{Record: rec, Similarity: cosine(q, rec.Features.vec())})
	}
	pool = len(scored)
	sort.SliceStable(scored, func(i, j int) bool {
		return scored[i].Similarity > scored[j].Similarity
	})
	if k > 0 && len(scored) > k {
		scored = scored[:k]
	}
	return scored, pool
}

// OutcomeSummary returns the WIN/LOSS breakdown of the top-k trades most
// similar to f (PRD-014), used by recall_trades to annotate its results.
func (s *Store) OutcomeSummary(symbol string, f Features, k int) OutcomeStats {
	return OutcomeStatsOf(s.Similar(symbol, f, k))
}

func cosine(a, b []float64) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
