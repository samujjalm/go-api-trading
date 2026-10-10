package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/samujjal/go-service/internal/domain"
)

type AuthService struct {
	store      Store
	hasher     PasswordHasher
	tokens     AccessTokenIssuer
	refreshTTL time.Duration
	now        func() time.Time

	dummyHash func() string
}

var _ AuthUseCase = (*AuthService)(nil) // compile-time check that the port is satisfied

func NewAuthService(store Store, hasher PasswordHasher, tokens AccessTokenIssuer, refreshTTL time.Duration, opts ...Option) *AuthService {
	o := buildOptions(opts)
	return &AuthService{
		store:      store,
		hasher:     hasher,
		tokens:     tokens,
		refreshTTL: refreshTTL,
		now:        o.now,
		dummyHash: sync.OnceValue(func() string {
			h, _ := hasher.Hash("timing-equaliser-password")
			return h
		}),
	}
}

func (s *AuthService) Register(ctx context.Context, email, password string) (*domain.User, error) {
	email, err := domain.NormalizeEmail(email)
	if err != nil {
		return nil, err
	}
	if err := domain.ValidatePassword(password); err != nil {
		return nil, err
	}
	hash, err := s.hasher.Hash(password)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}

	u := &domain.User{Email: email, PasswordHash: hash}
	if err := s.store.Users().Create(ctx, u); err != nil {
		if errors.Is(err, domain.ErrConflict) {
			return nil, fmt.Errorf("%w: email already registered", domain.ErrConflict)
		}
		return nil, err
	}
	return u, nil
}

func (s *AuthService) Login(ctx context.Context, email, password string) (TokenPair, error) {
	u, err := s.store.Users().GetByEmail(ctx, strings.ToLower(strings.TrimSpace(email)))
	if errors.Is(err, domain.ErrNotFound) {
		// Spend the same time as a real check so response latency doesn't
		// reveal which emails are registered.
		s.hasher.Compare(s.dummyHash(), password)
		return TokenPair{}, domain.ErrInvalidCredentials
	}
	if err != nil {
		return TokenPair{}, err
	}
	if !s.hasher.Compare(u.PasswordHash, password) {
		return TokenPair{}, domain.ErrInvalidCredentials
	}

	refresh, hash, err := newRefreshToken()
	if err != nil {
		return TokenPair{}, err
	}
	if _, err := s.store.RefreshTokens().Create(ctx, u.ID, hash, s.now().Add(s.refreshTTL)); err != nil {
		return TokenPair{}, err
	}
	return s.pair(u.ID, refresh)
}

// Refresh implements refresh-token rotation: every refresh token is single
// use and is exchanged for a new one. Presenting a token that was already
// rotated means it was copied, so all of the user's sessions are revoked.
func (s *AuthService) Refresh(ctx context.Context, refreshToken string) (TokenPair, error) {
	if refreshToken == "" {
		return TokenPair{}, domain.ErrInvalidToken
	}

	var (
		userID, newToken string
		reused           bool
	)
	err := s.store.WithTx(ctx, func(tx Store) error {
		reused = false // reset: WithTx may retry fn
		rt, err := tx.RefreshTokens().GetByHashForUpdate(ctx, hashRefreshToken(refreshToken))
		if errors.Is(err, domain.ErrNotFound) {
			return domain.ErrInvalidToken
		}
		if err != nil {
			return err
		}

		now := s.now()
		if rt.WasRotated() {
			reused = true
			// Returning nil (not an error) so this revocation is committed.
			return tx.RefreshTokens().RevokeAllForUser(ctx, rt.UserID, now)
		}
		if !rt.Usable(now) {
			return domain.ErrInvalidToken
		}

		token, hash, err := newRefreshToken()
		if err != nil {
			return err
		}
		newID, err := tx.RefreshTokens().Create(ctx, rt.UserID, hash, now.Add(s.refreshTTL))
		if err != nil {
			return err
		}
		if err := tx.RefreshTokens().MarkReplaced(ctx, rt.ID, newID, now); err != nil {
			return err
		}
		userID, newToken = rt.UserID, token
		return nil
	})
	if err != nil {
		return TokenPair{}, err
	}
	if reused {
		return TokenPair{}, domain.ErrRefreshTokenReused
	}
	return s.pair(userID, newToken)
}

// Logout revokes the refresh token. It is idempotent. The access token stays
// valid until it expires, which is why access tokens are short-lived.
func (s *AuthService) Logout(ctx context.Context, refreshToken string) error {
	if refreshToken == "" {
		return nil
	}
	return s.store.RefreshTokens().RevokeByHash(ctx, hashRefreshToken(refreshToken), s.now())
}

func (s *AuthService) Authenticate(_ context.Context, accessToken string) (string, error) {
	return s.tokens.Verify(accessToken)
}

func (s *AuthService) GetUser(ctx context.Context, userID string) (*domain.User, error) {
	return s.store.Users().GetByID(ctx, userID)
}

func (s *AuthService) pair(userID, refresh string) (TokenPair, error) {
	access, err := s.tokens.Issue(userID)
	if err != nil {
		return TokenPair{}, fmt.Errorf("issue access token: %w", err)
	}
	return TokenPair{AccessToken: access, RefreshToken: refresh, ExpiresIn: s.tokens.TTL()}, nil
}

// newRefreshToken returns an opaque random token for the client and the hash
// stored server-side. A SHA-256 (not bcrypt) is fine here: the token has 256
// bits of entropy, so there is nothing to brute-force, and lookups must be
// by exact hash.
func newRefreshToken() (token string, hash []byte, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, hashRefreshToken(token), nil
}

func hashRefreshToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}
