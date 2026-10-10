//go:build integration

package postgres_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	mrand "math/rand/v2"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/samujjal/go-service/internal/adapters/postgres"
	"github.com/samujjal/go-service/internal/adapters/security"
	"github.com/samujjal/go-service/internal/app"
	"github.com/samujjal/go-service/internal/domain"
)

var pool *pgxpool.Pool

func TestMain(m *testing.M) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		println("TEST_DATABASE_URL not set; skipping integration tests")
		os.Exit(0)
	}
	ctx := context.Background()
	var err error
	if pool, err = postgres.Connect(ctx, url); err != nil {
		panic(err)
	}
	if err := postgres.Migrate(ctx, pool); err != nil {
		panic(err)
	}
	code := m.Run()
	pool.Close()
	os.Exit(code)
}

// uniq keeps tests independent without truncating tables between runs.
func uniq(prefix string) string {
	b := make([]byte, 4)
	rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func createUser(t *testing.T, store app.Store) string {
	t.Helper()
	u := &domain.User{Email: uniq("u") + "@example.com", PasswordHash: "x"}
	if err := store.Users().Create(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	return u.ID
}

func TestAuthFlowAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	store := postgres.NewStore(pool)
	svc := app.NewAuthService(store, security.NewBcryptHasher(bcrypt.MinCost),
		security.NewJWTIssuer([]byte("0123456789abcdef0123456789abcdef"), "test", time.Minute), time.Hour)

	email := uniq("auth") + "@example.com"
	if _, err := svc.Register(ctx, email, "password1"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Register(ctx, email, "password1"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate register: %v", err)
	}
	first, err := svc.Login(ctx, email, "password1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Refresh(ctx, first.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Refresh(ctx, first.RefreshToken); !errors.Is(err, domain.ErrRefreshTokenReused) {
		t.Fatalf("reuse: %v", err)
	}
	if _, err := svc.Refresh(ctx, second.RefreshToken); !errors.Is(err, domain.ErrInvalidToken) {
		t.Fatalf("after reuse: %v", err)
	}
}

func TestMalformedIDIsNotFound(t *testing.T) {
	_, err := postgres.NewStore(pool).Orders().GetByID(context.Background(), "not-a-uuid")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

// TestConcurrentMatchingInvariants hammers two symbols from many goroutines
// with the same set of users (so transactions on different books contend for
// the same balance rows and can deadlock), then checks in SQL the properties
// that must hold for any correct exchange. Run with -race to also catch data
// races in the Go code.
func TestConcurrentMatchingInvariants(t *testing.T) {
	ctx := context.Background()
	store := postgres.NewStore(pool)
	svc := app.NewTradingService(store)
	accounts := app.NewAccountService(store)
	symbols := []string{strings.ToUpper("T" + uniq("")[:6]), strings.ToUpper("T" + uniq("")[:6])}

	const (
		cashPerUser   = 10_000 // cents: enough for some orders, not all, so rejections happen too
		sharesPerUser = 60
	)
	users := make([]string, 6)
	for i := range users {
		users[i] = createUser(t, store)
		mustDeposit(t, accounts, users[i], domain.QuoteAsset, cashPerUser)
		for _, sym := range symbols {
			mustDeposit(t, accounts, users[i], sym, sharesPerUser)
		}
	}
	deadlocksBefore := deadlockCount(t)

	const workers, ordersPerWorker = 8, 50
	var wg sync.WaitGroup
	var placed, rejected atomic.Int64
	errs := make(chan error, workers)
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rng := mrand.New(mrand.NewPCG(uint64(w), 42))
			for range ordersPerWorker {
				p := domain.NewOrderParams{
					UserID:   users[rng.IntN(len(users))],
					Symbol:   symbols[rng.IntN(len(symbols))],
					Side:     []domain.Side{domain.SideBuy, domain.SideSell}[rng.IntN(2)],
					Type:     domain.OrderTypeLimit,
					Price:    int64(95 + rng.IntN(11)),
					Quantity: int64(1 + rng.IntN(20)),
				}
				if rng.IntN(10) == 0 {
					p.Type, p.Price = domain.OrderTypeMarket, 0
				}
				_, err := svc.PlaceOrder(ctx, p)
				switch {
				case err == nil:
					placed.Add(1)
				case errors.Is(err, domain.ErrInsufficientFunds):
					rejected.Add(1)
				default:
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("PlaceOrder: %v", err)
	}

	// Each query counts violations of one invariant; all must be zero.
	invariants := []struct {
		name, sql string
		args      []any
	}{
		{"order fill equals sum of its trades", `
			SELECT count(*) FROM orders o WHERE o.symbol = ANY($1) AND o.filled_quantity <> COALESCE(
			  (SELECT sum(quantity) FROM trades t WHERE t.buy_order_id = o.id OR t.sell_order_id = o.id), 0)`, []any{symbols}},
		{"trade prints at maker price", `
			SELECT count(*) FROM trades t JOIN orders m ON m.id = t.maker_order_id
			 WHERE t.symbol = ANY($1) AND t.price <> m.price`, []any{symbols}},
		{"open order reservation covers exactly its remainder", `
			SELECT count(*) FROM orders WHERE symbol = ANY($1) AND status IN ('NEW', 'PARTIALLY_FILLED')
			   AND reserved <> CASE side WHEN 'BUY' THEN price * (quantity - filled_quantity)
			                             ELSE quantity - filled_quantity END`, []any{symbols}},
		{"balance reserved equals open orders' reservations", `
			SELECT count(*) FROM balances b WHERE b.user_id = ANY($1::uuid[]) AND b.reserved <> COALESCE(
			  (SELECT sum(o.reserved) FROM orders o
			    WHERE o.user_id = b.user_id AND o.status IN ('NEW', 'PARTIALLY_FILLED')
			      AND CASE o.side WHEN 'BUY' THEN 'USD' ELSE o.symbol END = b.asset), 0)`, []any{users}},
		{"balance equals sum of its journal", `
			SELECT count(*) FROM balances b WHERE b.user_id = ANY($1::uuid[]) AND (b.available, b.reserved) <>
			  (SELECT COALESCE(sum(delta_available), 0), COALESCE(sum(delta_reserved), 0)
			     FROM balance_movements m WHERE m.user_id = b.user_id AND m.asset = b.asset)`, []any{users}},
	}
	for _, inv := range invariants {
		var violations int
		if err := pool.QueryRow(ctx, inv.sql, inv.args...).Scan(&violations); err != nil {
			t.Fatalf("%s: %v", inv.name, err)
		}
		if violations > 0 {
			t.Errorf("invariant %q violated %d times", inv.name, violations)
		}
	}

	// Conservation: trading moves assets between users but never creates or
	// destroys them.
	wantTotal := map[string]int64{domain.QuoteAsset: cashPerUser * int64(len(users))}
	for _, sym := range symbols {
		wantTotal[sym] = sharesPerUser * int64(len(users))
	}
	for asset, want := range wantTotal {
		var got int64
		if err := pool.QueryRow(ctx,
			`SELECT COALESCE(sum(available + reserved), 0) FROM balances WHERE user_id = ANY($1::uuid[]) AND asset = $2`,
			users, asset).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s not conserved: total %d, want %d", asset, got, want)
		}
	}

	var trades int
	pool.QueryRow(ctx, `SELECT count(*) FROM trades WHERE symbol = ANY($1)`, symbols).Scan(&trades)
	if trades == 0 {
		t.Fatal("expected some trades; the test isn't exercising matching")
	}
	for _, sym := range symbols {
		book, err := svc.OrderBook(ctx, sym, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(book.Bids) > 0 && len(book.Asks) > 0 && book.Bids[0].Price >= book.Asks[0].Price {
			t.Errorf("%s crossed: best bid %d >= best ask %d", sym, book.Bids[0].Price, book.Asks[0].Price)
		}
	}
	t.Logf("%d orders placed, %d rejected for funds, %d trades, %d deadlocks detected and retried",
		placed.Load(), rejected.Load(), trades, deadlockCount(t)-deadlocksBefore)
}

func mustDeposit(t *testing.T, accounts *app.AccountService, user, asset string, amount int64) {
	t.Helper()
	if _, err := accounts.Deposit(context.Background(), user, asset, amount); err != nil {
		t.Fatal(err)
	}
}

// deadlockCount reads Postgres' own statistics. They are flushed
// asynchronously, so the number is informational, not asserted.
func deadlockCount(t *testing.T) int64 {
	var n int64
	if err := pool.QueryRow(context.Background(),
		"SELECT deadlocks FROM pg_stat_database WHERE datname = current_database()").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
