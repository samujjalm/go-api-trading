package domain

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// QuoteAsset is the currency every symbol is priced in. Amounts of it are in
// minor units (cents); amounts of a symbol's asset are whole units, matching
// order quantities.
const QuoteAsset = "USD"

// MaxTransferAmount bounds a single deposit or withdrawal.
const MaxTransferAmount int64 = 1_000_000_000_000

var ErrInsufficientFunds = errors.New("insufficient funds")

func ValidateAsset(raw string) (string, error) {
	a := strings.ToUpper(strings.TrimSpace(raw))
	if a == QuoteAsset {
		return a, nil
	}
	if _, err := ValidateSymbol(a); err != nil {
		return "", invalid("asset", "must be "+QuoteAsset+" or a valid symbol")
	}
	return a, nil
}

func ValidateTransferAmount(amount int64) error {
	if amount <= 0 || amount > MaxTransferAmount {
		return invalid("amount", fmt.Sprintf("must be between 1 and %d", MaxTransferAmount))
	}
	return nil
}

type MovementKind string

const (
	MovementDeposit     MovementKind = "DEPOSIT"
	MovementWithdrawal  MovementKind = "WITHDRAWAL"
	MovementReserve     MovementKind = "RESERVE"      // available → reserved when an order is placed
	MovementRelease     MovementKind = "RELEASE"      // reserved → available on cancel, IOC remainder, price improvement
	MovementTradeDebit  MovementKind = "TRADE_DEBIT"  // reserved funds leave the account in a trade
	MovementTradeCredit MovementKind = "TRADE_CREDIT" // proceeds of a trade arrive in available
)

// Movement is one entry in the append-only balance journal. For every
// balance, available == Σ DeltaAvailable and reserved == Σ DeltaReserved
// over its movements; the integration tests check exactly that. Never update
// or delete movements: corrections are new movements.
type Movement struct {
	ID             string
	UserID         string
	Asset          string
	Kind           MovementKind
	DeltaAvailable int64
	DeltaReserved  int64
	OrderID        string
	TradeID        string
	CreatedAt      time.Time
}

// Balance is one user's holding of one asset. All changes go through its
// methods, which enforce non-negativity and record a Movement; the
// repository persists the new totals and the pending movements together.
type Balance struct {
	UserID    string
	Asset     string
	Available int64
	Reserved  int64

	pending []Movement
}

func (b *Balance) Total() int64 { return b.Available + b.Reserved }

func (b *Balance) Deposit(amount int64) error {
	if err := ValidateTransferAmount(amount); err != nil {
		return err
	}
	b.apply(MovementDeposit, amount, 0, "", "")
	return nil
}

func (b *Balance) Withdraw(amount int64) error {
	if err := ValidateTransferAmount(amount); err != nil {
		return err
	}
	if b.Available < amount {
		return b.insufficient(amount)
	}
	b.apply(MovementWithdrawal, -amount, 0, "", "")
	return nil
}

// Reserve locks funds for an order so they can't be spent twice.
func (b *Balance) Reserve(amount int64, orderID string) error {
	if amount <= 0 || b.Available < amount {
		return b.insufficient(amount)
	}
	b.apply(MovementReserve, -amount, amount, orderID, "")
	return nil
}

// Release returns reserved funds to available. A zero amount is a no-op.
func (b *Balance) Release(amount int64, orderID, tradeID string) {
	if amount != 0 {
		b.apply(MovementRelease, amount, -amount, orderID, tradeID)
	}
}

func (b *Balance) debitReserved(amount int64, orderID, tradeID string) {
	b.apply(MovementTradeDebit, 0, -amount, orderID, tradeID)
}

func (b *Balance) credit(amount int64, orderID, tradeID string) {
	b.apply(MovementTradeCredit, amount, 0, orderID, tradeID)
}

// apply is the single place balances change. A negative result or an
// overflow means a bug upstream (e.g. releasing more than was reserved), so
// it panics and the surrounding transaction rolls back.
func (b *Balance) apply(kind MovementKind, dAvail, dRes int64, orderID, tradeID string) {
	avail, ok1 := addChecked(b.Available, dAvail)
	res, ok2 := addChecked(b.Reserved, dRes)
	if !ok1 || !ok2 || avail < 0 || res < 0 {
		panic(fmt.Sprintf("balance %s/%s: %s(%d, %d) on available=%d reserved=%d breaks invariants",
			b.UserID, b.Asset, kind, dAvail, dRes, b.Available, b.Reserved))
	}
	b.Available, b.Reserved = avail, res
	b.pending = append(b.pending, Movement{
		UserID: b.UserID, Asset: b.Asset, Kind: kind,
		DeltaAvailable: dAvail, DeltaReserved: dRes, OrderID: orderID, TradeID: tradeID,
	})
}

func (b *Balance) insufficient(amount int64) error {
	return fmt.Errorf("%w: need %d %s, available %d", ErrInsufficientFunds, amount, b.Asset, b.Available)
}

// PendingMovements returns changes not yet persisted.
func (b *Balance) PendingMovements() []Movement { return b.pending }

// MarkPersisted is called by the repository after saving.
func (b *Balance) MarkPersisted() { b.pending = nil }

func addChecked(a, b int64) (int64, bool) {
	if (b > 0 && a > math.MaxInt64-b) || (b < 0 && a < math.MinInt64-b) {
		return 0, false
	}
	return a + b, true
}

// TradeParties are the four balances a trade touches.
type TradeParties struct {
	BuyerQuote, BuyerBase, SellerQuote, SellerBase *Balance
}

// Settle moves funds for one executed trade (delivery versus payment: both
// legs happen in the same transaction or not at all):
//
//	buyer:  reserved USD  −price×qty  (plus any price improvement released)
//	        available ACME +qty
//	seller: reserved ACME −qty
//	        available USD  +price×qty
func Settle(t *Trade, buy, sell *Order, p TradeParties) {
	if p.BuyerQuote.Asset != QuoteAsset || p.SellerQuote.Asset != QuoteAsset ||
		p.BuyerBase.Asset != t.Symbol || p.SellerBase.Asset != t.Symbol ||
		p.BuyerQuote.UserID != buy.UserID || p.SellerBase.UserID != sell.UserID {
		panic("settle: balances don't match the trade")
	}
	notional := t.Price * t.Quantity // bounded by MaxPrice*MaxQuantity, fits in int64

	p.BuyerQuote.debitReserved(notional, buy.ID, t.ID)
	p.BuyerQuote.Release(priceImprovement(buy, t.Price, t.Quantity), buy.ID, t.ID)
	p.BuyerBase.credit(t.Quantity, buy.ID, t.ID)

	p.SellerBase.debitReserved(t.Quantity, sell.ID, t.ID)
	p.SellerQuote.credit(notional, sell.ID, t.ID)
}

// priceImprovement is what a limit buyer reserved but didn't need because
// the trade printed below their limit. Market buys have no limit; their
// unused budget is released when the order closes instead.
func priceImprovement(buy *Order, price, qty int64) int64 {
	if buy.Type != OrderTypeLimit {
		return 0
	}
	return (buy.Price - price) * qty
}
