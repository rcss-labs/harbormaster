package oauthsrv

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"strconv"
	"time"

	"github.com/rcss-labs/harbormaster/internal/apitoken"
	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const (
	minVerifierLength = 43
	maxVerifierLength = 128
	auditWriteTimeout = 5 * time.Second
)

// TokenRequest is a token endpoint request's form parameters, named as in OAuth. ClientIP is not a parameter: it is
// the caller's address for the audit log.
type TokenRequest struct {
	GrantType    string
	Code         string
	RedirectURI  string
	CodeVerifier string
	ClientID     string
	RefreshToken string
	Resource     string
	ClientIP     string
}

// TokenResponse is a successful token endpoint answer (RFC 6749 section 5.1).
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
}

// TokenError is a refused token request (RFC 6749 section 5.2). Description never carries a code or token.
type TokenError struct {
	Code        string
	Description string
}

func invalidGrant() *TokenError {
	return &TokenError{Code: "invalid_grant", Description: "The grant is invalid, expired or already used."}
}

// Exchange answers the token endpoint: authorization_code (with PKCE) and refresh_token (rotating). A malformed code
// or refresh token is refused before any database read; a well-formed code is spent by the first attempt, whatever
// its outcome.
func (s *Service) Exchange(ctx context.Context, req TokenRequest, now time.Time) (TokenResponse, *TokenError) {
	switch req.GrantType {
	case "authorization_code":
		return s.exchangeCode(ctx, req, now)
	case "refresh_token":
		return s.exchangeRefresh(ctx, req, now)
	case "":
		return TokenResponse{}, &TokenError{Code: "invalid_request", Description: "grant_type is required."}
	default:
		return TokenResponse{}, &TokenError{Code: "unsupported_grant_type",
			Description: "Only authorization_code and refresh_token are supported."}
	}
}

func (s *Service) exchangeCode(ctx context.Context, req TokenRequest, now time.Time) (TokenResponse, *TokenError) {
	if len(req.Code) != codeLength || !isBase64URL(req.Code) {
		return TokenResponse{}, invalidGrant()
	}
	code, err := s.Store.ConsumeOAuthCode(ctx, apitoken.Hash(req.Code))
	if errors.Is(err, store.ErrNotFound) {
		return TokenResponse{}, invalidGrant()
	}
	if err != nil {
		return TokenResponse{}, serverError()
	}
	// The code is spent from here on: every refusal below leaves it unusable.
	if !equalStrings(req.ClientID, code.ClientID) || !equalStrings(req.RedirectURI, code.RedirectURI) {
		return TokenResponse{}, invalidGrant()
	}
	if !verifierMatches(req.CodeVerifier, code.CodeChallenge) {
		return TokenResponse{}, invalidGrant()
	}
	if req.Resource != "" && req.Resource != code.Resource {
		return TokenResponse{}, &TokenError{Code: "invalid_target", Description: "resource does not match the grant."}
	}
	user, err := s.Store.GetHumanUser(ctx, code.UserID) // only a person holds an OAuth grant
	if errors.Is(err, store.ErrNotFound) {
		return TokenResponse{}, invalidGrant()
	}
	if err != nil {
		return TokenResponse{}, serverError()
	}
	subject, err := s.Store.LoadSubject(ctx, user.ID)
	if errors.Is(err, store.ErrNotFound) {
		return TokenResponse{}, invalidGrant()
	}
	if err != nil {
		return TokenResponse{}, serverError()
	}
	// The ceiling is decided again from the user row the store locks, so a promotion or SSO link that commits between
	// the load above and the insert still caps the grant.
	grantEnd := now.Add(auth.TokenMaxTTL(s.Cfg, user, subject.Admin))
	pair, err := newTokenPair(now, s.Cfg.OAuthAccessTTL, grantEnd)
	if err != nil {
		return TokenResponse{}, serverError()
	}
	pair.access.Name, pair.refresh.Name = code.ClientName, code.ClientName
	grant, err := s.Store.CreateOAuthGrantWithTokensWithin(ctx, store.OAuthGrant{
		UserID:     user.ID,
		ClientID:   code.ClientID,
		ClientName: code.ClientName,
		Scope:      Scope,
		Resource:   code.Resource,
		AuthSource: code.AuthSource,
		ExpiresAt:  grantEnd,
	}, pair.access, pair.refresh, func(locked store.User) time.Time {
		return now.Add(auth.TokenMaxTTL(s.Cfg, locked, locked.Admin))
	})
	if errors.Is(err, store.ErrNotFound) {
		return TokenResponse{}, invalidGrant()
	}
	if err != nil {
		return TokenResponse{}, serverError()
	}
	s.audit(ctx, store.AuditEntry{
		ActorUserID: &user.ID, ActorUsername: user.Username,
		Action: "oauth_grant", Target: "oauth_grant:" + strconv.FormatInt(grant.ID, 10),
		Summary: "Connected " + grant.ClientName + " with read-only access.",
		Details: map[string]any{"grant_id": grant.ID, "client_id": grant.ClientID, "client_name": grant.ClientName,
			"scope": grant.Scope, "expires_at": grant.ExpiresAt, "access_token_id": pair.access.PublicID},
		IP: req.ClientIP,
	})
	return pair.response(now, grant.ExpiresAt), nil
}

func (s *Service) exchangeRefresh(ctx context.Context, req TokenRequest, now time.Time) (TokenResponse, *TokenError) {
	parsed, ok := apitoken.Parse(req.RefreshToken)
	if !ok || parsed.Kind != apitoken.KindOAuthRefresh {
		return TokenResponse{}, invalidGrant()
	}
	if req.ClientID == "" {
		return TokenResponse{}, &TokenError{Code: "invalid_request", Description: "client_id is required."}
	}
	if req.Resource != "" && req.Resource != s.Resource() {
		return TokenResponse{}, &TokenError{Code: "invalid_target", Description: "resource does not match the grant."}
	}
	// The refresh token never outlives its grant; the store caps both new expiries at the grant's end, so the long
	// maximum here is only an upper bound.
	pair, err := newTokenPair(now, s.Cfg.OAuthAccessTTL, now.Add(s.Cfg.TokenMaxTTL))
	if err != nil {
		return TokenResponse{}, serverError()
	}
	grant, err := s.Store.RotateOAuthRefreshWithin(ctx, apitoken.DisplayID(parsed), apitoken.Hash(parsed.Secret),
		pair.access, pair.refresh, now, refreshRetryGrace, func(current store.User) time.Duration {
			return auth.TokenMaxTTL(s.Cfg, current, current.Admin)
		})
	switch {
	case errors.Is(err, store.ErrRefreshReused):
		s.auditRevoke(ctx, grant, "refresh_reuse", req.ClientIP)
		return TokenResponse{}, invalidGrant()
	case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrRefreshRetry):
		return TokenResponse{}, invalidGrant()
	case err != nil:
		return TokenResponse{}, serverError()
	}
	// A refresh token presented by a client other than the one it was issued to is treated like reuse: the token has
	// leaked, so the grant ends.
	if !equalStrings(req.ClientID, grant.ClientID) {
		if err := s.Store.DeleteOAuthGrant(ctx, grant.ID, grant.UserID); err != nil && !errors.Is(err, store.ErrNotFound) {
			return TokenResponse{}, serverError()
		}
		s.auditRevoke(ctx, grant, "client_mismatch", req.ClientIP)
		return TokenResponse{}, invalidGrant()
	}
	return pair.response(now, grant.ExpiresAt), nil
}

// auditRevoke records a grant ended by the token endpoint. The actor is the grant's user; no secret is written.
func (s *Service) auditRevoke(ctx context.Context, grant store.OAuthGrant, reason, ip string) {
	userID := grant.UserID
	username := ""
	if user, err := s.Store.GetUser(ctx, grant.UserID); err == nil {
		username = user.Username
	}
	s.audit(ctx, store.AuditEntry{
		ActorUserID: &userID, ActorUsername: username,
		Action: "oauth_revoke", Target: "oauth_grant:" + strconv.FormatInt(grant.ID, 10),
		Summary: "Disconnected " + grant.ClientName + ": a refresh token was misused.",
		Details: map[string]any{"grant_id": grant.ID, "client_id": grant.ClientID, "client_name": grant.ClientName,
			"reason": reason},
		IP: ip,
	})
}

// audit writes on a context detached from the request, so a client that hangs up does not lose the entry.
func (s *Service) audit(ctx context.Context, e store.AuditEntry) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), auditWriteTimeout)
	defer cancel()
	if err := s.Store.Audit(ctx, e); err != nil && s.Log != nil {
		s.Log.Error("audit write failed", "action", e.Action, "error", err)
	}
}

// tokenPair is a freshly generated access and refresh token, as handed out and as stored.
type tokenPair struct {
	accessRaw, refreshRaw string
	access, refresh       store.APIToken
}

// newTokenPair generates hmo_ and hmr_ tokens. The access token lives accessTTL, the refresh token until limit; both
// are capped at limit (the grant's end, or an upper bound the store caps further).
func newTokenPair(now time.Time, accessTTL time.Duration, limit time.Time) (tokenPair, error) {
	accessRaw, accessParsed, err := apitoken.Generate(apitoken.KindOAuthAccess)
	if err != nil {
		return tokenPair{}, err
	}
	refreshRaw, refreshParsed, err := apitoken.Generate(apitoken.KindOAuthRefresh)
	if err != nil {
		return tokenPair{}, err
	}
	accessEnd := now.Add(accessTTL)
	if accessEnd.After(limit) {
		accessEnd = limit
	}
	return tokenPair{
		accessRaw:  accessRaw,
		refreshRaw: refreshRaw,
		access: store.APIToken{PublicID: apitoken.DisplayID(accessParsed), Kind: string(apitoken.KindOAuthAccess),
			SecretHash: apitoken.Hash(accessParsed.Secret), Scope: string(auth.ScopeRead), ExpiresAt: accessEnd},
		refresh: store.APIToken{PublicID: apitoken.DisplayID(refreshParsed), Kind: string(apitoken.KindOAuthRefresh),
			SecretHash: apitoken.Hash(refreshParsed.Secret), Scope: string(auth.ScopeRead), ExpiresAt: limit},
	}, nil
}

// response is the answer for the pair, with expires_in taken from the access token's real end (never past grantEnd).
func (p tokenPair) response(now, grantEnd time.Time) TokenResponse {
	accessEnd := p.access.ExpiresAt
	if accessEnd.After(grantEnd) {
		accessEnd = grantEnd
	}
	expiresIn := int(accessEnd.Sub(now) / time.Second)
	if expiresIn < 0 {
		expiresIn = 0
	}
	return TokenResponse{AccessToken: p.accessRaw, TokenType: "Bearer", ExpiresIn: expiresIn,
		RefreshToken: p.refreshRaw, Scope: Scope}
}

// verifierMatches checks PKCE S256: a 43 to 128 character verifier of unreserved characters whose base64url SHA-256
// equals the stored challenge, compared in constant time.
func verifierMatches(verifier, challenge string) bool {
	if len(verifier) < minVerifierLength || len(verifier) > maxVerifierLength || !isPKCEVerifier(verifier) {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	computed := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(computed), []byte(challenge)) == 1
}

// isPKCEVerifier reports whether s uses only RFC 7636's unreserved characters.
func isPKCEVerifier(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '.', c == '_', c == '~':
		default:
			return false
		}
	}
	return true
}

func equalStrings(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func serverError() *TokenError {
	return &TokenError{Code: "server_error", Description: "Something went wrong on our side. Try again."}
}
