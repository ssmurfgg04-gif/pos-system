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
	IatMs  int64  `json:"iat_ms,omitempty"`
	jwt.RegisteredClaims
}

// IssueToken signs a HS256 token for the user in a shop. The secret is
// per-box (shared across that box's shops, stored in the tenant registry)
// — never hardcoded. Empty shop means the legacy single-shop context.
func IssueToken(secret []byte, userID int64, username, shopID string) (string, error) {
	now := time.Now()
	c := claims{
		UserID: userID,
		ShopID: shopID,
		IatMs:  now.UnixMilli(),
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   username,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(TokenTTL)),
		},
	}
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, c)
	return t.SignedString(secret)
}

// ParseChangedAt parses a password_changed_at stamp. Unparseable (or empty,
// pre-feature) values yield the zero time, which predates every token —
// sessions survive data quirks; only well-formed rotations invalidate.
func ParseChangedAt(s string) time.Time {
        t, err := time.Parse("2006-01-02T15:04:05.000Z", s)
        if err != nil {
                return time.Time{}
        }
        return t
}

// ParseToken validates and returns the user id, shop id, and issue time
// (for credential-change invalidation).
func ParseToken(secret []byte, tokenStr string) (int64, string, time.Time, error) {
	var c claims
	_, err := jwt.ParseWithClaims(tokenStr, &c, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, ErrInvalidToken
		}
		return secret, nil
	})
	if err != nil {
		return 0, "", time.Time{}, ErrInvalidToken
	}
	if c.UserID == 0 {
		return 0, "", time.Time{}, ErrInvalidToken
	}
	issued := time.Time{}
	if c.IatMs != 0 {
		issued = time.UnixMilli(c.IatMs)
	} else if c.IssuedAt != nil {
		issued = c.IssuedAt.Time
	}
	return c.UserID, c.ShopID, issued, nil
}
