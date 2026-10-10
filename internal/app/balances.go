package app

import (
	"context"

	"github.com/samujjal/go-service/internal/domain"
)

type balanceKey struct{ userID, asset string }

// balanceSet loads each balance a transaction touches once (locking it), so
// repeated trades against the same user mutate one in-memory value, and
// saves them all at the end.
type balanceSet struct {
	repo  BalanceRepository
	byKey map[balanceKey]*domain.Balance
	order []*domain.Balance // save in load order for deterministic SQL
}

func newBalanceSet(repo BalanceRepository) *balanceSet {
	return &balanceSet{repo: repo, byKey: map[balanceKey]*domain.Balance{}}
}

func (s *balanceSet) get(ctx context.Context, userID, asset string) (*domain.Balance, error) {
	k := balanceKey{userID, asset}
	if b, ok := s.byKey[k]; ok {
		return b, nil
	}
	b, err := s.repo.GetForUpdate(ctx, userID, asset)
	if err != nil {
		return nil, err
	}
	s.byKey[k] = b
	s.order = append(s.order, b)
	return b, nil
}

func (s *balanceSet) saveAll(ctx context.Context) error {
	for _, b := range s.order {
		if len(b.PendingMovements()) == 0 {
			continue
		}
		if err := s.repo.Save(ctx, b); err != nil {
			return err
		}
	}
	return nil
}

func (s *balanceSet) tradeParties(ctx context.Context, symbol string, buy, sell *domain.Order) (domain.TradeParties, error) {
	var (
		p   domain.TradeParties
		err error
	)
	if p.BuyerQuote, err = s.get(ctx, buy.UserID, domain.QuoteAsset); err != nil {
		return p, err
	}
	if p.BuyerBase, err = s.get(ctx, buy.UserID, symbol); err != nil {
		return p, err
	}
	if p.SellerQuote, err = s.get(ctx, sell.UserID, domain.QuoteAsset); err != nil {
		return p, err
	}
	p.SellerBase, err = s.get(ctx, sell.UserID, symbol)
	return p, err
}
