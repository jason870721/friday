package strategy

import (
	"fmt"

	"github.com/johnny1110/friday/internal/binance"
)

// DivergenceSignals is the cross-symbol strategy: when one symbol is moving
// decisively while another (typically BTC, the anchor) is flat, trade the
// mover in its direction. It needs every symbol's candles at once, so it is
// a standalone function rather than a single-symbol Strategy.
//
// data maps symbol → candles. anchor is the reference symbol (e.g.
// "BTCUSDT"). A symbol "moves decisively" when its recent return exceeds
// moveThresholdPct while the anchor's is within flatThresholdPct.
//
// NOTE: not yet wired into the live per-symbol klines flow (that path has
// one symbol at a time). It is unit-tested and ready; live wiring pairs
// naturally with the multi-symbol pass in PRD-008.
func DivergenceSignals(data map[string][]binance.Kline, anchor string, moveThresholdPct, flatThresholdPct float64) []Signal {
	anchorMove, ok := recentReturnPct(data[anchor])
	if !ok {
		return nil
	}
	anchorFlat := anchorMove <= flatThresholdPct && anchorMove >= -flatThresholdPct
	if !anchorFlat {
		return nil // anchor is itself trending — no divergence setup
	}

	var out []Signal
	for symbol, ks := range data {
		if symbol == anchor {
			continue
		}
		move, ok := recentReturnPct(ks)
		if !ok {
			continue
		}
		sig := Signal{Symbol: symbol, Strategy: "divergence", Direction: Neutral}
		switch {
		case move >= moveThresholdPct:
			sig.Direction = Long
			sig.Confidence = 0.6
			sig.Reason = fmt.Sprintf("%s +%.2f%% while %s flat (%.2f%%) — trade the mover", symbol, move, anchor, anchorMove)
		case move <= -moveThresholdPct:
			sig.Direction = Short
			sig.Confidence = 0.6
			sig.Reason = fmt.Sprintf("%s %.2f%% while %s flat (%.2f%%) — trade the mover", symbol, move, anchor, anchorMove)
		default:
			sig.Reason = fmt.Sprintf("%s not diverging (%.2f%%)", symbol, move)
		}
		out = append(out, sig)
	}
	return out
}

// recentReturnPct is the percent change over the last ~10 candles (or the
// whole series if shorter).
func recentReturnPct(ks []binance.Kline) (float64, bool) {
	if len(ks) < 2 {
		return 0, false
	}
	n := 10
	if len(ks) < n+1 {
		n = len(ks) - 1
	}
	start := ks[len(ks)-1-n].Close
	end := ks[len(ks)-1].Close
	if start == 0 {
		return 0, false
	}
	return (end - start) / start * 100, true
}
