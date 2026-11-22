package oauthsrv

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/url"
	"time"

	"github.com/rcss-labs/harbormaster/internal/apitoken"
	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// Scope is the only OAuth scope that exists: read-only access as the approving user.
const Scope = "harbormaster:read"

const (
	codeLifetime      = 60 * time.Second
	codeRandomBytes   = 32
	codeLength        = 43 // base64url of 32 bytes, no padding
	challengeLength   = 43 // base64url of a SHA-256, no padding
	maxStateLength    = 512
	refreshRetryGrace = 60 * time.Second
)

// Store is what the authorization server needs from Postgres. *store.Store satisfies it; tests may wrap it.
type Store interface {
	CreateOAuthCode(ctx context.Context, c store.OAuthCode) error
	ConsumeOAuthCode(ctx context.Context, codeHash string) (store.OAuthCode, error)
	CreateOAuthGrantWithTokens(ctx context.Context, g store.OAuthGrant, access, refresh store.APIToken) (store.OAuthGrant, error)
	RotateOAuthRefresh(ctx context.Context, publicID, secretHash string, newAccess, newRefresh store.APIToken,
		now time.Time, grace time.Duration) (store.OAuthGrant, error)
	GetUser(ctx context.Context, id int64) (store.User, error)
	LoadSubject(ctx context.Context, userID int64) (authz.Subject, error)
}

// Service is the authorization server. Cfg.MCPPublicURL is the issuer, and its "/mcp" the only resource.
type Service struct {
	Store   Store
	Clients *ClientResolver
	Cfg     config.Config
	Now     func() time.Time
}

// NewService builds the service from configuration, with a client resolver over the guarded HTTP client.
func NewService(st Store, cfg config.Config) *Service {
	return &Service{
		Store:   st,
		Clients: NewClientResolver(cfg.OAuthClientHosts, cfg.OAuthRedirectURIs),
		Cfg:     cfg,
		Now:     time.Now,
	}
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Issuer is the authorization server's identifier, sent as iss on every authorization response.
func (s *Service) Issuer() string { return s.Cfg.MCPPublicURL }

// Resource is the one protected resource tokens are issued for: the gateway's MCP endpoint.
func (s *Service) Resource() string { return s.Cfg.MCPPublicURL + "/mcp" }

// AuthorizeRequest is an authorization request's parameters, named as in OAuth.
type AuthorizeRequest struct {
	ResponseType        string `json:"response_type"`
	ClientID            string `json:"client_id"`
	RedirectURI         string `json:"redirect_uri"`
	CodeChallenge       string `json:"code_challenge"`
	CodeChallengeMethod string `json:"code_challenge_method"`
	Scope               string `json:"scope"`
	State               string `json:"state"`
	Resource            string `json:"resource"`
}

// AuthorizeView is what the consent page shows.
type AuthorizeView struct {
	ClientName    string `json:"client_name"`
	ClientHost    string `json:"client_host"`
	RedirectHost  string `json:"redirect_host"`
	Scope         string `json:"scope"`
	ReadOnly      bool   `json:"read_only"`
	ExpiresInDays int    `json:"expires_in_days"`
	SeesLogs      bool   `json:"sees_logs"`
}

// AuthorizeError is a refused request. Redirectable is false while the client or its redirect URI is not validated:
// such an error is shown to the user and never sent anywhere.
type AuthorizeError struct {
	Code         string
	Description  string
	Redirectable bool
}

func shownError(code, description string) *AuthorizeError {
	return &AuthorizeError{Code: code, Description: description}
}

func redirectError(code, description string) *AuthorizeError {
	return &AuthorizeError{Code: code, Description: description, Redirectable: true}
}

// checkClient validates the client and the redirect URI, in that order. Every failure here is not redirectable.
func (s *Service) checkClient(ctx context.Context, req AuthorizeRequest) (Client, *AuthorizeError) {
	if s.Clients == nil {
		return Client{}, shownError("server_error", "Connecting applications is not set up.")
	}
	client, err := s.Clients.Resolve(ctx, req.ClientID)
	switch {
	case errors.Is(err, ErrClientNotAllowed):
		return Client{}, shownError("invalid_client", "This application is not allowed to connect to Harbormaster.")
	case errors.Is(err, ErrClientUnavailable):
		return Client{}, shownError("temporarily_unavailable",
			"Harbormaster could not read this application's details. Try again in a minute.")
	case err != nil:
		return Client{}, shownError("invalid_client", "This application's details are not valid.")
	}
	if !s.Clients.RedirectAllowed(client, req.RedirectURI) {
		return Client{}, shownError("invalid_request",
			"The application asked to return to an address it may not use. Nothing was sent to it.")
	}
	return client, nil
}

// checkRequest validates the rest of the request, after the client and redirect URI passed. Failures redirect back
// to the client with the named OAuth error.
func (s *Service) checkRequest(req AuthorizeRequest) *AuthorizeError {
	if req.ResponseType != "code" {
		return redirectError("unsupported_response_type", "Only the authorization code flow is supported.")
	}
	if req.CodeChallengeMethod != "S256" {
		return redirectError("invalid_request", "PKCE with code_challenge_method S256 is required.")
	}
	if len(req.CodeChallenge) != challengeLength || !isBase64URL(req.CodeChallenge) {
		return redirectError("invalid_request", "code_challenge must be a base64url SHA-256 (43 characters).")
	}
	if req.Scope != "" && req.Scope != Scope {
		return redirectError("invalid_scope", "The only scope is "+Scope+".")
	}
	if req.Resource != s.Resource() {
		return redirectError("invalid_target", "resource must be "+s.Resource()+".")
	}
	if len(req.State) > maxStateLength {
		return redirectError("invalid_request", "state is too long.")
	}
	return nil
}

// Validate checks an authorization request for the signed-in user and returns what the consent page shows.
func (s *Service) Validate(ctx context.Context, req AuthorizeRequest, user store.User, subject authz.Subject) (AuthorizeView, *AuthorizeError) {
	client, aerr := s.checkClient(ctx, req)
	if aerr != nil {
		return AuthorizeView{}, aerr
	}
	if aerr := s.checkRequest(req); aerr != nil {
		return AuthorizeView{}, aerr
	}
	return AuthorizeView{
		ClientName:    client.Name,
		ClientHost:    hostOf(client.ID),
		RedirectHost:  hostOf(req.RedirectURI),
		Scope:         Scope,
		ReadOnly:      true,
		ExpiresInDays: int(auth.TokenMaxTTL(s.Cfg, user, subject.Admin) / (24 * time.Hour)),
		SeesLogs:      seesLogs(subject),
	}, nil
}

// seesLogs reports whether the subject may read logs anywhere: an admin, or Logs on at least one tag.
func seesLogs(subject authz.Subject) bool {
	if subject.Admin {
		return true
	}
	for tag := range subject.Perms {
		if authz.Can(subject, authz.Logs, tag) {
			return true
		}
	}
	return false
}

// Approve re-validates the request, stores a 60-second code bound to it and returns where to send the browser:
// redirect_uri with code, state and iss. The code itself is never stored or logged; only its hash is.
func (s *Service) Approve(ctx context.Context, req AuthorizeRequest, user store.User, authSource string) (string, *AuthorizeError) {
	client, aerr := s.checkClient(ctx, req)
	if aerr != nil {
		return "", aerr
	}
	if aerr := s.checkRequest(req); aerr != nil {
		return s.errorRedirect(req, aerr), aerr
	}
	code, err := newCode()
	if err != nil {
		return "", shownError("server_error", "Something went wrong on our side. Try again.")
	}
	if authSource == "" {
		authSource = "password"
	}
	err = s.Store.CreateOAuthCode(ctx, store.OAuthCode{
		CodeHash:      apitoken.Hash(code),
		UserID:        user.ID,
		ClientID:      client.ID,
		ClientName:    client.Name,
		RedirectURI:   req.RedirectURI,
		CodeChallenge: req.CodeChallenge,
		Scope:         Scope,
		Resource:      req.Resource,
		AuthSource:    authSource,
		ExpiresAt:     s.now().Add(codeLifetime),
	})
	if err != nil {
		return "", shownError("server_error", "Something went wrong on our side. Try again.")
	}
	return s.withParams(req.RedirectURI, map[string]string{"code": code, "state": req.State}), nil
}

// Deny validates the client and redirect URI and returns where to send the browser: redirect_uri with
// error=access_denied, state and iss.
func (s *Service) Deny(ctx context.Context, req AuthorizeRequest) (string, *AuthorizeError) {
	if _, aerr := s.checkClient(ctx, req); aerr != nil {
		return "", aerr
	}
	state := req.State
	if len(state) > maxStateLength {
		state = ""
	}
	return s.withParams(req.RedirectURI, map[string]string{
		"error": "access_denied", "error_description": "The user denied the request.", "state": state,
	}), nil
}

// ErrorRedirect is where to send the browser for a redirectable error; empty when the error is not redirectable.
// The caller must have validated the client and redirect URI, which a redirectable error implies.
func (s *Service) ErrorRedirect(req AuthorizeRequest, aerr *AuthorizeError) string {
	return s.errorRedirect(req, aerr)
}

func (s *Service) errorRedirect(req AuthorizeRequest, aerr *AuthorizeError) string {
	if aerr == nil || !aerr.Redirectable {
		return ""
	}
	state := req.State
	if len(state) > maxStateLength {
		state = ""
	}
	return s.withParams(req.RedirectURI, map[string]string{
		"error": aerr.Code, "error_description": aerr.Description, "state": state,
	})
}

// withParams adds params (empty values skipped) and iss to a validated redirect URI.
func (s *Service) withParams(redirectURI string, params map[string]string) string {
	target, err := url.Parse(redirectURI)
	if err != nil {
		return ""
	}
	query := target.Query()
	for name, value := range params {
		if value != "" {
			query.Set(name, value)
		}
	}
	query.Set("iss", s.Issuer())
	target.RawQuery = query.Encode()
	return target.String()
}

func newCode() (string, error) {
	raw := make([]byte, codeRandomBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func hostOf(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return parsed.Host
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
