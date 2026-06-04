package binance

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
)

// MarkPrice is the mark price for a single symbol.
type MarkPrice struct {
	Symbol    string `json:"symbol"`
	MarkPrice string `json:"markPrice"`
}

// Price returns the current mark price for a symbol.
func (c *Client) Price(ctx context.Context, symbol string) (*MarkPrice, error) {
	var mp MarkPrice
	if err := c.get(ctx, "/fapi/v1/premiumIndex", url.Values{"symbol": {symbol}}, &mp); err != nil {
		return nil, fmt.Errorf("price: %w", err)
	}
	return &mp, nil
}

// Ticker24h holds the 24-hour rolling stats for a symbol.
type Ticker24h struct {
	Symbol             string `json:"symbol"`
	PriceChange        string `json:"priceChange"`
	PriceChangePercent string `json:"priceChangePercent"`
	LastPrice          string `json:"lastPrice"`
	HighPrice          string `json:"highPrice"`
	LowPrice           string `json:"lowPrice"`
	Volume             string `json:"volume"`
	QuoteVolume        string `json:"quoteVolume"`
	OpenPrice          string `json:"openPrice"`
}

// Ticker24hr returns the 24h rolling-window statistics for a symbol.
func (c *Client) Ticker24hr(ctx context.Context, symbol string) (*Ticker24h, error) {
	var t Ticker24h
	if err := c.get(ctx, "/fapi/v1/ticker/24hr", url.Values{"symbol": {symbol}}, &t); err != nil {
		return nil, fmt.Errorf("ticker24hr: %w", err)
	}
	return &t, nil
}

// FundingRateEntry is one funding-rate observation from the historical
// endpoint. The most recent entry is the current funding rate.
type FundingRateEntry struct {
	Symbol      string `json:"symbol"`
	FundingRate string `json:"fundingRate"`
	FundingTime int64  `json:"fundingTime"`
}

// FundingRate returns the most recent funding rate entry for a symbol.
// Returns the singular latest observation — callers wanting history can
// extend this to accept a limit.
func (c *Client) FundingRate(ctx context.Context, symbol string) (*FundingRateEntry, error) {
	params := url.Values{
		"symbol": {symbol},
		"limit":  {"1"},
	}
	var entries []FundingRateEntry
	if err := c.get(ctx, "/fapi/v1/fundingRate", params, &entries); err != nil {
		return nil, fmt.Errorf("fundingRate: %w", err)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("fundingRate: no entries for %s", symbol)
	}
	return &entries[0], nil
}

// CommissionRateResponse holds maker and taker commission rates for the
// account on a given symbol. Both rates are decimal strings, e.g.
// "0.0004" = 4 bps = 0.04%.
type CommissionRateResponse struct {
	Symbol              string `json:"symbol"`
	MakerCommissionRate string `json:"makerCommissionRate"`
	TakerCommissionRate string `json:"takerCommissionRate"`
}

// CommissionRate returns the account's maker/taker fee for a symbol.
// Signed endpoint — rates are account-specific (VIP tier, BNB discount,
// etc.) so the API key matters.
func (c *Client) CommissionRate(ctx context.Context, symbol string) (*CommissionRateResponse, error) {
	var out CommissionRateResponse
	if err := c.getSigned(ctx, "/fapi/v1/commissionRate", url.Values{"symbol": {symbol}}, &out); err != nil {
		return nil, fmt.Errorf("commissionRate: %w", err)
	}
	return &out, nil
}

// Kline is a single candlestick.
type Kline struct {
	OpenTime  int64
	Open      float64
	High      float64
	Low       float64
	Close     float64
	Volume    float64
	CloseTime int64
}

// Klines returns recent klines for a symbol.
func (c *Client) Klines(ctx context.Context, symbol, interval string, limit int) ([]Kline, error) {
	return c.KlinesUntil(ctx, symbol, interval, limit, 0)
}

// KlinesUntil is Klines bounded by an end time: the most recent `limit` candles
// at or before endMs (epoch ms). endMs ≤ 0 means "up to now" (same as Klines).
// Used by cmd/backtest to fetch an older, out-of-sample window.
func (c *Client) KlinesUntil(ctx context.Context, symbol, interval string, limit int, endMs int64) ([]Kline, error) {
	params := url.Values{
		"symbol":   {symbol},
		"interval": {interval},
	}
	if limit > 0 {
		params.Set("limit", strconv.Itoa(limit))
	}
	if endMs > 0 {
		params.Set("endTime", strconv.FormatInt(endMs, 10))
	}

	var raw [][]any
	if err := c.get(ctx, "/fapi/v1/klines", params, &raw); err != nil {
		return nil, fmt.Errorf("klines: %w", err)
	}

	klines := make([]Kline, 0, len(raw))
	for _, r := range raw {
		if len(r) < 7 {
			continue
		}
		klines = append(klines, Kline{
			OpenTime:  toInt64(r[0]),
			Open:      toFloat(r[1]),
			High:      toFloat(r[2]),
			Low:       toFloat(r[3]),
			Close:     toFloat(r[4]),
			Volume:    toFloat(r[5]),
			CloseTime: toInt64(r[6]),
		})
	}
	return klines, nil
}

func toFloat(v any) float64 {
	switch val := v.(type) {
	case string:
		f, _ := strconv.ParseFloat(val, 64)
		return f
	case float64:
		return val
	default:
		return 0
	}
}

func toInt64(v any) int64 {
	switch val := v.(type) {
	case float64:
		return int64(val)
	default:
		return 0
	}
}
