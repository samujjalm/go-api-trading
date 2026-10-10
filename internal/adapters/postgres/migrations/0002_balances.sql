-- Orders now lock funds while open.
ALTER TABLE orders ADD COLUMN reserved bigint NOT NULL DEFAULT 0 CHECK (reserved >= 0);

-- Data migration: orders that opened before reservations existed aren't
-- backed by funds, so they can't safely keep trading. Cancel them.
UPDATE orders SET status = 'CANCELLED', updated_at = now()
 WHERE status IN ('NEW', 'PARTIALLY_FILLED');

ALTER TABLE orders ADD CONSTRAINT orders_closed_releases_funds
    CHECK (status IN ('NEW', 'PARTIALLY_FILLED') OR reserved = 0);

CREATE TABLE balances (
    user_id    uuid NOT NULL REFERENCES users (id),
    asset      text NOT NULL,
    available  bigint NOT NULL DEFAULT 0 CHECK (available >= 0),
    reserved   bigint NOT NULL DEFAULT 0 CHECK (reserved >= 0),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, asset)
);

-- Append-only journal: one row per balance change. Balances can always be
-- rebuilt (and audited) from it.
CREATE TABLE balance_movements (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    seq             bigint GENERATED ALWAYS AS IDENTITY UNIQUE,
    user_id         uuid NOT NULL,
    asset           text NOT NULL,
    kind            text NOT NULL CHECK (kind IN
                        ('DEPOSIT', 'WITHDRAWAL', 'RESERVE', 'RELEASE', 'TRADE_DEBIT', 'TRADE_CREDIT')),
    delta_available bigint NOT NULL,
    delta_reserved  bigint NOT NULL,
    order_id        uuid REFERENCES orders (id),
    trade_id        uuid REFERENCES trades (id),
    created_at      timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (user_id, asset) REFERENCES balances (user_id, asset)
);

CREATE INDEX balance_movements_user_idx ON balance_movements (user_id, seq DESC);
