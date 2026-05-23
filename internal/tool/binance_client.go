package tool

import (
	"fmt"
	"os"
	"sync"

	"github.com/johnny1110/friday/internal/binance"
)

// Shared Binance client. All nine binance_* tools call sharedBinanceClient()
// to lazily build (and cache) a single *binance.Client per process. The env
// vars are read on first call so a user editing ~/.friday/.env doesn't need
// to know about an init order.
//
// Required env:
//
//	BINANCE_API_KEY     — API key (testnet or mainnet)
//	BINANCE_SECRET_KEY  — corresponding secret
//	BINANCE_BASE_URL    — endpoint; defaults to testnet if empty
var (
	binanceClientOnce sync.Once
	binanceClient     *binance.Client
	binanceClientErr  error
)

const defaultBinanceBaseURL = "https://testnet.binancefuture.com"

// sharedBinanceClient returns the process-wide Binance Futures client.
// Returns an error (cached) if credentials aren't configured — every tool
// surfaces this verbatim so the user sees the same hint regardless of
// which tool the agent tried first.
func sharedBinanceClient() (*binance.Client, error) {
	binanceClientOnce.Do(func() {
		apiKey := os.Getenv("BINANCE_API_KEY")
		secret := os.Getenv("BINANCE_SECRET_KEY")
		baseURL := os.Getenv("BINANCE_BASE_URL")
		if apiKey == "" || secret == "" {
			binanceClientErr = fmt.Errorf(
				"binance: BINANCE_API_KEY and BINANCE_SECRET_KEY must be set in ~/.friday/.env")
			return
		}
		if baseURL == "" {
			baseURL = defaultBinanceBaseURL
		}
		binanceClient = binance.New(baseURL, apiKey, secret)
	})
	return binanceClient, binanceClientErr
}
