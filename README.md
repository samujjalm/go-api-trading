# go-service: a trading API in Go

A small but production-shaped Go service: password auth with JWT access tokens and rotating refresh tokens, plus a **limit/market order book with price-time-priority matching**, **account balances with fund reservation and trade settlement**, and an append-only balance journal, all persisted in PostgreSQL. It's built as a learning project for writing and maintaining high-volume fintech services.

Only Docker is required. Go itself runs inside containers.

**Visual explainer:** open [`docs/order-matching.html`](docs/order-matching.html) in a browser. It has an interactive order book where you can send orders and watch each matching step, plus the path an order takes through the service. [`docs/go-features.html`](docs/go-features.html) covers the Go features the service uses: goroutines per request, an animated lock timeline, channels and graceful shutdown, context, retries, and the concurrency test.

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

## Local development and debugging

### Prerequisites

| Tool | Needed for |
|---|---|
| Docker with Compose v2 (`docker compose`) | everything |
| `make` | the shortcuts below (each target is a plain `docker compose` command, see `Makefile`) |
| `curl`, `jq` | trying the API and filtering logs |
| Go 1.26+ *(optional)* | running natively with `go run`; your editor's Go tooling |

Run `make help` for every target.

### Three ways to run the API

| | Command | Use it when |
|---|---|---|
| **All in Docker** | `make up` | You just want the service running. Rebuild after code changes with `make up` again. |
| **Native Go, DB in Docker** | `make run` | You have Go installed and want the fastest edit-and-rerun loop (Ctrl-C, `make run`). Starts Postgres, stops the API container, runs `go run ./cmd/api` against `localhost:5432`. |
| **Under the debugger** | `make debug` | You want breakpoints. Runs the API inside Delve in Docker (port `8080` for HTTP, `2345` for the debugger). `make debug-stop` returns to the normal container. |

All three use the same database, so data survives switching between them. The settings `make run` uses are in [`.env.example`](.env.example).

### Debugging with breakpoints

`make debug` builds `Dockerfile.debug`. That image compiles the API with optimisations and inlining turned off (`-gcflags="all=-N -l"`), so every variable can be inspected and breakpoints land on the exact line. It then runs the API under Delve. The API starts serving immediately, and you can attach and detach as often as you like.

**VS Code** (with the Go extension). `.vscode/launch.json` is included:

1. `make debug`
2. Run and Debug → **Attach to API in Docker (make debug)**
3. Set a breakpoint, e.g. in `handlePlaceOrder` in `internal/adapters/http/trading_handlers.go`, then send a request with curl.

The config maps your checkout to `/src`, where the image built the code. If breakpoints show as unverified, the mapping is the first thing to check.

**GoLand.** Run → Edit Configurations → **+ Go Remote**, host `localhost`, port `2345`, then Debug. The binary was built from `/src`. If breakpoints don't bind, add a path mapping from your project folder to `/src`.

**Delve CLI** (no IDE needed, nothing to install):

```sh
make debug
make dlv                     # opens Delve attached to the container
(dlv) break internal/adapters/http/trading_handlers.go:102
(dlv) continue               # now send an order with curl in another terminal
(dlv) print req              # the decoded request body
(dlv) next / step / stack    # step over, step in, show the call stack
(dlv) goroutines             # every goroutine; requests each get their own
(dlv) clear 1
(dlv) exit -c                # detach and leave the API running
```

While the API is stopped at a breakpoint, its request (and any open transaction) waits too. Other requests keep being served.

**Running natively.** After `make db`, choose **Run API locally (make db first)** in VS Code, or create a GoLand *Go Build* configuration for `./cmd/api` with the environment from `.env.example`.

### Debugging tests

```sh
make test PKG=./internal/app/ RUN=TestPriceTimePriority        # run one test
make test-integration RUN=TestConcurrentMatchingInvariants     # one integration test
make debug-test PKG=./internal/domain/ RUN=TestCancel          # step through it in Delve
(dlv) break domain.(*Order).Cancel
(dlv) continue
(dlv) print o.Status
```

`RUN` is a regular expression; `RUN='^TestCancel$'` matches exactly one test. With Go installed, the **Debug tests in current package** config in VS Code debugs the test file you have open, integration tests included (it needs `make db`).

### Tracing a request through the logs

Every response carries an `X-Request-ID` header, and every log line for that request has the same `request_id`. Send your own ID to find a request easily:

```sh
curl -i localhost:8080/api/v1/markets/AAPL/book -H 'X-Request-ID: trace-me-123'
docker compose logs --no-log-prefix api | grep trace-me-123 | jq .

# Every failed request:
docker compose logs --no-log-prefix api | grep '^{' | jq -c 'select(.status >= 400) | {time, request_id, method, path, status}'
```

Use `api-debug` instead of `api` while `make debug` is running, or `make logs` to follow whichever is up. A `500` response never contains the cause. The matching `"level":"ERROR"` log line does, and a panic logs its full stack trace.

### Inspecting the database

`make psql` opens a shell on the dev database. Queries that answer the usual questions:

```sql
-- The order book for one symbol
SELECT side, price, sum(quantity - filled_quantity) AS qty, count(*) AS orders
  FROM orders WHERE symbol = 'AAPL' AND status IN ('NEW', 'PARTIALLY_FILLED')
 GROUP BY side, price ORDER BY side DESC, price DESC;

-- A user's balances, and the journal entries behind them
SELECT b.asset, b.available, b.reserved
  FROM balances b JOIN users u ON u.id = b.user_id WHERE u.email = 'bob@example.com';

SELECT m.created_at, m.asset, m.kind, m.delta_available, m.delta_reserved, m.order_id, m.trade_id
  FROM balance_movements m JOIN users u ON u.id = m.user_id
 WHERE u.email = 'bob@example.com' ORDER BY m.seq;

-- Recent trades
SELECT executed_at, symbol, price, quantity, taker_side FROM trades ORDER BY seq DESC LIMIT 20;

-- Sanity check: balances that don't match their journal (should be 0)
SELECT count(*) FROM balances b WHERE (b.available, b.reserved) <>
  (SELECT COALESCE(sum(delta_available), 0), COALESCE(sum(delta_reserved), 0)
     FROM balance_movements m WHERE m.user_id = b.user_id AND m.asset = b.asset);
```

Integration tests write to a separate database, `trading_test`, so they never touch your dev data. Inspect it with `docker compose exec db psql -U trading -d trading_test`.

### Troubleshooting

| Symptom | Fix |
|---|---|
| `port is already allocated` for 5432, 8080 or 2345 | Pick other host ports: `POSTGRES_PORT=5433 API_PORT=8081 DLV_PORT=2346 make up` (or `make debug`). For `make run`, pass the same `POSTGRES_PORT`. |
| `make run`: `connection refused` | Postgres isn't up or is on another port. `make db` starts it; check `make ps`. |
| `401 invalid_token` after a while | Access tokens last 15 minutes. Log in again or call `/auth/refresh`. |
| `422 insufficient_funds` | Deposit first: `POST /api/v1/account/deposits`. Funds reserved by open orders don't count as available. |
| Breakpoints never hit | Make sure you started `make debug`, not `make up`, and attached to port `2345`. In VS Code, check the `/src` path mapping. |
| Integration tests fail with `database "trading_test" does not exist` | It's created only when the Postgres volume is first made. Run `docker compose exec db createdb -U trading trading_test`, or `make clean` to start fresh. |
| Natively run tests say `TEST_DATABASE_URL not set; skipping` | Export the URL from `.env.example`, or use `make test-integration`, which runs in Docker. |
| Open orders became `CANCELLED` after upgrading | Expected: migration `0002_balances.sql` cancels orders placed before balances existed, because no funds back them. |
| Start from a clean slate | `make clean` deletes all containers and data; then `make up`. |

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
