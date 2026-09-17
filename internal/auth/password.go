package auth

import (
        "sync"

        "posapp/internal/hash"
)

var (
        dummyOnce sync.Once
        dummyHash string
)

// EqualizeLoginTiming burns one bcrypt verification on unknown-user logins
// so valid and invalid usernames take the same time (no enumeration oracle).
func EqualizeLoginTiming(plain string) {
        dummyOnce.Do(func() {
                h, err := hash.Password("ledgerpos-timing-dummy-value")
                if err == nil {
                        dummyHash = h
                }
        })
        if dummyHash != "" {
                VerifyPassword(dummyHash, plain)
        }
}

// HashPassword hashes passwords and PINs with bcrypt.
func HashPassword(plain string) (string, error) { return hash.Password(plain) }

// VerifyPassword reports whether the plaintext matches the hash.
func VerifyPassword(hashValue, plain string) bool { return hash.Verify(hashValue, plain) }

// Username rules: 3–32 chars of letters, digits, dot, underscore, hyphen.
// Anything else (angle brackets, quotes, slashes, control chars, emoji)
// is rejected at every entry point so usernames are safe in URLs, logs,
// and rendered output by construction.
func ValidUsername(u string) bool {
        if len(u) < 3 || len(u) > 32 {
                return false
        }
        for i := 0; i < len(u); i++ {
                c := u[i]
                ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
                        c == '.' || c == '_' || c == '-'
                if !ok {
                        return false
                }
        }
        return true
}

// MaxPasswordLen caps passwords (bcrypt silently truncates past 72 bytes;
// reject absurd lengths explicitly instead).
const MaxPasswordLen = 128

func ValidPassword(pw string) bool {
        return len(pw) >= 6 && len(pw) <= MaxPasswordLen
}
