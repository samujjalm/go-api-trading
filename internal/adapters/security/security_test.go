package security

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

var testSecret = []byte("0123456789abcdef0123456789abcdef")

func TestJWTRoundTrip(t *testing.T) {
	j := NewJWTIssuer(testSecret, "test", time.Minute)
	tok, err := j.Issue("user-123")
	if err != nil {
		t.Fatal(err)
	}
	sub, err := j.Verify(tok)
	if err != nil || sub != "user-123" {
		t.Fatalf("Verify = %q, %v", sub, err)
	}
}

func TestJWTRejects(t *testing.T) {
	j := NewJWTIssuer(testSecret, "test", time.Minute)
	valid, _ := j.Issue("u")

	past := NewJWTIssuer(testSecret, "test", time.Minute)
	past.now = func() time.Time { return time.Now().Add(-2 * time.Minute) }
	expired, _ := past.Issue("u")

	otherIssuer, _ := NewJWTIssuer(testSecret, "other", time.Minute).Issue("u")
	otherSecret, _ := NewJWTIssuer([]byte("ffffffffffffffffffffffffffffffff"), "test", time.Minute).Issue("u")
	algNone, _ := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.RegisteredClaims{
		Subject: "u", Issuer: "test", ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)),
	}).SignedString(jwt.UnsafeAllowNoneSignatureType)

	for name, tok := range map[string]string{
		"expired":      expired,
		"wrong issuer": otherIssuer,
		"wrong secret": otherSecret,
		"alg none":     algNone,
		"tampered":     valid[:len(valid)-2] + "xx",
		"garbage":      "not-a-jwt",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := j.Verify(tok); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestBcrypt(t *testing.T) {
	h := NewBcryptHasher(bcrypt.MinCost)
	hash, err := h.Hash("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !h.Compare(hash, "correct horse") || h.Compare(hash, "wrong horse") {
		t.Fatal("bcrypt compare misbehaves")
	}
}
