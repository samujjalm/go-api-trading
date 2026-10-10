package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/samujjal/go-service/internal/domain"
)

const (
	matchBatchSize   = 100
	defaultListLimit = 50
	maxListLimit     = 200
	defaultBookDepth = 10
	maxBookDepth     = 100
)

type TradingService struct {
	store Store
	now   func() time.Time
}

var _ TradingUseCase = (*TradingService)(nil)

func NewTradingService(store Store, opts ...Option) *TradingService {
	o := buildOptions(opts)
	return &TradingService{store: store, now: o.now}
}

// PlaceOrder validates the order, reserves funds, matches it against the
// book and settles every trade, all in one transaction: either the order,
// its trades and every balance change are stored, or nothing is.
//
// Concurrency: LockBook serialises matching per symbol, so two orders for
// the same symbol never match against the same resting liquidity at once,
// while different symbols proceed in parallel. Balance rows are locked as
// they're touched; orders on different symbols can lock two users'
// balances in opposite orders and deadlock, in which case Postgres aborts
// one transaction and WithTx retries it.
func (s *TradingService) PlaceOrder(ctx context.Context, p domain.NewOrderParams) (PlaceOrderResult, error) {
	validated, err := domain.NewOrder(p, s.now())
	if err != nil {
		return PlaceOrderResult{}, err
	}

	var res PlaceOrderResult
	err = s.store.WithTx(ctx, func(tx Store) error {
		order := *validated // fresh copy per attempt, since WithTx may retry
		if err := tx.Orders().LockBook(ctx, order.Symbol); err != nil {
			return err
		}

		// Idempotency: clients retry on timeouts, and a retried order must
		// not be placed twice. Checked under the lock so concurrent retries
		// of the same order are serialised too.
		if order.ClientOrderID != "" {
			existing, err := tx.Orders().GetByClientOrderID(ctx, order.UserID, order.ClientOrderID)
			if err == nil {
				res = PlaceOrderResult{Order: existing, Replayed: true}
				return nil
			}
			if !errors.Is(err, domain.ErrNotFound) {
				return err
			}
		}

		balances := newBalanceSet(tx.Balances())
		funds, err := balances.get(ctx, order.UserID, order.ReserveAsset())
		if err != nil {
			return err
		}
		order.Reserved = order.ReservationFor(funds.Available)

		if err := tx.Orders().Create(ctx, &order); err != nil {
			if errors.Is(err, domain.ErrConflict) {
				return fmt.Errorf("%w: client_order_id already used", domain.ErrConflict)
			}
			return err
		}
		// Fails with ErrInsufficientFunds, which rolls back the insert too.
		if err := funds.Reserve(order.Reserved, order.ID); err != nil {
			return err
		}

		trades, err := s.match(ctx, tx, balances, &order)
		if err != nil {
			return err
		}
		if err := balances.saveAll(ctx); err != nil {
			return err
		}
		res = PlaceOrderResult{Order: &order, Trades: trades}
		return nil
	})
	if err != nil {
		return PlaceOrderResult{}, err
	}
	return res, nil
}

func (s *TradingService) match(ctx context.Context, tx Store, balances *balanceSet, taker *domain.Order) ([]*domain.Trade, error) {
	var (
		trades    []*domain.Trade
		selfTrade bool
	)
	for taker.Remaining() > 0 {
		makers, err := tx.Orders().MatchCandidates(ctx, taker, matchBatchSize)
		if err != nil {
			return nil, err
		}
		if len(makers) == 0 {
			break
		}

		r := domain.Match(taker, makers, s.now())
		for i, t := range r.Trades {
			maker := r.Makers[i]
			if err := tx.Orders().Update(ctx, maker); err != nil {
				return nil, err
			}
			if err := tx.Trades().Create(ctx, t); err != nil {
				return nil, err
			}
			buy, sell := taker, maker
			if taker.Side == domain.SideSell {
				buy, sell = maker, taker
			}
			parties, err := balances.tradeParties(ctx, t.Symbol, buy, sell)
			if err != nil {
				return nil, err
			}
			domain.Settle(t, buy, sell, parties)
		}
		trades = append(trades, r.Trades...)
		if r.Halted {
			selfTrade = r.SelfTradePrevented
			break
		}
	}

	taker.FinishMatching(selfTrade, s.now())
	if err := releaseIfClosed(ctx, balances, taker); err != nil {
		return nil, err
	}
	if err := tx.Orders().Update(ctx, taker); err != nil {
		return nil, err
	}
	return trades, nil
}

// releaseIfClosed returns a closed order's leftover reservation (a market
// buy's unspent budget, a cancelled remainder) to the user's available balance.
func releaseIfClosed(ctx context.Context, balances *balanceSet, o *domain.Order) error {
	amount := o.DrainReserve()
	if amount == 0 {
		return nil
	}
	b, err := balances.get(ctx, o.UserID, o.ReserveAsset())
	if err != nil {
		return err
	}
	b.Release(amount, o.ID, "")
	return nil
}

func (s *TradingService) CancelOrder(ctx context.Context, userID, orderID string) (*domain.Order, error) {
	var order *domain.Order
	err := s.store.WithTx(ctx, func(tx Store) error {
		o, err := s.ownedOrder(ctx, tx, userID, orderID)
		if err != nil {
			return err
		}
		if err := tx.Orders().LockBook(ctx, o.Symbol); err != nil {
			return err
		}
		// Re-read under the lock: it may have been filled since the first read.
		if o, err = tx.Orders().GetByID(ctx, orderID); err != nil {
			return err
		}
		if err := o.Cancel(s.now()); err != nil {
			return err
		}
		balances := newBalanceSet(tx.Balances())
		if err := releaseIfClosed(ctx, balances, o); err != nil {
			return err
		}
		if err := tx.Orders().Update(ctx, o); err != nil {
			return err
		}
		order = o
		return balances.saveAll(ctx)
	})
	return order, err
}

func (s *TradingService) GetOrder(ctx context.Context, userID, orderID string) (*domain.Order, error) {
	return s.ownedOrder(ctx, s.store, userID, orderID)
}

// ownedOrder returns ErrNotFound for other users' orders rather than a
// "forbidden" error, so callers can't probe which order IDs exist.
func (s *TradingService) ownedOrder(ctx context.Context, st Store, userID, orderID string) (*domain.Order, error) {
	o, err := st.Orders().GetByID(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if o.UserID != userID {
		return nil, domain.ErrNotFound
	}
	return o, nil
}

func (s *TradingService) ListOrders(ctx context.Context, userID string, f OrderFilter) ([]*domain.Order, error) {
	if f.Symbol != "" {
		sym, err := domain.ValidateSymbol(f.Symbol)
		if err != nil {
			return nil, err
		}
		f.Symbol = sym
	}
	if f.Status != "" && !f.Status.Valid() {
		return nil, &domain.ValidationError{Field: "status", Message: "unknown status"}
	}
	f.Limit = clamp(f.Limit, defaultListLimit, maxListLimit)
	f.Offset = max(f.Offset, 0)
	return s.store.Orders().ListByUser(ctx, userID, f)
}

// OrderBook returns aggregated price levels. Bids and asks are read in two
// queries without locking the book, so under heavy load the snapshot may be
// a few microseconds apart. That's acceptable for market data; matching
// itself never relies on it.
func (s *TradingService) OrderBook(ctx context.Context, symbol string, depth int) (domain.OrderBookSnapshot, error) {
	symbol, err := domain.ValidateSymbol(symbol)
	if err != nil {
		return domain.OrderBookSnapshot{}, err
	}
	depth = clamp(depth, defaultBookDepth, maxBookDepth)

	bids, err := s.store.Orders().BookLevels(ctx, symbol, domain.SideBuy, depth)
	if err != nil {
		return domain.OrderBookSnapshot{}, err
	}
	asks, err := s.store.Orders().BookLevels(ctx, symbol, domain.SideSell, depth)
	if err != nil {
		return domain.OrderBookSnapshot{}, err
	}
	return domain.OrderBookSnapshot{Symbol: symbol, Bids: bids, Asks: asks}, nil
}

func (s *TradingService) RecentTrades(ctx context.Context, symbol string, limit int) ([]*domain.Trade, error) {
	symbol, err := domain.ValidateSymbol(symbol)
	if err != nil {
		return nil, err
	}
	return s.store.Trades().ListBySymbol(ctx, symbol, clamp(limit, defaultListLimit, maxListLimit))
}

// clamp returns def for non-positive v and caps v at hi.
func clamp(v, def, hi int) int {
	if v <= 0 {
		return def
	}
	return min(v, hi)
}
