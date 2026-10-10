package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/samujjal/go-service/internal/domain"
)

type tradeRepo struct{ q querier }

func (r tradeRepo) Create(ctx context.Context, t *domain.Trade) error {
	err := r.q.QueryRow(ctx,
		`INSERT INTO trades (symbol, buy_order_id, sell_order_id, maker_order_id, taker_order_id,
		                     taker_side, price, quantity, executed_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		 RETURNING id`,
		t.Symbol, t.BuyOrderID, t.SellOrderID, t.MakerOrderID, t.TakerOrderID,
		t.TakerSide, t.Price, t.Quantity, t.ExecutedAt).Scan(&t.ID)
	return mapErr(err)
}

func (r tradeRepo) ListBySymbol(ctx context.Context, symbol string, limit int) ([]*domain.Trade, error) {
	rows, err := r.q.Query(ctx,
		`SELECT id, symbol, buy_order_id, sell_order_id, maker_order_id, taker_order_id,
		        taker_side, price, quantity, executed_at
		   FROM trades WHERE symbol = $1 ORDER BY seq DESC LIMIT $2`,
		symbol, limit)
	if err != nil {
		return nil, mapErr(err)
	}
	trades, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (*domain.Trade, error) {
		var t domain.Trade
		err := row.Scan(&t.ID, &t.Symbol, &t.BuyOrderID, &t.SellOrderID, &t.MakerOrderID,
			&t.TakerOrderID, &t.TakerSide, &t.Price, &t.Quantity, &t.ExecutedAt)
		return &t, err
	})
	return trades, mapErr(err)
}
