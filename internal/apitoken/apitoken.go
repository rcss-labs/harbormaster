// Package apitoken defines the format of Harbormaster bearer tokens:
// <prefix><id>_<secret>. The prefix says what kind of token it is, the id is
// a public lookup key, and the secret is the value only the caller holds.
// This package checks shape only; looking a token up is the store's job.
package apitoken

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

// Kind identifies which kind of token a string is, by its prefix.
type Kind string

const (
	KindPAT          Kind = "pat"
	KindOAuthAccess  Kind = "oauth_access"
	KindOAuthRefresh Kind = "oauth_refresh"
)

// Parsed is the three parts of a token string.
type Parsed struct {
	Kind   Kind
	ID     string
	Secret string
}

const (
	idLength     = 12
	secretLength = 43
	idRandBytes  = 8
	secretBytes  = 32
)

var prefixes = map[Kind]string{
	KindPAT:          "hmp_",
	KindOAuthAccess:  "hmo_",
	KindOAuthRefresh: "hmr_",
}

// base32Encoding is lowercase base32 without padding; the id alphabet is a-z2-7.
var base32Encoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// Generate returns a new random token of the given kind, both as the string
// to hand out and as its parts.
func Generate(kind Kind) (string, Parsed, error) {
	prefix, known := prefixes[kind]
	if !known {
		return "", Parsed{}, fmt.Errorf("unknown token kind %q", kind)
	}

	idBytes := make([]byte, idRandBytes)
	if _, err := rand.Read(idBytes); err != nil {
		return "", Parsed{}, fmt.Errorf("generate token id: %w", err)
	}
	id := strings.ToLower(base32Encoding.EncodeToString(idBytes))[:idLength]

	secretRaw := make([]byte, secretBytes)
	if _, err := rand.Read(secretRaw); err != nil {
		return "", Parsed{}, fmt.Errorf("generate token secret: %w", err)
	}
	secret := base64.RawURLEncoding.EncodeToString(secretRaw)

	parsed := Parsed{Kind: kind, ID: id, Secret: secret}
	return prefix + id + "_" + secret, parsed, nil
}

// Parse splits raw into its parts when it has the exact token shape.
// It does not check that the token exists.
func Parse(raw string) (Parsed, bool) {
	if len(raw) != len("hmp_")+idLength+1+secretLength {
		return Parsed{}, false
	}
	for kind, prefix := range prefixes {
		if !strings.HasPrefix(raw, prefix) {
			continue
		}
		rest := raw[len(prefix):]
		id := rest[:idLength]
		if rest[idLength] != '_' {
			return Parsed{}, false
		}
		secret := rest[idLength+1:]
		if !isLowerBase32(id) || !isBase64URL(secret) {
			return Parsed{}, false
		}
		return Parsed{Kind: kind, ID: id, Secret: secret}, true
	}
	return Parsed{}, false
}

// Hash returns the lowercase hex SHA-256 of a secret. Only this hash is stored.
func Hash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// DisplayID returns the prefix and id only, for logs and the UI. It never
// contains the secret.
func DisplayID(p Parsed) string {
	return prefixes[p.Kind] + p.ID
}

func isLowerBase32(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z') && !(c >= '2' && c <= '7') {
			return false
		}
	}
	return true
}

func isBase64URL(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}
