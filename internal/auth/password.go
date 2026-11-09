// Package auth implements administrator credentials and persistent sessions.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// RFC 9106's memory-constrained profile: 64 MiB, three passes, four lanes.
const hashPrefix = "$argon2id$v=19$m=65536,t=3,p=4$"

func RandomToken(bytes int) (string, error) {
	b := make([]byte, bytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func ValidUsername(username string) bool {
	return len(username) >= 1 && len(username) <= 32 &&
		strings.Trim(username, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-") == ""
}

func ValidPassword(password string) bool {
	return utf8.ValidString(password) && utf8.RuneCountInString(password) >= 10 && len(password) <= 1024
}

func HashPassword(password string) (string, error) {
	if !ValidPassword(password) {
		return "", fmt.Errorf("hash password: use at least 10 characters and at most 1024 bytes")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, 3, 64*1024, 4, 32)
	return hashPrefix + base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(key), nil
}

func VerifyPassword(encoded, password string) (bool, error) {
	// Accept only the profile we write. Corrupt/hostile database parameters must
	// never request unbounded memory, CPU or an invalid lane count.
	if !strings.HasPrefix(encoded, hashPrefix) || len(password) > 1024 {
		return false, fmt.Errorf("verify password: unsupported password hash or oversized password")
	}
	parts := strings.Split(strings.TrimPrefix(encoded, hashPrefix), "$")
	if len(parts) != 2 {
		return false, fmt.Errorf("verify password: malformed password hash")
	}
	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[0])
	if err != nil || len(salt) != 16 {
		return false, fmt.Errorf("verify password: invalid salt")
	}
	key, err := base64.RawStdEncoding.Strict().DecodeString(parts[1])
	if err != nil || len(key) != 32 {
		return false, fmt.Errorf("verify password: invalid derived key")
	}
	actual := argon2.IDKey([]byte(password), salt, 3, 64*1024, 4, 32)
	return subtle.ConstantTimeCompare(actual, key) == 1, nil
}
