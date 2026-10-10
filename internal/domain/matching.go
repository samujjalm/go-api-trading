package domain

import (
	"fmt"
	"time"
)

type Trade struct {
	ID           string
	Symbol       string
	BuyOrderID   string
	SellOrderID  string
	MakerOrderID string // the resting order that provided liquidity
	TakerOrderID string // the incoming order that took it
	TakerSide    Side
	Price        int64
	Quantity     int64
	ExecutedAt   time.Time
}

type PriceLevel struct {
	Price    int64
	Quantity int64
	Orders   int
}

type OrderBookSnapshot struct {
	Symbol string
	Bids   []PriceLevel // highest price first
	Asks   []PriceLevel // lowest price first
}

type MatchResult struct {
	Trades []*Trade
	// Makers[i] is the resting order filled by Trades[i]; each must be persisted.
	Makers []*Order
	// Halted is true when matching stopped before exhausting makers or
	// filling the taker: the next maker didn't cross, self-trade, or a
	// market buy ran out of budget.
	Halted             bool
	SelfTradePrevented bool
}

// Match executes taker against makers using price-time priority.
//
// makers must be open resting orders on the opposite side of the same
// symbol, already sorted best price first and oldest first within a price
// (that's the storage layer's job). Match mutates taker and makers in place.
//
// Rules:
//   - Trades execute at the maker's price, so the taker gets any price
//     improvement.
//   - An order never trades more than its reservation pays for. For limit
//     orders that's always the full remaining quantity; for a market buy
//     it's its budget.
//   - Self-trade prevention: if the next maker belongs to the same user,
//     matching stops and the taker's remainder will be cancelled
//     ("cancel newest"), so the book can never become crossed.
func Match(taker *Order, makers []*Order, now time.Time) MatchResult {
	var r MatchResult
	for _, maker := range makers {
		if taker.Remaining() == 0 {
			return r
		}
		mustBeMatchable(taker, maker)

		if !taker.Crosses(maker) {
			r.Halted = true
			return r
		}
		if maker.UserID == taker.UserID {
			r.Halted, r.SelfTradePrevented = true, true
			return r
		}

		qty := taker.affordable(min(taker.Remaining(), maker.Remaining()), maker.Price)
		if qty == 0 { // market buy budget can't cover even one unit at this price
			r.Halted = true
			return r
		}
		taker.fill(qty, maker.Price, now)
		maker.fill(qty, maker.Price, now)
		r.Trades = append(r.Trades, newTrade(taker, maker, qty, now))
		r.Makers = append(r.Makers, maker)
	}
	return r
}

// mustBeMatchable guards invariants the caller is responsible for. Violating
// one is a programming bug that could move money incorrectly, so we panic
// (the HTTP layer recovers and the DB transaction rolls back) rather than
// guess.
func mustBeMatchable(taker, maker *Order) {
	switch {
	case maker.Symbol != taker.Symbol:
		panic(fmt.Sprintf("match: symbol mismatch %s/%s", taker.Symbol, maker.Symbol))
	case maker.Side == taker.Side:
		panic("match: maker on same side as taker")
	case maker.Type != OrderTypeLimit:
		panic("match: resting order is not a limit order")
	case !maker.IsOpen() || maker.Remaining() <= 0:
		panic(fmt.Sprintf("match: maker %s is not open", maker.ID))
	case maker.affordable(maker.Remaining(), maker.Price) != maker.Remaining():
		panic(fmt.Sprintf("match: maker %s reservation %d doesn't cover its remaining quantity", maker.ID, maker.Reserved))
	}
}

func newTrade(taker, maker *Order, qty int64, now time.Time) *Trade {
	t := &Trade{
		Symbol:       taker.Symbol,
		MakerOrderID: maker.ID,
		TakerOrderID: taker.ID,
		TakerSide:    taker.Side,
		Price:        maker.Price,
		Quantity:     qty,
		ExecutedAt:   now,
	}
	if taker.Side == SideBuy {
		t.BuyOrderID, t.SellOrderID = taker.ID, maker.ID
	} else {
		t.BuyOrderID, t.SellOrderID = maker.ID, taker.ID
	}
	return t
}
