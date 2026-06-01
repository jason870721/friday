package risk

import (
	"context"
	"fmt"
	"sync"
)

// PaperPortfolio is the in-memory virtual book for paper-trading mode (PRD-021
// §4). When FRIDAY_PAPER=true the trading tools become no-ops that update this
// portfolio instead of placing real orders, so a strategy can be validated
// against live market data without risking testnet balance, consuming rate
// limits, or interfering with a parallel live session.
//
// Fills are at the mark price the caller passes (no slippage — optimistic, by
// design; a slippage model is a follow-up). Realised PnL accrues into the
// virtual wallet balance. Safe for concurrent use (the StopMonitor closes from
// its own goroutine while the trading tools open from the round loop).
type PaperPortfolio struct {
	mu        sync.Mutex
	balance   float64 // virtual wallet balance (initial + cumulative realised PnL)
	positions map[string]*paperPosition
}

// paperPosition is one virtual position. Amt is signed: >0 long, <0 short.
type paperPosition struct {
	amt      float64 // signed base-asset size
	entry    float64 // avg entry price
	leverage float64 // last leverage set for the symbol (≥1)
}

// PaperPosition is the exported snapshot of one virtual position.
type PaperPosition struct {
	Symbol   string
	Amt      float64 // signed
	Entry    float64
	Leverage float64
}

// Side reports LONG/SHORT for the (signed) amount.
func (p PaperPosition) Side() string {
	if p.Amt < 0 {
		return DirShort
	}
	return DirLong
}

// NewPaperPortfolio builds a portfolio seeded with initialBalance USDT (a
// non-positive value falls back to 1000).
func NewPaperPortfolio(initialBalance float64) *PaperPortfolio {
	if initialBalance <= 0 {
		initialBalance = 1000
	}
	return &PaperPortfolio{balance: initialBalance, positions: map[string]*paperPosition{}}
}

// SetLeverage records the leverage for a symbol (used in margin/uPnL display).
func (p *PaperPortfolio) SetLeverage(symbol string, leverage float64) {
	if leverage < 1 {
		leverage = 1
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	pos := p.positions[symbol]
	if pos == nil {
		pos = &paperPosition{leverage: leverage}
		p.positions[symbol] = pos
		return
	}
	pos.leverage = leverage
}

// Trade applies a market order at `price`. side is BUY/SELL; qty is positive
// base-asset size. It opens/adds in the order's direction, or reduces/flips an
// opposing position (realising PnL on the reduced portion). reduceOnly never
// flips: it caps the fill at the existing size. Returns the realised PnL booked
// by this trade (0 when only opening/adding).
func (p *PaperPortfolio) Trade(symbol, side string, qty, price float64, reduceOnly bool) float64 {
	if qty <= 0 || price <= 0 {
		return 0
	}
	signed := qty
	if side == "SELL" {
		signed = -qty
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	pos := p.positions[symbol]
	if pos == nil {
		pos = &paperPosition{leverage: 1}
		p.positions[symbol] = pos
	}

	// Same direction (or flat) → open/add: blend the entry price.
	if pos.amt == 0 || sameSign(pos.amt, signed) {
		if reduceOnly {
			return 0 // reduce-only can't add
		}
		newAmt := pos.amt + signed
		if pos.amt == 0 {
			pos.entry = price
		} else {
			pos.entry = (pos.entry*abs(pos.amt) + price*abs(signed)) / abs(newAmt)
		}
		pos.amt = newAmt
		return 0
	}

	// Opposing direction → reduce (and possibly flip).
	closeQty := abs(signed)
	if closeQty > abs(pos.amt) {
		closeQty = abs(pos.amt)
	}
	realised := realisedPnL(pos.amt, pos.entry, price, closeQty)
	p.balance += realised

	remainder := abs(signed) - closeQty
	if pos.amt > 0 {
		pos.amt -= closeQty
	} else {
		pos.amt += closeQty
	}
	if pos.amt == 0 {
		pos.entry = 0
	}
	// Flip the residual into a fresh position in the new direction (unless
	// reduce-only, which caps at the existing size).
	if remainder > 0 && !reduceOnly {
		flip := remainder
		if signed < 0 {
			flip = -remainder
		}
		pos.amt = flip
		pos.entry = price
	}
	return realised
}

// CloseReduceOnly flattens (part of) a position at `price`, returning the
// realised PnL. positionSide is the side being CLOSED (LONG → SELL to flatten).
// This satisfies the StopMonitor's needs in paper mode.
func (p *PaperPortfolio) CloseReduceOnly(ctx context.Context, symbol string, qty float64, positionSide string) error {
	side := "SELL" // flatten a long
	if positionSide == DirShort {
		side = "BUY"
	}
	p.Trade(symbol, side, qty, p.markFallback(symbol), true)
	return nil
}

// CloseAt flattens a position fully at `price`, returning the realised PnL and
// the signed size that was closed.
func (p *PaperPortfolio) CloseAt(symbol string, price float64) (realised, closedAmt float64) {
	p.mu.Lock()
	pos := p.positions[symbol]
	if pos == nil || pos.amt == 0 {
		p.mu.Unlock()
		return 0, 0
	}
	amt := pos.amt
	p.mu.Unlock()
	side := "SELL"
	if amt < 0 {
		side = "BUY"
	}
	return p.Trade(symbol, side, abs(amt), price, true), amt
}

// markFallback returns a position's entry price as a stand-in mark when the
// caller can't supply one (CloseReduceOnly from the StopMonitor path computes
// at entry → ~0 PnL; the real mark is used by CloseAt where the tool fetches it).
func (p *PaperPortfolio) markFallback(symbol string) float64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	if pos := p.positions[symbol]; pos != nil && pos.entry > 0 {
		return pos.entry
	}
	return 1
}

// Position returns a snapshot of one symbol's virtual position (ok=false when
// flat).
func (p *PaperPortfolio) Position(symbol string) (PaperPosition, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pos := p.positions[symbol]
	if pos == nil || pos.amt == 0 {
		return PaperPosition{}, false
	}
	return PaperPosition{Symbol: symbol, Amt: pos.amt, Entry: pos.entry, Leverage: pos.leverage}, true
}

// Positions returns snapshots of all non-zero virtual positions.
func (p *PaperPortfolio) Positions() []PaperPosition {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]PaperPosition, 0, len(p.positions))
	for sym, pos := range p.positions {
		if pos.amt == 0 {
			continue
		}
		out = append(out, PaperPosition{Symbol: sym, Amt: pos.amt, Entry: pos.entry, Leverage: pos.leverage})
	}
	return out
}

// Balance returns the virtual wallet balance (initial + cumulative realised).
func (p *PaperPortfolio) Balance() float64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.balance
}

// realisedPnL is the PnL of closing closeQty (positive) of a position with the
// given signed size and entry, at exit price.
func realisedPnL(signedAmt, entry, exit, closeQty float64) float64 {
	if signedAmt > 0 { // long: profit when exit > entry
		return (exit - entry) * closeQty
	}
	return (entry - exit) * closeQty // short
}

func sameSign(a, b float64) bool { return (a > 0 && b > 0) || (a < 0 && b < 0) }

// fmtPaper renders a one-line position summary for tool output.
func (p PaperPosition) String() string {
	return fmt.Sprintf("%s %s size=%g entry=%.4f lev=%gx", p.Symbol, p.Side(), abs(p.Amt), p.Entry, p.Leverage)
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
