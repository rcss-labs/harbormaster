package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/google/uuid"

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
	// RunID and RedactEnv are set for a run token: the agent run it belongs to, and whether environment values are
	// hidden from it.
	RunID     *uuid.UUID
	RedactEnv bool
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

// authenticatesAPI reports whether a token of kind may authenticate an API call: a personal token only while
// HM_API_TOKENS is on, an OAuth access token only while HM_OAUTH is on as well, a run token only while the agent is on
// (independent of the other two switches), an OAuth refresh token never (it is only ever exchanged at the token
// endpoint).
func (a *Service) authenticatesAPI(kind apitoken.Kind) bool {
	switch kind {
	case apitoken.KindPAT:
		return a.cfg.APITokens
	case apitoken.KindOAuthAccess:
		return a.cfg.APITokens && a.cfg.OAuth
	case apitoken.KindRun:
		return a.cfg.AgentEnabled()
	default:
		return false
	}
}

// AuthenticateToken resolves a raw bearer token to its stored row and user. It returns ErrUnauthenticated when the
// token is malformed, unknown, expired, of a kind that may not call the API (see authenticatesAPI), not of the read
// scope, its secret does not match, its user is gone, or the user's kind may not use it: a person's token is accepted as before,
// the agent identity's only when it is a run token, and any other kind never. It records last use at most once a minute, and for an OAuth
// access token its grant's last use with it. Errors never carry the token or its secret.
func (a *Service) AuthenticateToken(ctx context.Context, raw, ip string) (store.APIToken, store.User, error) {
	parsed, ok := apitoken.Parse(raw)
	if !ok || !a.authenticatesAPI(parsed.Kind) {
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
	if !now.Before(tok.ExpiresAt) || tok.Kind != string(parsed.Kind) || tok.Scope != string(ScopeRead) {
		return store.APIToken{}, store.User{}, ErrUnauthenticated
	}
	user, err := a.store.GetUser(ctx, tok.UserID)
	if errors.Is(err, store.ErrNotFound) {
		return store.APIToken{}, store.User{}, ErrUnauthenticated
	}
	if err != nil {
		return store.APIToken{}, store.User{}, err
	}
	// A person's token is as before. The agent identity authenticates only with a run token, and any other kind of user
	// (or one this code does not know) with nothing.
	switch user.Kind {
	case store.UserKindHuman:
	case store.UserKindAgent:
		if parsed.Kind != apitoken.KindRun {
			return store.APIToken{}, store.User{}, ErrUnauthenticated
		}
	default:
		return store.APIToken{}, store.User{}, ErrUnauthenticated
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
		if tok.GrantID != nil {
			if err := a.store.TouchOAuthGrant(ctx, *tok.GrantID, now); err != nil {
				return store.APIToken{}, store.User{}, err
			}
		}
	}
	return tok, user, nil
}

// bearerToken returns the token from an "Authorization: Bearer <token>" header when the value has the exact shape of
// a Harbormaster token and a token of its kind may authenticate right now (see authenticatesAPI). Any other
// Authorization header is not a token request and is ignored, so the cookie decides.
func (a *Service) bearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	const scheme = "bearer "
	if len(header) <= len(scheme) || !strings.EqualFold(header[:len(scheme)], scheme) {
		return "", false
	}
	raw := header[len(scheme):]
	parsed, ok := apitoken.Parse(raw)
	if !ok || !a.tokenPathOpen(parsed.Kind) {
		return "", false
	}
	return raw, true
}

// tokenPathOpen reports whether a bearer of kind takes the token path in Middleware: a run token while the agent is on,
// every other kind while HM_API_TOKENS is on. A bearer whose switch is off is ignored like any other header, not
// answered 401. Whether the kind may then authenticate is authenticatesAPI's decision.
func (a *Service) tokenPathOpen(kind apitoken.Kind) bool {
	if kind == apitoken.KindRun {
		return a.cfg.AgentEnabled()
	}
	return a.cfg.APITokens
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
	tok, user, err := a.AuthenticateToken(r.Context(), raw, ClientIP(r, a.cfg.TrustedProxy, a.cfg.MCPGatewayCIDRs))
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
		RunID:         tok.RunID,
		RedactEnv:     tok.RedactEnv,
	}
	ctx := context.WithValue(r.Context(), userKey{}, user)
	ctx = context.WithValue(ctx, subjectKey{}, subject)
	ctx = context.WithValue(ctx, credentialKey{}, cred)
	next.ServeHTTP(w, r.WithContext(ctx))
}

// ClientIP is the peer address, unless trustedProxy says exactly one trusted proxy (the load balancer) sits in front,
// in which case it is the rightmost X-Forwarded-For entry, the one that proxy appended.
//
// When that entry lies inside gatewayCIDRs the request came through the MCP gateway: the gateway sends exactly one
// X-Forwarded-For entry (the client address its public load balancer appended), so the entry to its left is the real
// client. Exactly one such extra hop is read, and only from a configured gateway address; anything further left is
// never used.
func ClientIP(r *http.Request, trustedProxy bool, gatewayCIDRs []netip.Prefix) string {
	if trustedProxy {
		hops := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
		for i := len(hops) - 1; i >= 0; i-- {
			hop := strings.TrimSpace(hops[i])
			if hop == "" {
				continue
			}
			if i > 0 && inPrefixes(hop, gatewayCIDRs) {
				if client, err := netip.ParseAddr(strings.TrimSpace(hops[i-1])); err == nil {
					return client.String()
				}
			}
			return hop
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func inPrefixes(addr string, prefixes []netip.Prefix) bool {
	parsed, err := netip.ParseAddr(addr)
	if err != nil {
		return false
	}
	parsed = parsed.Unmap()
	for _, prefix := range prefixes {
		if prefix.Contains(parsed) {
			return true
		}
	}
	return false
}
