package auth

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const TokenTTL = 12 * time.Hour

var ErrInvalidToken = errors.New("invalid token")

type claims struct {
	UserID int64  `json:"uid"`
	ShopID string `json:"shop,omitempty"`
	jwt.RegisteredClaims
}

// IssueToken signs a HS256 token for the user in a shop. The secret is
// per-box (shared across that box's shops, stored in the tenant registry)
// — never hardcoded. Empty shop means the legacy single-shop context.
func IssueToken(secret []byte, userID int64, username, shopID string) (string, error) {
	c := claims{
		UserID: userID,
		ShopID: shopID,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   username,
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(TokenTTL)),
		},
	}
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, c)
	return t.SignedString(secret)
}

// ParseToken validates and returns the user id and shop id.
func ParseToken(secret []byte, tokenStr string) (int64, string, error) {
	var c claims
	_, err := jwt.ParseWithClaims(tokenStr, &c, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, ErrInvalidToken
		}
		return secret, nil
	})
	if err != nil {
		return 0, "", ErrInvalidToken
	}
	if c.UserID == 0 {
		return 0, "", ErrInvalidToken
	}
	return c.UserID, c.ShopID, nil
}
