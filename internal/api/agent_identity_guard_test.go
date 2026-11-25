package api

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// identityServer is a server with access tokens and the agent on (so every users route exists), the identity row
// inserted directly, and an admin client.
func identityServer(t *testing.T) (admin *apiClient, st *store.Store, identityID int64) {
	t.Helper()
	srv, _, _, st, _ := newServerCustomAPI(t, true, withTokens(func(c *config.Config) {
		c.AgentURL, c.AgentRunTokenTTL = testAgentURL, 15*time.Minute
	}, nil))
	if err := st.Pool.QueryRow(t.Context(),
		`insert into users (username, kind) values ('harbormaster-agent', 'agent') returning id`).Scan(&identityID); err != nil {
		t.Fatal(err)
	}
	return login(t, srv, "sam", testPassword), st, identityID
}

func TestUsersRoutes_AnswerNotFoundForTheIdentity(t *testing.T) {
	// Every route gets its own server: one that wrongly succeeded (a delete) must not hide the routes after it.
	for _, tc := range []struct {
		name, method, path string
		body               any
	}{
		{"delete", "DELETE", "/api/users/%d", nil},
		{"reset password", "POST", "/api/users/%d/reset-password", nil},
		{"admin", "POST", "/api/users/%d/admin", map[string]any{"admin": true}},
		{"break-glass", "POST", "/api/users/%d/break-glass", map[string]any{"break_glass": true}},
		{"pending SSO link", "PUT", "/api/users/%d/sso-link", map[string]any{"name_id": "someone@example.test"}},
		{"clear SSO link", "DELETE", "/api/users/%d/sso-link", nil},
		{"SSO unlink", "POST", "/api/users/%d/sso-unlink", nil},
		{"AI chat", "POST", "/api/users/%d/ai-chat", map[string]any{"on": true}},
		{"permission", "PUT", "/api/permissions/%d/1", map[string]any{"actions": 1}},
		{"list tokens", "GET", "/api/users/%d/tokens", nil},
		{"revoke token", "DELETE", "/api/users/%d/tokens/1", nil},
		{"revoke OAuth grant", "DELETE", "/api/users/%d/oauth-grants/1", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			admin, st, id := identityServer(t)
			if _, err := st.CreateTag(t.Context(), "Prod", "red", false); err != nil {
				t.Fatal(err)
			}
			path := fmt.Sprintf(tc.path, id)
			resp := admin.do(tc.method, path, tc.body, nil)
			resp.Body.Close()
			if resp.StatusCode != http.StatusNotFound {
				t.Fatalf("%s %s = %d, want 404", tc.method, path, resp.StatusCode)
			}
			u, err := st.GetUser(t.Context(), id)
			if err != nil || u.Admin || u.BreakGlass || u.AIChat || u.HasPassword() || u.SSOLinked() || u.PendingLinkNameID != "" {
				t.Fatalf("the identity changed: %+v, %v", u, err)
			}
			if perms, _ := st.PermissionsOf(t.Context(), id); len(perms) != 0 {
				t.Fatalf("permissions of the identity = %v, want none", perms)
			}
		})
	}
}

func TestUsersList_OmitsTheIdentity(t *testing.T) {
	admin, st, _ := identityServer(t)
	if _, err := st.CreateTag(t.Context(), "Prod", "red", false); err != nil {
		t.Fatal(err)
	}
	var users struct {
		Items []testUserDTO `json:"items"`
	}
	decode(t, admin.get("/api/users"), &users)
	if len(users.Items) != 2 {
		t.Fatalf("users = %+v, want sam and omer only", users.Items)
	}
	for _, u := range users.Items {
		if u.Username == "harbormaster-agent" {
			t.Fatalf("the identity is listed: %+v", u)
		}
	}
	var matrix struct {
		Users []struct {
			Username string `json:"username"`
		} `json:"users"`
	}
	decode(t, admin.get("/api/permissions"), &matrix)
	for _, u := range matrix.Users {
		if u.Username == "harbormaster-agent" {
			t.Fatal("the permission matrix lists the identity")
		}
	}
}

func TestCreateUser_RefusesTheReservedPrefix(t *testing.T) {
	admin, st, _ := identityServer(t)
	for _, name := range []string{"harbormaster-agent", "harbormaster-agent-1", "harbormaster-agent-7"} {
		resp := admin.post("/api/users", map[string]any{"username": name, "admin": true})
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("POST /api/users %q = %d, want 422", name, resp.StatusCode)
		}
	}
	if n, _ := st.CountUsers(t.Context()); n != 2 {
		t.Fatalf("users = %d, want 2", n)
	}
}

func TestLogin_IdentityNameIsRefused(t *testing.T) {
	admin, _, _ := identityServer(t)
	resp := admin.do("POST", "/api/auth/login", map[string]string{"username": "harbormaster-agent", "password": testPassword}, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("login as the identity = %d, want 401", resp.StatusCode)
	}
}
