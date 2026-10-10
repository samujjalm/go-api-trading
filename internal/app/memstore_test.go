package app_test

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/samujjal/go-service/internal/app"
	"github.com/samujjal/go-service/internal/domain"
)

// memStore is an in-memory implementation of app.Store used to unit-test the
// services. It mirrors the semantics of the Postgres adapter (copies on
// read/write, rollback on error) closely enough for business-logic tests;
// the real adapter is covered by the integration tests.
type memStore struct {
	mu     sync.Mutex
	nextID int
	users  map[string]domain.User
	tokens map[string]memToken
	orders []domain.Order // insertion order == time priority
	trades []domain.Trade

	balances  map[[2]string]domain.Balance // key: user, asset
	movements []domain.Movement

	// replayTx makes WithTx run every transaction twice, rolling back the
	// first attempt, to prove services survive the retries the Postgres
	// adapter performs on deadlocks.
	replayTx bool
}

type memToken struct {
	domain.RefreshToken
	hash []byte
}

func newMemStore() *memStore {
	return &memStore{users: map[string]domain.User{}, tokens: map[string]memToken{},
		balances: map[[2]string]domain.Balance{}}
}

func (s *memStore) id(prefix string) string {
	s.nextID++
	return fmt.Sprintf("%s-%d", prefix, s.nextID)
}

func (s *memStore) Users() app.UserRepository                 { return memUsers{s} }
func (s *memStore) RefreshTokens() app.RefreshTokenRepository { return memTokens{s} }
func (s *memStore) Orders() app.OrderRepository               { return memOrders{s} }
func (s *memStore) Trades() app.TradeRepository               { return memTrades{s} }
func (s *memStore) Balances() app.BalanceRepository           { return memBalances{s} }

func (s *memStore) WithTx(_ context.Context, fn func(app.Store) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	users, tokens, balances := maps.Clone(s.users), maps.Clone(s.tokens), maps.Clone(s.balances)
	orders, trades, movements := slices.Clone(s.orders), slices.Clone(s.trades), slices.Clone(s.movements)
	nextID := s.nextID
	rollback := func() {
		s.users, s.tokens, s.balances = users, tokens, balances
		s.orders, s.trades, s.movements, s.nextID = orders, trades, movements, nextID
	}
	if s.replayTx {
		fn(s)
		rollback()
	}
	if err := fn(s); err != nil {
		rollback()
		return err
	}
	return nil
}

// ---- users ----

type memUsers struct{ s *memStore }

func (r memUsers) Create(_ context.Context, u *domain.User) error {
	for _, existing := range r.s.users {
		if existing.Email == u.Email {
			return domain.ErrConflict
		}
	}
	u.ID, u.CreatedAt = r.s.id("user"), time.Now()
	r.s.users[u.ID] = *u
	return nil
}

func (r memUsers) GetByEmail(_ context.Context, email string) (*domain.User, error) {
	for _, u := range r.s.users {
		if u.Email == email {
			return &u, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (r memUsers) GetByID(_ context.Context, id string) (*domain.User, error) {
	u, ok := r.s.users[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return &u, nil
}

// ---- refresh tokens ----

type memTokens struct{ s *memStore }

func (r memTokens) Create(_ context.Context, userID string, hash []byte, expiresAt time.Time) (string, error) {
	id := r.s.id("rt")
	r.s.tokens[id] = memToken{RefreshToken: domain.RefreshToken{ID: id, UserID: userID, ExpiresAt: expiresAt}, hash: hash}
	return id, nil
}

func (r memTokens) GetByHashForUpdate(_ context.Context, hash []byte) (*domain.RefreshToken, error) {
	for _, t := range r.s.tokens {
		if bytes.Equal(t.hash, hash) {
			rt := t.RefreshToken
			return &rt, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (r memTokens) MarkReplaced(_ context.Context, id, replacedBy string, at time.Time) error {
	t := r.s.tokens[id]
	t.RevokedAt, t.ReplacedBy = &at, &replacedBy
	r.s.tokens[id] = t
	return nil
}

func (r memTokens) RevokeByHash(_ context.Context, hash []byte, at time.Time) error {
	for id, t := range r.s.tokens {
		if bytes.Equal(t.hash, hash) && t.RevokedAt == nil {
			t.RevokedAt = &at
			r.s.tokens[id] = t
		}
	}
	return nil
}

func (r memTokens) RevokeAllForUser(_ context.Context, userID string, at time.Time) error {
	for id, t := range r.s.tokens {
		if t.UserID == userID && t.RevokedAt == nil {
			t.RevokedAt = &at
			r.s.tokens[id] = t
		}
	}
	return nil
}

// ---- orders ----

type memOrders struct{ s *memStore }

func (r memOrders) LockBook(context.Context, string) error { return nil } // WithTx already serialises

func (r memOrders) Create(_ context.Context, o *domain.Order) error {
	if o.ClientOrderID != "" {
		if _, err := r.GetByClientOrderID(context.Background(), o.UserID, o.ClientOrderID); err == nil {
			return domain.ErrConflict
		}
	}
	o.ID = r.s.id("order")
	r.s.orders = append(r.s.orders, *o)
	return nil
}

func (r memOrders) Update(_ context.Context, o *domain.Order) error {
	for i := range r.s.orders {
		if r.s.orders[i].ID == o.ID {
			r.s.orders[i] = *o
			return nil
		}
	}
	return domain.ErrNotFound
}

func (r memOrders) GetByID(_ context.Context, id string) (*domain.Order, error) {
	for _, o := range r.s.orders {
		if o.ID == id {
			return &o, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (r memOrders) GetByClientOrderID(_ context.Context, userID, clientOrderID string) (*domain.Order, error) {
	for _, o := range r.s.orders {
		if o.UserID == userID && o.ClientOrderID == clientOrderID {
			return &o, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (r memOrders) ListByUser(_ context.Context, userID string, f app.OrderFilter) ([]*domain.Order, error) {
	var out []*domain.Order
	for i := len(r.s.orders) - 1; i >= 0; i-- {
		o := r.s.orders[i]
		if o.UserID == userID && (f.Symbol == "" || o.Symbol == f.Symbol) && (f.Status == "" || o.Status == f.Status) {
			out = append(out, &o)
		}
	}
	if f.Offset >= len(out) {
		return nil, nil
	}
	out = out[f.Offset:]
	return out[:min(len(out), f.Limit)], nil
}

func (r memOrders) open(symbol string, side domain.Side) []*domain.Order {
	var out []*domain.Order
	for _, o := range r.s.orders {
		if o.Symbol == symbol && o.Side == side && o.IsOpen() {
			out = append(out, &o)
		}
	}
	// Stable sort keeps insertion (time) order within a price level.
	sort.SliceStable(out, func(i, j int) bool {
		if side == domain.SideBuy {
			return out[i].Price > out[j].Price
		}
		return out[i].Price < out[j].Price
	})
	return out
}

func (r memOrders) MatchCandidates(_ context.Context, taker *domain.Order, limit int) ([]*domain.Order, error) {
	opposite := domain.SideSell
	if taker.Side == domain.SideSell {
		opposite = domain.SideBuy
	}
	var out []*domain.Order
	for _, o := range r.open(taker.Symbol, opposite) {
		if taker.Crosses(o) && len(out) < limit {
			out = append(out, o)
		}
	}
	return out, nil
}

func (r memOrders) BookLevels(_ context.Context, symbol string, side domain.Side, depth int) ([]domain.PriceLevel, error) {
	var levels []domain.PriceLevel
	for _, o := range r.open(symbol, side) {
		if n := len(levels); n > 0 && levels[n-1].Price == o.Price {
			levels[n-1].Quantity += o.Remaining()
			levels[n-1].Orders++
			continue
		}
		if len(levels) == depth {
			break
		}
		levels = append(levels, domain.PriceLevel{Price: o.Price, Quantity: o.Remaining(), Orders: 1})
	}
	return levels, nil
}

// ---- trades ----

type memTrades struct{ s *memStore }

func (r memTrades) Create(_ context.Context, t *domain.Trade) error {
	t.ID = r.s.id("trade")
	r.s.trades = append(r.s.trades, *t)
	return nil
}

func (r memTrades) ListBySymbol(_ context.Context, symbol string, limit int) ([]*domain.Trade, error) {
	var out []*domain.Trade
	for i := len(r.s.trades) - 1; i >= 0 && len(out) < limit; i-- {
		if t := r.s.trades[i]; t.Symbol == symbol {
			out = append(out, &t)
		}
	}
	return out, nil
}

// ---- balances ----

type memBalances struct{ s *memStore }

func (r memBalances) GetForUpdate(_ context.Context, userID, asset string) (*domain.Balance, error) {
	b, ok := r.s.balances[[2]string{userID, asset}]
	if !ok {
		b = domain.Balance{UserID: userID, Asset: asset}
	}
	return &b, nil
}

func (r memBalances) Save(_ context.Context, b *domain.Balance) error {
	for _, m := range b.PendingMovements() {
		m.ID = r.s.id("mv")
		r.s.movements = append(r.s.movements, m)
	}
	b.MarkPersisted()
	r.s.balances[[2]string{b.UserID, b.Asset}] = *b
	return nil
}

func (r memBalances) ListByUser(_ context.Context, userID string) ([]*domain.Balance, error) {
	var out []*domain.Balance
	for k, b := range r.s.balances {
		if k[0] == userID {
			out = append(out, &b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Asset < out[j].Asset })
	return out, nil
}

func (r memBalances) ListMovements(_ context.Context, userID string, limit int) ([]domain.Movement, error) {
	var out []domain.Movement
	for i := len(r.s.movements) - 1; i >= 0 && len(out) < limit; i-- {
		if m := r.s.movements[i]; m.UserID == userID {
			out = append(out, m)
		}
	}
	return out, nil
}

// ---- fakes for the security ports ----

type fakeHasher struct{}

func (fakeHasher) Hash(pw string) (string, error) { return "hashed:" + pw, nil }
func (fakeHasher) Compare(hash, pw string) bool   { return hash == "hashed:"+pw }

type fakeTokens struct{}

func (fakeTokens) Issue(userID string) (string, error) { return "access:" + userID, nil }
func (fakeTokens) TTL() time.Duration                  { return 15 * time.Minute }
func (fakeTokens) Verify(tok string) (string, error) {
	if id, ok := strings.CutPrefix(tok, "access:"); ok && id != "" {
		return id, nil
	}
	return "", domain.ErrInvalidToken
}
