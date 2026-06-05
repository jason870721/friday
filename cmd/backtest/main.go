// Command backtest replays friday's deterministic strategy engine over
// historical candles and simulates position management with the same rules the
// Risk Manager uses (ATR sizing, 1×ATR invalidation filter, ATR-scaled TP/SL,
// round-trip taker fees) — no LLM, no exchange. It answers "what would friday
// have done?" without risking a cent. It reports win rate, expectancy, payoff,
// profit factor, Sharpe/Sortino, max drawdown and max consecutive losses.
//
// Klines are PUBLIC, so point BINANCE_BASE_URL at mainnet for real data even
// when you trade testnet (the API key only needs to be non-empty):
//
//	# single-timeframe replay (the strategy signal on one interval)
//	BINANCE_BASE_URL=https://fapi.binance.com \
//	  go run ./cmd/backtest -symbols BTCUSDT,ETHUSDT,SOLUSDT -interval 5m -days 5
//
//	# multi-timeframe: walk 5m and combine 5m+1h+4h via AggregateMTF — the live
//	# Analyst's signal path (new 5m-leading weights, RSI filter, quorum, 4h veto).
//	# Bounded to ~5-day windows by the 5m 1500-candle cap; -interval is ignored.
//	... go run ./cmd/backtest -days 5 -mtf
//
//	# out-of-sample / walk-forward: shift the window back N days (no overlap)
//	... go run ./cmd/backtest -interval 1h -days 40 -end-days-ago 40
//
// Flags: -symbols, -interval, -days, -balance, -leverage, -risk, -fee (taker
// rate/side, default 4 bps), -end-days-ago (window shift), -mtf (multi-timeframe).
package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
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
	endAgoFlag := flag.Int("end-days-ago", 0, "End the window this many days before now (0 = now); use with -days for an out-of-sample window, e.g. -days 40 -end-days-ago 40")
	mtfFlag := flag.Bool("mtf", false, "Multi-timeframe mode: walk the 5m entry TF and combine 5m+1h+4h via AggregateMTF (the live signal path); -interval is ignored. Limited to ~5 days (5m 1500-candle cap).")
	tpMultFlag := flag.Float64("tp-mult", 4.0, "Take-profit distance in ATR multiples (default 4.0 = 2:1 reward:risk).")
	slMultFlag := flag.Float64("sl-mult", 2.0, "Stop-loss distance in ATR multiples (default 2.0).")
	regimeGateFlag := flag.Bool("regime-gate", false, "Only open when the regime (4h in MTF mode, entry-TF in single) is TRENDING (ADX>25) — suppress RANGING/TRANSITIONAL chop.")
	tieredFlag := flag.Bool("tiered", false, "Faithful tiered exit (mirrors the live prompt): tier-1 closes -tier1-frac at -tp1×R + moves stop to break-even, tier-2 closes the rest at -tp2×R, with a trailing stop. Overrides the single-TP exit. R = the stop distance.")
	tp1Flag := flag.Float64("tp1", 2.0, "Tiered: tier-1 take-profit in R multiples (R = stop distance).")
	tp2Flag := flag.Float64("tp2", 4.0, "Tiered: tier-2 (final) take-profit in R multiples.")
	tier1FracFlag := flag.Float64("tier1-frac", 0.5, "Tiered: fraction of the position closed at tier-1.")
	breakevenFlag := flag.Bool("breakeven", true, "Tiered: after tier-1, move the stop to break-even on the remainder.")
	trailStartFlag := flag.Float64("trail-start", 2.0, "Tiered: peak favourable excursion (in R) that engages the trailing stop.")
	trailGiveFlag := flag.Float64("trail-give", 1.0, "Tiered: once trailing, close if uPnL gives back to this many R.")
	persistenceFlag := flag.Int("persistence", 2, "Signal-persistence: require the same MTF direction to hold ≥N consecutive 5m bars before opening (mirrors the live prompt's persistence gate). 0 = off.")
	noChopFlag := flag.Bool("no-chop", false, "No-chop gate: skip when price sits on its 5m MA20 (|Δ| < 0.3%) with a neutral 45–55 RSI — no displacement, stop sits inside noise band.")
	trendAlignFlag := flag.Bool("trend-align", false, "Only open in the direction of the 4h trend (4h close vs 4h MA20): suppress longs while 4h is below its MA20 and shorts while above. Tests whether the long-side bleed in a downtrend is cured by higher-TF trend alignment. MTF mode only.")
	erMinFlag := flag.Float64("er-min", 0, "Whipsaw filter: require the 5m Kaufman Efficiency Ratio (|net move| / Σ|bar moves| over -er-lookback bars) ≥ this to open. ~1 = clean trend, ~0 = chop. 0 = off. Suppresses entries in oscillating ranges where trend signals get sawed.")
	erLookbackFlag := flag.Int("er-lookback", 20, "Whipsaw filter: lookback (5m bars) for the Efficiency Ratio.")
	rungsFlag := flag.String("rungs", "", "Laddered scale-out (implies -tiered): comma-separated R:frac rungs, e.g. \"1:0.33,2:0.33\" closes 33%% of the ORIGINAL position at 1R and 2R, leaving 34%% to trail. Overrides the tier-1/tier-2 take-profits. Empty = use tier-1/tier-2.")
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
	if *mtfFlag {
		fmt.Printf("mode:     multi-timeframe (5m entry + 1h + 4h via AggregateMTF; -interval ignored)\n")
	}
	fmt.Println()

	// Fetch klines. Binance returns up to 1500 candles per request.
	// For 5m that's ~5 days, 15m ~15 days, 1h ~62 days, 4h ~250 days.
	// -end-days-ago shifts the window back for out-of-sample tests.
	endTime := time.Now().Add(-time.Duration(*endAgoFlag) * 24 * time.Hour)
	startTime := endTime.Add(-time.Duration(days) * 24 * time.Hour)
	endMs := int64(0)
	if *endAgoFlag > 0 {
		endMs = endTime.UnixMilli()
	}

	// Multi-timeframe mode: fetch 5m (entry, capped to the window) plus generous
	// 1h/4h lookback so every 5m bar has full higher-TF history behind it, then
	// replay through AggregateMTF.
	if *mtfFlag {
		bt := newBacktest(*balanceFlag, *leverageFlag, *riskFlag, *feeFlag, *tpMultFlag, *slMultFlag, *regimeGateFlag)
		bt.setTiered(*tieredFlag, *tp1Flag, *tp2Flag, *tier1FracFlag, *trailStartFlag, *trailGiveFlag, *breakevenFlag)
		bt.persistence = *persistenceFlag
		bt.noChop = *noChopFlag
		bt.trendAlign = *trendAlignFlag
		bt.erMin = *erMinFlag
		bt.erLookback = *erLookbackFlag
		bt.rungs = parseRungs(*rungsFlag)
		if len(bt.rungs) > 0 {
			bt.tiered = true
		}
		lim5 := days * 288
		if lim5 > 1500 {
			lim5 = 1500
		}
		fmt.Printf("Fetching 5m/1h/4h klines (5m≤%d candles, ending %s)...\n", lim5, endTime.Format("01/02 15:04"))
		for _, sym := range symbols {
			k5, e5 := cli.KlinesUntil(ctx, sym, "5m", lim5, endMs)
			k1, e1 := cli.KlinesUntil(ctx, sym, "1h", 1500, endMs)
			k4, e4 := cli.KlinesUntil(ctx, sym, "4h", 1500, endMs)
			if e5 != nil || e1 != nil || e4 != nil {
				fmt.Printf("  %s: fetch ERROR (5m:%v 1h:%v 4h:%v)\n", sym, e5, e1, e4)
				continue
			}
			fmt.Printf("  %s: 5m=%d 1h=%d 4h=%d candles\n", sym, len(k5), len(k1), len(k4))
			bt.runMTF(sym, k5, k1, k4)
		}
		bt.report()
		return
	}

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
		ks, err := cli.KlinesUntil(ctx, sym, interval, limit, endMs)
		results[i] = symData{sym, ks, err}
		if err != nil {
			fmt.Printf("  %s: ERROR %v\n", sym, err)
		} else {
			fmt.Printf("  %s: %d candles (%.0f–%.0f)\n", sym, len(ks), ks[0].Close, ks[len(ks)-1].Close)
		}
	}
	fmt.Println()

	// Run the backtest.
	bt := newBacktest(*balanceFlag, *leverageFlag, *riskFlag, *feeFlag, *tpMultFlag, *slMultFlag, *regimeGateFlag)
	bt.setTiered(*tieredFlag, *tp1Flag, *tp2Flag, *tier1FracFlag, *trailStartFlag, *trailGiveFlag, *breakevenFlag)
	bt.persistence = *persistenceFlag
	bt.noChop = *noChopFlag
	bt.trendAlign = *trendAlignFlag
	bt.erMin = *erMinFlag
	bt.erLookback = *erLookbackFlag
	bt.rungs = parseRungs(*rungsFlag)
	if len(bt.rungs) > 0 {
		bt.tiered = true
	}
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
	// Tiered-exit state (used when the engine runs in tiered mode).
	rDist     float64 // 1R in price = the stop distance at entry
	tier1Px   float64 // tier-1 take-profit price
	tier2Px   float64 // tier-2 (final) take-profit price
	tier1Done bool    // tier-1 partial already taken
	peakFav   float64 // best favourable excursion (price distance) seen, for the trail
	origQty   float64 // qty at entry (rungs close fractions of THIS, not the remainder)
	rungsDone int     // how many ladder rungs have been taken (they fire in order)
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
	tpMult       float64 // take-profit distance in ATR multiples (single-TP mode)
	slMult       float64 // stop-loss distance in ATR multiples
	regimeGate   bool    // only open in a TRENDING regime
	persistence  int     // signal-persistence threshold (0 = off)
	noChop       bool    // no-chop gate
	trendAlign   bool    // only open in the direction of the 4h trend (price vs 4h MA20)
	erMin        float64 // whipsaw filter: min 5m Efficiency Ratio to open (0 = off)
	erLookback   int     // Efficiency Ratio lookback in 5m bars
	// Tiered exit (mirrors the live prompt). R = the stop distance.
	tiered      bool
	tp1         float64 // tier-1 TP in R
	tp2         float64 // tier-2 (final) TP in R
	tier1Frac   float64 // fraction closed at tier-1
	breakeven   bool    // move stop to break-even after tier-1
	trailStart  float64 // peak excursion (R) that engages the trail
	trailGive   float64 // once trailing, exit if uPnL gives back to this many R
	rungs       []rung  // laddered scale-out (overrides tier-1/tier-2 when set)
	positions   map[string]*position
	trades      []trade
	equityCurve []float64
}

// rung is one step of a laddered scale-out: when favourable excursion reaches
// r×R, close frac of the ORIGINAL position. Rungs fire in ascending r order; the
// fraction left after the last rung is handed to the trailing stop.
type rung struct {
	r    float64
	frac float64
}

// setTiered installs the faithful tiered-exit plan (mirrors the live prompt).
func (bt *backtestEngine) setTiered(on bool, tp1, tp2, frac, trailStart, trailGive float64, breakeven bool) {
	bt.tiered, bt.tp1, bt.tp2, bt.tier1Frac = on, tp1, tp2, frac
	bt.trailStart, bt.trailGive, bt.breakeven = trailStart, trailGive, breakeven
}

func newBacktest(balance float64, leverage int, riskPct, feeRate, tpMult, slMult float64, regimeGate bool) *backtestEngine {
	return &backtestEngine{
		balance:      balance,
		startBalance: balance,
		leverage:     leverage,
		riskPct:      riskPct,
		feeRate:      feeRate,
		tpMult:       tpMult,
		slMult:       slMult,
		regimeGate:   regimeGate,
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
	pnl := bt.book(pos, exitPrice, pos.qty, reason)
	delete(bt.positions, pos.symbol)
	return pnl
}

// book records a close of `qty` of the position at exitPrice, net of the
// round-trip taker fee on that qty, and appends it as a trade. It does NOT
// remove the position (the caller decides) — so it serves both full and partial
// (tiered) closes.
func (bt *backtestEngine) book(pos *position, exitPrice, qty float64, reason string) float64 {
	var gross float64
	switch pos.direction {
	case strategy.Long:
		gross = (exitPrice - pos.entryPrice) * qty
	case strategy.Short:
		gross = (pos.entryPrice - exitPrice) * qty
	}
	fee := bt.feeRate * qty * (pos.entryPrice + exitPrice) // taker on both legs
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
	return pnl
}

// manage runs the faithful tiered exit for one bar (mirrors the live prompt):
// hard stop → tier-2 (full) → tier-1 (partial + move stop to break-even) →
// trailing (full, once peak ≥ trailStart R, exit on give-back to trailGive R).
// Returns the PnL booked this bar, how many closes were booked, how many were
// wins, and whether the position is now fully closed. Checked at the bar close
// (the backtest's price granularity).
func (bt *backtestEngine) manage(pos *position, price float64) (pnl float64, closes, wins int, done bool) {
	book := func(exitPrice, qty float64, reason string, remove bool) {
		p := bt.book(pos, exitPrice, qty, reason)
		pnl += p
		closes++
		if p > 0 {
			wins++
		}
		if remove {
			delete(bt.positions, pos.symbol)
			done = true
		}
	}
	// Favourable excursion (price distance in our favour) + peak for the trail.
	fav := price - pos.entryPrice
	if pos.direction == strategy.Short {
		fav = pos.entryPrice - price
	}
	if fav > pos.peakFav {
		pos.peakFav = fav
	}
	R := pos.rDist

	// 1. Hard stop (adverse) — current stopPrice (moves to break-even after tier-1).
	if pos.direction == strategy.Long && price <= pos.stopPrice {
		reason := "stop-loss"
		if pos.tier1Done && bt.breakeven {
			reason = "break-even"
		}
		book(pos.stopPrice, pos.qty, reason, true)
		return
	}
	if pos.direction == strategy.Short && price >= pos.stopPrice {
		reason := "stop-loss"
		if pos.tier1Done && bt.breakeven {
			reason = "break-even"
		}
		book(pos.stopPrice, pos.qty, reason, true)
		return
	}
	// 2/3. Take-profit ladder.
	if len(bt.rungs) > 0 {
		// Laddered scale-out: fire the next unfilled rung when favourable
		// excursion reaches its R level, closing a fraction of the ORIGINAL qty.
		// The first rung also moves the stop to break-even. Whatever is left after
		// the last rung is handed to the trailing block below.
		for pos.rungsDone < len(bt.rungs) {
			rg := bt.rungs[pos.rungsDone]
			if fav < rg.r*R {
				break
			}
			closeQty := rg.frac * pos.origQty
			if closeQty > pos.qty {
				closeQty = pos.qty
			}
			var px float64
			if pos.direction == strategy.Long {
				px = pos.entryPrice + rg.r*R
			} else {
				px = pos.entryPrice - rg.r*R
			}
			last := pos.rungsDone == len(bt.rungs)-1 && pos.qty-closeQty <= 1e-9
			book(px, closeQty, fmt.Sprintf("rung-%gR", rg.r), last)
			pos.qty -= closeQty
			if pos.rungsDone == 0 && bt.breakeven {
				pos.stopPrice = pos.entryPrice
			}
			pos.tier1Done = true
			pos.rungsDone++
			if done {
				return
			}
		}
	} else {
		// Tier-2 (final) — close the whole remainder.
		if fav >= bt.tp2*R {
			book(pos.tier2Px, pos.qty, "take-profit-t2", true)
			return
		}
		// Tier-1 partial — close tier1Frac, then move the stop to break-even.
		if !pos.tier1Done && fav >= bt.tp1*R {
			book(pos.tier1Px, pos.qty*bt.tier1Frac, "take-profit-t1", false)
			pos.qty -= pos.qty * bt.tier1Frac
			pos.tier1Done = true
			if bt.breakeven {
				pos.stopPrice = pos.entryPrice
			}
		}
	}
	// 4. Trailing — once the peak reached trailStart R, exit the remainder if the
	// move gives back to trailGive R of favourable excursion.
	if pos.peakFav >= bt.trailStart*R && fav <= bt.trailGive*R {
		var exitPrice float64
		if pos.direction == strategy.Long {
			exitPrice = pos.entryPrice + bt.trailGive*R
		} else {
			exitPrice = pos.entryPrice - bt.trailGive*R
		}
		book(exitPrice, pos.qty, "trail", true)
		return
	}
	return
}

// exitDecision reports whether an open position should close at the current
// price, returning the fill price and reason (stop-loss / take-profit).
func exitDecision(pos *position, price float64) (exitPrice float64, reason string, hit bool) {
	switch pos.direction {
	case strategy.Long:
		if price <= pos.stopPrice {
			return pos.stopPrice, "stop-loss", true
		}
		if price >= pos.tpPrice {
			return pos.tpPrice, "take-profit", true
		}
	case strategy.Short:
		if price >= pos.stopPrice {
			return pos.stopPrice, "stop-loss", true
		}
		if price <= pos.tpPrice {
			return pos.tpPrice, "take-profit", true
		}
	}
	return 0, "", false
}

// openFromConsensus sizes and opens a position from a directional consensus,
// using the entry-timeframe window for price/ATR. It applies the ATR fee-floor
// (expected move ≥ 0.24%), risk-based sizing (risk% ÷ 2×ATR), and a stop that is
// the tighter of 2×ATR or the consensus invalidation (when ≥1×ATR away). No-op
// when NEUTRAL or already in a position. Returns true if it opened.
// efficiencyRatio is Kaufman's Efficiency Ratio over the last n closes:
// |close[last] − close[last-n]| / Σ|close[i] − close[i-1]|. 1.0 = a perfectly
// straight move, →0 = pure chop (lots of motion, no net progress). ok=false when
// there aren't n+1 closes, or the path length is zero.
func efficiencyRatio(window []binance.Kline, n int) (float64, bool) {
	if n < 1 || len(window) < n+1 {
		return 0, false
	}
	seg := window[len(window)-n-1:]
	net := math.Abs(seg[len(seg)-1].Close - seg[0].Close)
	var path float64
	for i := 1; i < len(seg); i++ {
		path += math.Abs(seg[i].Close - seg[i-1].Close)
	}
	if path == 0 {
		return 0, false
	}
	return net / path, true
}

// trend4h returns the 4h trend direction (last 4h close vs its 20-bar MA): Long
// above, Short below, Neutral when there aren't enough 4h bars to judge.
func trend4h(k4 []binance.Kline) strategy.Direction {
	if len(k4) < 20 {
		return strategy.Neutral
	}
	var sum float64
	for _, k := range k4[len(k4)-20:] {
		sum += k.Close
	}
	ma20 := sum / 20
	switch last := k4[len(k4)-1].Close; {
	case last > ma20:
		return strategy.Long
	case last < ma20:
		return strategy.Short
	default:
		return strategy.Neutral
	}
}

// parseRungs parses "1:0.33,2:0.5" into ascending-R scale-out rungs (R-level :
// fraction-of-original). Malformed or empty → nil (the caller keeps tier-1/2).
func parseRungs(s string) []rung {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var out []rung
	for _, part := range strings.Split(s, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), ":", 2)
		if len(kv) != 2 {
			continue
		}
		r, e1 := strconv.ParseFloat(strings.TrimSpace(kv[0]), 64)
		f, e2 := strconv.ParseFloat(strings.TrimSpace(kv[1]), 64)
		if e1 != nil || e2 != nil || r <= 0 || f <= 0 {
			continue
		}
		out = append(out, rung{r: r, frac: f})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].r < out[j].r })
	return out
}

// trackStreak updates a same-direction run length (mirrors the live
// signal-persistence counter): a non-NEUTRAL direction matching the previous bar
// increments it, a different direction resets to 1, NEUTRAL resets to 0.
func trackStreak(prev strategy.Direction, streak int, cur strategy.Direction) (strategy.Direction, int) {
	switch {
	case cur == strategy.Neutral:
		return strategy.Neutral, 0
	case cur == prev:
		return cur, streak + 1
	default:
		return cur, 1
	}
}

// onMABand reports whether the last close sits on its 5m MA20 (|Δ| < 0.3%) with a
// neutral 45–55 RSI — the live no-chop condition. ok=false when there aren't
// enough candles to judge (caller then doesn't suppress).
func onMABand(window []binance.Kline) (chop, ok bool) {
	if len(window) < 20 {
		return false, false
	}
	closes := make([]float64, len(window))
	for i, k := range window {
		closes[i] = k.Close
	}
	var sum float64
	for _, c := range closes[len(closes)-20:] {
		sum += c
	}
	ma20 := sum / 20
	price := closes[len(closes)-1]
	rsi, rok := binance.RSI(closes, 14)
	if !rok {
		return false, false
	}
	onMA := math.Abs(price-ma20)/price*100 < 0.3
	neutralRSI := rsi >= 45 && rsi <= 55
	return onMA && neutralRSI, true
}

func (bt *backtestEngine) openFromConsensus(symbol string, window []binance.Kline, c strategy.Consensus, openAt int, regime strategy.Regime, confirmed bool, trendDir strategy.Direction) bool {
	if c.Direction == strategy.Neutral {
		return false
	}
	// Trend-alignment gate (-trend-align): never open against the 4h trend
	// (4h close vs 4h MA20). Catches the downtrend longs the 4h-consensus veto
	// misses when the 4h consensus is itself NEUTRAL.
	if bt.trendAlign && trendDir != strategy.Neutral && c.Direction != trendDir {
		return false
	}
	// Regime gate (PRD-016 idea, measured here): suppress entries outside a
	// committed trend — momentum/breakout in chop is where the losses cluster.
	if bt.regimeGate && regime != strategy.RegimeTrending {
		return false
	}
	if bt.persistence > 0 && !confirmed {
		return false
	}
	if bt.noChop {
		if chop, ok := onMABand(window); ok && chop {
			return false
		}
	}
	// Whipsaw filter (-er-min): the Kaufman Efficiency Ratio over the last
	// er-lookback 5m bars must clear the threshold. ER ≈ 1 in a clean directional
	// move, ≈ 0 in an oscillating range — this is what the flat-on-MA no-chop gate
	// and the slow 4h ADX regime read both miss.
	if bt.erMin > 0 {
		if er, ok := efficiencyRatio(window, bt.erLookback); ok && er < bt.erMin {
			return false
		}
	}
	if _, ok := bt.positions[symbol]; ok {
		return false
	}
	price := window[len(window)-1].Close
	atr, ok := binance.ATR(window, 14)
	if !ok || atr/price*100 < 0.24 { // fee floor: expected move must clear ~3× round-trip fee
		return false
	}
	qty := (bt.riskPct * bt.balance) / (bt.slMult * atr) // size by the actual stop so risk/trade stays constant across sweeps

	stopDist := bt.slMult * atr
	tpDist := bt.tpMult * atr
	if inval := c.Invalidation(); inval != 0 {
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
		stopPrice, tpPrice = price-stopDist, price+tpDist
	case strategy.Short:
		stopPrice, tpPrice = price+stopDist, price-tpDist
	}
	pos := &position{
		symbol: symbol, direction: c.Direction, qty: qty, origQty: qty,
		entryPrice: price, stopPrice: stopPrice, tpPrice: tpPrice, atr: atr, openAt: openAt,
	}
	// Tiered exit: R = the actual stop distance; tier-1/tier-2 sit at tp1×R / tp2×R
	// from entry. The single-TP tpPrice is overridden by tier-2 (the final target).
	if bt.tiered {
		pos.rDist = stopDist
		switch c.Direction {
		case strategy.Long:
			pos.tier1Px, pos.tier2Px = price+bt.tp1*stopDist, price+bt.tp2*stopDist
		case strategy.Short:
			pos.tier1Px, pos.tier2Px = price-bt.tp1*stopDist, price-bt.tp2*stopDist
		}
		pos.tpPrice = pos.tier2Px
	}
	bt.positions[symbol] = pos
	return true
}

func (bt *backtestEngine) run(ctx context.Context, symbol string, klines []binance.Kline, symbolIdx int) {
	if len(klines) < 50 {
		fmt.Printf("  %s: too few candles (%d), skipping\n", symbol, len(klines))
		return
	}

	trades, wins := 0, 0
	totalPnL := 0.0
	var prevDir strategy.Direction
	streak := 0

	// We need at least 50 candles for the strategies to work (EMA50).
	// Walk forward, simulating one round per candle.
	for i := 50; i < len(klines); i++ {
		window := klines[:i+1]
		price := window[len(window)-1].Close

		if pos, ok := bt.positions[symbol]; ok {
			if bt.tiered {
				p, n, w, fin := bt.manage(pos, price)
				totalPnL += p
				trades += n
				wins += w
				if fin {
					continue
				}
				// still open (partial taken or nothing triggered): fall through;
				// openFromConsensus no-ops while a position exists.
			} else if exitPrice, reason, hit := exitDecision(pos, price); hit {
				pnl := bt.closePosition(pos, exitPrice, reason)
				totalPnL += pnl
				trades++
				if pnl > 0 {
					wins++
				}
				continue
			}
		}

		c := strategy.ConsensusForWithRegime(symbol, window)
		prevDir, streak = trackStreak(prevDir, streak, c.Direction)
		// Single-TF mode has no 4h series → trend-align is a no-op (Neutral).
		bt.openFromConsensus(symbol, window, c, i, strategy.DetectRegime(window), streak >= bt.persistence, strategy.Neutral)
	}

	totalPnL += bt.closeRemaining(symbol, klines[len(klines)-1].Close, &trades, &wins)
	reportSymbol(symbol, trades, wins, totalPnL)
}

// runMTF is the multi-timeframe variant (-mtf): it walks the 5m entry timeframe
// and, at each bar, runs the full calibrated+regime strategy engine on the 5m,
// 1h, and 4h windows aligned to that moment, combines them via AggregateMTF
// (the same path the live Analyst reads), and opens on the combined signal.
// Higher-TF consensuses are recomputed only when a new higher-TF bar closes.
func (bt *backtestEngine) runMTF(symbol string, k5, k1, k4 []binance.Kline) {
	if len(k5) < 50 {
		fmt.Printf("  %s: too few 5m candles (%d), skipping\n", symbol, len(k5))
		return
	}
	trades, wins := 0, 0
	totalPnL := 0.0
	lastJ1, lastJ4 := -1, -1
	var c1, c4 strategy.Consensus
	var prevDir strategy.Direction
	streak := 0

	for i := 50; i < len(k5); i++ {
		w5 := k5[:i+1]
		price := w5[len(w5)-1].Close
		t := w5[len(w5)-1].CloseTime

		if pos, ok := bt.positions[symbol]; ok {
			if bt.tiered {
				p, n, w, fin := bt.manage(pos, price)
				totalPnL += p
				trades += n
				wins += w
				if fin {
					continue
				}
				// still open (partial taken or nothing triggered): fall through;
				// openFromConsensus no-ops while a position exists.
			} else if exitPrice, reason, hit := exitDecision(pos, price); hit {
				pnl := bt.closePosition(pos, exitPrice, reason)
				totalPnL += pnl
				trades++
				if pnl > 0 {
					wins++
				}
				continue
			}
		}

		// Align the higher timeframes to "now" (bars closed at or before t);
		// recompute their consensus only when a new higher-TF bar has closed.
		j1, j4 := countBarsUpTo(k1, t), countBarsUpTo(k4, t)
		if j1 != lastJ1 {
			c1 = strategy.Consensus{}
			if j1 > 0 {
				c1 = strategy.ConsensusForWithRegime(symbol, k1[:j1])
			}
			lastJ1 = j1
		}
		if j4 != lastJ4 {
			c4 = strategy.Consensus{}
			if j4 > 0 {
				c4 = strategy.ConsensusForWithRegime(symbol, k4[:j4])
			}
			lastJ4 = j4
		}

		byTF := map[string]strategy.Consensus{"5m": strategy.ConsensusForWithRegime(symbol, w5)}
		if j1 > 0 {
			byTF["1h"] = c1
		}
		if j4 > 0 {
			byTF["4h"] = c4
		}
		// Regime gate uses the 4h read (matching the live Analyst's 4h ADX regime);
		// fall back to the 5m window when no 4h bar has closed yet.
		regime := strategy.DetectRegime(w5)
		if j4 > 0 {
			regime = strategy.DetectRegime(k4[:j4])
		}
		cons := strategy.AggregateMTF(byTF)
		prevDir, streak = trackStreak(prevDir, streak, cons.Direction)
		trendDir := strategy.Neutral
		if j4 > 0 {
			trendDir = trend4h(k4[:j4])
		}
		bt.openFromConsensus(symbol, w5, cons, i, regime, streak >= bt.persistence, trendDir)
	}

	totalPnL += bt.closeRemaining(symbol, k5[len(k5)-1].Close, &trades, &wins)
	reportSymbol(symbol, trades, wins, totalPnL)
}

// closeRemaining flattens a still-open position at the final price (end-of-data)
// and folds it into the running trade/win counters; returns its PnL.
func (bt *backtestEngine) closeRemaining(symbol string, lastPrice float64, trades, wins *int) float64 {
	pos, ok := bt.positions[symbol]
	if !ok {
		return 0
	}
	pnl := bt.closePosition(pos, lastPrice, "end-of-data")
	*trades++
	if pnl > 0 {
		*wins++
	}
	return pnl
}

func reportSymbol(symbol string, trades, wins int, totalPnL float64) {
	winRate := 0.0
	if trades > 0 {
		winRate = float64(wins) / float64(trades) * 100
	}
	fmt.Printf("  %s: %d trades, %.0f%% win, PnL %+.2f USDT\n", symbol, trades, winRate, totalPnL)
}

// countBarsUpTo returns how many bars (sorted ascending by CloseTime) have
// closed at or before t — i.e. the bars visible at moment t.
func countBarsUpTo(bars []binance.Kline, t int64) int {
	lo, hi := 0, len(bars)
	for lo < hi {
		mid := (lo + hi) / 2
		if bars[mid].CloseTime <= t {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
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
