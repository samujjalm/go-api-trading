// Package app is the application layer of the hexagon: use cases that
// orchestrate the domain, plus the ports (interfaces) through which the
// outside world talks to them.
//
//	driving adapters (HTTP)  ──▶  inbound ports  ──▶  services  ──▶  outbound ports  ◀──  driven adapters (Postgres, bcrypt, JWT)
//
// Dependencies only point inward: adapters import app and domain, never the
// other way round. That's what lets the tests swap Postgres for an in-memory
// store without touching any business logic.
package app

import (
	"context"
	"time"

	"github.com/samujjal/go-service/internal/domain"
)

// ---- Inbound (driving) ports: what the application offers. ----

type AuthUseCase interface {
	Register(ctx context.Context, email, password string) (*domain.User, error)
	Login(ctx context.Context, email, password string) (TokenPair, error)
	Refresh(ctx context.Context, refreshToken string) (TokenPair, error)
	Logout(ctx context.Context, refreshToken string) error
	Authenticate(ctx context.Context, accessToken string) (userID string, err error)
	GetUser(ctx context.Context, userID string) (*domain.User, error)
}

type TradingUseCase interface {
	PlaceOrder(ctx context.Context, p domain.NewOrderParams) (PlaceOrderResult, error)
	CancelOrder(ctx context.Context, userID, orderID string) (*domain.Order, error)
	GetOrder(ctx context.Context, userID, orderID string) (*domain.Order, error)
	ListOrders(ctx context.Context, userID string, f OrderFilter) ([]*domain.Order, error)
	OrderBook(ctx context.Context, symbol string, depth int) (domain.OrderBookSnapshot, error)
	RecentTrades(ctx context.Context, symbol string, limit int) ([]*domain.Trade, error)
}

type AccountUseCase interface {
	Balances(ctx context.Context, userID string) ([]*domain.Balance, error)
	Deposit(ctx context.Context, userID, asset string, amount int64) (*domain.Balance, error)
	Withdraw(ctx context.Context, userID, asset string, amount int64) (*domain.Balance, error)
	Movements(ctx context.Context, userID string, limit int) ([]domain.Movement, error)
}

type TokenPair struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    time.Duration
}

type PlaceOrderResult struct {
	Order  *domain.Order
	Trades []*domain.Trade
	// Replayed is true when the client_order_id had already been used and
	// the original order is returned instead of creating a new one.
	Replayed bool
}

type OrderFilter struct {
	Symbol string
	Status domain.OrderStatus
	Limit  int
	Offset int
}

// ---- Outbound (driven) ports: what the application needs. ----

// Store groups the repositories and gives use cases a way to run several
// operations atomically without knowing anything about SQL transactions.
type Store interface {
	Users() UserRepository
	RefreshTokens() RefreshTokenRepository
	Orders() OrderRepository
	Trades() TradeRepository
	Balances() BalanceRepository

	// WithTx runs fn in a transaction. The Store passed to fn is bound to
	// that transaction; if fn returns an error everything is rolled back.
	//
	// fn may be called more than once: implementations retry transient
	// failures such as deadlocks. fn must therefore start from fresh state on
	// every call and must not have side effects outside the transaction.
	WithTx(ctx context.Context, fn func(tx Store) error) error
}

type UserRepository interface {
	// Create assigns ID and CreatedAt. Returns domain.ErrConflict if the
	// email is taken.
	Create(ctx context.Context, u *domain.User) error
	GetByEmail(ctx context.Context, email string) (*domain.User, error)
	GetByID(ctx context.Context, id string) (*domain.User, error)
}

type RefreshTokenRepository interface {
	Create(ctx context.Context, userID string, tokenHash []byte, expiresAt time.Time) (id string, err error)
	// GetByHashForUpdate locks the row until the transaction ends so two
	// concurrent refreshes with the same token can't both succeed.
	GetByHashForUpdate(ctx context.Context, tokenHash []byte) (*domain.RefreshToken, error)
	MarkReplaced(ctx context.Context, id, replacedBy string, at time.Time) error
	RevokeByHash(ctx context.Context, tokenHash []byte, at time.Time) error
	RevokeAllForUser(ctx context.Context, userID string, at time.Time) error
}

type OrderRepository interface {
	// LockBook serialises all matching for one symbol until the current
	// transaction ends. Must be called inside WithTx.
	LockBook(ctx context.Context, symbol string) error
	// Create assigns ID. Returns domain.ErrConflict if the user already has
	// an order with the same ClientOrderID.
	Create(ctx context.Context, o *domain.Order) error
	Update(ctx context.Context, o *domain.Order) error
	GetByID(ctx context.Context, id string) (*domain.Order, error)
	GetByClientOrderID(ctx context.Context, userID, clientOrderID string) (*domain.Order, error)
	ListByUser(ctx context.Context, userID string, f OrderFilter) ([]*domain.Order, error)
	// MatchCandidates returns open orders on the opposite side that cross
	// the taker's price, best price first then oldest first.
	MatchCandidates(ctx context.Context, taker *domain.Order, limit int) ([]*domain.Order, error)
	BookLevels(ctx context.Context, symbol string, side domain.Side, depth int) ([]domain.PriceLevel, error)
}

type TradeRepository interface {
	Create(ctx context.Context, t *domain.Trade) error
	ListBySymbol(ctx context.Context, symbol string, limit int) ([]*domain.Trade, error)
}

type BalanceRepository interface {
	// GetForUpdate returns the balance, creating a zero balance if none
	// exists, and locks it until the transaction ends.
	GetForUpdate(ctx context.Context, userID, asset string) (*domain.Balance, error)
	// Save persists the totals and the balance's pending movements.
	Save(ctx context.Context, b *domain.Balance) error
	ListByUser(ctx context.Context, userID string) ([]*domain.Balance, error)
	ListMovements(ctx context.Context, userID string, limit int) ([]domain.Movement, error)
}

type PasswordHasher interface {
	Hash(password string) (string, error)
	Compare(hash, password string) bool
}

type AccessTokenIssuer interface {
	Issue(userID string) (string, error)
	Verify(token string) (userID string, err error)
	TTL() time.Duration
}
