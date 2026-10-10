package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/samujjal/go-service/internal/domain"
)

type balanceRepo struct{ q querier }

func (r balanceRepo) GetForUpdate(ctx context.Context, userID, asset string) (*domain.Balance, error) {
	// Ensure the row exists so there is something to lock. ON CONFLICT DO
	// NOTHING makes concurrent first-time inserts safe.
	if _, err := r.q.Exec(ctx,
		"INSERT INTO balances (user_id, asset) VALUES ($1, $2) ON CONFLICT DO NOTHING",
		userID, asset); err != nil {
		return nil, mapErr(err)
	}
	b := domain.Balance{UserID: userID, Asset: asset}
	err := r.q.QueryRow(ctx,
		"SELECT available, reserved FROM balances WHERE user_id = $1 AND asset = $2 FOR UPDATE",
		userID, asset).Scan(&b.Available, &b.Reserved)
	if err != nil {
		return nil, mapErr(err)
	}
	return &b, nil
}

// Save writes the new totals and appends the journal entries in one round
// trip using a pgx batch.
func (r balanceRepo) Save(ctx context.Context, b *domain.Balance) error {
	batch := &pgx.Batch{}
	batch.Queue("UPDATE balances SET available = $3, reserved = $4, updated_at = now() WHERE user_id = $1 AND asset = $2",
		b.UserID, b.Asset, b.Available, b.Reserved)
	for _, m := range b.PendingMovements() {
		batch.Queue(`INSERT INTO balance_movements
		               (user_id, asset, kind, delta_available, delta_reserved, order_id, trade_id)
		             VALUES ($1, $2, $3, $4, $5, NULLIF($6, '')::uuid, NULLIF($7, '')::uuid)`,
			m.UserID, m.Asset, m.Kind, m.DeltaAvailable, m.DeltaReserved, m.OrderID, m.TradeID)
	}
	if err := r.q.SendBatch(ctx, batch).Close(); err != nil {
		return mapErr(err)
	}
	b.MarkPersisted()
	return nil
}

func (r balanceRepo) ListByUser(ctx context.Context, userID string) ([]*domain.Balance, error) {
	rows, err := r.q.Query(ctx,
		"SELECT user_id, asset, available, reserved FROM balances WHERE user_id = $1 ORDER BY asset",
		userID)
	if err != nil {
		return nil, mapErr(err)
	}
	balances, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (*domain.Balance, error) {
		var b domain.Balance
		err := row.Scan(&b.UserID, &b.Asset, &b.Available, &b.Reserved)
		return &b, err
	})
	return balances, mapErr(err)
}

func (r balanceRepo) ListMovements(ctx context.Context, userID string, limit int) ([]domain.Movement, error) {
	rows, err := r.q.Query(ctx,
		`SELECT id, user_id, asset, kind, delta_available, delta_reserved,
		        COALESCE(order_id::text, ''), COALESCE(trade_id::text, ''), created_at
		   FROM balance_movements WHERE user_id = $1 ORDER BY seq DESC LIMIT $2`,
		userID, limit)
	if err != nil {
		return nil, mapErr(err)
	}
	ms, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.Movement, error) {
		var m domain.Movement
		err := row.Scan(&m.ID, &m.UserID, &m.Asset, &m.Kind, &m.DeltaAvailable, &m.DeltaReserved,
			&m.OrderID, &m.TradeID, &m.CreatedAt)
		return m, err
	})
	return ms, mapErr(err)
}
