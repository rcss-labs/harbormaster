package main

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/store"
)

// revokeFixture is a migrated schema holding one personal token, one OAuth grant (two tokens) and one pending code.
type revokeFixture struct {
	url    string
	getenv func(string) string
}

func newRevokeFixture(t *testing.T) revokeFixture {
	t.Helper()
	dbURL := os.Getenv("HM_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("HM_TEST_DATABASE_URL not set")
	}
	ctx := t.Context()
	schemaURL := schemaScopedURL(t, dbURL)
	st, err := store.Open(ctx, schemaURL)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	u, err := st.CreateUser(ctx, "alice", "h", false, false)
	if err != nil {
		t.Fatal(err)
	}
	end := time.Now().Add(time.Hour)
	if _, err := st.CreateAPIToken(ctx, store.APIToken{PublicID: "hmp_cli000000001", UserID: u.ID, Kind: "pat", Name: "laptop",
		SecretHash: "h1", Scope: "read", AuthSource: "password", ExpiresAt: end}, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateOAuthGrantWithTokens(ctx, store.OAuthGrant{UserID: u.ID, ClientID: "c", ClientName: "Claude",
		Scope: "harbormaster:read", Resource: "r", AuthSource: "password", ExpiresAt: end},
		store.APIToken{PublicID: "hmo_cli000000001", Kind: "oauth_access", SecretHash: "h2", Scope: "read", ExpiresAt: end},
		store.APIToken{PublicID: "hmr_cli000000001", Kind: "oauth_refresh", SecretHash: "h3", Scope: "read", ExpiresAt: end}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateOAuthCode(ctx, store.OAuthCode{CodeHash: "code-cli", UserID: u.ID, ClientID: "c", ClientName: "Claude",
		RedirectURI: "https://claude.ai/cb", CodeChallenge: "x", Scope: "harbormaster:read", Resource: "r",
		AuthSource: "password", ExpiresAt: end}); err != nil {
		t.Fatal(err)
	}
	// HM_API_TOKENS and HM_OAUTH are deliberately unset: revoking must work with the switches off.
	return revokeFixture{url: schemaURL, getenv: func(k string) string {
		if k == "HM_DATABASE_URL" {
			return schemaURL
		}
		return ""
	}}
}

func (f revokeFixture) counts(t *testing.T) (tokens, grants, codes int) {
	t.Helper()
	st, err := store.Open(t.Context(), f.url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for q, dst := range map[string]*int{
		`select count(*) from api_tokens`: &tokens, `select count(*) from oauth_grants`: &grants,
		`select count(*) from oauth_codes`: &codes,
	} {
		if err := st.Pool.QueryRow(t.Context(), q).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	return
}

func TestRevokeTokens_WithoutAllIsUsageAndDeletesNothing(t *testing.T) {
	f := newRevokeFixture(t)
	for _, args := range [][]string{nil, {"--user", ""}, {"extra"}} {
		var out bytes.Buffer
		err := runRevokeTokens(t.Context(), args, f.getenv, &out)
		if !errors.As(err, new(errUsage)) {
			t.Errorf("args %v: err = %v, want usage error", args, err)
		}
	}
	if tk, g, c := f.counts(t); tk != 3 || g != 1 || c != 1 {
		t.Errorf("rows after refused runs tokens=%d grants=%d codes=%d, want 3/1/1", tk, g, c)
	}
	var stderr bytes.Buffer
	if code := exitCode(errUsage{revokeTokensUsage}, &stderr); code != 2 || !strings.Contains(stderr.String(), "revoke-tokens") {
		t.Errorf("exit code %d, stderr %q; want 2 with usage", code, stderr.String())
	}
}

func TestRevokeTokens_AllRevokesEverythingAuditsAndPrintsCounts(t *testing.T) {
	f := newRevokeFixture(t)
	var out bytes.Buffer
	if err := runRevokeTokens(t.Context(), []string{"--all"}, f.getenv, &out); err != nil {
		t.Fatal(err)
	}
	if want := "revoked 3 tokens, 1 grants, 1 codes"; !strings.Contains(out.String(), want) {
		t.Errorf("output %q does not contain %q", out.String(), want)
	}
	if tk, g, c := f.counts(t); tk+g+c != 0 {
		t.Errorf("rows left tokens=%d grants=%d codes=%d", tk, g, c)
	}
	st, err := store.Open(t.Context(), f.url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	entries, err := st.ListAudit(t.Context(), 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	var found *store.AuditEntry
	for i := range entries {
		if entries[i].Action == "tokens_revoke_all" {
			found = &entries[i]
		}
	}
	if found == nil {
		t.Fatalf("no tokens_revoke_all audit entry in %+v", entries)
	}
	if found.ActorUsername != "cli" || found.Details["tokens"] != float64(3) || found.Details["grants"] != float64(1) ||
		found.Details["codes"] != float64(1) {
		t.Errorf("audit entry = %+v", found)
	}
	for _, secret := range []string{"hmp_cli", "hmo_cli", "hmr_cli", "code-cli"} {
		if strings.Contains(found.Summary, secret) || strings.Contains(out.String(), secret) {
			t.Errorf("token material %q leaked into summary or output", secret)
		}
	}
}

func TestRevokeTokens_UserRevokesOnlyThatUser(t *testing.T) {
	f := newRevokeFixture(t)
	st, err := store.Open(t.Context(), f.url)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := st.CreateUser(t.Context(), "bob", "h", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAPIToken(t.Context(), store.APIToken{PublicID: "hmp_cli000000002", UserID: bob.ID, Kind: "pat",
		Name: "n", SecretHash: "h9", Scope: "read", AuthSource: "password", ExpiresAt: time.Now().Add(time.Hour)}, 100); err != nil {
		t.Fatal(err)
	}
	st.Close()

	var out bytes.Buffer
	if err := runRevokeTokens(t.Context(), []string{"--user", "bob"}, f.getenv, &out); err != nil {
		t.Fatal(err)
	}
	if tk, g, c := f.counts(t); tk != 3 || g != 1 || c != 1 {
		t.Errorf("alice's credentials changed: tokens=%d grants=%d codes=%d, want 3/1/1", tk, g, c)
	}
	if err := runRevokeTokens(t.Context(), []string{"--user", "nobody"}, f.getenv, &out); err == nil {
		t.Error("unknown user: want an error")
	}
}

// Through the real dispatcher: no flag is exit 2 with usage on stderr, and nothing is deleted.
func TestRevokeTokens_DispatchWithoutFlagExits2(t *testing.T) {
	f := newRevokeFixture(t)
	var stdout, stderr bytes.Buffer
	if code := runWith(t.Context(), []string{"revoke-tokens"}, f.getenv, strings.NewReader(""), &stdout, &stderr); code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "revoke-tokens --all") {
		t.Errorf("stderr %q lacks usage", stderr.String())
	}
	if tk, _, _ := f.counts(t); tk != 3 {
		t.Errorf("tokens = %d, want 3 untouched", tk)
	}
	if code := runWith(t.Context(), []string{"revoke-tokens", "--all"}, f.getenv, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Errorf("--all exit = %d, want 0", code)
	}
}
