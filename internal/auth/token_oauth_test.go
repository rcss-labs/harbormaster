package auth

import (
	"errors"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/apitoken"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// oauthGrantFor stores a grant with a live access token and refresh token and returns their raw secrets.
func oauthGrantFor(t *testing.T, st *store.Store, user store.User) (access, refresh string, grant store.OAuthGrant) {
	t.Helper()
	end := time.Now().Add(24 * time.Hour)
	accessRaw, accessParsed, err := apitoken.Generate(apitoken.KindOAuthAccess)
	if err != nil {
		t.Fatal(err)
	}
	refreshRaw, refreshParsed, err := apitoken.Generate(apitoken.KindOAuthRefresh)
	if err != nil {
		t.Fatal(err)
	}
	grant, err = st.CreateOAuthGrantWithTokens(t.Context(), store.OAuthGrant{
		UserID: user.ID, ClientID: "https://claude.ai/c.json", ClientName: "Claude", Scope: "harbormaster:read",
		Resource: "https://mcp.example.com/mcp", AuthSource: "password", ExpiresAt: end,
	}, store.APIToken{PublicID: apitoken.DisplayID(accessParsed), Kind: "oauth_access", SecretHash: apitoken.Hash(accessParsed.Secret),
		Scope: "read", ExpiresAt: time.Now().Add(time.Hour)},
		store.APIToken{PublicID: apitoken.DisplayID(refreshParsed), Kind: "oauth_refresh", SecretHash: apitoken.Hash(refreshParsed.Secret),
			Scope: "read", ExpiresAt: end})
	if err != nil {
		t.Fatal(err)
	}
	return accessRaw, refreshRaw, grant
}

func TestAuthenticateToken_OAuthAccessWorksRefreshDoesNot(t *testing.T) {
	st := newTestServiceStore(t)
	cfg := testConfig()
	cfg.APITokens, cfg.OAuth = true, true
	svc := NewService(st, cfg)
	user := mustUser(t, st, "alice", false)
	access, refresh, grant := oauthGrantFor(t, st, user)

	tok, got, err := svc.AuthenticateToken(t.Context(), access, "10.0.0.1")
	if err != nil {
		t.Fatalf("access token: %v", err)
	}
	if got.ID != user.ID || tok.Kind != "oauth_access" || tok.Name != "Claude" || tok.GrantID == nil || *tok.GrantID != grant.ID {
		t.Fatalf("token = %+v, user %+v", tok, got)
	}
	// Using it touches the grant's last use.
	grants, err := st.ListOAuthGrantsForUser(t.Context(), user.ID)
	if err != nil || len(grants) != 1 || grants[0].LastUsedAt == nil {
		t.Fatalf("grant last_used_at not set: %+v, %v", grants, err)
	}
	if _, _, err := svc.AuthenticateToken(t.Context(), refresh, "10.0.0.1"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("a refresh token authenticated an API call: %v", err)
	}
	// An access token with a wrong secret is refused.
	parsed, _ := apitoken.Parse(access)
	forged := "hmo_" + parsed.ID + parsed.Secret[:len(parsed.Secret)-1] + "A"
	if forged == access {
		forged = "hmo_" + parsed.ID + parsed.Secret[:len(parsed.Secret)-1] + "B"
	}
	if _, _, err := svc.AuthenticateToken(t.Context(), forged, ""); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("forged secret: %v", err)
	}
}

func TestAuthenticateToken_OAuthAccessStopsWhenItsGrantIsGone(t *testing.T) {
	st := newTestServiceStore(t)
	cfg := testConfig()
	cfg.APITokens, cfg.OAuth = true, true
	svc := NewService(st, cfg)
	user := mustUser(t, st, "alice", false)
	access, _, grant := oauthGrantFor(t, st, user)
	if _, _, err := svc.AuthenticateToken(t.Context(), access, ""); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteOAuthGrant(t.Context(), grant.ID, user.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.AuthenticateToken(t.Context(), access, ""); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("after revoking the grant: %v", err)
	}
}

func TestAuthenticateToken_OAuthAccessRefusedWhenOAuthOff(t *testing.T) {
	st := newTestServiceStore(t)
	cfg := testConfig()
	cfg.APITokens, cfg.OAuth = true, false
	svc := NewService(st, cfg)
	user := mustUser(t, st, "alice", false)
	access, refresh, _ := oauthGrantFor(t, st, user)
	for name, raw := range map[string]string{"access": access, "refresh": refresh} {
		if _, _, err := svc.AuthenticateToken(t.Context(), raw, ""); !errors.Is(err, ErrUnauthenticated) {
			t.Errorf("%s token with OAuth off: err = %v, want ErrUnauthenticated", name, err)
		}
	}
	// Personal tokens are unaffected by the switch.
	pat, parsed, _ := apitoken.Generate(apitoken.KindPAT)
	if _, err := st.CreateAPIToken(t.Context(), store.APIToken{PublicID: apitoken.DisplayID(parsed), UserID: user.ID, Kind: "pat",
		Name: "p", SecretHash: apitoken.Hash(parsed.Secret), Scope: "read", ExpiresAt: time.Now().Add(time.Hour)}, 10); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.AuthenticateToken(t.Context(), pat, ""); err != nil {
		t.Fatalf("personal token with OAuth off: %v", err)
	}
}

func newTestServiceStore(t *testing.T) *store.Store {
	t.Helper()
	_, st := newTestService(t)
	return st
}
