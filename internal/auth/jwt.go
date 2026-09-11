package auth

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const TokenTTL = 12 * time.Hour

var ErrInvalidToken = errors.New("invalid token")

type claims struct {
	UserID int64 `json:"uid"`
	jwt.RegisteredClaims
}

// IssueToken signs a HS256 token for the user. The secret is per-installation
// (generated on first boot, stored in settings) — never hardcoded.
func IssueToken(secret []byte, userID int64, username string) (string, error) {
	c := claims{
		UserID: userID,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   username,
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(TokenTTL)),
		},
	}
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, c)
	return t.SignedString(secret)
}

// ParseToken validates and returns the user id.
func ParseToken(secret []byte, tokenStr string) (int64, error) {
	var c claims
	_, err := jwt.ParseWithClaims(tokenStr, &c, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, ErrInvalidToken
		}
		return secret, nil
	})
	if err != nil {
		return 0, ErrInvalidToken
	}
	if c.UserID == 0 {
		return 0, ErrInvalidToken
	}
	return c.UserID, nil
}
