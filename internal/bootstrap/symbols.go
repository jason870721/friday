package bootstrap

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/johnny1110/friday/internal/backtest"
	"github.com/johnny1110/friday/internal/binance"
	"github.com/johnny1110/friday/internal/orchestrator"
	"github.com/johnny1110/friday/internal/strategy"
	fridaytool "github.com/johnny1110/friday/internal/tool"
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
	hasTradFi := false
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
		if s.IsTradFiPerp() {
			hasTradFi = true
		}
		out = append(out, orchestrator.MarketSymbol{Name: sym, StepSize: step})
	}

	// PRD-012/019: per-symbol leverage brackets (signed leverageBracket), fetched
	// once. We derive each symbol's max leverage (shown as "≤Nx" + fed to the
	// binance_leverage clamp so an over-cap request can't fail with -4028) AND
	// the full notional→leverage tier table (fed to binance_order so a position's
	// notional can't exceed the tier its leverage allows — avoids -2027). We also
	// attach each symbol's max-leverage notional ceiling to the prompt.
	if os.Getenv("BINANCE_API_KEY") != "" && os.Getenv("BINANCE_SECRET_KEY") != "" {
		if brackets, lerr := cli.LeverageBrackets(ctx); lerr != nil {
			fmt.Fprintf(os.Stderr, "friday: leverage preflight failed (%v) — per-symbol caps unknown this session\n", lerr)
		} else {
			maxLev := make(map[string]int, len(brackets))
			for sym, bs := range brackets {
				mx, mxCap := 0, 0.0
				for _, b := range bs {
					if b.InitialLeverage > mx {
						mx, mxCap = b.InitialLeverage, b.NotionalCap
					}
				}
				if mx > 0 {
					maxLev[sym] = mx
				}
				for i := range out {
					if out[i].Name == sym {
						out[i].MaxLeverage = mx
						out[i].MaxNotional = mxCap
					}
				}
			}
			fridaytool.SetMaxLeverages(maxLev)
			fridaytool.SetLeverageBrackets(brackets)
		}
	}

	// Stock-linked (TradFi) perps need a one-time, account-level agreement
	// before Binance accepts orders on them (otherwise binance_order fails with
	// code -4411). Sign it once at startup — idempotent — when any resolved
	// symbol is a TradFi perp and credentials are present. Market data reads
	// fine without it, so a sign failure is a loud warning, not a reason to
	// drop the symbols.
	if hasTradFi {
		signTradFiAgreement(ctx, cli)
	}

	names := make([]string, len(out))
	for i, s := range out {
		names[i] = s.Name
	}
	fmt.Fprintf(os.Stderr, "friday: trading %d symbol(s): %s\n", len(out), strings.Join(names, ", "))
	return out
}

// calibrateStrategies runs the PRD-015 startup backtest sweep: fetch recent 4h
// candles for each symbol, replay every strategy, and install the win-rate →
// confidence map so the strategies vote with their real per-symbol track record
// for the session. Best-effort — any failure (no candles, network) logs a note
// and leaves the hardcoded confidences in place; startup always proceeds.
//
// klines is a public endpoint, so calibration runs even without trading creds.
func calibrateStrategies(symbols []orchestrator.MarketSymbol) {
	cli := binance.New(binanceBaseURL(), os.Getenv("BINANCE_API_KEY"), os.Getenv("BINANCE_SECRET_KEY"))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	candles := make(map[string][]binance.Kline, len(symbols))
	for _, s := range symbols {
		ks, err := cli.Klines(ctx, s.Name, "4h", 200)
		if err != nil {
			fmt.Fprintf(os.Stderr, "friday: calibration klines fetch failed for %s (%v) — skipping it\n", s.Name, err)
			continue
		}
		candles[s.Name] = ks
	}
	if len(candles) == 0 {
		fmt.Fprintln(os.Stderr, "friday: strategy calibration skipped (no candles) — using hardcoded confidences")
		return
	}

	cal := backtest.Calibrate(strategy.DefaultStrategies(), candles)
	strategy.SetDefaultCalibration(cal)

	calibrated := 0
	for _, m := range cal {
		calibrated += len(m)
	}
	fmt.Fprintf(os.Stderr,
		"friday: calibrated %d strategy×symbol confidence(s) from 4h backtests (the rest fall back to defaults)\n", calibrated)
}

// signTradFiAgreement signs the TradFi-Perps agreement so stock-perp orders are
// accepted this session. Requires signed-endpoint credentials; skipped (with a
// note) when they are absent.
func signTradFiAgreement(ctx context.Context, cli *binance.Client) {
	if os.Getenv("BINANCE_API_KEY") == "" || os.Getenv("BINANCE_SECRET_KEY") == "" {
		fmt.Fprintln(os.Stderr,
			"friday: TradFi perps configured but BINANCE_API_KEY/SECRET unset — cannot sign agreement; stock-perp orders will be rejected")
		return
	}
	if err := cli.SignTradFiPerpsAgreement(ctx); err != nil {
		fmt.Fprintf(os.Stderr,
			"friday: TradFi-Perps agreement sign failed (%v) — stock-perp orders may be rejected with code -4411 until signed\n", err)
		return
	}
	fmt.Fprintln(os.Stderr, "friday: TradFi-Perps agreement signed — stock perps tradable")
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
