package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/johnny1110/friday/internal/risk"
)

// haltedBreaker returns a breaker driven into the Halted state via a >20%
// drawdown observation.
func haltedBreaker() *risk.CircuitBreaker {
	cb := risk.NewCircuitBreaker(0.10, 5, 0.20, 20)
	cb.Observe(10000)
	cb.Observe(7000) // -30% → halt
	return cb
}

func TestBinanceOrder_BreakerBlocksOpening(t *testing.T) {
	SetCircuitBreaker(haltedBreaker())
	defer SetCircuitBreaker(nil)

	// An OPENING order (reduce_only false) must be blocked by the breaker
	// before any network call.
	res, err := NewBinanceOrder().Execute(context.Background(), nopLogger(),
		json.RawMessage(`{"symbol":"BTCUSDT","side":"BUY","quantity":0.01}`))
	if err != nil {
		t.Fatalf("unexpected go error: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "CIRCUIT BREAKER HALTED") {
		t.Errorf("expected breaker-halt block; got isErr=%v content=%q", res.IsError, res.Content)
	}
}

func TestBinanceOrder_BreakerBypassedForReduceOnly(t *testing.T) {
	SetCircuitBreaker(haltedBreaker())
	defer SetCircuitBreaker(nil)

	// A reduce-only CLOSE must bypass the breaker. It then proceeds toward
	// the client (which errors without creds in tests) — the key assertion
	// is that it is NOT blocked by the circuit breaker.
	res, _ := NewBinanceOrder().Execute(context.Background(), nopLogger(),
		json.RawMessage(`{"symbol":"BTCUSDT","side":"SELL","quantity":0.01,"reduce_only":true}`))
	if strings.Contains(res.Content, "CIRCUIT BREAKER") {
		t.Errorf("reduce-only close should bypass the breaker; got %q", res.Content)
	}
}
