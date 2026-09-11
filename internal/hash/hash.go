// Package hash provides bcrypt hashing with zero project dependencies
// (keeps database and auth decoupled — no import cycle).
package hash

import (
	"golang.org/x/crypto/bcrypt"
)

// Password hashes a plaintext password or PIN.
func Password(plain string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	return string(h), err
}

// Verify reports whether the plaintext matches the hash.
func Verify(hashValue, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hashValue), []byte(plain)) == nil
}
