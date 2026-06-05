package tool

import "testing"

// makerSuitableStrategy gates the post-only maker entry to PASSIVE fade setups
// (mean-reversion / Bollinger) where price reverts toward a resting limit;
// momentum/breakout chase the move and would just fall back to taker, so they
// must stay MARKET. This locks the routing so a prompt/label tweak can't silently
// start routing momentum entries through maker (the live "always taker" finding).
func TestMakerSuitableStrategy(t *testing.T) {
	maker := []string{
		"mean_reversion", "mean-reversion", "MeanReversion", "MEAN_REVERSION",
		"bollinger", "Bollinger", "boll",
		" mean_reversion ", // trimmed
	}
	taker := []string{
		"momentum", "breakout", "ema_cross", "divergence",
		"", "unknown", "trend", "macd",
	}
	for _, s := range maker {
		if !makerSuitableStrategy(s) {
			t.Errorf("makerSuitableStrategy(%q) = false, want true (passive fade → maker)", s)
		}
	}
	for _, s := range taker {
		if makerSuitableStrategy(s) {
			t.Errorf("makerSuitableStrategy(%q) = true, want false (chasing/unknown → taker)", s)
		}
	}
}
