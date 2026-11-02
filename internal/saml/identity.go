package saml

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Identity is what a verified assertion asserts. Attributes is keyed by both
// the attribute Name and, where it does not collide with another Name, its
// FriendlyName.
type Identity struct {
	NameID, NameIDFormat string
	Attributes           map[string][]string
	AssertionID          string
	ResponseID           string
	NotOnOrAfter         time.Time // Conditions/@NotOnOrAfter; the replay-cache entry must live at least this long plus the skew
	SessionNotOnOrAfter  *time.Time
}

var (
	wellKnownEmail = []string{
		"email", "mail", "emailAddress",
		"http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress",
		"urn:oid:0.9.2342.19200300.100.1.3",
	}
	wellKnownDisplayName = []string{
		"displayName", "name",
		"http://schemas.xmlsoap.org/ws/2005/05/identity/claims/name",
		"http://schemas.microsoft.com/identity/claims/displayname",
		"urn:oid:2.16.840.1.113730.3.1.241",
	}
)

// Email returns the email attribute: the configured name if set (and only
// that), else the first well-known name present.
func (id Identity) Email(cfg Config) *string { return id.lookup(cfg.AttrEmail, wellKnownEmail) }

// DisplayName is Email's counterpart for the display name.
func (id Identity) DisplayName(cfg Config) *string {
	return id.lookup(cfg.AttrDisplayName, wellKnownDisplayName)
}

func (id Identity) lookup(configured string, wellKnown []string) *string {
	names := wellKnown
	if configured != "" {
		names = []string{configured}
	}
	for _, name := range names {
		for _, v := range id.Attributes[name] {
			if v = strings.TrimSpace(v); v != "" {
				return &v
			}
		}
	}
	return nil
}

const (
	maxUsernameLen = 32
	maxSuffix      = 99
	fallbackUser   = "sso-user"
)

// DeriveUsername implements spec D6 rule 4. taken reports whether a candidate
// is already in use (case-insensitively; candidates are lowercase).
func DeriveUsername(id Identity, cfg Config, taken func(string) bool) (string, error) {
	base := sanitizeUsername(usernameCandidate(id, cfg))
	if !taken(base) {
		return base, nil
	}
	for n := 2; n <= maxSuffix; n++ {
		suffix := fmt.Sprintf("-%d", n)
		trimmed := base
		if len(trimmed)+len(suffix) > maxUsernameLen {
			trimmed = trimmed[:maxUsernameLen-len(suffix)]
		}
		if candidate := trimmed + suffix; !taken(candidate) {
			return candidate, nil
		}
	}
	return "", &Error{Reason: ReasonUserCreateFailed, Err: errors.New("no free username")}
}

func usernameCandidate(id Identity, cfg Config) string {
	if cfg.AttrUsername != "" {
		if v := firstValue(id.Attributes[cfg.AttrUsername]); v != "" {
			return v
		}
	}
	if email := id.Email(cfg); email != nil {
		return localPart(*email)
	}
	return localPart(strings.TrimSpace(id.NameID))
}

func firstValue(values []string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

func localPart(s string) string {
	if i := strings.Index(s, "@"); i > 0 {
		return s[:i]
	}
	return s
}

func sanitizeUsername(candidate string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(candidate) {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-'
		if !ok {
			r = '-'
		}
		if r == '-' {
			if lastDash {
				continue
			}
			lastDash = true
		} else {
			lastDash = false
		}
		b.WriteRune(r)
	}
	s := strings.TrimLeft(b.String(), "0123456789._-")
	if s == "" {
		return fallbackUser
	}
	if len(s) > maxUsernameLen {
		s = s[:maxUsernameLen]
	}
	if len(s) < 2 { // the username pattern needs at least two characters
		s += "-"
	}
	return s
}
