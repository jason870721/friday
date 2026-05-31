package tool

import (
	"maps"
	"sync"

	"github.com/johnny1110/friday/internal/binance"
	"github.com/johnny1110/friday/internal/strategy"
)

// PRD-013: live wiring for the cross-symbol divergence strategy. The
// single-symbol klines tools (binance_klines / binance_mtf_klines) each see one
// symbol at a time, but divergence needs the anchor (BTC) AND the queried
// symbol together. We bridge that with a small process-wide cache: every klines
// fetch stores the symbol's recent candles, so a later fetch for another symbol
// can cross-reference the anchor without an extra API call.
//
// The cache is keyed by symbol and OVERWRITTEN on every fetch, so it never
// grows beyond the traded-symbol set and each round's reads replace the prior
// round's (no explicit clear needed). A symbol whose fetch fails keeps its
// stale entry — an accepted degradation (it may produce a one-round-late hint).
const (
	divergenceAnchor  = "BTCUSDT" // hardcoded per PRD-013 (configurable anchor deferred)
	divergenceMovePct = 2.0       // a symbol "moves decisively" past ±2% over the window
	divergenceFlatPct = 0.5       // the anchor is "flat" within ±0.5%
)

var (
	klinesCacheMu sync.Mutex
	klinesCache   = map[string][]binance.Kline{}
)

// cacheKlines stores a symbol's latest candles for cross-symbol divergence.
// A zero-length series is ignored (a failed fetch leaves the prior entry).
func cacheKlines(symbol string, ks []binance.Kline) {
	if symbol == "" || len(ks) == 0 {
		return
	}
	klinesCacheMu.Lock()
	defer klinesCacheMu.Unlock()
	klinesCache[symbol] = ks
}

// divergenceHint returns a one-line divergence note for `symbol` measured
// against the BTC anchor, or "" when there is no divergence setup: the anchor
// is itself trending, fewer than two symbols are known, the anchor isn't
// cached, or this symbol isn't diverging. `ks` is the symbol's freshly-fetched
// series, merged over the cache so the hint reflects the latest read.
func divergenceHint(symbol string, ks []binance.Kline) string {
	klinesCacheMu.Lock()
	data := make(map[string][]binance.Kline, len(klinesCache)+1)
	maps.Copy(data, klinesCache)
	klinesCacheMu.Unlock()

	if len(ks) > 0 {
		data[symbol] = ks
	}
	if len(data) < 2 || len(data[divergenceAnchor]) == 0 {
		return ""
	}

	for _, s := range strategy.DivergenceSignals(data, divergenceAnchor, divergenceMovePct, divergenceFlatPct) {
		if s.Symbol == symbol && s.Direction != strategy.Neutral {
			return "Divergence signal: " + s.Reason
		}
	}
	return ""
}
