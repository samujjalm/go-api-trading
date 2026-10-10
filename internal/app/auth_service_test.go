package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/samujjal/go-service/internal/app"
	"github.com/samujjal/go-service/internal/domain"
)

type clock struct{ t time.Time }

func (c *clock) Now() time.Time          { return c.t }
func (c *clock) Advance(d time.Duration) { c.t = c.t.Add(d) }

func newAuth(t *testing.T) (*app.AuthService, *clock) {
	t.Helper()
	c := &clock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	return app.NewAuthService(newMemStore(), fakeHasher{}, fakeTokens{}, time.Hour, app.WithClock(c.Now)), c
}

func TestRegisterAndLogin(t *testing.T) {
	ctx := context.Background()
	svc, _ := newAuth(t)

	u, err := svc.Register(ctx, "  Alice@Example.com ", "s3cret-password")
	if err != nil {
		t.Fatal(err)
	}
	if u.Email != "alice@example.com" {
		t.Fatalf("email not normalised: %q", u.Email)
	}

	pair, err := svc.Login(ctx, "ALICE@example.com", "s3cret-password")
	if err != nil {
		t.Fatal(err)
	}
	if pair.AccessToken == "" || pair.RefreshToken == "" {
		t.Fatalf("empty tokens: %+v", pair)
	}
	gotID, err := svc.Authenticate(ctx, pair.AccessToken)
	if err != nil || gotID != u.ID {
		t.Fatalf("Authenticate = %q, %v; want %q", gotID, err, u.ID)
	}
}

func TestRegisterRejects(t *testing.T) {
	ctx := context.Background()
	svc, _ := newAuth(t)
	if _, err := svc.Register(ctx, "a@example.com", "password1"); err != nil {
		t.Fatal(err)
	}

	tests := map[string]struct {
		email, password string
		check           func(error) bool
	}{
		"duplicate email": {"A@example.com", "password1", func(err error) bool { return errors.Is(err, domain.ErrConflict) }},
		"bad email":       {"not-an-email", "password1", isValidation},
		"short password":  {"b@example.com", "short", isValidation},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := svc.Register(ctx, tt.email, tt.password); !tt.check(err) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestLoginRejectsBadCredentials(t *testing.T) {
	ctx := context.Background()
	svc, _ := newAuth(t)
	svc.Register(ctx, "a@example.com", "password1")

	for _, c := range [][2]string{{"a@example.com", "wrong-password"}, {"nobody@example.com", "password1"}} {
		if _, err := svc.Login(ctx, c[0], c[1]); !errors.Is(err, domain.ErrInvalidCredentials) {
			t.Errorf("Login(%q) = %v, want ErrInvalidCredentials", c[0], err)
		}
	}
}

func TestRefreshRotatesAndDetectsReuse(t *testing.T) {
	ctx := context.Background()
	svc, _ := newAuth(t)
	svc.Register(ctx, "a@example.com", "password1")
	first, _ := svc.Login(ctx, "a@example.com", "password1")

	second, err := svc.Refresh(ctx, first.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if second.RefreshToken == first.RefreshToken {
		t.Fatal("refresh token was not rotated")
	}

	// Replaying the old token is treated as theft...
	if _, err := svc.Refresh(ctx, first.RefreshToken); !errors.Is(err, domain.ErrRefreshTokenReused) {
		t.Fatalf("reuse: got %v, want ErrRefreshTokenReused", err)
	}
	// ...which also kills the legitimately rotated token.
	if _, err := svc.Refresh(ctx, second.RefreshToken); !errors.Is(err, domain.ErrInvalidToken) {
		t.Fatalf("after reuse: got %v, want ErrInvalidToken", err)
	}
}

func TestRefreshExpiry(t *testing.T) {
	ctx := context.Background()
	svc, clk := newAuth(t)
	svc.Register(ctx, "a@example.com", "password1")
	pair, _ := svc.Login(ctx, "a@example.com", "password1")

	clk.Advance(time.Hour + time.Second)
	if _, err := svc.Refresh(ctx, pair.RefreshToken); !errors.Is(err, domain.ErrInvalidToken) {
		t.Fatalf("got %v, want ErrInvalidToken", err)
	}
}

func TestLogoutRevokesRefreshToken(t *testing.T) {
	ctx := context.Background()
	svc, _ := newAuth(t)
	svc.Register(ctx, "a@example.com", "password1")
	pair, _ := svc.Login(ctx, "a@example.com", "password1")

	if err := svc.Logout(ctx, pair.RefreshToken); err != nil {
		t.Fatal(err)
	}
	if err := svc.Logout(ctx, pair.RefreshToken); err != nil {
		t.Fatalf("logout should be idempotent: %v", err)
	}
	if _, err := svc.Refresh(ctx, pair.RefreshToken); !errors.Is(err, domain.ErrInvalidToken) {
		t.Fatalf("got %v, want ErrInvalidToken", err)
	}
}

func isValidation(err error) bool {
	var ve *domain.ValidationError
	return errors.As(err, &ve)
}
