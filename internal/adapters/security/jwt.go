package security

import (
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/samujjal/go-service/internal/domain"
)

// JWTIssuer issues and verifies short-lived HS256 access tokens. The token
// subject is the user ID.
type JWTIssuer struct {
	secret []byte
	issuer string
	ttl    time.Duration
	now    func() time.Time
}

func NewJWTIssuer(secret []byte, issuer string, ttl time.Duration) *JWTIssuer {
	return &JWTIssuer{secret: secret, issuer: issuer, ttl: ttl, now: time.Now}
}

func (j *JWTIssuer) TTL() time.Duration { return j.ttl }

func (j *JWTIssuer) Issue(userID string) (string, error) {
	now := j.now()
	claims := jwt.RegisteredClaims{
		Subject:   userID,
		Issuer:    j.issuer,
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(j.ttl)),
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(j.secret)
}

func (j *JWTIssuer) Verify(token string) (string, error) {
	claims := &jwt.RegisteredClaims{}
	_, err := jwt.ParseWithClaims(token, claims,
		func(*jwt.Token) (any, error) { return j.secret, nil },
		// Pinning the algorithm blocks "alg: none" and algorithm-confusion attacks.
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(j.issuer),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(j.now),
	)
	if err != nil || claims.Subject == "" {
		return "", domain.ErrInvalidToken
	}
	return claims.Subject, nil
}
