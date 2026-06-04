package tool

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// FetchSnapshot returns a one-line supplementary market read for a symbol —
// mark price, 24h change + high/low, and the latest funding rate — the data the
// Analyst used to gather via binance_price / binance_ticker / binance_funding.
// Preloaded into the round prompt so the Analyst spends no tool round-trips on
// it. Best effort: any leg that errors is simply omitted.
func FetchSnapshot(ctx context.Context, symbol string) string {
	cli, err := sharedBinanceClient()
	if err != nil {
		return fmt.Sprintf("%s snapshot: unavailable (%v)", symbol, err)
	}
	parts := make([]string, 0, 3)
	if mp, err := cli.Price(ctx, symbol); err == nil {
		parts = append(parts, "mark="+mp.MarkPrice)
	}
	if tk, err := cli.Ticker24hr(ctx, symbol); err == nil {
		parts = append(parts, fmt.Sprintf("24h %s%% (high %s / low %s)", tk.PriceChangePercent, tk.HighPrice, tk.LowPrice))
	}
	if fr, err := cli.FundingRate(ctx, symbol); err == nil {
		parts = append(parts, "funding="+fr.FundingRate)
	}
	if len(parts) == 0 {
		return fmt.Sprintf("%s snapshot: unavailable", symbol)
	}
	return fmt.Sprintf("%s snapshot: %s", symbol, strings.Join(parts, ", "))
}

// FetchFearGreed returns the (cached) market-wide Fear & Greed line for prompt
// preload, or a short unavailable note. Reuses the cached FearGreedIndexTool.
func FetchFearGreed(ctx context.Context) string {
	var t FearGreedIndexTool
	res, err := t.Execute(ctx, slog.Default(), nil)
	if err != nil || res.IsError {
		return "Fear & Greed: unavailable this round"
	}
	return res.Content
}
