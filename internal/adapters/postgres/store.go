package postgres

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/samujjal/go-service/internal/app"
)

// querier is satisfied by both *pgxpool.Pool and pgx.Tx, so every repository
// works identically inside or outside a transaction.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
}

type Store struct {
	pool *pgxpool.Pool
	q    querier
	inTx bool
}

var _ app.Store = (*Store)(nil)

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, q: pool}
}

func (s *Store) Users() app.UserRepository                 { return userRepo{s.q} }
func (s *Store) RefreshTokens() app.RefreshTokenRepository { return refreshTokenRepo{s.q} }
func (s *Store) Orders() app.OrderRepository               { return orderRepo{s.q} }
func (s *Store) Trades() app.TradeRepository               { return tradeRepo{s.q} }
func (s *Store) Balances() app.BalanceRepository           { return balanceRepo{s.q} }

const maxTxAttempts = 5

// WithTx runs fn in a READ COMMITTED transaction. Correctness of matching
// comes from the per-symbol advisory lock (see orderRepo.LockBook) plus row
// locks on balances, not from a stricter isolation level.
//
// Row locks taken in different orders by concurrent transactions can
// deadlock; Postgres detects that and aborts one of them. That is expected
// under load, so such failures are retried with jittered backoff. Nested
// calls reuse the outer transaction (and never retry on their own).
func (s *Store) WithTx(ctx context.Context, fn func(app.Store) error) error {
	if s.inTx {
		return fn(s)
	}
	for attempt := 1; ; attempt++ {
		err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			return fn(&Store{pool: s.pool, q: tx, inTx: true})
		})
		if err == nil || !isRetryable(err) || attempt == maxTxAttempts {
			return err
		}
		backoff := time.Duration(attempt)*5*time.Millisecond + time.Duration(rand.Int64N(int64(5*time.Millisecond)))
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func isRetryable(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	switch pgErr.Code {
	case "40P01", // deadlock_detected
		"40001": // serialization_failure
		return true
	}
	return false
}
