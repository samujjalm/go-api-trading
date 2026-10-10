// Package domain holds the core business model: users, orders, trades and the
// matching rules. It has no knowledge of HTTP, SQL or any other technology,
// which keeps the most important code in the service trivially unit-testable.
package domain

import (
	"errors"
	"fmt"
)

// Sentinel errors. Adapters translate these into transport-specific responses
// (e.g. HTTP status codes), so the domain never has to know about them.
var (
	ErrNotFound           = errors.New("not found")
	ErrConflict           = errors.New("conflict")
	ErrInvalidCredentials = errors.New("invalid email or password")
	ErrInvalidToken       = errors.New("invalid or expired token")
	ErrOrderNotOpen       = errors.New("order is not open")

	// ErrRefreshTokenReused wraps ErrInvalidToken so callers that only care
	// about "unauthorized" can keep using errors.Is(err, ErrInvalidToken).
	ErrRefreshTokenReused = fmt.Errorf("%w: refresh token reuse detected", ErrInvalidToken)
)

// ValidationError describes bad input on a specific field.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Message }

func invalid(field, msg string) error { return &ValidationError{Field: field, Message: msg} }
