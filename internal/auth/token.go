package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/rcss-labs/harbormaster/internal/apitoken"
	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// Scope is what an access token may do. Read is the only scope that exists; a route admits a token only when it is
// marked with the token's scope.
type Scope string

// ScopeRead lets a token call the routes marked Token: ScopeRead, and nothing else.
const ScopeRead Scope = "read"

// CredentialKind says how a request was authenticated.
type CredentialKind int

const (
	// CredentialSession is a browser session (cookie).
	CredentialSession CredentialKind = iota + 1
	// CredentialToken is an access token sent as Authorization: Bearer.
	CredentialToken
)

// Credential is how the request was authenticated. For a token, TokenPublicID is the display form (hmp_<id>),
// safe to log; the secret is never kept.
type Credential struct {
	Kind          CredentialKind
	TokenID       int64
	TokenPublicID string
	TokenKind     apitoken.Kind
	TokenName     string
	Scope         Scope
	ExpiresAt     time.Time
}

type credentialKey struct{}

// CredentialFrom returns how the request was authenticated, and false for an anonymous request.
func CredentialFrom(ctx context.Context) (Credential, bool) {
	if cred, ok := ctx.Value(credentialKey{}).(Credential); ok {
		return cred, true
	}
	if _, ok := SessionFrom(ctx); ok {
		return Credential{Kind: CredentialSession}, true
	}
	return Credential{}, false
}

// IsToken reports whether the request was authenticated by an access token.
func IsToken(ctx context.Context) bool {
	cred, ok := CredentialFrom(ctx)
	return ok && cred.Kind == CredentialToken
}

// TokenMaxTTL is the longest lifetime the user may give a new token: the short maximum for an SSO-linked user or an
// admin, the long one otherwise. It is the only place that decides it.
func TokenMaxTTL(cfg config.Config, user store.User, admin bool) time.Duration {
	if user.SSOLinked() || admin {
		return cfg.TokenMaxTTLSSO
	}
	return cfg.TokenMaxTTL
}

// AuthenticateToken resolves a raw bearer token to its stored row and user. It returns ErrUnauthenticated when the
// token is malformed, unknown, expired, not a personal token of the read scope, its secret does not match, or its
// user is gone. It records last use at most once a minute. Errors never carry the token or its secret.
func (a *Service) AuthenticateToken(ctx context.Context, raw, ip string) (store.APIToken, store.User, error) {
	parsed, ok := apitoken.Parse(raw)
	if !ok || parsed.Kind != apitoken.KindPAT {
		return store.APIToken{}, store.User{}, ErrUnauthenticated
	}
	tok, err := a.store.GetAPITokenByPublicID(ctx, apitoken.DisplayID(parsed))
	if errors.Is(err, store.ErrNotFound) {
		return store.APIToken{}, store.User{}, ErrUnauthenticated
	}
	if err != nil {
		return store.APIToken{}, store.User{}, err
	}
	gotHash := apitoken.Hash(parsed.Secret)
	if subtle.ConstantTimeCompare([]byte(gotHash), []byte(tok.SecretHash)) != 1 {
		return store.APIToken{}, store.User{}, ErrUnauthenticated
	}
	now := a.now()
	if !now.Before(tok.ExpiresAt) || tok.Kind != string(apitoken.KindPAT) || tok.Scope != string(ScopeRead) {
		return store.APIToken{}, store.User{}, ErrUnauthenticated
	}
	user, err := a.store.GetUser(ctx, tok.UserID)
	if errors.Is(err, store.ErrNotFound) {
		return store.APIToken{}, store.User{}, ErrUnauthenticated
	}
	if err != nil {
		return store.APIToken{}, store.User{}, err
	}
	if tok.LastUsedAt == nil || now.Sub(*tok.LastUsedAt) >= touchInterval {
		err := a.store.TouchAPIToken(ctx, tok.ID, now, ip)
		if errors.Is(err, store.ErrNotFound) {
			return store.APIToken{}, store.User{}, ErrUnauthenticated // revoked in the meantime
		}
		if err != nil {
			return store.APIToken{}, store.User{}, err
		}
		tok.LastUsedAt, tok.LastUsedIP = &now, ip
	}
	return tok, user, nil
}

// bearerToken returns the token from an "Authorization: Bearer <token>" header when the value has the exact shape of
// a Harbormaster token. Any other Authorization header is not a token request.
func bearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	const scheme = "bearer "
	if len(header) <= len(scheme) || !strings.EqualFold(header[:len(scheme)], scheme) {
		return "", false
	}
	raw := header[len(scheme):]
	if _, ok := apitoken.Parse(raw); !ok {
		return "", false
	}
	return raw, true
}

// writeTokenUnauthenticated answers a bearer token that did not resolve. It names no reason, so a caller cannot tell
// an unknown token from a revoked or expired one.
func writeTokenUnauthenticated(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	writeError(w, http.StatusUnauthorized, "unauthenticated", "The access token is not valid. It may have expired or been revoked.")
}

// serveToken is the token path of Middleware. The cookie is never read: a token that does not resolve ends the
// request with 401, on every route, and never continues as anonymous or as a session.
func (a *Service) serveToken(w http.ResponseWriter, r *http.Request, next http.Handler, raw string) {
	tok, user, err := a.AuthenticateToken(r.Context(), raw, ClientIP(r, a.cfg.TrustedProxy))
	if errors.Is(err, ErrUnauthenticated) {
		writeTokenUnauthenticated(w)
		return
	}
	if err != nil {
		failInternal(w, r, "authenticate token", err)
		return
	}
	subject, err := a.store.LoadSubject(r.Context(), user.ID)
	if errors.Is(err, store.ErrNotFound) {
		writeTokenUnauthenticated(w)
		return
	}
	if err != nil {
		failInternal(w, r, "load subject", err)
		return
	}
	subject.ReadOnly = true
	cred := Credential{
		Kind:          CredentialToken,
		TokenID:       tok.ID,
		TokenPublicID: tok.PublicID,
		TokenKind:     apitoken.Kind(tok.Kind),
		TokenName:     tok.Name,
		Scope:         Scope(tok.Scope),
		ExpiresAt:     tok.ExpiresAt,
	}
	ctx := context.WithValue(r.Context(), userKey{}, user)
	ctx = context.WithValue(ctx, subjectKey{}, subject)
	ctx = context.WithValue(ctx, credentialKey{}, cred)
	next.ServeHTTP(w, r.WithContext(ctx))
}

// ClientIP is the peer address, unless trustedProxy says exactly one trusted proxy (the load balancer) sits in front,
// in which case it is the rightmost X-Forwarded-For entry, the one that proxy appended.
func ClientIP(r *http.Request, trustedProxy bool) string {
	if trustedProxy {
		hops := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
		for i := len(hops) - 1; i >= 0; i-- {
			if hop := strings.TrimSpace(hops[i]); hop != "" {
				return hop
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
