package tool

import "github.com/johnny1110/friday/internal/risk"

// globalBreaker is the process-wide session circuit breaker (PRD-005).
// bootstrap constructs it (reading env thresholds) and installs it via
// SetCircuitBreaker; binance_order consults it before opening, and
// log_trade feeds it realised PnL on every close. The orchestrator holds
// the same pointer to Observe/Tick it each round. Nil when unset (tests),
// in which case the gate is a no-op.
var globalBreaker *risk.CircuitBreaker

// SetCircuitBreaker installs the shared breaker. Called once at bootstrap.
func SetCircuitBreaker(cb *risk.CircuitBreaker) { globalBreaker = cb }
