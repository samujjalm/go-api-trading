CREATE TABLE users (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email         text NOT NULL UNIQUE,
    password_hash text NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE refresh_tokens (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash  bytea NOT NULL UNIQUE,
    expires_at  timestamptz NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    revoked_at  timestamptz,
    replaced_by uuid REFERENCES refresh_tokens (id) ON DELETE SET NULL
);

CREATE INDEX refresh_tokens_user_id_idx ON refresh_tokens (user_id);

CREATE TABLE orders (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Monotonic arrival sequence: the tie-breaker for time priority.
    -- Timestamps can collide; an identity column cannot.
    seq             bigint GENERATED ALWAYS AS IDENTITY UNIQUE,
    user_id         uuid NOT NULL REFERENCES users (id),
    client_order_id text,
    symbol          text NOT NULL,
    side            text NOT NULL CHECK (side IN ('BUY', 'SELL')),
    type            text NOT NULL CHECK (type IN ('LIMIT', 'MARKET')),
    price           bigint NOT NULL CHECK (price >= 0),
    quantity        bigint NOT NULL CHECK (quantity > 0),
    filled_quantity bigint NOT NULL DEFAULT 0,
    status          text NOT NULL CHECK (status IN ('NEW', 'PARTIALLY_FILLED', 'FILLED', 'CANCELLED')),
    created_at      timestamptz NOT NULL,
    updated_at      timestamptz NOT NULL,

    -- The database is the last line of defence for money invariants.
    CONSTRAINT orders_fill_bounds CHECK (filled_quantity BETWEEN 0 AND quantity),
    CONSTRAINT orders_limit_has_price CHECK (type = 'MARKET' OR price > 0),
    CONSTRAINT orders_client_order_id_unique UNIQUE (user_id, client_order_id)
);

-- Partial index covering exactly the resting orders the matcher scans.
CREATE INDEX orders_book_idx ON orders (symbol, side, price, seq)
    WHERE status IN ('NEW', 'PARTIALLY_FILLED');
CREATE INDEX orders_user_idx ON orders (user_id, seq DESC);

CREATE TABLE trades (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    seq            bigint GENERATED ALWAYS AS IDENTITY UNIQUE,
    symbol         text NOT NULL,
    buy_order_id   uuid NOT NULL REFERENCES orders (id),
    sell_order_id  uuid NOT NULL REFERENCES orders (id),
    maker_order_id uuid NOT NULL REFERENCES orders (id),
    taker_order_id uuid NOT NULL REFERENCES orders (id),
    taker_side     text NOT NULL CHECK (taker_side IN ('BUY', 'SELL')),
    price          bigint NOT NULL CHECK (price > 0),
    quantity       bigint NOT NULL CHECK (quantity > 0),
    executed_at    timestamptz NOT NULL
);

CREATE INDEX trades_symbol_idx ON trades (symbol, seq DESC);
