package app_test

import (
	"context"
	"errors"
	"testing"

	"github.com/samujjal/go-service/internal/app"
	"github.com/samujjal/go-service/internal/domain"
)

type tradingFixture struct {
	t      *testing.T
	store  *memStore
	svc    *app.TradingService
	acct   *app.AccountService
	funded map[string]bool
}

// newTrading returns a fixture whose place/buy/sell helpers give each user a
// large balance on first use, so matching tests needn't care about funds.
// Tests about funds call f.deposit and f.svc directly instead. Every test
// ends with a ledger consistency check.
func newTrading(t *testing.T) *tradingFixture {
	store := newMemStore()
	f := &tradingFixture{t: t, store: store, svc: app.NewTradingService(store),
		acct: app.NewAccountService(store), funded: map[string]bool{}}
	t.Cleanup(f.checkLedger)
	return f
}

func (f *tradingFixture) deposit(user, asset string, amount int64) {
	f.t.Helper()
	if _, err := f.acct.Deposit(context.Background(), user, asset, amount); err != nil {
		f.t.Fatalf("Deposit: %v", err)
	}
}

func (f *tradingFixture) balance(user, asset string) domain.Balance {
	f.t.Helper()
	b, _ := f.store.Balances().GetForUpdate(context.Background(), user, asset)
	return *b
}

// checkLedger asserts the two accounting invariants:
//  1. each balance equals the sum of its journal movements
//  2. each balance's reserved amount equals the reservations of the user's open orders
func (f *tradingFixture) checkLedger() {
	sums := map[[2]string][2]int64{}
	for _, m := range f.store.movements {
		k := [2]string{m.UserID, m.Asset}
		v := sums[k]
		sums[k] = [2]int64{v[0] + m.DeltaAvailable, v[1] + m.DeltaReserved}
	}
	reserved := map[[2]string]int64{}
	for _, o := range f.store.orders {
		if o.IsOpen() {
			reserved[[2]string{o.UserID, o.ReserveAsset()}] += o.Reserved
		} else if o.Reserved != 0 {
			f.t.Errorf("closed order %s still reserves %d", o.ID, o.Reserved)
		}
	}
	for k, b := range f.store.balances {
		if got := sums[k]; got != [2]int64{b.Available, b.Reserved} {
			f.t.Errorf("%v: balance %d/%d but journal sums to %d/%d", k, b.Available, b.Reserved, got[0], got[1])
		}
		if b.Reserved != reserved[k] {
			f.t.Errorf("%v: reserved %d but open orders reserve %d", k, b.Reserved, reserved[k])
		}
	}
}

func (f *tradingFixture) place(user string, side domain.Side, typ domain.OrderType, price, qty int64) app.PlaceOrderResult {
	f.t.Helper()
	if !f.funded[user] {
		f.deposit(user, domain.QuoteAsset, 1_000_000_000)
		f.deposit(user, "ACME", 1_000_000)
		f.funded[user] = true
	}
	res, err := f.svc.PlaceOrder(context.Background(), domain.NewOrderParams{
		UserID: user, Symbol: "ACME", Side: side, Type: typ, Price: price, Quantity: qty,
	})
	if err != nil {
		f.t.Fatalf("PlaceOrder: %v", err)
	}
	return res
}

func (f *tradingFixture) buy(user string, price, qty int64) app.PlaceOrderResult {
	return f.place(user, domain.SideBuy, domain.OrderTypeLimit, price, qty)
}

func (f *tradingFixture) sell(user string, price, qty int64) app.PlaceOrderResult {
	return f.place(user, domain.SideSell, domain.OrderTypeLimit, price, qty)
}

func (f *tradingFixture) order(user, id string) *domain.Order {
	f.t.Helper()
	o, err := f.svc.GetOrder(context.Background(), user, id)
	if err != nil {
		f.t.Fatalf("GetOrder: %v", err)
	}
	return o
}

func TestLimitOrderRestsWithoutMatch(t *testing.T) {
	f := newTrading(t)
	res := f.buy("bob", 100, 10)
	if len(res.Trades) != 0 || res.Order.Status != domain.StatusNew {
		t.Fatalf("got %d trades, status %s", len(res.Trades), res.Order.Status)
	}

	book, _ := f.svc.OrderBook(context.Background(), "ACME", 0)
	if len(book.Bids) != 1 || book.Bids[0] != (domain.PriceLevel{Price: 100, Quantity: 10, Orders: 1}) {
		t.Fatalf("unexpected bids: %+v", book.Bids)
	}
}

func TestMatchesAtMakerPrice(t *testing.T) {
	f := newTrading(t)
	ask := f.sell("alice", 100, 10)
	res := f.buy("bob", 105, 10)

	if len(res.Trades) != 1 || res.Trades[0].Price != 100 || res.Trades[0].Quantity != 10 {
		t.Fatalf("unexpected trades: %+v", res.Trades)
	}
	if res.Order.Status != domain.StatusFilled {
		t.Errorf("taker status %s", res.Order.Status)
	}
	if got := f.order("alice", ask.Order.ID).Status; got != domain.StatusFilled {
		t.Errorf("maker status %s", got)
	}
}

func TestPriceTimePriority(t *testing.T) {
	f := newTrading(t)
	expensive := f.sell("alice", 101, 5)
	early := f.sell("carol", 100, 5)
	late := f.sell("dave", 100, 5)

	res := f.buy("bob", 101, 12)

	want := []struct {
		maker string
		price int64
		qty   int64
	}{
		{early.Order.ID, 100, 5}, // best price, first in time
		{late.Order.ID, 100, 5},  // same price, later
		{expensive.Order.ID, 101, 2},
	}
	if len(res.Trades) != len(want) {
		t.Fatalf("got %d trades, want %d", len(res.Trades), len(want))
	}
	for i, w := range want {
		tr := res.Trades[i]
		if tr.MakerOrderID != w.maker || tr.Price != w.price || tr.Quantity != w.qty {
			t.Errorf("trade %d = %s %d@%d, want %s %d@%d", i, tr.MakerOrderID, tr.Quantity, tr.Price, w.maker, w.qty, w.price)
		}
	}

	rest := f.order("alice", expensive.Order.ID)
	if rest.Status != domain.StatusPartiallyFilled || rest.Remaining() != 3 {
		t.Errorf("partially filled maker: status %s remaining %d", rest.Status, rest.Remaining())
	}
}

func TestPartiallyFilledTakerRests(t *testing.T) {
	f := newTrading(t)
	f.sell("alice", 100, 4)
	res := f.buy("bob", 100, 10)

	if res.Order.Status != domain.StatusPartiallyFilled || res.Order.Remaining() != 6 {
		t.Fatalf("status %s remaining %d", res.Order.Status, res.Order.Remaining())
	}
	book, _ := f.svc.OrderBook(context.Background(), "ACME", 0)
	if len(book.Asks) != 0 || len(book.Bids) != 1 || book.Bids[0].Quantity != 6 {
		t.Fatalf("unexpected book: %+v", book)
	}
}

func TestMarketOrderIsImmediateOrCancel(t *testing.T) {
	f := newTrading(t)
	f.sell("alice", 100, 5)

	res := f.place("bob", domain.SideBuy, domain.OrderTypeMarket, 0, 8)
	if res.Order.FilledQuantity != 5 || res.Order.Status != domain.StatusCancelled {
		t.Fatalf("filled %d status %s", res.Order.FilledQuantity, res.Order.Status)
	}

	empty := f.place("bob", domain.SideBuy, domain.OrderTypeMarket, 0, 1)
	if empty.Order.FilledQuantity != 0 || empty.Order.Status != domain.StatusCancelled {
		t.Fatalf("no liquidity: filled %d status %s", empty.Order.FilledQuantity, empty.Order.Status)
	}
}

func TestSelfTradePrevention(t *testing.T) {
	f := newTrading(t)
	ask := f.sell("alice", 100, 5)
	res := f.buy("alice", 100, 5)

	if len(res.Trades) != 0 || res.Order.Status != domain.StatusCancelled {
		t.Fatalf("got %d trades, status %s", len(res.Trades), res.Order.Status)
	}
	if f.order("alice", ask.Order.ID).Status != domain.StatusNew {
		t.Fatal("resting order should be untouched")
	}
}

func TestCancelOrder(t *testing.T) {
	ctx := context.Background()
	f := newTrading(t)
	bid := f.buy("bob", 100, 5)

	if _, err := f.svc.CancelOrder(ctx, "mallory", bid.Order.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cancel by other user: %v, want ErrNotFound", err)
	}
	o, err := f.svc.CancelOrder(ctx, "bob", bid.Order.ID)
	if err != nil || o.Status != domain.StatusCancelled {
		t.Fatalf("cancel: %v, status %v", err, o)
	}
	if _, err := f.svc.CancelOrder(ctx, "bob", bid.Order.ID); !errors.Is(err, domain.ErrOrderNotOpen) {
		t.Fatalf("second cancel: %v, want ErrOrderNotOpen", err)
	}

	// A cancelled order must not match.
	if res := f.sell("alice", 100, 5); len(res.Trades) != 0 {
		t.Fatal("matched against a cancelled order")
	}
}

func TestIdempotentPlaceOrder(t *testing.T) {
	ctx := context.Background()
	f := newTrading(t)
	f.deposit("bob", domain.QuoteAsset, 1_000)
	p := domain.NewOrderParams{UserID: "bob", ClientOrderID: "abc-1", Symbol: "ACME",
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Price: 100, Quantity: 1}

	first, err := f.svc.PlaceOrder(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.svc.PlaceOrder(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Replayed || second.Order.ID != first.Order.ID {
		t.Fatalf("expected replay of %s, got %+v", first.Order.ID, second)
	}
	orders, _ := f.svc.ListOrders(ctx, "bob", app.OrderFilter{})
	if len(orders) != 1 {
		t.Fatalf("got %d orders, want 1", len(orders))
	}
}

func TestBuyReservesThenSettlesWithPriceImprovement(t *testing.T) {
	ctx := context.Background()
	f := newTrading(t)
	f.deposit("carol", domain.QuoteAsset, 10_000)
	f.deposit("dave", "ACME", 10)

	if _, err := f.svc.PlaceOrder(ctx, domain.NewOrderParams{UserID: "dave", Symbol: "ACME",
		Side: domain.SideSell, Type: domain.OrderTypeLimit, Price: 100, Quantity: 10}); err != nil {
		t.Fatal(err)
	}
	if b := f.balance("dave", "ACME"); b.Available != 0 || b.Reserved != 10 {
		t.Fatalf("seller ACME after placing: %+v", b)
	}

	// Carol bids 6 @ 105, reserving 630, and trades 6 @ 100.
	res, err := f.svc.PlaceOrder(ctx, domain.NewOrderParams{UserID: "carol", Symbol: "ACME",
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Price: 105, Quantity: 6})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Trades) != 1 || res.Order.Reserved != 0 {
		t.Fatalf("trades %d, reserved left %d", len(res.Trades), res.Order.Reserved)
	}

	want := map[[2]string][2]int64{ // available, reserved
		{"carol", "USD"}:  {9_400, 0}, // paid 600; the 30 of improvement came back
		{"carol", "ACME"}: {6, 0},
		{"dave", "USD"}:   {600, 0},
		{"dave", "ACME"}:  {0, 4}, // 4 still resting on the book
	}
	for k, w := range want {
		if b := f.balance(k[0], k[1]); b.Available != w[0] || b.Reserved != w[1] {
			t.Errorf("%v: %d/%d, want %d/%d", k, b.Available, b.Reserved, w[0], w[1])
		}
	}
}

func TestInsufficientFundsRejectsOrder(t *testing.T) {
	ctx := context.Background()
	f := newTrading(t)
	f.deposit("erin", domain.QuoteAsset, 500)

	for name, p := range map[string]domain.NewOrderParams{
		"buy over budget":       {Side: domain.SideBuy, Type: domain.OrderTypeLimit, Price: 100, Quantity: 10},
		"sell without holdings": {Side: domain.SideSell, Type: domain.OrderTypeLimit, Price: 100, Quantity: 1},
		"market sell":           {Side: domain.SideSell, Type: domain.OrderTypeMarket, Quantity: 1},
	} {
		t.Run(name, func(t *testing.T) {
			p.UserID, p.Symbol = "erin", "ACME"
			if _, err := f.svc.PlaceOrder(ctx, p); !errors.Is(err, domain.ErrInsufficientFunds) {
				t.Fatalf("got %v, want ErrInsufficientFunds", err)
			}
		})
	}

	// Nothing was persisted: no order, balance untouched.
	if orders, _ := f.svc.ListOrders(ctx, "erin", app.OrderFilter{}); len(orders) != 0 {
		t.Fatalf("rejected orders were stored: %d", len(orders))
	}
	if b := f.balance("erin", domain.QuoteAsset); b.Available != 500 || b.Reserved != 0 {
		t.Fatalf("balance changed: %+v", b)
	}
}

func TestCancelReleasesReservation(t *testing.T) {
	ctx := context.Background()
	f := newTrading(t)
	f.deposit("erin", domain.QuoteAsset, 1_000)

	res, err := f.svc.PlaceOrder(ctx, domain.NewOrderParams{UserID: "erin", Symbol: "ACME",
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Price: 100, Quantity: 5})
	if err != nil {
		t.Fatal(err)
	}
	if b := f.balance("erin", domain.QuoteAsset); b.Available != 500 || b.Reserved != 500 {
		t.Fatalf("after placing: %+v", b)
	}
	if _, err := f.acct.Withdraw(ctx, "erin", domain.QuoteAsset, 600); !errors.Is(err, domain.ErrInsufficientFunds) {
		t.Fatalf("withdrawing reserved funds: got %v", err)
	}
	if _, err := f.svc.CancelOrder(ctx, "erin", res.Order.ID); err != nil {
		t.Fatal(err)
	}
	if b := f.balance("erin", domain.QuoteAsset); b.Available != 1_000 || b.Reserved != 0 {
		t.Fatalf("after cancel: %+v", b)
	}
}

func TestMarketBuyLimitedByBudget(t *testing.T) {
	ctx := context.Background()
	f := newTrading(t)
	f.sell("alice", 100, 5)
	f.sell("carol", 110, 5)
	f.deposit("frank", domain.QuoteAsset, 750)

	res, err := f.svc.PlaceOrder(ctx, domain.NewOrderParams{UserID: "frank", Symbol: "ACME",
		Side: domain.SideBuy, Type: domain.OrderTypeMarket, Quantity: 10})
	if err != nil {
		t.Fatal(err)
	}
	// 5 @ 100 + 2 @ 110 = 720; a third unit at 110 would exceed 750.
	if res.Order.FilledQuantity != 7 || res.Order.Status != domain.StatusCancelled {
		t.Fatalf("filled %d status %s", res.Order.FilledQuantity, res.Order.Status)
	}
	if b := f.balance("frank", domain.QuoteAsset); b.Available != 30 || b.Reserved != 0 {
		t.Fatalf("unspent budget not released: %+v", b)
	}
	if b := f.balance("frank", "ACME"); b.Available != 7 {
		t.Fatalf("frank ACME: %+v", b)
	}

	// No cash at all: rejected up front.
	f.deposit("gina", "ACME", 1)
	_, err = f.svc.PlaceOrder(ctx, domain.NewOrderParams{UserID: "gina", Symbol: "ACME",
		Side: domain.SideBuy, Type: domain.OrderTypeMarket, Quantity: 1})
	if !errors.Is(err, domain.ErrInsufficientFunds) {
		t.Fatalf("got %v, want ErrInsufficientFunds", err)
	}
}

// The Postgres adapter retries deadlocked transactions; services must give
// the same result when their transaction body runs twice.
func TestPlaceOrderSurvivesTransactionRetry(t *testing.T) {
	f := newTrading(t)
	f.sell("alice", 100, 5)

	f.store.replayTx = true
	res := f.buy("bob", 100, 3)
	f.store.replayTx = false

	if len(res.Trades) != 1 || res.Order.FilledQuantity != 3 || res.Order.Status != domain.StatusFilled {
		t.Fatalf("unexpected result after replay: %+v trades=%d", res.Order, len(res.Trades))
	}
	if n := len(f.store.trades); n != 1 {
		t.Fatalf("got %d stored trades, want 1", n)
	}
}

func TestPlaceOrderValidation(t *testing.T) {
	f := newTrading(t)
	_, err := f.svc.PlaceOrder(context.Background(), domain.NewOrderParams{
		UserID: "bob", Symbol: "ACME", Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 1,
	})
	if !isValidation(err) {
		t.Fatalf("got %v, want validation error", err)
	}
}

func TestOrderBookAggregatesLevels(t *testing.T) {
	f := newTrading(t)
	f.buy("a", 99, 1)
	f.buy("b", 100, 2)
	f.buy("c", 100, 3)
	f.sell("d", 102, 4)
	f.sell("e", 101, 5)

	book, err := f.svc.OrderBook(context.Background(), "acme", 0)
	if err != nil {
		t.Fatal(err)
	}
	wantBids := []domain.PriceLevel{{Price: 100, Quantity: 5, Orders: 2}, {Price: 99, Quantity: 1, Orders: 1}}
	wantAsks := []domain.PriceLevel{{Price: 101, Quantity: 5, Orders: 1}, {Price: 102, Quantity: 4, Orders: 1}}
	if !equalLevels(book.Bids, wantBids) || !equalLevels(book.Asks, wantAsks) {
		t.Fatalf("got bids %+v asks %+v", book.Bids, book.Asks)
	}
}

func equalLevels(a, b []domain.PriceLevel) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
