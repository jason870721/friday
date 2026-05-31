package tool

import (
	"strings"
	"testing"

	"github.com/johnny1110/friday/internal/binance"
)

// closesToKlines builds a candle series where only Close matters (what
// divergence's recentReturnPct reads).
func closesToKlines(closes ...float64) []binance.Kline {
	ks := make([]binance.Kline, len(closes))
	for i, c := range closes {
		ks[i] = binance.Kline{Close: c}
	}
	return ks
}

func flatSeries(v float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func rampSeries(start, step float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = start + float64(i)*step
	}
	return out
}

// resetKlinesCache clears the package-global cache so tests don't contaminate
// each other.
func resetKlinesCache() {
	klinesCacheMu.Lock()
	defer klinesCacheMu.Unlock()
	klinesCache = map[string][]binance.Kline{}
}

func TestDivergenceHint_FiresWhenBTCFlatAndSymbolMoves(t *testing.T) {
	resetKlinesCache()
	// BTC flat (0% over the window); SOL ramping ~+15% over the last 10 candles.
	cacheKlines(divergenceAnchor, closesToKlines(flatSeries(100, 12)...))
	mover := closesToKlines(rampSeries(100, 1.5, 12)...)

	hint := divergenceHint("SOLUSDT", mover)
	if !strings.Contains(hint, "Divergence signal") || !strings.Contains(hint, "SOLUSDT") {
		t.Fatalf("expected a SOLUSDT divergence hint, got %q", hint)
	}
}

func TestDivergenceHint_EmptyWhenAnchorTrending(t *testing.T) {
	resetKlinesCache()
	// BTC itself ramping ~+15% → not flat → no divergence setup.
	cacheKlines(divergenceAnchor, closesToKlines(rampSeries(100, 1.5, 12)...))
	mover := closesToKlines(rampSeries(100, 1.5, 12)...)

	if hint := divergenceHint("SOLUSDT", mover); hint != "" {
		t.Errorf("anchor trending should yield no hint, got %q", hint)
	}
}

func TestDivergenceHint_EmptyWithoutAnchor(t *testing.T) {
	resetKlinesCache()
	// Only the queried symbol is known — the anchor is not cached.
	mover := closesToKlines(rampSeries(100, 1.5, 12)...)
	if hint := divergenceHint("SOLUSDT", mover); hint != "" {
		t.Errorf("no anchor cached should yield no hint, got %q", hint)
	}
}

func TestDivergenceHint_NoneOnTheAnchorItself(t *testing.T) {
	resetKlinesCache()
	cacheKlines("SOLUSDT", closesToKlines(rampSeries(100, 1.5, 12)...))
	// Querying BTC (the anchor) never produces a divergence hint for itself.
	if hint := divergenceHint(divergenceAnchor, closesToKlines(flatSeries(100, 12)...)); hint != "" {
		t.Errorf("anchor query should yield no self-hint, got %q", hint)
	}
}

func TestKlinesCache_OverwritesAndDoesNotLeak(t *testing.T) {
	resetKlinesCache()
	// Re-caching the same symbol across "rounds" overwrites, never grows.
	for round := range 5 {
		cacheKlines("BTCUSDT", closesToKlines(flatSeries(100+float64(round), 12)...))
		cacheKlines("SOLUSDT", closesToKlines(rampSeries(100, 1, 12)...))
	}
	klinesCacheMu.Lock()
	n := len(klinesCache)
	klinesCacheMu.Unlock()
	if n != 2 {
		t.Errorf("cache holds %d symbols after 5 rounds; want 2 (overwrite, no leak)", n)
	}
	// A zero-length series (failed fetch) must not wipe the prior entry.
	cacheKlines("BTCUSDT", nil)
	klinesCacheMu.Lock()
	_, ok := klinesCache["BTCUSDT"]
	klinesCacheMu.Unlock()
	if !ok {
		t.Error("a failed (empty) fetch should leave the prior cache entry intact")
	}
}
