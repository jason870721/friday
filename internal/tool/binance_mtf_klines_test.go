package tool

import (
	"strings"
	"testing"

	"github.com/johnny1110/friday/internal/binance"
)

func tf(interval, dir string) tfRead { return tfRead{interval: interval, dir: dir} }

func TestRegimeLine(t *testing.T) {
	// A strong, steady uptrend → high ADX → TRENDING (PRD-016).
	ks := make([]binance.Kline, 40)
	for i := range ks {
		b := 100.0 + float64(i)*2
		ks[i] = binance.Kline{High: b + 1, Low: b - 1, Close: b + 0.5}
	}
	line := regimeLine(ks)
	if !strings.Contains(line, "Regime: TRENDING") || !strings.Contains(line, "ADX") {
		t.Errorf("regimeLine = %q; want a TRENDING classification with ADX", line)
	}
	// Too few candles for ADX(14) → no regime line.
	if got := regimeLine(ks[:10]); got != "" {
		t.Errorf("regimeLine with <29 candles = %q; want empty", got)
	}
}

func TestCrossTimeframeVerdict(t *testing.T) {
	bull, bear, neut := binance.DirectionBullish, binance.DirectionBearish, binance.DirectionNeutral

	cases := []struct {
		name       string
		reads      []tfRead
		wantPrefix string
		wantSubstr string // extra assertion (e.g. dominant TF on conflict)
	}{
		{"all bullish", []tfRead{tf("5m", bull), tf("1h", bull), tf("4h", bull)}, "ALIGNED BULLISH", ""},
		{"all bearish", []tfRead{tf("5m", bear), tf("1h", bear), tf("4h", bear)}, "ALIGNED BEARISH", ""},
		{"bull with neutral", []tfRead{tf("5m", bull), tf("1h", neut), tf("4h", bull)}, "ALIGNED BULLISH", ""},
		{"all neutral", []tfRead{tf("5m", neut), tf("1h", neut), tf("4h", neut)}, "NO-EDGE", ""},
		// 5m long against a 4h downtrend → CONFLICT, 4h dominates.
		{"conflict 4h dominates", []tfRead{tf("5m", bull), tf("1h", bull), tf("4h", bear)}, "CONFLICT", "4h BEARISH"},
		// highest non-neutral is 1h here (4h neutral).
		{"conflict 1h dominates", []tfRead{tf("5m", bull), tf("1h", bear), tf("4h", neut)}, "CONFLICT", "1h BEARISH"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := crossTimeframeVerdict(c.reads)
			if !strings.HasPrefix(got, c.wantPrefix) {
				t.Errorf("verdict = %q; want prefix %q", got, c.wantPrefix)
			}
			if c.wantSubstr != "" && !strings.Contains(got, c.wantSubstr) {
				t.Errorf("verdict = %q; want substring %q", got, c.wantSubstr)
			}
		})
	}
}
