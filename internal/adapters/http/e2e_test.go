//go:build integration

package http_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	httpadapter "github.com/samujjal/go-service/internal/adapters/http"
	"github.com/samujjal/go-service/internal/adapters/postgres"
	"github.com/samujjal/go-service/internal/adapters/security"
	"github.com/samujjal/go-service/internal/app"
)

// newTestServer wires the real adapters exactly like cmd/api does, but serves
// on an in-process httptest.Server.
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := postgres.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	store := postgres.NewStore(pool)
	auth := app.NewAuthService(store, security.NewBcryptHasher(bcrypt.MinCost),
		security.NewJWTIssuer([]byte("0123456789abcdef0123456789abcdef"), "test", time.Minute), time.Hour)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(httpadapter.NewServer(auth, app.NewTradingService(store), app.NewAccountService(store), pool, log).Handler())
	t.Cleanup(srv.Close)
	return srv
}

type client struct {
	t     *testing.T
	base  string
	token string
}

func (c *client) do(method, path string, body any, wantStatus int, out any) {
	c.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest(method, c.base+path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		c.t.Fatalf("%s %s: status %d, want %d; body: %s", method, path, resp.StatusCode, wantStatus, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			c.t.Fatalf("decode %s: %v", raw, err)
		}
	}
}

type tokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

type orderResult struct {
	Order struct {
		ID             string `json:"id"`
		Status         string `json:"status"`
		FilledQuantity int64  `json:"filled_quantity"`
	} `json:"order"`
	Trades []struct {
		Price    int64 `json:"price"`
		Quantity int64 `json:"quantity"`
	} `json:"trades"`
}

func signUp(t *testing.T, base string) (*client, tokens) {
	b := make([]byte, 4)
	rand.Read(b)
	creds := map[string]string{"email": "e2e" + hex.EncodeToString(b) + "@example.com", "password": "password123"}

	c := &client{t: t, base: base}
	c.do("POST", "/api/v1/auth/register", creds, http.StatusCreated, nil)
	var tok tokens
	c.do("POST", "/api/v1/auth/login", creds, http.StatusOK, &tok)
	c.token = tok.AccessToken
	return c, tok
}

func TestEndToEnd(t *testing.T) {
	srv := newTestServer(t)
	alice, _ := signUp(t, srv.URL)
	bob, bobTokens := signUp(t, srv.URL)

	b := make([]byte, 3)
	rand.Read(b)
	symbol := strings.ToUpper("E" + hex.EncodeToString(b)) // the API upper-cases symbols

	// Unauthenticated requests are rejected.
	anon := &client{t: t, base: srv.URL}
	anon.do("GET", "/api/v1/orders", nil, http.StatusUnauthorized, nil)

	// Without funds, orders are rejected.
	bob.do("POST", "/api/v1/orders", map[string]any{
		"symbol": symbol, "side": "BUY", "type": "LIMIT", "price": 10_100, "quantity": 4,
	}, http.StatusUnprocessableEntity, nil)

	alice.do("POST", "/api/v1/account/deposits", map[string]any{"asset": symbol, "amount": 10}, http.StatusOK, nil)
	bob.do("POST", "/api/v1/account/deposits", map[string]any{"asset": "USD", "amount": 100_000}, http.StatusOK, nil)

	// Alice rests an ask, Bob lifts part of it.
	var ask orderResult
	alice.do("POST", "/api/v1/orders", map[string]any{
		"symbol": symbol, "side": "SELL", "type": "LIMIT", "price": 10_050, "quantity": 10,
	}, http.StatusCreated, &ask)

	var bid orderResult
	bob.do("POST", "/api/v1/orders", map[string]any{
		"symbol": symbol, "side": "BUY", "type": "LIMIT", "price": 10_100, "quantity": 4,
		"client_order_id": "bob-1",
	}, http.StatusCreated, &bid)
	if bid.Order.Status != "FILLED" || len(bid.Trades) != 1 || bid.Trades[0].Price != 10_050 {
		t.Fatalf("unexpected fill: %+v", bid)
	}

	// Bob paid 4 × 10_050 at the ask, not his 10_100 limit, and received 4 shares.
	assertBalances(t, bob, map[string][2]int64{"USD": {100_000 - 4*10_050, 0}, symbol: {4, 0}})
	assertBalances(t, alice, map[string][2]int64{"USD": {4 * 10_050, 0}, symbol: {0, 6}})

	// Retrying with the same client_order_id doesn't trade twice.
	var replay orderResult
	bob.do("POST", "/api/v1/orders", map[string]any{
		"symbol": symbol, "side": "BUY", "type": "LIMIT", "price": 10_100, "quantity": 4,
		"client_order_id": "bob-1",
	}, http.StatusOK, &replay)
	if replay.Order.ID != bid.Order.ID {
		t.Fatal("idempotent replay returned a different order")
	}

	var book struct {
		Asks []struct{ Price, Quantity int64 } `json:"asks"`
	}
	anon.do("GET", "/api/v1/markets/"+symbol+"/book", nil, http.StatusOK, &book)
	if len(book.Asks) != 1 || book.Asks[0].Quantity != 6 {
		t.Fatalf("unexpected book: %+v", book)
	}

	// Bob can't see or cancel Alice's order; Alice can cancel it.
	bob.do("GET", "/api/v1/orders/"+ask.Order.ID, nil, http.StatusNotFound, nil)
	bob.do("DELETE", "/api/v1/orders/"+ask.Order.ID, nil, http.StatusNotFound, nil)
	alice.do("DELETE", "/api/v1/orders/"+ask.Order.ID, nil, http.StatusOK, nil)
	alice.do("DELETE", "/api/v1/orders/"+ask.Order.ID, nil, http.StatusConflict, nil)
	assertBalances(t, alice, map[string][2]int64{"USD": {4 * 10_050, 0}, symbol: {6, 0}}) // reservation released

	// Withdrawals can't exceed available funds.
	alice.do("POST", "/api/v1/account/withdrawals", map[string]any{"asset": symbol, "amount": 7}, http.StatusUnprocessableEntity, nil)
	alice.do("POST", "/api/v1/account/withdrawals", map[string]any{"asset": symbol, "amount": 6}, http.StatusOK, nil)

	var journal struct {
		Movements []struct{ Kind string } `json:"movements"`
	}
	alice.do("GET", "/api/v1/account/movements", nil, http.StatusOK, &journal)
	if len(journal.Movements) == 0 || journal.Movements[0].Kind != "WITHDRAWAL" {
		t.Fatalf("unexpected journal: %+v", journal)
	}

	// Validation errors are 400s with the offending field.
	var verr struct {
		Error struct{ Field string } `json:"error"`
	}
	bob.do("POST", "/api/v1/orders", map[string]any{
		"symbol": symbol, "side": "BUY", "type": "LIMIT", "price": 0, "quantity": 1,
	}, http.StatusBadRequest, &verr)
	if verr.Error.Field != "price" {
		t.Fatalf("validation field = %q", verr.Error.Field)
	}

	// Refresh rotation over HTTP.
	var rotated tokens
	anon.do("POST", "/api/v1/auth/refresh", map[string]string{"refresh_token": bobTokens.RefreshToken}, http.StatusOK, &rotated)
	anon.do("POST", "/api/v1/auth/refresh", map[string]string{"refresh_token": bobTokens.RefreshToken}, http.StatusUnauthorized, nil)

	bob.token = rotated.AccessToken
	bob.do("GET", "/api/v1/me", nil, http.StatusOK, nil)
	bob.do("POST", "/api/v1/auth/logout", map[string]string{"refresh_token": rotated.RefreshToken}, http.StatusNoContent, nil)
}

func assertBalances(t *testing.T, c *client, want map[string][2]int64) {
	t.Helper()
	var got struct {
		Balances []struct {
			Asset               string
			Available, Reserved int64
		} `json:"balances"`
	}
	c.do("GET", "/api/v1/account/balances", nil, http.StatusOK, &got)
	have := map[string][2]int64{}
	for _, b := range got.Balances {
		have[b.Asset] = [2]int64{b.Available, b.Reserved}
	}
	for asset, w := range want {
		if have[asset] != w {
			t.Errorf("%s balance: available/reserved %v, want %v", asset, have[asset], w)
		}
	}
}
