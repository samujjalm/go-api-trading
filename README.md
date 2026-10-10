# go-service: a trading API in Go

A small but production-shaped Go service: password auth with JWT access tokens and rotating refresh tokens, plus a **limit/market order book with price-time-priority matching**, **account balances with fund reservation and trade settlement**, and an append-only balance journal, all persisted in PostgreSQL. It's built as a learning project for writing and maintaining high-volume fintech services.

Only Docker is required. Go itself runs inside containers.

## Quick start

```sh
make up                # build + start Postgres and the API on :8080
make test              # unit tests (no DB), with the race detector
make test-integration  # integration + end-to-end tests against real Postgres
make logs              # structured JSON request logs
make psql              # poke at the tables
make clean             # stop everything and wipe the database
```

### Try it

```sh
B=http://localhost:8080/api/v1
curl -XPOST $B/auth/register -d '{"email":"alice@example.com","password":"password123"}'
curl -XPOST $B/auth/register -d '{"email":"bob@example.com","password":"password123"}'
A=$(curl -s -XPOST $B/auth/login -d '{"email":"alice@example.com","password":"password123"}' | jq -r .access_token)
BOB=$(curl -s -XPOST $B/auth/login -d '{"email":"bob@example.com","password":"password123"}' | jq -r .access_token)

# Fund the accounts (amounts: USD in cents, symbols in whole shares)
curl -XPOST $B/account/deposits -H "Authorization: Bearer $A" -d '{"asset":"AAPL","amount":150}'
curl -XPOST $B/account/deposits -H "Authorization: Bearer $BOB" -d '{"asset":"USD","amount":5000000}'

# Alice offers 100 @ 189.50 and 50 @ 190.00 (prices are integer cents)
curl -XPOST $B/orders -H "Authorization: Bearer $A" -d '{"symbol":"AAPL","side":"SELL","type":"LIMIT","price":18950,"quantity":100}'
curl -XPOST $B/orders -H "Authorization: Bearer $A" -d '{"symbol":"AAPL","side":"SELL","type":"LIMIT","price":19000,"quantity":50}'

# Bob market-buys 120: fills 100 @ 18950, then 20 @ 19000
curl -XPOST $B/orders -H "Authorization: Bearer $BOB" -d '{"symbol":"AAPL","side":"BUY","type":"MARKET","quantity":120,"client_order_id":"bob-1"}'

curl $B/markets/AAPL/book
curl $B/markets/AAPL/trades
curl $B/account/balances  -H "Authorization: Bearer $BOB"
curl $B/account/movements -H "Authorization: Bearer $BOB"   # the journal behind the balance
```

## API

| Method | Path | Auth | Purpose |
|---|---|---|---|
| POST | `/api/v1/auth/register` | – | Create user `{email, password}` |
| POST | `/api/v1/auth/login` | – | Returns `access_token` (JWT, 15m) + `refresh_token` (opaque, 7d) |
| POST | `/api/v1/auth/refresh` | – | `{refresh_token}`: rotates it and returns a new pair |
| POST | `/api/v1/auth/logout` | – | `{refresh_token}`: revokes it |
| GET | `/api/v1/me` | Bearer | Current user |
| GET | `/api/v1/account/balances` | Bearer | `available`, `reserved`, `total` per asset |
| POST | `/api/v1/account/deposits` | Bearer | `{asset, amount}` (dev stand-in for a payment rail) |
| POST | `/api/v1/account/withdrawals` | Bearer | `{asset, amount}`: only from `available` |
| GET | `/api/v1/account/movements?limit=` | Bearer | Balance journal, newest first |
| POST | `/api/v1/orders` | Bearer | Place order `{symbol, side, type, price?, quantity, client_order_id?}` → `{order, trades}` |
| GET | `/api/v1/orders?symbol=&status=&limit=&offset=` | Bearer | Your orders |
| GET | `/api/v1/orders/{id}` | Bearer | One of your orders |
| DELETE | `/api/v1/orders/{id}` | Bearer | Cancel an open order |
| GET | `/api/v1/markets/{symbol}/book?depth=` | – | Aggregated bids/asks |
| GET | `/api/v1/markets/{symbol}/trades?limit=` | – | Recent trades |
| GET | `/healthz` | – | DB-backed health check |

Insufficient funds returns `422 insufficient_funds`. Errors always look like `{"error": {"code": "...", "message": "...", "field": "..."}}`.

## Money flow: reservation and settlement

Every user has one balance per asset: `USD` (cents) and each symbol (shares). Each balance is split into **available** and **reserved**.

| Event | Buyer | Seller |
|---|---|---|
| Place limit buy `q @ p` | USD: available → reserved `p×q` | |
| Place market buy | USD: reserve *all* available as budget | |
| Place sell `q` | | shares: available → reserved `q` |
| Trade `q @ price` | USD reserved −`price×q`; **price improvement** `(limit−price)×q` → available; shares available +`q` | shares reserved −`q`; USD available +`price×q` |
| Cancel / IOC remainder / self-trade | unused reservation → available | unused reservation → available |

Rules this enforces:

* **No double spending.** Funds backing an open order can't be withdrawn or used for another order.
* **Delivery versus payment.** Both legs of a trade settle in the same transaction as the match itself.
* **Market buys can't overspend.** Matching stops when the budget can't cover one more unit at the next price.

Every change goes through `domain.Balance` methods, which refuse to go negative and append a `Movement` to `balance_movements`. That journal is append-only, so any balance can be rebuilt and audited from it.

## Architecture: hexagonal (ports and adapters)

```
            driving side                                     driven side
   ┌──────────────────────┐                          ┌──────────────────────────┐
   │ adapters/http        │──▶ app (inbound ports) ──▶│ app (outbound ports)     │
   │  handlers, DTOs,     │    AuthUseCase            │  Store, *Repository      │◀── adapters/postgres
   │  middleware          │    TradingUseCase         │  PasswordHasher          │◀── adapters/security (bcrypt)
   └──────────────────────┘          │                │  AccessTokenIssuer       │◀── adapters/security (JWT)
                                     ▼                └──────────────────────────┘
                              domain (pure Go)
                       Order, Trade, Match(), errors
```

* **`internal/domain`**: the business rules, with zero infrastructure imports. `Match()` is a pure function, so the most important code is the easiest to test.
* **`internal/app`**: the use cases (`AuthService`, `TradingService`) and the **ports** (`ports.go`) they need. Services orchestrate: open a transaction, lock the book, call the domain, persist.
* **`internal/adapters/*`**: technology. They import `app`/`domain`, never the reverse. `postgres.mapErr` converts driver errors to domain errors, so nothing above the adapter knows pgx exists.
* **`cmd/api/main.go`**: the composition root, the only file that knows every concrete type.

You can see the payoff in `internal/app/*_test.go`: the full trading and auth logic runs against an in-memory `Store` with fake hashers and token issuers, in milliseconds, with no Docker.

## Fintech concepts in the code (and where to find them)

| Concept | Where |
|---|---|
| **No floats for money**: integer minor units, bounded so `price*qty` can't overflow | `domain/order.go` |
| **Price-time priority**, trade at the maker's price | `domain/matching.go`, `postgres/orders.go` `MatchCandidates` (`ORDER BY price, seq`) |
| **Atomicity**: the order and all its trades commit together or not at all | `TradingService.PlaceOrder` + `Store.WithTx` |
| **Concurrency control**: per-symbol `pg_advisory_xact_lock` serialises matching per book, while different symbols run in parallel | `postgres/orders.go` `LockBook` |
| **Idempotency**: retrying with the same `client_order_id` returns the original order (`Idempotent-Replayed: true`) instead of trading twice | `TradingService.PlaceOrder`, unique constraint in migration |
| **Self-trade prevention** (cancel newest) | `domain.Match` |
| **Fund reservation**: order and balance reservations move in lock-step (`Order.fill` mirrors `Settle`) | `domain/order.go`, `domain/balance.go` |
| **Atomic settlement** (delivery versus payment) | `domain.Settle`, called per trade inside `PlaceOrder`'s transaction |
| **Append-only journal**: balances are derivable from movements | `Balance.apply`, `balance_movements` table |
| **Deadlock-tolerant transactions**: Postgres aborts one side of a deadlock, and `WithTx` retries with jittered backoff, so the use case must be safe to re-run | `postgres/store.go`, `TestPlaceOrderSurvivesTransactionRetry` |
| **Immediate-or-cancel** market orders | `Order.FinishMatching` |
| **DB as last line of defence**: CHECK constraints, e.g. `filled_quantity BETWEEN 0 AND quantity` | `postgres/migrations/0001_init.sql` |
| **Invariant panics**: a matcher fed bad input panics instead of guessing; the transaction rolls back | `domain.mustBeMatchable` |
| **Refresh-token rotation + reuse detection**: a replayed token revokes every session | `AuthService.Refresh` |
| Timing-safe login, hashed refresh tokens, pinned JWT alg, `no-store` on tokens | `auth_service.go`, `security/jwt.go` |
| Graceful shutdown, server timeouts, request IDs, structured logs | `cmd/api/main.go`, `adapters/http/middleware.go` |

## Tests

* `domain/matching_test.go`: table-driven tests, the idiomatic Go style.
* `app/*_test.go`: use-case tests against in-memory adapters (external `app_test` package).
* `app/trading_service_test.go` ends **every** test with a ledger check: each balance equals the sum of its journal, and each balance's reserved amount equals what the user's open orders reserve.
* `adapters/postgres/integration_test.go` (`-tags integration`): includes **`TestConcurrentMatchingInvariants`**. Eight goroutines fire 400 random orders at two symbols shared by the same users. That provokes real deadlocks, which get retried, and real insufficient-funds rejections. The test then checks in SQL:
  * every order's fill equals the sum of its trades, at the maker's price
  * every open order's reservation exactly backs its remainder
  * every balance's reserved amount equals its open orders' reservations
  * every balance equals the sum of its journal
  * **each asset's total across users is conserved**
  * neither book is crossed
* `adapters/http/e2e_test.go` (`-tags integration`): the full HTTP flow through real adapters, using `httptest`.

**Exercise:** comment out the `LockBook` call in `TradingService.PlaceOrder` and run `make test-integration` a few times. Watch what breaks, and work out why.

## Suggested reading order

1. `domain/order.go` → `domain/matching.go` → `domain/matching_test.go`
2. `app/ports.go` → `app/trading_service.go` → `app/trading_service_test.go`
3. `adapters/postgres/store.go` (how `WithTx` works), then `orders.go`
4. `adapters/http/server.go` → `trading_handlers.go` → `respond.go`
5. `cmd/api/main.go`

## Deliberately left out (good next steps)

* **Fees**: charge a maker/taker fee on settlement and credit a fee account. A natural step from the journal to full double-entry bookkeeping, where every movement has an equal and opposite entry.
* **Idempotent deposits**: real deposits arrive from a payment rail or custodian with an external ID, which should be unique.
* **Avoiding deadlocks rather than retrying them**: each deadlock costs about `deadlock_timeout` (1s) of latency. A single-writer matching engine per symbol, or locking balances in a consistent order, removes them.
* **Outbox pattern**: publish trade events (Kafka/NATS) reliably by writing them in the same transaction.
* In-memory matching engine per symbol (single goroutine, fed by a channel) with the DB as journal. That's how high-throughput venues get microsecond latency.
* Rate limiting on `/auth/login`; Prometheus metrics; OpenTelemetry tracing.
* `sqlc` to generate type-safe queries; a migration tool such as goose or atlas; `golangci-lint`.
* Decimal string prices in the API (`"189.50"`) with per-symbol tick sizes.

## Configuration

| Env var | Default | |
|---|---|---|
| `DATABASE_URL` | required | |
| `JWT_SECRET` | required, ≥ 32 bytes | |
| `HTTP_ADDR` | `:8080` | |
| `JWT_ISSUER` | `go-service` | |
| `ACCESS_TOKEN_TTL` | `15m` | |
| `REFRESH_TOKEN_TTL` | `168h` | |

Migration `0002_balances.sql` cancels any orders left open from before balances existed, since no funds back them.
