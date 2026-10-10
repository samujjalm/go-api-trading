package domain

import (
	"errors"
	"testing"
	"time"
)

var now = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// limit returns an open limit order with its reservation already taken.
func limit(id, user string, side Side, price, qty int64) *Order {
	o := &Order{ID: id, UserID: user, Symbol: "ACME", Side: side, Type: OrderTypeLimit,
		Price: price, Quantity: qty, Status: StatusNew}
	o.Reserved = o.ReservationFor(0)
	return o
}

// market returns an open market order. budget is the cash reserved for a
// buy; sells reserve their quantity.
func market(id, user string, side Side, qty, budget int64) *Order {
	o := &Order{ID: id, UserID: user, Symbol: "ACME", Side: side, Type: OrderTypeMarket,
		Quantity: qty, Status: StatusNew}
	o.Reserved = o.ReservationFor(budget)
	return o
}

func TestMatch(t *testing.T) {
	type fill struct {
		maker string
		price int64
		qty   int64
	}
	tests := []struct {
		name          string
		taker         *Order
		makers        []*Order
		wantFills     []fill
		wantStatus    OrderStatus // taker status after Match (before FinishMatching)
		wantSelfTrade bool
	}{
		{
			name:       "no makers",
			taker:      limit("t", "bob", SideBuy, 100, 10),
			wantStatus: StatusNew,
		},
		{
			name:       "full fill at maker price",
			taker:      limit("t", "bob", SideBuy, 105, 10),
			makers:     []*Order{limit("m1", "alice", SideSell, 100, 10)},
			wantFills:  []fill{{"m1", 100, 10}},
			wantStatus: StatusFilled,
		},
		{
			name:  "walks the book across levels",
			taker: limit("t", "bob", SideBuy, 102, 12),
			makers: []*Order{
				limit("m1", "alice", SideSell, 100, 5),
				limit("m2", "carol", SideSell, 101, 5),
				limit("m3", "dave", SideSell, 102, 5),
			},
			wantFills:  []fill{{"m1", 100, 5}, {"m2", 101, 5}, {"m3", 102, 2}},
			wantStatus: StatusFilled,
		},
		{
			name:  "stops at a maker that doesn't cross",
			taker: limit("t", "bob", SideBuy, 100, 10),
			makers: []*Order{
				limit("m1", "alice", SideSell, 100, 4),
				limit("m2", "carol", SideSell, 101, 5),
			},
			wantFills:  []fill{{"m1", 100, 4}},
			wantStatus: StatusPartiallyFilled,
		},
		{
			name:  "sell taker hits bids",
			taker: limit("t", "bob", SideSell, 99, 6),
			makers: []*Order{
				limit("m1", "alice", SideBuy, 101, 3),
				limit("m2", "carol", SideBuy, 100, 3),
			},
			wantFills:  []fill{{"m1", 101, 3}, {"m2", 100, 3}},
			wantStatus: StatusFilled,
		},
		{
			name:       "market order takes any price",
			taker:      market("t", "bob", SideBuy, 3, 10_000_000),
			makers:     []*Order{limit("m1", "alice", SideSell, 999_999, 5)},
			wantFills:  []fill{{"m1", 999_999, 3}},
			wantStatus: StatusFilled,
		},
		{
			name:  "market buy stops when budget runs out",
			taker: market("t", "bob", SideBuy, 10, 750), // 5@100 = 500, then 250 buys 2@110
			makers: []*Order{
				limit("m1", "alice", SideSell, 100, 5),
				limit("m2", "carol", SideSell, 110, 5),
			},
			wantFills:  []fill{{"m1", 100, 5}, {"m2", 110, 2}},
			wantStatus: StatusPartiallyFilled,
		},
		{
			name:  "self-trade prevention stops matching",
			taker: limit("t", "bob", SideBuy, 100, 10),
			makers: []*Order{
				limit("m1", "alice", SideSell, 100, 2),
				limit("m2", "bob", SideSell, 100, 5),
				limit("m3", "carol", SideSell, 100, 5),
			},
			wantFills:     []fill{{"m1", 100, 2}},
			wantStatus:    StatusPartiallyFilled,
			wantSelfTrade: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Match(tt.taker, tt.makers, now)

			if len(r.Trades) != len(tt.wantFills) {
				t.Fatalf("got %d trades, want %d", len(r.Trades), len(tt.wantFills))
			}
			var filled int64
			for i, want := range tt.wantFills {
				got := r.Trades[i]
				if got.MakerOrderID != want.maker || got.Price != want.price || got.Quantity != want.qty {
					t.Errorf("trade %d = {maker %s, %d @ %d}, want {maker %s, %d @ %d}",
						i, got.MakerOrderID, got.Quantity, got.Price, want.maker, want.qty, want.price)
				}
				filled += got.Quantity
			}
			if tt.taker.FilledQuantity != filled {
				t.Errorf("taker filled %d, want %d", tt.taker.FilledQuantity, filled)
			}
			if tt.taker.Status != tt.wantStatus {
				t.Errorf("taker status %s, want %s", tt.taker.Status, tt.wantStatus)
			}
			if r.SelfTradePrevented != tt.wantSelfTrade {
				t.Errorf("SelfTradePrevented = %v, want %v", r.SelfTradePrevented, tt.wantSelfTrade)
			}
			if len(r.Makers) != len(r.Trades) {
				t.Errorf("got %d touched makers for %d trades", len(r.Makers), len(r.Trades))
			}
		})
	}
}

func TestMatchTradeSides(t *testing.T) {
	taker := limit("t", "bob", SideSell, 100, 1)
	r := Match(taker, []*Order{limit("m", "alice", SideBuy, 100, 1)}, now)
	tr := r.Trades[0]
	if tr.BuyOrderID != "m" || tr.SellOrderID != "t" || tr.TakerSide != SideSell {
		t.Fatalf("unexpected trade sides: %+v", tr)
	}
}

func TestMatchPanicsOnInvariantViolation(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	Match(limit("t", "bob", SideBuy, 100, 1), []*Order{limit("m", "alice", SideBuy, 100, 1)}, now)
}

func TestFinishMatching(t *testing.T) {
	tests := []struct {
		name      string
		order     *Order
		selfTrade bool
		want      OrderStatus
	}{
		{"limit rests", limit("o", "u", SideBuy, 100, 5), false, StatusNew},
		{"market remainder cancelled", market("o", "u", SideBuy, 5, 1000), false, StatusCancelled},
		{"self-trade remainder cancelled", limit("o", "u", SideBuy, 100, 5), true, StatusCancelled},
		{"filled stays filled", &Order{Type: OrderTypeMarket, Quantity: 1, FilledQuantity: 1, Status: StatusFilled}, false, StatusFilled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.order.FinishMatching(tt.selfTrade, now)
			if tt.order.Status != tt.want {
				t.Fatalf("status = %s, want %s", tt.order.Status, tt.want)
			}
		})
	}
}

func TestNewOrderValidation(t *testing.T) {
	valid := NewOrderParams{UserID: "u", Symbol: "acme", Side: SideBuy, Type: OrderTypeLimit, Price: 100, Quantity: 1}

	o, err := NewOrder(valid, now)
	if err != nil {
		t.Fatalf("valid order rejected: %v", err)
	}
	if o.Symbol != "ACME" || o.Status != StatusNew {
		t.Fatalf("unexpected order: %+v", o)
	}

	tests := map[string]struct {
		mutate func(*NewOrderParams)
		field  string
	}{
		"bad symbol":        {func(p *NewOrderParams) { p.Symbol = "no spaces" }, "symbol"},
		"bad side":          {func(p *NewOrderParams) { p.Side = "HOLD" }, "side"},
		"bad type":          {func(p *NewOrderParams) { p.Type = "STOP" }, "type"},
		"zero price":        {func(p *NewOrderParams) { p.Price = 0 }, "price"},
		"price too high":    {func(p *NewOrderParams) { p.Price = MaxPrice + 1 }, "price"},
		"market with price": {func(p *NewOrderParams) { p.Type = OrderTypeMarket }, "price"},
		"zero quantity":     {func(p *NewOrderParams) { p.Quantity = 0 }, "quantity"},
		"negative quantity": {func(p *NewOrderParams) { p.Quantity = -5 }, "quantity"},
		"long client id":    {func(p *NewOrderParams) { p.ClientOrderID = string(make([]byte, 65)) }, "client_order_id"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			p := valid
			tt.mutate(&p)
			_, err := NewOrder(p, now)
			var ve *ValidationError
			if !errors.As(err, &ve) || ve.Field != tt.field {
				t.Fatalf("got %v, want validation error on %q", err, tt.field)
			}
		})
	}
}

func TestCancel(t *testing.T) {
	o := limit("o", "u", SideBuy, 100, 5)
	if err := o.Cancel(now); err != nil {
		t.Fatal(err)
	}
	if err := o.Cancel(now); !errors.Is(err, ErrOrderNotOpen) {
		t.Fatalf("second cancel: got %v, want ErrOrderNotOpen", err)
	}
}

func TestMatchPanicsOnUnderReservedMaker(t *testing.T) {
	maker := limit("m", "alice", SideSell, 100, 5)
	maker.Reserved = 2
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	Match(limit("t", "bob", SideBuy, 100, 5), []*Order{maker}, now)
}

func TestReservationTracksFills(t *testing.T) {
	// Limit buy 10 @ 105 reserves 1050. Filling 4 @ 100 consumes 400 and
	// releases 4×5 = 20 of price improvement: 630 = 105 × 6 remains.
	taker := limit("t", "bob", SideBuy, 105, 10)
	Match(taker, []*Order{limit("m", "alice", SideSell, 100, 4)}, now)
	if taker.Reserved != 105*6 {
		t.Fatalf("taker reserved %d, want %d", taker.Reserved, 105*6)
	}

	// A closed order hands back whatever is left.
	mkt := market("t2", "bob", SideBuy, 2, 1000)
	Match(mkt, []*Order{limit("m2", "alice", SideSell, 100, 5)}, now)
	mkt.FinishMatching(false, now)
	if got := mkt.DrainReserve(); got != 800 || mkt.Reserved != 0 {
		t.Fatalf("drained %d (left %d), want 800 (left 0)", got, mkt.Reserved)
	}
}
