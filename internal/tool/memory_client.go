package tool

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/johnny1110/friday/internal/memory"
)

// Shared trade-memory store (PRD-004). Like sharedBinanceClient, all
// memory tools (log_trade, recall_trades) lazily open a single
// process-wide store backed by ~/.friday/memory/trades.jsonl.
var (
	tradeStoreOnce sync.Once
	tradeStore     *memory.Store
	tradeStoreErr  error
)

func sharedTradeStore() (*memory.Store, error) {
	tradeStoreOnce.Do(func() {
		home, _ := os.UserHomeDir()
		path := filepath.Join(home, ".friday", "memory", "trades.jsonl")
		tradeStore, tradeStoreErr = memory.Open(path)
	})
	return tradeStore, tradeStoreErr
}
