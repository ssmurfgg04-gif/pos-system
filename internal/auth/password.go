package auth

import (
        "strings"
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

// DefaultPasswords is the public installer defaults — rejected as NEW
// credentials (any case, even "admin123 "). Seeded rows may hold these
// values; choosing them again is the problem.
var DefaultPasswords = map[string]bool{
        "admin123": true, "cashier123": true, "designer123": true,
        "password": true, "letmein": true, "qwerty": true,
        "1234": true, "0000": true, "2222": true, "3333": true, "1111": true, "123456": true,
}

func IsDefaultPassword(pw string) bool {
        return DefaultPasswords[strings.ToLower(strings.TrimSpace(pw))]
}

// WeakPINs are rejected everywhere a PIN is set (create user, rotate,
// team join). One shared list so no entry point is softer than another:
// sequences, repeats and the two most-guessed Kenyan patterns.
var WeakPINs = map[string]bool{
        "0000": true, "1111": true, "2222": true, "3333": true,
        "4444": true, "5555": true, "6666": true, "7777": true,
        "8888": true, "9999": true, "1212": true, "1122": true,
        "1234": true, "4321": true, "2580": true, "0123": true,
        "6789": true, "1379": true, "1004": true, "2000": true,
}

// ValidPIN is the single source of truth for staff PINs: EXACTLY 4 digits,
// not a well-known/guessable pattern. Enforced at every entry point.
func ValidPIN(p string) bool {
        if len(p) != 4 {
                return false
        }
        for i := 0; i < 4; i++ {
                if p[i] < '0' || p[i] > '9' {
                        return false
                }
        }
        return !WeakPINs[p]
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
