package postgres

import (
	"context"
	"time"

	"github.com/samujjal/go-service/internal/domain"
)

type refreshTokenRepo struct{ q querier }

func (r refreshTokenRepo) Create(ctx context.Context, userID string, hash []byte, expiresAt time.Time) (string, error) {
	var id string
	err := r.q.QueryRow(ctx,
		"INSERT INTO refresh_tokens (user_id, token_hash, expires_at) VALUES ($1, $2, $3) RETURNING id",
		userID, hash, expiresAt).Scan(&id)
	return id, mapErr(err)
}

func (r refreshTokenRepo) GetByHashForUpdate(ctx context.Context, hash []byte) (*domain.RefreshToken, error) {
	var t domain.RefreshToken
	err := r.q.QueryRow(ctx,
		`SELECT id, user_id, expires_at, revoked_at, replaced_by
		   FROM refresh_tokens WHERE token_hash = $1 FOR UPDATE`,
		hash).Scan(&t.ID, &t.UserID, &t.ExpiresAt, &t.RevokedAt, &t.ReplacedBy)
	if err != nil {
		return nil, mapErr(err)
	}
	return &t, nil
}

func (r refreshTokenRepo) MarkReplaced(ctx context.Context, id, replacedBy string, at time.Time) error {
	_, err := r.q.Exec(ctx,
		"UPDATE refresh_tokens SET revoked_at = $3, replaced_by = $2 WHERE id = $1",
		id, replacedBy, at)
	return mapErr(err)
}

func (r refreshTokenRepo) RevokeByHash(ctx context.Context, hash []byte, at time.Time) error {
	_, err := r.q.Exec(ctx,
		"UPDATE refresh_tokens SET revoked_at = $2 WHERE token_hash = $1 AND revoked_at IS NULL",
		hash, at)
	return mapErr(err)
}

func (r refreshTokenRepo) RevokeAllForUser(ctx context.Context, userID string, at time.Time) error {
	_, err := r.q.Exec(ctx,
		"UPDATE refresh_tokens SET revoked_at = $2 WHERE user_id = $1 AND revoked_at IS NULL",
		userID, at)
	return mapErr(err)
}
