package bootstrap

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/johnny1110/friday/internal/binance"
	"github.com/johnny1110/friday/internal/orchestrator"
)

// defaultSymbols is friday's out-of-the-box market list when FRIDAY_SYMBOLS is
// unset: the three core crypto pairs plus four US-stock perps (NVDA/GOOGL/AMZN/
// META). Availability of the stock perps varies by endpoint — the startup
// exchangeInfo preflight (resolveSymbols) drops any symbol the endpoint does
// not list as TRADING, so an unavailable one resolves away cleanly and the bot
// runs on whatever survives. Symbols become tradable with no code change the
// moment the venue lists them.
const defaultSymbols = "BTCUSDT,ETHUSDT,SOLUSDT,NVDAUSDT,GOOGLUSDT,AMZNUSDT,METAUSDT"

// fallbackStepSizes seeds the quantity step for the core pairs so the Risk
// Manager prompt still carries a concrete sizing hint if the preflight is
// unreachable (network error). Symbols absent from this map are only kept when
// the preflight actively confirms them.
var fallbackStepSizes = map[string]string{
	"BTCUSDT": "0.001",
	"ETHUSDT": "0.01",
	"SOLUSDT": "0.1",
}

// resolveSymbols turns FRIDAY_SYMBOLS (or the default) into the venue-validated
// market list the orchestrator trades. It calls Binance exchangeInfo once at
// startup and keeps only symbols the endpoint lists as TRADING, attaching each
// one's real LOT_SIZE step. Symbols the venue does not list (e.g. the stock
// perps on testnet) are logged and skipped here — so the per-round agents never
// iterate an unavailable market and a single bad symbol can't poison the loop.
//
// If the preflight itself is unreachable, it degrades to the symbols we have a
// known-good fallback step for, rather than trading unvalidated tickers.
func resolveSymbols() []orchestrator.MarketSymbol {
	want := parseSymbolList(envOr("FRIDAY_SYMBOLS", defaultSymbols))

	// exchangeInfo is a public/unsigned endpoint — creds are optional, so the
	// preflight runs even when trading keys are blank.
	cli := binance.New(binanceBaseURL(), os.Getenv("BINANCE_API_KEY"), os.Getenv("BINANCE_SECRET_KEY"))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	info, err := cli.ExchangeInfo(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"friday: symbol preflight failed (%v) — falling back to validated core pairs\n", err)
		return fallbackSymbols(want)
	}

	listed := make(map[string]binance.SymbolInfo, len(info))
	for _, s := range info {
		listed[s.Symbol] = s
	}

	out := make([]orchestrator.MarketSymbol, 0, len(want))
	for _, sym := range want {
		s, ok := listed[sym]
		if !ok {
			fmt.Fprintf(os.Stderr, "friday: symbol %s not listed on %s — skipping\n", sym, binanceBaseURL())
			continue
		}
		if s.Status != "TRADING" {
			fmt.Fprintf(os.Stderr, "friday: symbol %s status=%s (not TRADING) — skipping\n", sym, s.Status)
			continue
		}
		step := s.StepSize
		if step == "" {
			step = fallbackStepSizes[sym]
		}
		out = append(out, orchestrator.MarketSymbol{Name: sym, StepSize: step})
	}

	names := make([]string, len(out))
	for i, s := range out {
		names[i] = s.Name
	}
	fmt.Fprintf(os.Stderr, "friday: trading %d symbol(s): %s\n", len(out), strings.Join(names, ", "))
	return out
}

// fallbackSymbols is the degraded path when the preflight can't reach the
// venue: keep only the requested symbols we have a known-good step for, so we
// never trade a ticker we couldn't validate.
func fallbackSymbols(want []string) []orchestrator.MarketSymbol {
	out := make([]orchestrator.MarketSymbol, 0, len(want))
	for _, sym := range want {
		step, ok := fallbackStepSizes[sym]
		if !ok {
			fmt.Fprintf(os.Stderr, "friday: symbol %s unvalidated and no fallback — skipping\n", sym)
			continue
		}
		out = append(out, orchestrator.MarketSymbol{Name: sym, StepSize: step})
	}
	return out
}

// parseSymbolList splits a comma-separated list into normalised, de-duplicated,
// upper-cased symbol names, dropping blanks.
func parseSymbolList(raw string) []string {
	seen := make(map[string]bool)
	out := make([]string, 0, 8)
	for _, part := range strings.Split(raw, ",") {
		sym := strings.ToUpper(strings.TrimSpace(part))
		if sym == "" || seen[sym] {
			continue
		}
		seen[sym] = true
		out = append(out, sym)
	}
	return out
}

// binanceBaseURL mirrors the tool package's resolution so the preflight talks
// to the same endpoint the trading tools will.
func binanceBaseURL() string {
	if v := os.Getenv("BINANCE_BASE_URL"); v != "" {
		return v
	}
	return "https://testnet.binancefuture.com"
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
