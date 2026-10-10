package domain

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

type Side string

const (
	SideBuy  Side = "BUY"
	SideSell Side = "SELL"
)

type OrderType string

const (
	// OrderTypeLimit trades at the given price or better; any unfilled
	// remainder rests on the book.
	OrderTypeLimit OrderType = "LIMIT"
	// OrderTypeMarket trades at whatever price is available and never rests:
	// any unfilled remainder is cancelled (immediate-or-cancel).
	OrderTypeMarket OrderType = "MARKET"
)

type OrderStatus string

const (
	StatusNew             OrderStatus = "NEW"
	StatusPartiallyFilled OrderStatus = "PARTIALLY_FILLED"
	StatusFilled          OrderStatus = "FILLED"
	StatusCancelled       OrderStatus = "CANCELLED"
)

func (s OrderStatus) Valid() bool {
	switch s {
	case StatusNew, StatusPartiallyFilled, StatusFilled, StatusCancelled:
		return true
	}
	return false
}

// Prices are integers in minor units (e.g. cents) and quantities are whole
// units. Never use float64 for money: 0.1+0.2 != 0.3. The bounds guarantee
// price*quantity fits in an int64 (1e9 * 1e9 = 1e18 < 9.2e18).
const (
	MaxPrice            int64 = 1_000_000_000
	MaxQuantity         int64 = 1_000_000_000
	maxClientOrderIDLen       = 64
)

var symbolRE = regexp.MustCompile(`^[A-Z][A-Z0-9.]{0,11}$`)

func ValidateSymbol(raw string) (string, error) {
	s := strings.ToUpper(strings.TrimSpace(raw))
	if !symbolRE.MatchString(s) {
		return "", invalid("symbol", "must be 1-12 characters: A-Z, 0-9 or '.'")
	}
	if s == QuoteAsset {
		return "", invalid("symbol", QuoteAsset+" is the quote currency, not a tradable symbol")
	}
	return s, nil
}

type Order struct {
	ID             string
	UserID         string
	ClientOrderID  string // caller-chosen idempotency key, optional
	Symbol         string
	Side           Side
	Type           OrderType
	Price          int64 // limit price in minor units; 0 for market orders
	Quantity       int64
	FilledQuantity int64
	// Reserved is how much of ReserveAsset() is still locked for this
	// order: price×remaining for limit buys, the unspent budget for market
	// buys, remaining quantity for sells. It is 0 once the order closes.
	Reserved  int64
	Status    OrderStatus
	CreatedAt time.Time
	UpdatedAt time.Time
}

type NewOrderParams struct {
	UserID        string
	ClientOrderID string
	Symbol        string
	Side          Side
	Type          OrderType
	Price         int64
	Quantity      int64
}

// NewOrder validates the parameters and returns an order in status NEW.
// Constructors like this make invalid orders unrepresentable further in.
func NewOrder(p NewOrderParams, now time.Time) (*Order, error) {
	symbol, err := ValidateSymbol(p.Symbol)
	if err != nil {
		return nil, err
	}
	if p.Side != SideBuy && p.Side != SideSell {
		return nil, invalid("side", "must be BUY or SELL")
	}
	switch p.Type {
	case OrderTypeLimit:
		if p.Price <= 0 || p.Price > MaxPrice {
			return nil, invalid("price", fmt.Sprintf("must be between 1 and %d", MaxPrice))
		}
	case OrderTypeMarket:
		if p.Price != 0 {
			return nil, invalid("price", "must be omitted for MARKET orders")
		}
	default:
		return nil, invalid("type", "must be LIMIT or MARKET")
	}
	if p.Quantity <= 0 || p.Quantity > MaxQuantity {
		return nil, invalid("quantity", fmt.Sprintf("must be between 1 and %d", MaxQuantity))
	}
	if len(p.ClientOrderID) > maxClientOrderIDLen {
		return nil, invalid("client_order_id", fmt.Sprintf("must be at most %d characters", maxClientOrderIDLen))
	}

	return &Order{
		UserID:        p.UserID,
		ClientOrderID: p.ClientOrderID,
		Symbol:        symbol,
		Side:          p.Side,
		Type:          p.Type,
		Price:         p.Price,
		Quantity:      p.Quantity,
		Status:        StatusNew,
		CreatedAt:     now,
		UpdatedAt:     now,
	}, nil
}

func (o *Order) Remaining() int64 { return o.Quantity - o.FilledQuantity }

// ReserveAsset is what the order locks: cash for buys, the instrument for sells.
func (o *Order) ReserveAsset() string {
	if o.Side == SideBuy {
		return QuoteAsset
	}
	return o.Symbol
}

// ReservationFor returns how much to reserve given the user's available
// balance of ReserveAsset(). A market buy has no price, so it reserves
// everything available as its spending budget; matching can't spend more
// and the rest is released when it completes.
func (o *Order) ReservationFor(available int64) int64 {
	switch {
	case o.Side == SideSell:
		return o.Quantity
	case o.Type == OrderTypeLimit:
		return o.Price * o.Quantity
	default:
		return available
	}
}

// DrainReserve zeroes and returns the reservation of a closed order so it can
// be released back to the user. Open orders keep their reservation.
func (o *Order) DrainReserve() int64 {
	if o.IsOpen() {
		return 0
	}
	r := o.Reserved
	o.Reserved = 0
	return r
}

// affordable caps qty by what the order's reservation can pay for at price.
func (o *Order) affordable(qty, price int64) int64 {
	if o.Side == SideBuy {
		return min(qty, o.Reserved/price)
	}
	return min(qty, o.Reserved)
}

func (o *Order) IsOpen() bool {
	return o.Status == StatusNew || o.Status == StatusPartiallyFilled
}

// Crosses reports whether o, as the incoming order, is willing to trade at
// the resting order's price.
func (o *Order) Crosses(resting *Order) bool {
	if o.Type == OrderTypeMarket {
		return true
	}
	if o.Side == SideBuy {
		return o.Price >= resting.Price
	}
	return o.Price <= resting.Price
}

func (o *Order) Cancel(now time.Time) error {
	if !o.IsOpen() {
		return ErrOrderNotOpen
	}
	o.Status = StatusCancelled
	o.UpdatedAt = now
	return nil
}

// fill records an execution and consumes the matching part of the
// reservation, mirroring what Settle does to the balance.
func (o *Order) fill(qty, price int64, now time.Time) {
	if o.Side == SideBuy {
		o.Reserved -= price*qty + priceImprovement(o, price, qty)
	} else {
		o.Reserved -= qty
	}
	if o.Reserved < 0 {
		panic(fmt.Sprintf("order %s: reservation went negative", o.ID))
	}
	o.FilledQuantity += qty
	if o.Remaining() == 0 {
		o.Status = StatusFilled
	} else {
		o.Status = StatusPartiallyFilled
	}
	o.UpdatedAt = now
}

// FinishMatching decides what happens to an incoming order's unfilled
// remainder once matching is done: limit orders rest on the book, market
// orders and self-trade-prevented orders are cancelled.
func (o *Order) FinishMatching(selfTradePrevented bool, now time.Time) {
	if !o.IsOpen() {
		return
	}
	if o.Type == OrderTypeMarket || selfTradePrevented {
		o.Status = StatusCancelled
		o.UpdatedAt = now
	}
}
