package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rcss-labs/harbormaster/internal/apitoken"
	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/logs"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const maxTokenNameLength = 60

// TokenDTO is an access token as listed. It never carries the secret.
type TokenDTO struct {
	ID         int64      `json:"id"`
	PublicID   string     `json:"public_id"`
	Name       string     `json:"name"`
	Kind       string     `json:"kind"`
	Scope      string     `json:"scope"`
	AuthSource string     `json:"auth_source"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	LastUsedIP string     `json:"last_used_ip"`
}

// GrantDTO is an OAuth connection ("connected app") as listed. It never carries a token.
type GrantDTO struct {
	ID         int64      `json:"id"`
	ClientID   string     `json:"client_id"`
	ClientName string     `json:"client_name"`
	Scope      string     `json:"scope"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

func grantDTOs(grants []store.OAuthGrant) []GrantDTO {
	out := make([]GrantDTO, 0, len(grants))
	for _, g := range grants {
		out = append(out, GrantDTO{ID: g.ID, ClientID: g.ClientID, ClientName: g.ClientName, Scope: g.Scope,
			CreatedAt: g.CreatedAt, ExpiresAt: g.ExpiresAt, LastUsedAt: g.LastUsedAt})
	}
	return out
}

// listGrants returns the user's connected apps while OAuth is on, and an empty list otherwise.
func (s *Server) listGrants(ctx context.Context, userID int64) ([]GrantDTO, error) {
	if !s.oauthOn() {
		return []GrantDTO{}, nil
	}
	grants, err := s.Store.ListOAuthGrantsForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	return grantDTOs(grants), nil
}

// TokenPolicyDTO is what the UI needs to offer token creation: this user's limits and today's log search budget.
type TokenPolicyDTO struct {
	MaxTTLDays                 int    `json:"max_ttl_days"`
	MaxTokens                  int    `json:"max_tokens"`
	SSOLinked                  bool   `json:"sso_linked"`
	Admin                      bool   `json:"admin"`
	MCPURL                     string `json:"mcp_url"`
	OAuth                      bool   `json:"oauth"`
	LogSearchDailyBytes        int64  `json:"log_search_daily_bytes"`
	LogSearchUsedBytes         int64  `json:"log_search_used_bytes"`
	LogSearchMaxBytesPerSearch int64  `json:"log_search_max_bytes_per_search"`
}

// TokenListResponse is the body of GET /api/tokens. Grants are the connected apps (empty while OAuth is off).
type TokenListResponse struct {
	Items  []TokenDTO     `json:"items"`
	Grants []GrantDTO     `json:"grants"`
	Policy TokenPolicyDTO `json:"policy"`
}

// UserTokensResponse is the body of GET /api/users/{id}/tokens.
type UserTokensResponse struct {
	Items  []TokenDTO `json:"items"`
	Grants []GrantDTO `json:"grants"`
}

// CreateTokenResponse is the body of POST /api/tokens. Secret is shown here once and never again.
type CreateTokenResponse struct {
	Token  TokenDTO `json:"token"`
	Secret string   `json:"secret"`
}

// WhoAmIUserDTO, WhoAmITokenDTO, WhoAmITagDTO and WhoAmILogSearchDTO make up WhoAmIDTO.
type WhoAmIUserDTO struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Admin       bool   `json:"admin"`
}

type WhoAmITokenDTO struct {
	PublicID  string    `json:"public_id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	Scope     string    `json:"scope"`
	ExpiresAt time.Time `json:"expires_at"`
}

type WhoAmITagDTO struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	CanView bool   `json:"can_view"`
	CanLogs bool   `json:"can_logs"`
}

type WhoAmILogSearchDTO struct {
	DailyBytes        int64     `json:"daily_bytes"`
	UsedBytes         int64     `json:"used_bytes"`
	MaxBytesPerSearch int64     `json:"max_bytes_per_search"`
	ResetsAt          time.Time `json:"resets_at"`
}

// WhoAmIDTO is the body of GET /api/tokens/current: who the token acts as and what it may see.
type WhoAmIDTO struct {
	User      WhoAmIUserDTO      `json:"user"`
	Token     WhoAmITokenDTO     `json:"token"`
	Tags      []WhoAmITagDTO     `json:"tags"`
	LogSearch WhoAmILogSearchDTO `json:"log_search"`
}

func tokenDTO(t store.APIToken) TokenDTO {
	return TokenDTO{ID: t.ID, PublicID: t.PublicID, Name: t.Name, Kind: t.Kind, Scope: t.Scope,
		AuthSource: t.AuthSource, CreatedAt: t.CreatedAt, ExpiresAt: t.ExpiresAt,
		LastUsedAt: t.LastUsedAt, LastUsedIP: t.LastUsedIP}
}

func tokenDTOs(tokens []store.APIToken) []TokenDTO {
	out := make([]TokenDTO, 0, len(tokens))
	for _, t := range tokens {
		out = append(out, tokenDTO(t))
	}
	return out
}

// tokenLogLimitsGB are the per-search cap and the daily budget, in GiB, of token-initiated log searches by this user:
// the agent identity's own settings, a person's the access token settings. An unknown kind gets a budget of 0, which
// refuses the search.
func (s *Server) tokenLogLimitsGB(user store.User) (maxGB, dailyGB int) {
	switch user.Kind {
	case store.UserKindHuman:
		return s.Cfg.TokenLogSearchMaxGB, s.Cfg.TokenLogSearchDailyGB
	case store.UserKindAgent:
		return s.Cfg.AgentIdentityLogSearchMaxGB, s.Cfg.AgentIdentityLogSearchDailyGB
	default:
		return 0, 0
	}
}

// tokenCapBytes is the log search cap for one token-initiated search by the user before the day's remainder is
// applied: the user's cap, lowered to the global cap when that is set and smaller.
func (s *Server) tokenCapBytes(user store.User) int64 {
	maxGB, _ := s.tokenLogLimitsGB(user)
	capBytes := int64(maxGB) * logs.BytesPerGiB
	global := int64(s.Cfg.LogSearchMaxGB) * logs.BytesPerGiB
	if global > 0 && (capBytes == 0 || global < capBytes) {
		capBytes = global
	}
	return capBytes
}

// nextUTCMidnight is when the day's token log search budget resets.
func nextUTCMidnight(now time.Time) time.Time {
	y, m, d := now.UTC().Date()
	return time.Date(y, m, d+1, 0, 0, 0, 0, time.UTC)
}

// tokenLogSearchState is today's token log search budget for the user, from the user's own ledger row.
func (s *Server) tokenLogSearchState(ctx context.Context, user store.User) (WhoAmILogSearchDTO, error) {
	now := s.now()
	_, dailyGB := s.tokenLogLimitsGB(user)
	dto := WhoAmILogSearchDTO{
		DailyBytes:        int64(dailyGB) * logs.BytesPerGiB,
		MaxBytesPerSearch: s.tokenCapBytes(user),
		ResetsAt:          nextUTCMidnight(now),
	}
	used, _, err := s.Store.TokenLogScanUsage(ctx, user.ID, now)
	if err != nil {
		return dto, err
	}
	dto.UsedBytes = used
	return dto, nil
}

func (s *Server) tokenPolicy(ctx context.Context, user store.User, admin bool) (TokenPolicyDTO, error) {
	state, err := s.tokenLogSearchState(ctx, user)
	if err != nil {
		return TokenPolicyDTO{}, err
	}
	return TokenPolicyDTO{
		MaxTTLDays:                 int(auth.TokenMaxTTL(s.Cfg, user, admin) / (24 * time.Hour)),
		MaxTokens:                  s.Cfg.TokenMaxPerUser,
		SSOLinked:                  user.SSOLinked(),
		Admin:                      admin,
		MCPURL:                     s.Cfg.MCPPublicURL,
		OAuth:                      s.oauthOn(),
		LogSearchDailyBytes:        state.DailyBytes,
		LogSearchUsedBytes:         state.UsedBytes,
		LogSearchMaxBytesPerSearch: state.MaxBytesPerSearch,
	}, nil
}

func (s *Server) listTokens(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFrom(r.Context())
	subject := auth.SubjectFrom(r.Context())
	tokens, err := s.Store.ListAPITokensForUser(r.Context(), user.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	grants, err := s.listGrants(r.Context(), user.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	policy, err := s.tokenPolicy(r.Context(), user, subject.Admin)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, TokenListResponse{Items: tokenDTOs(tokens), Grants: grants, Policy: policy})
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFrom(r.Context())
	subject := auth.SubjectFrom(r.Context())
	var body struct {
		Name          string `json:"name"`
		ExpiresInDays int    `json:"expires_in_days"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		s.fail(w, err)
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" || utf8.RuneCountInString(name) > maxTokenNameLength {
		s.fail(w, invalid(fmt.Sprintf("Give the token a name of 1 to %d characters.", maxTokenNameLength)))
		return
	}
	maxTTL := auth.TokenMaxTTL(s.Cfg, user, subject.Admin)
	maxDays := int(maxTTL / (24 * time.Hour))
	days := body.ExpiresInDays
	if days == 0 {
		days = maxDays
	}
	if days < 1 || days > maxDays {
		s.fail(w, invalid(fmt.Sprintf("Choose a lifetime from 1 to %d days.", maxDays)))
		return
	}
	raw, parsed, err := apitoken.Generate(apitoken.KindPAT)
	if err != nil {
		s.fail(w, err)
		return
	}
	authSource := "password"
	if sess, ok := auth.SessionFrom(r.Context()); ok && sess.AuthSource != "" {
		authSource = sess.AuthSource
	}
	// The subject was loaded before the row lock; the store re-reads admin and SSO state under it and clamps.
	issuedAt := s.now()
	created, err := s.Store.CreateAPITokenWithin(r.Context(), store.APIToken{
		PublicID:   apitoken.DisplayID(parsed),
		UserID:     user.ID,
		Kind:       string(apitoken.KindPAT),
		Name:       name,
		SecretHash: apitoken.Hash(parsed.Secret),
		Scope:      string(auth.ScopeRead),
		AuthSource: authSource,
		ExpiresAt:  issuedAt.Add(time.Duration(days) * 24 * time.Hour),
	}, s.Cfg.TokenMaxPerUser, func(locked store.User) time.Time {
		return issuedAt.Add(auth.TokenMaxTTL(s.Cfg, locked, locked.Admin))
	})
	if errors.Is(err, store.ErrTokenLimit) {
		s.fail(w, &APIError{Status: http.StatusConflict, Code: "conflict",
			Message: fmt.Sprintf("You already have %d tokens. Revoke one first.", s.Cfg.TokenMaxPerUser)})
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	s.auditAdmin(r, "token_create", "token:"+created.PublicID, "Created access token "+created.PublicID+" ("+name+").",
		map[string]any{"public_id": created.PublicID, "name": name, "expires_at": created.ExpiresAt})
	writeJSON(w, http.StatusCreated, CreateTokenResponse{Token: tokenDTO(created), Secret: raw})
}

// revokeToken deletes one of ownerID's tokens and audits it; actingFor is the admin's view of the target ("" for a
// user revoking their own).
func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request, ownerID, tokenID int64, targetUsername string) {
	tokens, err := s.Store.ListAPITokensForUser(r.Context(), ownerID)
	if err != nil {
		s.fail(w, err)
		return
	}
	var found *store.APIToken
	for i := range tokens {
		if tokens[i].ID == tokenID {
			found = &tokens[i]
		}
	}
	if found == nil {
		s.fail(w, store.ErrNotFound)
		return
	}
	if err := s.Store.DeleteAPIToken(r.Context(), tokenID, ownerID); err != nil {
		s.fail(w, err)
		return
	}
	details := map[string]any{"public_id": found.PublicID, "name": found.Name, "expires_at": found.ExpiresAt}
	summary := "Revoked access token " + found.PublicID + "."
	if targetUsername != "" {
		details["user"] = targetUsername
		summary = "Revoked access token " + found.PublicID + " of " + targetUsername + "."
	}
	s.auditAdmin(r, "token_revoke", "token:"+found.PublicID, summary, details)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteToken(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.fail(w, store.ErrNotFound)
		return
	}
	user, _ := auth.UserFrom(r.Context())
	s.revokeToken(w, r, user.ID, id, "")
}

func (s *Server) listUserTokens(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.fail(w, store.ErrNotFound)
		return
	}
	if _, err := s.Store.GetHumanUser(r.Context(), id); err != nil {
		s.fail(w, err)
		return
	}
	tokens, err := s.Store.ListAPITokensForUser(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	grants, err := s.listGrants(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, UserTokensResponse{Items: tokenDTOs(tokens), Grants: grants})
}

func (s *Server) deleteUserToken(w http.ResponseWriter, r *http.Request) {
	userID, ok := pathID(r, "id")
	tokenID, ok2 := pathID(r, "tokenId")
	if !ok || !ok2 {
		s.fail(w, store.ErrNotFound)
		return
	}
	target, err := s.Store.GetHumanUser(r.Context(), userID)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.revokeToken(w, r, userID, tokenID, target.Username)
}

// revokeGrant deletes one of ownerID's OAuth grants (its tokens go with it, so they stop authenticating at once) and
// audits it as oauth_revoke; targetUsername is set when an admin revokes another user's grant.
func (s *Server) revokeGrant(w http.ResponseWriter, r *http.Request, ownerID, grantID int64, targetUsername string) {
	grant, err := s.Store.GetOAuthGrant(r.Context(), grantID, ownerID)
	if err != nil {
		s.fail(w, err)
		return
	}
	if err := s.Store.DeleteOAuthGrant(r.Context(), grantID, ownerID); err != nil {
		s.fail(w, err)
		return
	}
	details := map[string]any{"grant_id": grant.ID, "client_id": grant.ClientID, "client_name": grant.ClientName,
		"reason": "revoked"}
	summary := "Disconnected " + grant.ClientName + "."
	if targetUsername != "" {
		details["user"] = targetUsername
		summary = "Disconnected " + grant.ClientName + " of " + targetUsername + "."
	}
	s.auditAdmin(r, "oauth_revoke", "oauth_grant:"+strconv.FormatInt(grant.ID, 10), summary, details)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteGrant(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.fail(w, store.ErrNotFound)
		return
	}
	user, _ := auth.UserFrom(r.Context())
	s.revokeGrant(w, r, user.ID, id, "")
}

func (s *Server) deleteUserGrant(w http.ResponseWriter, r *http.Request) {
	userID, ok := pathID(r, "id")
	grantID, ok2 := pathID(r, "grantId")
	if !ok || !ok2 {
		s.fail(w, store.ErrNotFound)
		return
	}
	target, err := s.Store.GetHumanUser(r.Context(), userID)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.revokeGrant(w, r, userID, grantID, target.Username)
}

// whoAmI answers GET /api/tokens/current for a token credential: the user, the token, the tags the user can view and
// where they can read logs, and today's log search budget. A session has no token to describe, so it gets 400.
func (s *Server) whoAmI(w http.ResponseWriter, r *http.Request) {
	cred, _ := auth.CredentialFrom(r.Context())
	if cred.Kind != auth.CredentialToken {
		if !s.Cfg.APITokens {
			// Only the agent is on: the route exists for run tokens alone, so a session sees it as absent.
			s.fail(w, &APIError{Status: http.StatusNotFound, Code: "not_found", Message: "Not found."})
			return
		}
		s.fail(w, &APIError{Status: http.StatusBadRequest, Code: "invalid", Message: "Call this with an access token."})
		return
	}
	user, _ := auth.UserFrom(r.Context())
	subject := auth.SubjectFrom(r.Context())
	tags, err := s.Store.ListTags(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	dto := WhoAmIDTO{
		User: WhoAmIUserDTO{ID: user.ID, Username: user.Username, Admin: subject.Admin},
		Token: WhoAmITokenDTO{PublicID: cred.TokenPublicID, Name: cred.TokenName, Kind: string(cred.TokenKind),
			Scope: string(cred.Scope), ExpiresAt: cred.ExpiresAt},
		Tags: []WhoAmITagDTO{},
	}
	if user.DisplayName != nil {
		dto.User.DisplayName = *user.DisplayName
	}
	for _, t := range tags {
		canView := authz.Can(subject, authz.View, t.ID)
		if !canView {
			continue
		}
		dto.Tags = append(dto.Tags, WhoAmITagDTO{ID: int64(t.ID), Name: t.Name, CanView: true, CanLogs: authz.Can(subject, authz.Logs, t.ID)})
	}
	if dto.LogSearch, err = s.tokenLogSearchState(r.Context(), user); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

// tokenCountsByUser maps user id to the number of unexpired tokens, for the admin users screen.
func (s *Server) tokenCountsByUser(ctx context.Context) (map[int64]int, error) {
	if !s.Cfg.APITokens {
		return nil, nil
	}
	return s.Store.CountAPITokensByUser(ctx)
}
