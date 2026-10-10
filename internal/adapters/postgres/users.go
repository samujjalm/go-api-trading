package postgres

import (
	"context"

	"github.com/samujjal/go-service/internal/domain"
)

type userRepo struct{ q querier }

const userColumns = "id, email, password_hash, created_at"

func (r userRepo) Create(ctx context.Context, u *domain.User) error {
	err := r.q.QueryRow(ctx,
		"INSERT INTO users (email, password_hash) VALUES ($1, $2) RETURNING id, created_at",
		u.Email, u.PasswordHash).Scan(&u.ID, &u.CreatedAt)
	return mapErr(err)
}

func (r userRepo) GetByEmail(ctx context.Context, email string) (*domain.User, error) {
	return r.get(ctx, "SELECT "+userColumns+" FROM users WHERE email = $1", email)
}

func (r userRepo) GetByID(ctx context.Context, id string) (*domain.User, error) {
	return r.get(ctx, "SELECT "+userColumns+" FROM users WHERE id = $1", id)
}

func (r userRepo) get(ctx context.Context, sql string, arg any) (*domain.User, error) {
	var u domain.User
	if err := r.q.QueryRow(ctx, sql, arg).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.CreatedAt); err != nil {
		return nil, mapErr(err)
	}
	return &u, nil
}
