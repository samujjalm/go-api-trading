package domain

import (
	"net/mail"
	"strings"
	"time"
)

const (
	MinPasswordLen = 8
	MaxPasswordLen = 72 // bcrypt silently ignores bytes beyond 72
)

type User struct {
	ID           string
	Email        string
	PasswordHash string
	CreatedAt    time.Time
}

// NormalizeEmail lower-cases and trims an email so lookups are
// case-insensitive, and rejects anything that isn't a bare address.
func NormalizeEmail(raw string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(raw))
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email {
		return "", invalid("email", "must be a valid email address")
	}
	return email, nil
}

func ValidatePassword(pw string) error {
	if len(pw) < MinPasswordLen || len(pw) > MaxPasswordLen {
		return invalid("password", "must be between 8 and 72 bytes")
	}
	return nil
}

// RefreshToken is the server-side record of an opaque refresh token. The
// token itself is never stored, only its hash.
type RefreshToken struct {
	ID         string
	UserID     string
	ExpiresAt  time.Time
	RevokedAt  *time.Time
	ReplacedBy *string // set when the token was rotated into a new one
}

func (t RefreshToken) Usable(now time.Time) bool {
	return t.RevokedAt == nil && now.Before(t.ExpiresAt)
}

// WasRotated reports whether this token has already been exchanged. Seeing it
// again means a copy leaked, since a well-behaved client discards it.
func (t RefreshToken) WasRotated() bool { return t.ReplacedBy != nil }
