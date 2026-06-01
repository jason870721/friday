package tool

import "sync"

// lastRegime holds the most recent market regime (TRENDING / RANGING /
// TRANSITIONAL) computed per symbol by binance_mtf_klines (PRD-021 §2). The
// orchestrator reads it when writing the round log so the post-mortem tool can
// attribute each trade to the regime it was opened under. In-memory, last-write
// wins — a fresh value is written every round the symbol is analysed.
var (
	regimeMu   sync.RWMutex
	lastRegime = map[string]string{}
)

// recordRegime stores a symbol's latest regime classification.
func recordRegime(symbol, regime string) {
	regimeMu.Lock()
	defer regimeMu.Unlock()
	lastRegime[symbol] = regime
}

// RegimeFor returns the most recently recorded regime for a symbol, or "" when
// none has been computed yet this session.
func RegimeFor(symbol string) string {
	regimeMu.RLock()
	defer regimeMu.RUnlock()
	return lastRegime[symbol]
}
