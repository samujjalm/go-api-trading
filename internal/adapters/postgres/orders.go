package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/samujjal/go-service/internal/app"
	"github.com/samujjal/go-service/internal/domain"
)

type orderRepo struct{ q querier }

const (
	orderColumns = `id, user_id, COALESCE(client_order_id, ''), symbol, side, type, price,
		quantity, filled_quantity, reserved, status, created_at, updated_at`
	openStatuses = "('NEW', 'PARTIALLY_FILLED')"
)

func scanOrder(row pgx.Row) (*domain.Order, error) {
	var o domain.Order
	err := row.Scan(&o.ID, &o.UserID, &o.ClientOrderID, &o.Symbol, &o.Side, &o.Type, &o.Price,
		&o.Quantity, &o.FilledQuantity, &o.Reserved, &o.Status, &o.CreatedAt, &o.UpdatedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	return &o, nil
}

func collectOrders(rows pgx.Rows, err error) ([]*domain.Order, error) {
	if err != nil {
		return nil, mapErr(err)
	}
	orders, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (*domain.Order, error) {
		return scanOrder(row)
	})
	return orders, mapErr(err)
}

// LockBook takes a transaction-scoped advisory lock keyed on the symbol. It
// is released automatically at COMMIT/ROLLBACK. Outside a transaction it
// would be released immediately, which is why the port says "inside WithTx".
func (r orderRepo) LockBook(ctx context.Context, symbol string) error {
	_, err := r.q.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('orderbook:' || $1))", symbol)
	return mapErr(err)
}

func (r orderRepo) Create(ctx context.Context, o *domain.Order) error {
	err := r.q.QueryRow(ctx,
		`INSERT INTO orders (user_id, client_order_id, symbol, side, type, price, quantity,
		                     filled_quantity, reserved, status, created_at, updated_at)
		 VALUES ($1, NULLIF($2, ''), $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		 RETURNING id`,
		o.UserID, o.ClientOrderID, o.Symbol, o.Side, o.Type, o.Price, o.Quantity,
		o.FilledQuantity, o.Reserved, o.Status, o.CreatedAt, o.UpdatedAt).Scan(&o.ID)
	return mapErr(err)
}

func (r orderRepo) Update(ctx context.Context, o *domain.Order) error {
	tag, err := r.q.Exec(ctx,
		"UPDATE orders SET filled_quantity = $2, reserved = $3, status = $4, updated_at = $5 WHERE id = $1",
		o.ID, o.FilledQuantity, o.Reserved, o.Status, o.UpdatedAt)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r orderRepo) GetByID(ctx context.Context, id string) (*domain.Order, error) {
	return scanOrder(r.q.QueryRow(ctx, "SELECT "+orderColumns+" FROM orders WHERE id = $1", id))
}

func (r orderRepo) GetByClientOrderID(ctx context.Context, userID, clientOrderID string) (*domain.Order, error) {
	return scanOrder(r.q.QueryRow(ctx,
		"SELECT "+orderColumns+" FROM orders WHERE user_id = $1 AND client_order_id = $2",
		userID, clientOrderID))
}

func (r orderRepo) ListByUser(ctx context.Context, userID string, f app.OrderFilter) ([]*domain.Order, error) {
	return collectOrders(r.q.Query(ctx,
		`SELECT `+orderColumns+` FROM orders
		  WHERE user_id = $1
		    AND ($2::text = '' OR symbol = $2)
		    AND ($3::text = '' OR status = $3)
		  ORDER BY seq DESC
		  LIMIT $4 OFFSET $5`,
		userID, f.Symbol, f.Status, f.Limit, f.Offset))
}

// MatchCandidates implements price-time priority in SQL: best price first,
// then lowest seq (earliest arrival). FOR UPDATE is belt and braces on top
// of the book lock.
func (r orderRepo) MatchCandidates(ctx context.Context, taker *domain.Order, limit int) ([]*domain.Order, error) {
	var limitPrice *int64 // nil for market orders: any price crosses
	if taker.Type == domain.OrderTypeLimit {
		limitPrice = &taker.Price
	}

	var sql string
	if taker.Side == domain.SideBuy {
		sql = `SELECT ` + orderColumns + ` FROM orders
		        WHERE symbol = $1 AND side = 'SELL' AND status IN ` + openStatuses + `
		          AND ($2::bigint IS NULL OR price <= $2)
		        ORDER BY price ASC, seq ASC
		        LIMIT $3 FOR UPDATE`
	} else {
		sql = `SELECT ` + orderColumns + ` FROM orders
		        WHERE symbol = $1 AND side = 'BUY' AND status IN ` + openStatuses + `
		          AND ($2::bigint IS NULL OR price >= $2)
		        ORDER BY price DESC, seq ASC
		        LIMIT $3 FOR UPDATE`
	}
	return collectOrders(r.q.Query(ctx, sql, taker.Symbol, limitPrice, limit))
}

func (r orderRepo) BookLevels(ctx context.Context, symbol string, side domain.Side, depth int) ([]domain.PriceLevel, error) {
	order := "ASC"
	if side == domain.SideBuy {
		order = "DESC"
	}
	rows, err := r.q.Query(ctx,
		`SELECT price, SUM(quantity - filled_quantity)::bigint, COUNT(*)::int
		   FROM orders
		  WHERE symbol = $1 AND side = $2 AND status IN `+openStatuses+`
		  GROUP BY price
		  ORDER BY price `+order+`
		  LIMIT $3`,
		symbol, side, depth)
	if err != nil {
		return nil, mapErr(err)
	}
	levels, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.PriceLevel, error) {
		var l domain.PriceLevel
		err := row.Scan(&l.Price, &l.Quantity, &l.Orders)
		return l, err
	})
	return levels, mapErr(err)
}
