package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/apitoken"
	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// createTokenAs creates a token through the API as the signed-in client and returns its DTO and secret.
func createTokenAs(t *testing.T, c *apiClient, name string, days int) (TokenDTO, string) {
	t.Helper()
	body := map[string]any{"name": name}
	if days != 0 {
		body["expires_in_days"] = days
	}
	resp := c.post("/api/tokens", body)
	if resp.StatusCode != http.StatusCreated {
		code, msg := errorCode(t, resp)
		t.Fatalf("create token %q (%d days): status %d %s %s", name, days, resp.StatusCode, code, msg)
	}
	var out CreateTokenResponse
	decode(t, resp, &out)
	return out.Token, out.Secret
}

func TestTokens_CreateReturnsSecretOnce(t *testing.T) {
	srv, _, st := newTokenServer(t)
	c := login(t, srv, "omer", testPassword)
	created, secret := createTokenAs(t, c, "  laptop  ", 30)
	parsed, ok := apitoken.Parse(secret)
	if !ok || parsed.Kind != apitoken.KindPAT {
		t.Fatalf("secret %q does not parse as a personal token", secret)
	}
	if created.PublicID != apitoken.DisplayID(parsed) || created.Name != "laptop" || created.Scope != "read" ||
		created.Kind != "pat" || created.AuthSource != "password" {
		t.Errorf("token = %+v", created)
	}
	if want := time.Now().Add(30 * 24 * time.Hour); created.ExpiresAt.Before(want.Add(-time.Minute)) || created.ExpiresAt.After(want.Add(time.Minute)) {
		t.Errorf("expires_at = %v, want about 30 days out", created.ExpiresAt)
	}

	// The secret authenticates a follow-up call.
	bearer := newAPIClient(t, srv)
	bearer.bearer = secret
	resp := bearer.get("/api/tokens/current")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("whoami with the new secret: status %d", resp.StatusCode)
	}
	resp.Body.Close()

	// The listing never returns it.
	resp = c.get("/api/tokens")
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(raw), parsed.Secret) || strings.Contains(string(raw), `"secret"`) {
		t.Fatalf("listing exposes the secret: %s", raw)
	}
	var list TokenListResponse
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].PublicID != created.PublicID || list.Items[0].LastUsedAt == nil {
		t.Errorf("list = %+v", list.Items)
	}
	if list.Policy.MaxTTLDays != 90 || list.Policy.MaxTokens != 10 || list.Policy.SSOLinked || list.Policy.Admin {
		t.Errorf("policy = %+v", list.Policy)
	}
	// The stored row holds only the hash of the secret.
	row, err := st.GetAPITokenByPublicID(t.Context(), created.PublicID)
	if err != nil {
		t.Fatal(err)
	}
	if row.SecretHash != apitoken.Hash(parsed.Secret) || strings.Contains(row.SecretHash, parsed.Secret) {
		t.Errorf("stored hash = %q", row.SecretHash)
	}
}

func TestTokens_NameValidation(t *testing.T) {
	srv, _, _ := newTokenServer(t)
	c := login(t, srv, "omer", testPassword)
	for name, body := range map[string]map[string]any{
		"empty":      {"name": ""},
		"blank":      {"name": "   "},
		"missing":    {},
		"too long":   {"name": strings.Repeat("x", 61)},
		"days zero-": {"name": "a", "expires_in_days": -1},
	} {
		resp := c.post("/api/tokens", body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%s: status %d, want a validation error", name, resp.StatusCode)
		}
	}
	createTokenAs(t, c, strings.Repeat("x", 60), 1)
}

func TestTokens_LifetimeCappedByUserKind(t *testing.T) {
	srv, _, st := newTokenServer(t)
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	linked, err := st.CreateUser(t.Context(), "linda", hash, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.BindSSOIdentity(t.Context(), store.BindSSO{UserID: linked.ID, IdPEntityID: "e", ExternalID: "n1", ExternalIDFormat: "unspecified"}); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		user     string
		max      int
		wantDays int
	}{
		{"omer", 90, 90}, // password, not an admin
		{"linda", 7, 7},  // SSO-linked
		{"sam", 7, 7},    // admin with a password
	}
	for _, tc := range tests {
		t.Run(tc.user, func(t *testing.T) {
			c := login(t, srv, tc.user, testPassword)
			resp := c.post("/api/tokens", map[string]any{"name": "over", "expires_in_days": tc.max + 1})
			resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusUnprocessableEntity {
				t.Errorf("%d days: status %d, want a validation error", tc.max+1, resp.StatusCode)
			}
			exact, _ := createTokenAs(t, c, "exact", tc.max)
			if d := time.Until(exact.ExpiresAt); d > time.Duration(tc.max)*24*time.Hour || d < time.Duration(tc.max)*24*time.Hour-time.Minute {
				t.Errorf("%d days: expires in %v", tc.max, d)
			}
			omitted, _ := createTokenAs(t, c, "omitted", 0)
			if d := time.Until(omitted.ExpiresAt); d > time.Duration(tc.wantDays)*24*time.Hour || d < time.Duration(tc.wantDays)*24*time.Hour-time.Minute {
				t.Errorf("omitted lifetime: expires in %v, want %d days", d, tc.wantDays)
			}
			var list TokenListResponse
			decode(t, c.get("/api/tokens"), &list)
			if list.Policy.MaxTTLDays != tc.max || list.Policy.SSOLinked != (tc.user == "linda") || list.Policy.Admin != (tc.user == "sam") {
				t.Errorf("policy = %+v", list.Policy)
			}
		})
	}
}

func TestTokens_AdminTokenIsReadOnly(t *testing.T) {
	srv, fk, st := newTokenServer(t)
	seedAccounts(t, st)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	session := login(t, srv, "sam", testPassword)
	_, secret := createTokenAs(t, session, "admin-agent", 0)
	c := newAPIClient(t, srv)
	c.bearer = secret

	rawToken, rawSession := rawBody(t, c, "/api/overview"), rawBody(t, session, "/api/overview")
	if rawToken != rawSession {
		t.Errorf("an admin's token sees a different overview than the admin's session:\ntoken:   %.300s\nsession: %.300s", rawToken, rawSession)
	}
	resp := c.get("/api/users")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("GET /api/users with an admin token: status %d, want 403", resp.StatusCode)
	}
	if code, _ := errorCode(t, resp); code != "token_not_allowed" {
		t.Errorf("code %q", code)
	}
}

func rawBody(t *testing.T, c *apiClient, path string) string {
	t.Helper()
	resp := c.get(path)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d: %s", path, resp.StatusCode, raw)
	}
	return string(raw)
}

func TestTokens_LimitIsConflict(t *testing.T) {
	srv, _, _, _ := newServerCustom(t, true, withTokens(func(cfg *config.Config) { cfg.TokenMaxPerUser = 2 }, nil))
	c := login(t, srv, "omer", testPassword)
	createTokenAs(t, c, "one", 1)
	createTokenAs(t, c, "two", 1)
	resp := c.post("/api/tokens", map[string]any{"name": "three", "expires_in_days": 1})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status %d, want 409", resp.StatusCode)
	}
	code, msg := errorCode(t, resp)
	if code != "conflict" || msg != "You already have 2 tokens. Revoke one first." {
		t.Errorf("code %q message %q", code, msg)
	}
}

func TestTokens_RevokeOwnAndNotOthers(t *testing.T) {
	srv, _, st := newTokenServer(t)
	omer := login(t, srv, "omer", testPassword)
	sam := login(t, srv, "sam", testPassword)
	mine, secret := createTokenAs(t, omer, "mine", 1)
	theirs, samSecret := createTokenAs(t, sam, "theirs", 1)

	resp := omer.do(http.MethodDelete, fmt.Sprintf("/api/tokens/%d", theirs.ID), nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("deleting another user's token: status %d, want 404", resp.StatusCode)
	}
	if _, err := st.GetAPITokenByPublicID(t.Context(), theirs.PublicID); err != nil {
		t.Fatalf("another user's token was removed: %v", err)
	}
	bearer := newAPIClient(t, srv)
	bearer.bearer = samSecret
	if r := bearer.get("/api/tokens/current"); r.StatusCode != http.StatusOK {
		t.Fatalf("sam's token stopped working: %d", r.StatusCode)
	}

	resp = omer.do(http.MethodDelete, fmt.Sprintf("/api/tokens/%d", mine.ID), nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("deleting own token: status %d, want 204", resp.StatusCode)
	}
	bearer.bearer = secret
	if r := bearer.get("/api/tokens/current"); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked token: status %d, want 401", r.StatusCode)
	}
	resp = omer.do(http.MethodDelete, "/api/tokens/not-a-number", nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("non-numeric id: status %d, want 404", resp.StatusCode)
	}
}

func TestTokens_ATokenCannotManageTokens(t *testing.T) {
	srv, _, st := newTokenServer(t)
	c := tokenClient(t, srv, st, "sam")
	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/api/tokens", nil},
		{http.MethodPost, "/api/tokens", map[string]any{"name": "escalate"}},
		{http.MethodDelete, "/api/tokens/1", nil},
		{http.MethodGet, "/api/users/1/tokens", nil},
		{http.MethodDelete, "/api/users/1/tokens/1", nil},
	} {
		resp := c.do(tc.method, tc.path, tc.body, nil)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s with a token: status %d, want 403", tc.method, tc.path, resp.StatusCode)
			resp.Body.Close()
			continue
		}
		if code, _ := errorCode(t, resp); code != "token_not_allowed" {
			t.Errorf("%s %s: code %q, want token_not_allowed", tc.method, tc.path, code)
		}
	}
}

func TestTokens_WhoAmI(t *testing.T) {
	srv, fk, st := newTokenServer(t)
	seedAccounts(t, st)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	user, err := st.CreateUser(t.Context(), "reader", hash, false, false)
	if err != nil {
		t.Fatal(err)
	}
	tags, err := st.ListTags(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var withLogs, viewOnly store.Tag
	for _, tag := range tags {
		switch tag.Name {
		case "Prod":
			withLogs = tag
			if err := st.SetPermission(t.Context(), user.ID, tag.ID, authz.ActionSet(authz.View|authz.Logs)); err != nil {
				t.Fatal(err)
			}
		case "Staging":
			viewOnly = tag
			if err := st.SetPermission(t.Context(), user.ID, tag.ID, authz.ActionSet(authz.View)); err != nil {
				t.Fatal(err)
			}
		}
	}
	c := tokenClient(t, srv, st, "reader")
	var out WhoAmIDTO
	decode(t, c.get("/api/tokens/current"), &out)
	if out.User.Username != "reader" || out.User.Admin || out.Token.Scope != "read" || out.Token.Kind != "pat" ||
		!strings.HasPrefix(out.Token.PublicID, "hmp_") || out.Token.Name != "test-reader" || out.Token.ExpiresAt.IsZero() {
		t.Errorf("whoami = %+v", out)
	}
	if len(out.Tags) != 2 {
		t.Fatalf("tags = %+v, want only the two the user can view", out.Tags)
	}
	got := map[string]WhoAmITagDTO{}
	for _, tag := range out.Tags {
		got[tag.Name] = tag
	}
	if p := got[withLogs.Name]; !p.CanView || !p.CanLogs {
		t.Errorf("%s = %+v, want view and logs", withLogs.Name, p)
	}
	if s := got[viewOnly.Name]; !s.CanView || s.CanLogs {
		t.Errorf("%s = %+v, want view without logs", viewOnly.Name, s)
	}
	if out.LogSearch.DailyBytes != 20<<30 || out.LogSearch.MaxBytesPerSearch != 2<<30 || out.LogSearch.UsedBytes != 0 ||
		!out.LogSearch.ResetsAt.After(time.Now()) {
		t.Errorf("log_search = %+v", out.LogSearch)
	}

	// A session has no token to describe.
	session := login(t, srv, "reader", testPassword)
	resp := session.get("/api/tokens/current")
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("whoami with a session: status %d, want 400", resp.StatusCode)
	}
}

func TestTokens_AdminListsAndRevokes(t *testing.T) {
	srv, _, st := newTokenServer(t)
	omer := login(t, srv, "omer", testPassword)
	token, secret := createTokenAs(t, omer, "omers", 1)
	sam := login(t, srv, "sam", testPassword)
	user, _ := st.GetUserByUsername(t.Context(), "omer")

	var list ItemsResponse[TokenDTO]
	decode(t, sam.get(fmt.Sprintf("/api/users/%d/tokens", user.ID)), &list)
	if len(list.Items) != 1 || list.Items[0].PublicID != token.PublicID {
		t.Fatalf("admin list = %+v", list.Items)
	}
	resp := sam.get("/api/users/99999/tokens")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown user: status %d, want 404", resp.StatusCode)
	}
	resp = sam.do(http.MethodDelete, fmt.Sprintf("/api/users/%d/tokens/%d", user.ID, token.ID+999), nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown token: status %d, want 404", resp.StatusCode)
	}
	resp = sam.do(http.MethodDelete, fmt.Sprintf("/api/users/%d/tokens/%d", user.ID, token.ID), nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("admin revoke: status %d, want 204", resp.StatusCode)
	}
	bearer := newAPIClient(t, srv)
	bearer.bearer = secret
	if r := bearer.get("/api/tokens/current"); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked token: status %d, want 401", r.StatusCode)
	}
}

func TestTokens_NonAdminCannotListOthers(t *testing.T) {
	srv, _, st := newTokenServer(t)
	sam := login(t, srv, "sam", testPassword)
	token, _ := createTokenAs(t, sam, "sams", 1)
	samUser, _ := st.GetUserByUsername(t.Context(), "sam")
	omer := login(t, srv, "omer", testPassword)
	resp := omer.get(fmt.Sprintf("/api/users/%d/tokens", samUser.ID))
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("list: status %d, want 403", resp.StatusCode)
	}
	resp = omer.do(http.MethodDelete, fmt.Sprintf("/api/users/%d/tokens/%d", samUser.ID, token.ID), nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("revoke: status %d, want 403", resp.StatusCode)
	}
	if _, err := st.GetAPITokenByPublicID(t.Context(), token.PublicID); err != nil {
		t.Errorf("token was removed: %v", err)
	}
}

func TestTokens_Audited(t *testing.T) {
	srv, _, st := newTokenServer(t)
	omer := login(t, srv, "omer", testPassword)
	token, secret := createTokenAs(t, omer, "audited", 5)
	resp := omer.do(http.MethodDelete, fmt.Sprintf("/api/tokens/%d", token.ID), nil, nil)
	resp.Body.Close()

	entries, err := st.ListAudit(t.Context(), 200, 0)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, e := range entries {
		if e.Action != "token_create" && e.Action != "token_revoke" {
			continue
		}
		counts[e.Action]++
		if e.Details["public_id"] != token.PublicID || e.Details["name"] != "audited" || e.Details["expires_at"] == nil {
			t.Errorf("%s details = %v", e.Action, e.Details)
		}
		blob, _ := json.Marshal(e)
		parsed, _ := apitoken.Parse(secret)
		if strings.Contains(string(blob), parsed.Secret) || strings.Contains(string(blob), apitoken.Hash(parsed.Secret)) {
			t.Errorf("%s entry contains the secret or its hash: %s", e.Action, blob)
		}
	}
	if counts["token_create"] != 1 || counts["token_revoke"] != 1 {
		t.Fatalf("audit counts = %v, want one create and one revoke", counts)
	}
}

func TestTokens_AdminRevocationNamesTheTarget(t *testing.T) {
	srv, _, st := newTokenServer(t)
	omer := login(t, srv, "omer", testPassword)
	token, _ := createTokenAs(t, omer, "t", 1)
	sam := login(t, srv, "sam", testPassword)
	user, _ := st.GetUserByUsername(t.Context(), "omer")
	resp := sam.do(http.MethodDelete, fmt.Sprintf("/api/users/%d/tokens/%d", user.ID, token.ID), nil, nil)
	resp.Body.Close()
	entries, _ := st.ListAudit(t.Context(), 50, 0)
	for _, e := range entries {
		if e.Action == "token_revoke" {
			if e.ActorUsername != "sam" || e.Details["user"] != "omer" {
				t.Errorf("entry = actor %q details %v, want sam revoking omer's token", e.ActorUsername, e.Details)
			}
			return
		}
	}
	t.Fatal("no token_revoke entry")
}

func TestTokensOff_RoutesAre404(t *testing.T) {
	srv, _, _ := newTestServer(t)
	c := login(t, srv, "sam", testPassword)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/tokens"}, {http.MethodPost, "/api/tokens"}, {http.MethodDelete, "/api/tokens/1"},
		{http.MethodGet, "/api/tokens/current"}, {http.MethodGet, "/api/users/1/tokens"},
		{http.MethodDelete, "/api/users/1/tokens/1"},
	} {
		resp := c.do(tc.method, tc.path, map[string]any{"name": "x"}, nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s %s with tokens off: status %d, want 404", tc.method, tc.path, resp.StatusCode)
		}
	}
}

func TestTokensOff_RouteListHasNoTokenRoutes(t *testing.T) {
	_, api, _, _, _ := newServerCustomAPI(t, true, nil)
	for _, route := range api.Routes() {
		if strings.Contains(route.Pattern, "/tokens") {
			t.Errorf("%s is registered while tokens are off", route.Pattern)
		}
	}
}

func TestUsersList_TokenCountOnlyWhileTokensAreOn(t *testing.T) {
	users := func(c *apiClient) []map[string]any {
		var out struct {
			Items []map[string]any `json:"items"`
		}
		decode(t, c.get("/api/users"), &out)
		return out.Items
	}
	t.Run("off", func(t *testing.T) {
		srv, _, _ := newTestServer(t)
		for _, u := range users(login(t, srv, "sam", testPassword)) {
			if _, has := u["token_count"]; has {
				t.Fatalf("token_count present while tokens are off: %v", u)
			}
		}
	})
	t.Run("on", func(t *testing.T) {
		srv, _, st := newTokenServer(t)
		omer := login(t, srv, "omer", testPassword)
		createTokenAs(t, omer, "a", 1)
		createTokenAs(t, omer, "b", 1)
		issueToken(t, st, "sam", time.Now().Add(-time.Hour)) // expired: not counted
		counts := map[string]float64{}
		for _, u := range users(login(t, srv, "sam", testPassword)) {
			n, has := u["token_count"].(float64)
			if !has {
				t.Fatalf("token_count missing while tokens are on: %v", u)
			}
			counts[u["username"].(string)] = n
		}
		if counts["omer"] != 2 || counts["sam"] != 0 {
			t.Errorf("counts = %v, want omer 2 and sam 0", counts)
		}
	})
}
