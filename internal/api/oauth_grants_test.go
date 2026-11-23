package api

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/authz"
)

type grantList struct {
	Items  []TokenDTO `json:"items"`
	Grants []GrantDTO `json:"grants"`
}

func (e *oauthAPIEnv) grants(t *testing.T, c *apiClient) []GrantDTO {
	t.Helper()
	resp := c.get("/api/tokens")
	wantStatus(t, resp, http.StatusOK)
	var out grantList
	decode(t, resp, &out)
	return out.Grants
}

func grantPath(id int64) string { return "/api/oauth/grants/" + strconv.FormatInt(id, 10) }

func (e *oauthAPIEnv) userID(t *testing.T, name string) int64 {
	t.Helper()
	u, err := e.st.GetUserByUsername(t.Context(), name)
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

func TestOAuthToken_TakesTheSameGate(t *testing.T) {
	env := newOAuthAPIEnvWith(t, true, func(s *Server) {
		// A write route marked for tokens by mistake: only the read-only subject stands between a token and it.
		s.testTokenRoutes = map[string]auth.Scope{"POST /api/services/{serviceId}/restart": auth.ScopeRead}
	})
	env.seedEstate(t)
	issued := env.connect(t, env.omer)
	samIssued := env.connect(t, env.sam)

	wantStatus(t, env.bearer(t, http.MethodGet, "/api/overview", issued.AccessToken), http.StatusOK)

	// Unmarked routes refuse tokens: writes, sessions, user management, even a path that does not exist.
	for _, call := range []struct{ method, path string }{
		{http.MethodPost, "/api/auth/logout"}, {http.MethodGet, "/api/me"}, {http.MethodGet, "/api/users"},
		{http.MethodPost, "/api/tokens"}, {http.MethodGet, "/api/tokens"}, {http.MethodPost, "/api/focus"},
		{http.MethodGet, "/api/no-such-route"},
	} {
		resp := env.bearer(t, call.method, call.path, issued.AccessToken)
		wantStatus(t, resp, http.StatusForbidden)
		if code, _ := errorCode(t, resp); code != "token_not_allowed" {
			t.Errorf("%s %s: code %q, want token_not_allowed", call.method, call.path, code)
		}
	}

	// A write route marked for tokens by mistake is still refused for an admin's OAuth token: the subject is
	// read-only, so the handler's own permission check says no (and the refusal is not the route gate's).
	resp := env.bearer(t, http.MethodPost, "/api/services/"+webFrontendID()+"/restart", samIssued.AccessToken)
	wantStatus(t, resp, http.StatusForbidden)
	if code, _ := errorCode(t, resp); code == "token_not_allowed" {
		t.Fatal("the test hook did not mark the route, so this proves nothing about the read-only subject")
	}
	// And the same route, unmarked, is the route gate's refusal.
	plain := newOAuthAPIEnv(t)
	plain.seedEstate(t)
	plainIssued := plain.connect(t, plain.sam)
	resp = plain.bearer(t, http.MethodPost, "/api/services/"+webFrontendID()+"/restart", plainIssued.AccessToken)
	wantStatus(t, resp, http.StatusForbidden)
	if code, _ := errorCode(t, resp); code != "token_not_allowed" {
		t.Fatalf("unmarked write route: code %q, want token_not_allowed", code)
	}
}

func TestOAuthToken_HasTheLogSearchBudget(t *testing.T) {
	env := newOAuthAPIEnv(t)
	issued := env.connect(t, env.sam)
	var before WhoAmIDTO
	resp := env.bearer(t, http.MethodGet, "/api/tokens/current", issued.AccessToken)
	wantStatus(t, resp, http.StatusOK)
	decode(t, resp, &before)
	if before.LogSearch.DailyBytes == 0 || before.LogSearch.MaxBytesPerSearch == 0 {
		t.Fatalf("an OAuth token has no log search budget: %+v", before.LogSearch)
	}
	if before.Token.Kind != "oauth_access" {
		t.Fatalf("kind = %q", before.Token.Kind)
	}
}

func TestGrants_ListAndRevoke(t *testing.T) {
	env := newOAuthAPIEnv(t)
	issued := env.connect(t, env.omer)
	grants := env.grants(t, env.omer)
	if len(grants) != 1 || grants[0].ClientName != "Claude" || grants[0].ClientID != env.docs.clientID ||
		grants[0].Scope != "harbormaster:read" || grants[0].LastUsedAt != nil {
		t.Fatalf("grants = %+v", grants)
	}
	wantStatus(t, env.bearer(t, http.MethodGet, "/api/tokens/current", issued.AccessToken), http.StatusOK)
	if got := env.grants(t, env.omer); got[0].LastUsedAt == nil {
		t.Fatal("using the access token did not record the grant's last use")
	}

	wantStatus(t, env.omer.do(http.MethodDelete, grantPath(grants[0].ID), nil, nil), http.StatusNoContent)
	// Gone at once: the access token and the refresh token.
	wantStatus(t, env.bearer(t, http.MethodGet, "/api/tokens/current", issued.AccessToken), http.StatusUnauthorized)
	wantStatus(t, env.postToken(t, env.refreshForm(issued.RefreshToken)), http.StatusBadRequest)
	if len(env.grants(t, env.omer)) != 0 {
		t.Fatal("the grant is still listed")
	}
	// Audited.
	if n := env.countRows(t, `select count(*) from audit_log where action = 'oauth_revoke' and details->>'reason' = 'revoked'`); n != 1 {
		t.Fatalf("%d oauth_revoke entries, want 1", n)
	}
	// Revoking again is 404.
	wantStatus(t, env.omer.do(http.MethodDelete, grantPath(grants[0].ID), nil, nil), http.StatusNotFound)
}

func TestGrants_RevokeNeedsASessionAndCSRF(t *testing.T) {
	env := newOAuthAPIEnv(t)
	issued := env.connect(t, env.omer)
	id := env.grants(t, env.omer)[0].ID
	wantStatus(t, newAPIClient(t, env.srv).do(http.MethodDelete, grantPath(id), nil, nil), http.StatusUnauthorized)
	wantStatus(t, env.omer.do(http.MethodDelete, grantPath(id), nil, map[string]string{"X-CSRF-Token": ""}), http.StatusForbidden)
	// A token cannot revoke its own grant or any other.
	resp := env.bearer(t, http.MethodDelete, grantPath(id), issued.AccessToken)
	wantStatus(t, resp, http.StatusForbidden)
	if len(env.grants(t, env.omer)) != 1 {
		t.Fatal("a refused revocation deleted the grant")
	}
}

func TestGrants_AdminRevokes(t *testing.T) {
	env := newOAuthAPIEnv(t)
	issued := env.connect(t, env.omer)
	omerID := env.userID(t, "omer")
	resp := env.sam.get("/api/users/" + strconv.FormatInt(omerID, 10) + "/tokens")
	wantStatus(t, resp, http.StatusOK)
	var list grantList
	decode(t, resp, &list)
	if len(list.Grants) != 1 || list.Items == nil {
		t.Fatalf("admin view = %+v", list)
	}
	path := "/api/users/" + strconv.FormatInt(omerID, 10) + "/oauth-grants/" + strconv.FormatInt(list.Grants[0].ID, 10)
	wantStatus(t, env.sam.do(http.MethodDelete, path, nil, nil), http.StatusNoContent)
	wantStatus(t, env.bearer(t, http.MethodGet, "/api/tokens/current", issued.AccessToken), http.StatusUnauthorized)
	if n := env.countRows(t, `select count(*) from audit_log where action = 'oauth_revoke' and details->>'user' = 'omer'`); n != 1 {
		t.Fatalf("%d admin oauth_revoke entries naming the user, want 1", n)
	}
}

func TestGrants_NotOthers(t *testing.T) {
	env := newOAuthAPIEnv(t)
	env.connect(t, env.omer)
	omerGrant := env.grants(t, env.omer)[0].ID
	other := env.oauthUser(t, "dana", false, false, authz.ActionSet(authz.View))

	// A user cannot revoke another user's grant, nor see it; the answer is the same as for a missing grant.
	wantStatus(t, other.do(http.MethodDelete, grantPath(omerGrant), nil, nil), http.StatusNotFound)
	if len(env.grants(t, other)) != 0 {
		t.Fatal("another user's grants are listed")
	}
	if len(env.grants(t, env.omer)) != 1 {
		t.Fatal("the grant was deleted by another user")
	}
	// The admin routes are admin-only.
	omerID := env.userID(t, "omer")
	path := "/api/users/" + strconv.FormatInt(omerID, 10) + "/oauth-grants/" + strconv.FormatInt(omerGrant, 10)
	wantStatus(t, other.do(http.MethodDelete, path, nil, nil), http.StatusForbidden)
	wantStatus(t, other.get("/api/users/"+strconv.FormatInt(omerID, 10)+"/tokens"), http.StatusForbidden)
	// The admin route does not cross users: the grant must belong to the user in the path.
	danaID := env.userID(t, "dana")
	cross := "/api/users/" + strconv.FormatInt(danaID, 10) + "/oauth-grants/" + strconv.FormatInt(omerGrant, 10)
	wantStatus(t, env.sam.do(http.MethodDelete, cross, nil, nil), http.StatusNotFound)
	if len(env.grants(t, env.omer)) != 1 {
		t.Fatal("a crossed admin path deleted the grant")
	}
}

func TestRevokeTokens_PasswordChangeEndsGrants(t *testing.T) {
	env := newOAuthAPIEnv(t)
	issued := env.connect(t, env.omer)
	wantStatus(t, env.bearer(t, http.MethodGet, "/api/tokens/current", issued.AccessToken), http.StatusOK)
	resp := env.omer.post("/api/auth/password", map[string]string{"current": testPassword, "new": "another-good-password-2"})
	wantStatus(t, resp, http.StatusNoContent)
	wantStatus(t, env.bearer(t, http.MethodGet, "/api/tokens/current", issued.AccessToken), http.StatusUnauthorized)
	wantStatus(t, env.postToken(t, env.refreshForm(issued.RefreshToken)), http.StatusBadRequest)
	if n := env.countRows(t, `select count(*) from oauth_grants`); n != 0 {
		t.Fatalf("%d grants survive a password change", n)
	}
}

func TestRevokeTokens_PromotionToAdminEndsGrants(t *testing.T) {
	env := newOAuthAPIEnv(t)
	issued := env.connect(t, env.omer)
	omerID := env.userID(t, "omer")
	resp := env.sam.post("/api/users/"+strconv.FormatInt(omerID, 10)+"/admin", map[string]bool{"admin": true})
	wantStatus(t, resp, http.StatusNoContent)
	wantStatus(t, env.bearer(t, http.MethodGet, "/api/tokens/current", issued.AccessToken), http.StatusUnauthorized)
	if n := env.countRows(t, `select count(*) from oauth_grants where user_id = $1`, omerID); n != 0 {
		t.Fatalf("%d grants survive a promotion to admin", n)
	}
}

func TestRevokeTokens_AdminPasswordResetEndsGrants(t *testing.T) {
	env := newOAuthAPIEnv(t)
	issued := env.connect(t, env.omer)
	omerID := env.userID(t, "omer")
	resp := env.sam.post("/api/users/"+strconv.FormatInt(omerID, 10)+"/reset-password", nil)
	wantStatus(t, resp, http.StatusOK)
	wantStatus(t, env.bearer(t, http.MethodGet, "/api/tokens/current", issued.AccessToken), http.StatusUnauthorized)
}

func TestRetention_APIPathRemovesExpiredGrants(t *testing.T) {
	env := newOAuthAPIEnv(t)
	env.connect(t, env.omer)
	if _, err := env.st.Pool.Exec(t.Context(), `update oauth_grants set expires_at = now() - interval '2 days'`); err != nil {
		t.Fatal(err)
	}
	if len(env.grants(t, env.omer)) != 0 {
		t.Fatal("an expired grant is listed")
	}
	n, err := env.st.DeleteExpiredOAuth(t.Context(), time.Now())
	if err != nil || n != 1 {
		t.Fatalf("DeleteExpiredOAuth = %d, %v, want 1", n, err)
	}
}
