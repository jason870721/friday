// Command backtest replays friday's deterministic strategy engine over
// historical candles and simulates position management with the same rules the
// Risk Manager uses (ATR sizing, 1×ATR invalidation filter, ATR-scaled TP/SL) —
// no LLM, no exchange. It answers "what would friday have done?" without risking
// a cent.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/johnny1110/friday/internal/binance"
	"github.com/johnny1110/friday/internal/strategy"
)

func main() {
	symbolsFlag := flag.String("symbols", "", "Comma-separated symbols (default: FRIDAY_SYMBOLS env or BTCUSDT,ETHUSDT,SOLUSDT)")
	intervalFlag := flag.String("interval", "5m", "Candle interval: 5m, 15m, 1h, 4h")
	daysFlag := flag.Int("days", 3, "Days of history to replay")
	balanceFlag := flag.Float64("balance", 5000, "Starting balance in USDT")
	leverageFlag := flag.Int("leverage", 100, "Leverage")
	riskFlag := flag.Float64("risk", 0.01, "Risk per trade as fraction of balance")
	flag.Parse()

	symbols := parseSymbols(*symbolsFlag)
	interval := *intervalFlag
	days := *daysFlag

	ctx := context.Background()

	// Connect to Binance (uses same env vars as friday: BINANCE_API_KEY,
	// BINANCE_SECRET_KEY, BINANCE_BASE_URL).
	apiKey := os.Getenv("BINANCE_API_KEY")
	secret := os.Getenv("BINANCE_SECRET_KEY")
	baseURL := os.Getenv("BINANCE_BASE_URL")
	if apiKey == "" || secret == "" {
		fmt.Fprintln(os.Stderr, "backtest: BINANCE_API_KEY and BINANCE_SECRET_KEY must be set")
		os.Exit(1)
	}
	if baseURL == "" {
		baseURL = "https://fapi.binance.com"
	}
	cli := binance.New(baseURL, apiKey, secret)

	fmt.Printf("=== friday backtest ===\n")
	fmt.Printf("symbols:  %s\n", strings.Join(symbols, ", "))
	fmt.Printf("interval: %s\n", interval)
	fmt.Printf("history:  %d days\n", days)
	fmt.Printf("balance:  $%.0f\n", *balanceFlag)
	fmt.Printf("leverage: %dx\n", *leverageFlag)
	fmt.Printf("risk:     %.0f%% per trade\n", *riskFlag*100)
	fmt.Println()

	// Fetch klines.
	endTime := time.Now()
	startTime := endTime.Add(-time.Duration(days) * 24 * time.Hour)
	limit := days * 24 * 60 / candleMinutes(interval)
	if limit > 1500 {
		limit = 1500 // Binance max
	}

	type symData struct {
		symbol  string
		klines  []binance.Kline
		err     error
	}
	results := make([]symData, len(symbols))
	fmt.Printf("Fetching %s klines (%d candles, %s → %s)...\n", interval, limit,
		startTime.Format("01/02 15:04"), endTime.Format("01/02 15:04"))
	for i, sym := range symbols {
		ks, err := cli.Klines(ctx, sym, interval, limit)
		results[i] = symData{sym, ks, err}
		if err != nil {
			fmt.Printf("  %s: ERROR %v\n", sym, err)
		} else {
			fmt.Printf("  %s: %d candles (%.0f–%.0f)\n", sym, len(ks), ks[0].Close, ks[len(ks)-1].Close)
		}
	}
	fmt.Println()

	// Run the backtest.
	bt := newBacktest(*balanceFlag, *leverageFlag, *riskFlag)
	for i, sd := range results {
		if sd.err != nil {
			continue
		}
		bt.run(ctx, sd.symbol, sd.klines, i)
	}

	// Report.
	bt.report()
}

func parseSymbols(s string) []string {
	if s != "" {
		return strings.Split(s, ",")
	}
	if env := os.Getenv("FRIDAY_SYMBOLS"); env != "" {
		return strings.Split(env, ",")
	}
	return []string{"BTCUSDT", "ETHUSDT", "SOLUSDT"}
}

func candleMinutes(interval string) int {
	switch interval {
	case "1m":
		return 1
	case "5m":
		return 5
	case "15m":
		return 15
	case "1h":
		return 60
	case "4h":
		return 240
	default:
		return 5
	}
}

// --- backtest engine ---

type position struct {
	symbol     string
	direction  strategy.Direction
	qty        float64
	entryPrice float64
	stopPrice  float64
	tpPrice    float64
	atr        float64
	openAt     int // candle index
}

type trade struct {
	symbol    string
	direction strategy.Direction
	entry     float64
	exit      float64
	pnl       float64
	reason    string
}

type backtestEngine struct {
	balance   float64
	leverage  int
	riskPct   float64
	positions map[string]*position
	trades    []trade
	equityCurve []float64
}

func newBacktest(balance float64, leverage int, riskPct float64) *backtestEngine {
	return &backtestEngine{
		balance:    balance,
		leverage:   leverage,
		riskPct:    riskPct,
		positions:  make(map[string]*position),
		equityCurve: []float64{balance},
	}
}

func (bt *backtestEngine) run(ctx context.Context, symbol string, klines []binance.Kline, symbolIdx int) {
	if len(klines) < 50 {
		fmt.Printf("  %s: too few candles (%d), skipping\n", symbol, len(klines))
		return
	}

	trades := 0
	wins := 0
	totalPnL := 0.0

	// We need at least 50 candles for the strategies to work (EMA50).
	// Walk forward, simulating one round per candle.
	for i := 50; i < len(klines); i++ {
		window := klines[:i+1]
		price := window[len(window)-1].Close

		// Check existing position for stop/TP.
		if pos, ok := bt.positions[symbol]; ok {
			closed := false
			var exitPrice float64
			var reason string

			switch pos.direction {
			case strategy.Long:
				if price <= pos.stopPrice {
					exitPrice = pos.stopPrice
					reason = "stop-loss"
					closed = true
				} else if price >= pos.tpPrice {
					exitPrice = pos.tpPrice
					reason = "take-profit"
					closed = true
				}
			case strategy.Short:
				if price >= pos.stopPrice {
					exitPrice = pos.stopPrice
					reason = "stop-loss"
					closed = true
				} else if price <= pos.tpPrice {
					exitPrice = pos.tpPrice
					reason = "take-profit"
					closed = true
				}
			}

			if closed {
				pnl := posPnL(pos, exitPrice)
				bt.balance += pnl
				totalPnL += pnl
				trades++
				if pnl > 0 {
					wins++
				}
				bt.trades = append(bt.trades, trade{
					symbol:    symbol,
					direction: pos.direction,
					entry:     pos.entryPrice,
					exit:      exitPrice,
					pnl:       pnl,
					reason:    reason,
				})
				delete(bt.positions, symbol)
				continue
			}
		}

		// Run strategy engine.
		c := strategy.ConsensusForWithRegime(symbol, window)
		if c.Direction == strategy.Neutral {
			continue
		}

		// Only open if flat.
		if _, ok := bt.positions[symbol]; ok {
			continue
		}

		// Compute ATR.
		atr, ok := binance.ATR(window, 14)
		if !ok {
			continue
		}

		// Fee check: expected move must clear 3× round-trip fee (~0.24% for crypto).
		expectedMove := atr / price * 100
		if expectedMove < 0.24 {
			continue
		}

		// Size by risk: qty = (riskPct * balance) / (2 * atr).
		riskDollars := bt.riskPct * bt.balance
		qty := riskDollars / (2 * atr)

		// Compute stop and TP.
		stopDist := 2 * atr
		tpDist1 := 2 * atr // tier-1 TP

		// Apply 1×ATR invalidation filter.
		inval := c.Invalidation()
		if inval != 0 {
			invalDist := 0.0
			switch c.Direction {
			case strategy.Long:
				invalDist = price - inval
			case strategy.Short:
				invalDist = inval - price
			}
			if invalDist >= atr && invalDist < stopDist {
				stopDist = invalDist
			}
		}

		var stopPrice, tpPrice float64
		switch c.Direction {
		case strategy.Long:
			stopPrice = price - stopDist
			tpPrice = price + tpDist1
		case strategy.Short:
			stopPrice = price + stopDist
			tpPrice = price - tpDist1
		}

		pos := &position{
			symbol:     symbol,
			direction:  c.Direction,
			qty:        qty,
			entryPrice: price,
			stopPrice:  stopPrice,
			tpPrice:    tpPrice,
			atr:        atr,
			openAt:     i,
		}
		bt.positions[symbol] = pos
	}

	// Close any remaining position at the last price.
	if pos, ok := bt.positions[symbol]; ok {
		lastPrice := klines[len(klines)-1].Close
		pnl := posPnL(pos, lastPrice)
		bt.balance += pnl
		totalPnL += pnl
		trades++
		if pnl > 0 {
			wins++
		}
		bt.trades = append(bt.trades, trade{
			symbol:    symbol,
			direction: pos.direction,
			entry:     pos.entryPrice,
			exit:      lastPrice,
			pnl:       pnl,
			reason:    "end-of-data",
		})
		delete(bt.positions, symbol)
	}

	winRate := 0.0
	if trades > 0 {
		winRate = float64(wins) / float64(trades) * 100
	}
	fmt.Printf("  %s: %d trades, %.0f%% win, PnL %+.2f USDT\n", symbol, trades, winRate, totalPnL)
}

func (bt *backtestEngine) report() {
	fmt.Println()
	fmt.Println("=== BACKTEST REPORT ===")
	fmt.Printf("Final balance: $%.2f\n", bt.balance)

	totalPnL := 0.0
	wins := 0
	for _, t := range bt.trades {
		totalPnL += t.pnl
		if t.pnl > 0 {
			wins++
		}
	}
	winRate := 0.0
	if len(bt.trades) > 0 {
		winRate = float64(wins) / float64(len(bt.trades)) * 100
	}

	fmt.Printf("Trades:  %d (%d wins, %.0f%%)\n", len(bt.trades), wins, winRate)
	fmt.Printf("Net PnL: %+.2f USDT\n", totalPnL)
	if len(bt.trades) > 0 {
		avgWin := 0.0
		avgLoss := 0.0
		winsCount := 0
		lossesCount := 0
		for _, t := range bt.trades {
			if t.pnl > 0 {
				avgWin += t.pnl
				winsCount++
			} else {
				avgLoss += t.pnl
				lossesCount++
			}
		}
		if winsCount > 0 {
			avgWin /= float64(winsCount)
		}
		if lossesCount > 0 {
			avgLoss /= float64(lossesCount)
		}
		fmt.Printf("Avg win:  %+.2f  |  Avg loss: %+.2f\n", avgWin, avgLoss)
		if avgLoss != 0 {
			fmt.Printf("Profit factor: %.2f\n", avgWin/(-avgLoss))
		}
	}

	// Per-symbol breakdown.
	fmt.Println()
	fmt.Println("--- By symbol ---")
	symbolTrades := make(map[string][]trade)
	for _, t := range bt.trades {
		symbolTrades[t.symbol] = append(symbolTrades[t.symbol], t)
	}
	// Sort symbols.
	syms := make([]string, 0, len(symbolTrades))
	for s := range symbolTrades {
		syms = append(syms, s)
	}
	sort.Strings(syms)
	for _, sym := range syms {
		ts := symbolTrades[sym]
		symPnL := 0.0
		symWins := 0
		for _, t := range ts {
			symPnL += t.pnl
			if t.pnl > 0 {
				symWins++
			}
		}
		wr := 0.0
		if len(ts) > 0 {
			wr = float64(symWins) / float64(len(ts)) * 100
		}
		fmt.Printf("  %s: %d trades, %d wins (%.0f%%), PnL %+.2f\n", sym, len(ts), symWins, wr, symPnL)
	}

	// Trade log.
	fmt.Println()
	fmt.Println("--- Trade log ---")
	for _, t := range bt.trades {
		dir := "LONG"
		if t.direction == strategy.Short {
			dir = "SHORT"
		}
		fmt.Printf("  %s %s entry=%.4f exit=%.4f pnl=%+.2f %s\n",
			t.symbol, dir, t.entry, t.exit, t.pnl, t.reason)
	}
}

func posPnL(pos *position, exitPrice float64) float64 {
	switch pos.direction {
	case strategy.Long:
		return (exitPrice - pos.entryPrice) * pos.qty
	case strategy.Short:
		return (pos.entryPrice - exitPrice) * pos.qty
	}
	return 0
}
