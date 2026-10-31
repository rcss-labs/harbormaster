// Package auth implements password hashing, sessions, CSRF protection, login throttling,
// first-run setup and the HTTP middleware that enforces them.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// MinPasswordLength is the shortest accepted password, in characters.
const MinPasswordLength = 12

const (
	argonMemoryKiB = 64 * 1024
	argonTime      = 3
	argonThreads   = 2
	argonSaltLen   = 16
	argonKeyLen    = 32
)

// HashPassword returns a PHC-formatted argon2id hash of pw with a fresh random salt.
func HashPassword(pw string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemoryKiB, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemoryKiB, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword checks pw against a PHC argon2id hash, using the parameters stored in the hash.
// A malformed hash returns (false, error).
func VerifyPassword(hash, pw string) (bool, error) {
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, errors.New("malformed password hash")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errors.New("unsupported argon2 version")
	}
	var memory, passes uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &passes, &threads); err != nil {
		return false, errors.New("malformed argon2 parameters")
	}
	// Reject parameters that would let a corrupted row exhaust memory or CPU.
	if memory == 0 || memory > 1024*1024 || passes == 0 || passes > 20 || threads == 0 {
		return false, errors.New("argon2 parameters out of range")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return false, errors.New("malformed argon2 salt")
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false, errors.New("malformed argon2 hash")
	}
	got := argon2.IDKey([]byte(pw), salt, passes, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// ValidatePassword enforces the password policy.
func ValidatePassword(pw string) error {
	if len([]rune(pw)) < MinPasswordLength {
		return errors.New("Use at least 12 characters.")
	}
	return nil
}

// GenerateTemporaryPassword returns a password like "3f9a-c1d2-0b7e" for admin resets.
func GenerateTemporaryPassword() string {
	raw := make([]byte, 6)
	if _, err := rand.Read(raw); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return fmt.Sprintf("%02x%02x-%02x%02x-%02x%02x", raw[0], raw[1], raw[2], raw[3], raw[4], raw[5])
}
