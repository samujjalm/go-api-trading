package app

import (
	"context"

	"github.com/samujjal/go-service/internal/domain"
)

type AccountService struct {
	store Store
}

var _ AccountUseCase = (*AccountService)(nil)

func NewAccountService(store Store) *AccountService {
	return &AccountService{store: store}
}

func (s *AccountService) Balances(ctx context.Context, userID string) ([]*domain.Balance, error) {
	return s.store.Balances().ListByUser(ctx, userID)
}

// Deposit credits funds. In a real system this is driven by a payment rail
// or custodian callback carrying an external reference used for
// idempotency; here it's a direct API call so the service can be exercised.
func (s *AccountService) Deposit(ctx context.Context, userID, asset string, amount int64) (*domain.Balance, error) {
	return s.change(ctx, userID, asset, func(b *domain.Balance) error { return b.Deposit(amount) })
}

// Withdraw only touches available funds: money reserved for open orders
// can't leave the account.
func (s *AccountService) Withdraw(ctx context.Context, userID, asset string, amount int64) (*domain.Balance, error) {
	return s.change(ctx, userID, asset, func(b *domain.Balance) error { return b.Withdraw(amount) })
}

func (s *AccountService) change(ctx context.Context, userID, asset string, fn func(*domain.Balance) error) (*domain.Balance, error) {
	asset, err := domain.ValidateAsset(asset)
	if err != nil {
		return nil, err
	}
	var out *domain.Balance
	err = s.store.WithTx(ctx, func(tx Store) error {
		b, err := tx.Balances().GetForUpdate(ctx, userID, asset)
		if err != nil {
			return err
		}
		if err := fn(b); err != nil {
			return err
		}
		out = b
		return tx.Balances().Save(ctx, b)
	})
	return out, err
}

func (s *AccountService) Movements(ctx context.Context, userID string, limit int) ([]domain.Movement, error) {
	return s.store.Balances().ListMovements(ctx, userID, clamp(limit, defaultListLimit, maxListLimit))
}
