package auth

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

// CSRFHeader carries the per-session CSRF token on state-changing requests.
const CSRFHeader = "X-CSRF-Token"

// randomToken returns 32 random bytes as unpadded base64url.
func randomToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
