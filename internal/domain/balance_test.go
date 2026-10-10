package domain

import (
	"errors"
	"testing"
)

func TestBalanceReserveRelease(t *testing.T) {
	b := &Balance{UserID: "u", Asset: QuoteAsset}
	if err := b.Deposit(1000); err != nil {
		t.Fatal(err)
	}
	if err := b.Reserve(1500, "o1"); !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("over-reserve: got %v", err)
	}
	if err := b.Reserve(600, "o1"); err != nil {
		t.Fatal(err)
	}
	if b.Available != 400 || b.Reserved != 600 {
		t.Fatalf("after reserve: %+v", b)
	}
	if err := b.Withdraw(500); !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("reserved funds must not be withdrawable: %v", err)
	}
	b.Release(600, "o1", "")
	if b.Available != 1000 || b.Reserved != 0 {
		t.Fatalf("after release: %+v", b)
	}
	if n := len(b.PendingMovements()); n != 3 {
		t.Fatalf("got %d movements, want 3 (deposit, reserve, release)", n)
	}
}

func TestBalancePanicsOnOverRelease(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	b := &Balance{UserID: "u", Asset: QuoteAsset, Reserved: 10}
	b.Release(11, "o", "")
}

func TestBalanceRejectsBadAmounts(t *testing.T) {
	b := &Balance{UserID: "u", Asset: QuoteAsset}
	for _, amt := range []int64{0, -1, MaxTransferAmount + 1} {
		if err := b.Deposit(amt); err == nil {
			t.Errorf("Deposit(%d) accepted", amt)
		}
	}
}

func TestSettle(t *testing.T) {
	// Bob bids 10 @ 105 and buys 4 from Alice's ask @ 100.
	buy := limit("buy", "bob", SideBuy, 105, 10)
	sell := limit("sell", "alice", SideSell, 100, 4)
	p := TradeParties{
		BuyerQuote:  &Balance{UserID: "bob", Asset: QuoteAsset, Reserved: 1050},
		BuyerBase:   &Balance{UserID: "bob", Asset: "ACME"},
		SellerQuote: &Balance{UserID: "alice", Asset: QuoteAsset},
		SellerBase:  &Balance{UserID: "alice", Asset: "ACME", Reserved: 4},
	}
	r := Match(buy, []*Order{sell}, now)
	Settle(r.Trades[0], buy, sell, p)

	check := func(name string, b *Balance, avail, res int64) {
		t.Helper()
		if b.Available != avail || b.Reserved != res {
			t.Errorf("%s: available=%d reserved=%d, want %d/%d", name, b.Available, b.Reserved, avail, res)
		}
	}
	check("buyer USD", p.BuyerQuote, 20, 630) // 400 paid, 20 improvement released, 630 still backs 6 @ 105
	check("buyer ACME", p.BuyerBase, 4, 0)
	check("seller USD", p.SellerQuote, 400, 0)
	check("seller ACME", p.SellerBase, 0, 0)

	// The order's own reservation must agree with the balance.
	if buy.Reserved != p.BuyerQuote.Reserved {
		t.Errorf("order reserved %d != balance reserved %d", buy.Reserved, p.BuyerQuote.Reserved)
	}
}

func TestValidateAsset(t *testing.T) {
	if a, err := ValidateAsset("usd"); err != nil || a != QuoteAsset {
		t.Fatalf("usd: %q %v", a, err)
	}
	if _, err := ValidateSymbol("USD"); err == nil {
		t.Fatal("USD must not be a tradable symbol")
	}
	if _, err := ValidateAsset("not valid"); err == nil {
		t.Fatal("accepted invalid asset")
	}
}
