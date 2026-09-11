package auth

import (
	"posapp/internal/hash"
)

// HashPassword hashes passwords and PINs with bcrypt.
func HashPassword(plain string) (string, error) { return hash.Password(plain) }

// VerifyPassword reports whether the plaintext matches the hash.
func VerifyPassword(hashValue, plain string) bool { return hash.Verify(hashValue, plain) }
