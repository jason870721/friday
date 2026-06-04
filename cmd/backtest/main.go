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
	"math"
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
	feeFlag := flag.Float64("fee", 0.0004, "Taker fee rate per side (round-trip = 2×); 0.0004 = 4 bps")
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
	fmt.Printf("fee:      %.2f bps/side (%.2f bps round-trip)\n", *feeFlag*1e4, *feeFlag*2*1e4)
	fmt.Println()

	// Fetch klines. Binance returns up to 1500 candles per request.
	// For 5m that's ~5 days, 15m ~15 days, 1h ~62 days, 4h ~250 days.
	endTime := time.Now()
	startTime := endTime.Add(-time.Duration(days) * 24 * time.Hour)
	limit := days * 24 * 60 / candleMinutes(interval)
	if limit > 1500 {
		limit = 1500
	}
	maxDays := 1500 * candleMinutes(interval) / 60 / 24
	if days > maxDays {
		fmt.Printf("Note: %s interval maxes out at ~%d days with 1500 candles. Fetching %d candles.\n",
			interval, maxDays, limit)
	}

	type symData struct {
		symbol string
		klines []binance.Kline
		err    error
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
	bt := newBacktest(*balanceFlag, *leverageFlag, *riskFlag, *feeFlag)
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
	balance      float64
	startBalance float64
	leverage     int
	riskPct      float64
	feeRate      float64 // taker rate per side; round-trip = 2×
	positions    map[string]*position
	trades       []trade
	equityCurve  []float64
}

func newBacktest(balance float64, leverage int, riskPct, feeRate float64) *backtestEngine {
	return &backtestEngine{
		balance:      balance,
		startBalance: balance,
		leverage:     leverage,
		riskPct:      riskPct,
		feeRate:      feeRate,
		positions:    make(map[string]*position),
		equityCurve:  []float64{balance},
	}
}

// closePosition books a position at exitPrice, deducting the round-trip taker
// fee (entry side + exit side) so the simulated PnL is NET — matching how
// friday's exchange-reconciled accounting works. Without this the backtest is
// systematically optimistic (commissions were ~45% of live losses). Returns the
// net PnL and records the trade.
func (bt *backtestEngine) closePosition(pos *position, exitPrice float64, reason string) float64 {
	gross := posPnL(pos, exitPrice)
	fee := bt.feeRate * pos.qty * (pos.entryPrice + exitPrice) // taker on both legs
	pnl := gross - fee
	bt.balance += pnl
	bt.equityCurve = append(bt.equityCurve, bt.balance)
	bt.trades = append(bt.trades, trade{
		symbol:    pos.symbol,
		direction: pos.direction,
		entry:     pos.entryPrice,
		exit:      exitPrice,
		pnl:       pnl,
		reason:    reason,
	})
	delete(bt.positions, pos.symbol)
	return pnl
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
				pnl := bt.closePosition(pos, exitPrice, reason)
				totalPnL += pnl
				trades++
				if pnl > 0 {
					wins++
				}
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
		pnl := bt.closePosition(pos, lastPrice, "end-of-data")
		totalPnL += pnl
		trades++
		if pnl > 0 {
			wins++
		}
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

	totalReturn := 0.0
	if bt.startBalance > 0 {
		totalReturn = (bt.balance/bt.startBalance - 1) * 100
	}
	fmt.Printf("Trades:  %d (%d wins, %.0f%%)\n", len(bt.trades), wins, winRate)
	fmt.Printf("Net PnL: %+.2f USDT (%+.2f%% return, fees deducted)\n", totalPnL, totalReturn)
	if len(bt.trades) > 0 {
		var grossWin, grossLoss float64
		winsCount := 0
		lossesCount := 0
		maxConsecLoss, consec := 0, 0
		var returns []float64
		for _, t := range bt.trades {
			if t.pnl > 0 {
				grossWin += t.pnl
				winsCount++
				consec = 0
			} else if t.pnl < 0 {
				grossLoss += t.pnl
				lossesCount++
				consec++
				if consec > maxConsecLoss {
					maxConsecLoss = consec
				}
			}
			returns = append(returns, t.pnl/bt.startBalance) // return per trade vs starting equity
		}
		avgWin, avgLoss := 0.0, 0.0
		if winsCount > 0 {
			avgWin = grossWin / float64(winsCount)
		}
		if lossesCount > 0 {
			avgLoss = grossLoss / float64(lossesCount)
		}
		expectancy := totalPnL / float64(len(bt.trades))

		fmt.Printf("Expectancy/trade: %+.2f USDT\n", expectancy)
		fmt.Printf("Avg win:  %+.2f  |  Avg loss: %+.2f\n", avgWin, avgLoss)
		if avgLoss != 0 {
			fmt.Printf("Payoff (avgWin/avgLoss): %.2f\n", avgWin/(-avgLoss))
		}
		if grossLoss != 0 {
			fmt.Printf("Profit factor: %.2f\n", grossWin/(-grossLoss))
		}
		fmt.Printf("Max consecutive losses: %d\n", maxConsecLoss)

		// Sharpe & Sortino: mean(return) over total / downside dispersion.
		// Per-trade, non-annualized — a relative consistency read.
		if len(returns) > 1 {
			meanRet := mean(returns)
			if stdRet := stddev(returns, meanRet); stdRet > 0 {
				fmt.Printf("Sharpe (per-trade):  %.2f\n", meanRet/stdRet)
			}
			if dd := downsideDev(returns); dd > 0 {
				fmt.Printf("Sortino (per-trade): %.2f\n", meanRet/dd)
			}
		}

		// Max drawdown from equity curve.
		peak := bt.startBalance
		maxDD := 0.0
		for _, v := range bt.equityCurve {
			if v > peak {
				peak = v
			}
			if peak > 0 {
				if dd := (peak - v) / peak * 100; dd > maxDD {
					maxDD = dd
				}
			}
		}
		fmt.Printf("Max drawdown: %.1f%%\n", maxDD)
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

func mean(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	var sum float64
	for _, v := range vals {
		sum += v
	}
	return sum / float64(len(vals))
}

func stddev(vals []float64, mean float64) float64 {
	if len(vals) < 2 {
		return 0
	}
	var sum float64
	for _, v := range vals {
		d := v - mean
		sum += d * d
	}
	return math.Sqrt(sum / float64(len(vals)-1))
}

// downsideDev is the root-mean-square of the NEGATIVE returns (downside
// deviation against a 0 target) — the denominator of the Sortino ratio, which
// unlike Sharpe doesn't penalise upside volatility. Averaged over all trades so
// a strategy that rarely loses scores well.
func downsideDev(vals []float64) float64 {
	if len(vals) < 2 {
		return 0
	}
	var sum float64
	for _, v := range vals {
		if v < 0 {
			sum += v * v
		}
	}
	return math.Sqrt(sum / float64(len(vals)-1))
}
