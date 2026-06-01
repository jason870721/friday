package strategy

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// RSI extreme-zone thresholds (PRD-022 §4.1). A directional consensus formed at
// or beyond these levels is statistically likely to mean-revert against the
// trade — voting LONG into a peak or SHORT into a trough is exactly the pattern
// the loss analysis flagged (SOL LONG@30, NVDA SHORT@26). The filter blocks ANY
// directional consensus in either extreme zone, regardless of side.
const (
	rsiOverbought = 75.0
	rsiOversold   = 25.0
)

// RSIFilter downgrades a directional consensus to NEUTRAL when the timeframe's
// RSI(14) sits in an extreme zone (≥75 or ≤25) — a global gate on top of each
// strategy's own RSI guard (PRD-022 R1). A NEUTRAL consensus, an unavailable RSI
// (rsi == 0), or an in-range RSI passes through unchanged. Disable the whole
// filter with FRIDAY_RSI_FILTER=false (R2).
func RSIFilter(c Consensus, rsi float64) Consensus {
	if !rsiFilterEnabled() {
		return c
	}
	if c.Direction == Neutral || rsi == 0 {
		return c
	}
	if rsi >= rsiOverbought || rsi <= rsiOversold {
		c.Direction = Neutral
		c.Confidence = 0
		c.Summary = strings.TrimSpace(c.Summary) + fmt.Sprintf(" (blocked: RSI %.1f in extreme zone)", rsi)
		return c
	}
	return c
}

// --- env-configurable knobs (read per call so tests using t.Setenv take effect,
// and an operator can flip them without a rebuild) ---

// rsiFilterEnabled reports whether the RSI extreme-zone filter is active
// (FRIDAY_RSI_FILTER, default true; "false"/"0" disables).
func rsiFilterEnabled() bool { return envBool("FRIDAY_RSI_FILTER", true) }

// mtf5m1hOverrideEnabled reports whether the 5m+1h lower-timeframe override is
// active (FRIDAY_MTF_5M1H_OVERRIDE, default true).
func mtf5m1hOverrideEnabled() bool { return envBool("FRIDAY_MTF_5M1H_OVERRIDE", true) }

// mtfHysteresisValue is the dead-band around 0 in which the weighted net is read
// as NEUTRAL (FRIDAY_MTF_HYSTERESIS, default 0.05 — lowered from PRD-017's 0.1 so
// aligned lower-timeframe signals are not over-filtered, PRD-022 R7).
func mtfHysteresisValue() float64 { return envFloat("FRIDAY_MTF_HYSTERESIS", 0.05) }

func envBool(key string, def bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	switch strings.ToLower(v) {
	case "false", "0", "no", "off":
		return false
	case "true", "1", "yes", "on":
		return true
	default:
		return def
	}
}

func envFloat(key string, def float64) float64 {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def
	}
	return f
}
