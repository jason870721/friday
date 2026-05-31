package binance

import "testing"

// risingWithDips builds a generally-rising close series (so RSI lands in the
// trend band, not the overbought extreme) ending in 3 strictly higher closes.
func risingWithDips() []float64 {
	cs := []float64{100}
	// alternate +1.2 / −1.0 to keep RSI mid-band, net up.
	for i := 0; i < 18; i++ {
		last := cs[len(cs)-1]
		if i%2 == 0 {
			cs = append(cs, last+1.2)
		} else {
			cs = append(cs, last-1.0)
		}
	}
	// tail: 3 strictly rising closes.
	last := cs[len(cs)-1]
	cs = append(cs, last+0.5, last+1.0, last+1.6)
	return cs
}

func fallingWithBounces() []float64 {
	cs := []float64{200}
	for i := 0; i < 18; i++ {
		last := cs[len(cs)-1]
		if i%2 == 0 {
			cs = append(cs, last-1.2)
		} else {
			cs = append(cs, last+1.0)
		}
	}
	last := cs[len(cs)-1]
	cs = append(cs, last-0.5, last-1.0, last-1.6)
	return cs
}

func choppy() []float64 {
	cs := make([]float64, 0, 24)
	for i := 0; i < 24; i++ {
		if i%2 == 0 {
			cs = append(cs, 100)
		} else {
			cs = append(cs, 101)
		}
	}
	return cs
}

func TestClassifyDirection(t *testing.T) {
	cases := []struct {
		name   string
		closes []float64
		want   string
	}{
		{"bullish", risingWithDips(), DirectionBullish},
		{"bearish", fallingWithBounces(), DirectionBearish},
		{"choppy", choppy(), DirectionNeutral},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ks := klinesFromCloses(c.closes...)
			ma, _ := SMA(c.closes, 20)
			rsi, _ := RSI(c.closes, 14)
			got := ClassifyDirection(ks)
			if got != c.want {
				t.Errorf("ClassifyDirection = %s; want %s (last=%.2f MA20=%.2f RSI=%.2f)",
					got, c.want, c.closes[len(c.closes)-1], ma, rsi)
			}
		})
	}
}

func TestClassifyDirection_TooShort(t *testing.T) {
	if got := ClassifyDirection(klinesFromCloses(1, 2, 3)); got != DirectionNeutral {
		t.Errorf("short series = %s; want NEUTRAL", got)
	}
}
