package store_test

import (
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/store"
)

// M2: linking an account to SSO ends every token, grant and code that was issued under the longer password-user cap.
func TestBindSSOIdentity_RevokesTokensGrantsAndCodes(t *testing.T) {
	f := newOAuthFixture(t)
	ctx := t.Context()
	g, access, _ := f.grant(t, f.user.ID, "password", time.Now().Add(90*24*time.Hour), time.Hour)
	seedTokenWithSource(t, f.st, f.user.ID, "hmp_pwpwpwpwpwpw", "password")
	if err := f.st.CreateOAuthCode(ctx, f.code("code-m2", time.Now().Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	bystander, err := f.st.CreateUser(ctx, "bob", "h", false, false)
	if err != nil {
		t.Fatal(err)
	}
	seedTokenWithSource(t, f.st, bystander.ID, "hmp_bystanderbyst", "password")

	if _, err := f.st.BindSSOIdentity(ctx, store.BindSSO{UserID: f.user.ID, IdPEntityID: "e", ExternalID: "n1", ExternalIDFormat: "unspecified"}); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `select count(*) from oauth_grants where id = $1`, g.ID); n != 0 {
		t.Fatal("grant survived the SSO link")
	}
	if tokenExists(t, f.st, access.PublicID) || tokenExists(t, f.st, "hmp_pwpwpwpwpwpw") {
		t.Fatal("a token survived the SSO link")
	}
	if n := f.count(t, `select count(*) from oauth_codes where user_id = $1`, f.user.ID); n != 0 {
		t.Fatal("an authorization code survived the SSO link")
	}
	if !tokenExists(t, f.st, "hmp_bystanderbyst") {
		t.Fatal("another user's token was removed")
	}
}

// M2 defence in depth: a refresh never extends a grant past created_at + the user's current maximum.
func TestRotateOAuthRefreshWithin_ClampsToTheCurrentCeiling(t *testing.T) {
	f := newOAuthFixture(t)
	now := time.Now()
	g, _, refresh := f.grant(t, f.user.ID, "password", now.Add(90*24*time.Hour), time.Hour)
	far := now.Add(90 * 24 * time.Hour)
	var seen store.User
	got, err := f.st.RotateOAuthRefreshWithin(t.Context(), refresh.PublicID, refresh.SecretHash,
		f.token("oauth_access", far), f.token("oauth_refresh", far), now.Add(time.Minute), time.Minute,
		func(u store.User) time.Duration { seen = u; return 7 * 24 * time.Hour })
	if err != nil {
		t.Fatal(err)
	}
	if seen.ID != f.user.ID {
		t.Fatal("the ceiling was not asked about the grant's user")
	}
	ceiling := g.CreatedAt.Add(7 * 24 * time.Hour)
	if got.ExpiresAt.After(ceiling.Add(time.Second)) {
		t.Fatalf("returned grant ends %v, past the ceiling %v", got.ExpiresAt, ceiling)
	}
	if n := f.count(t, `select count(*) from api_tokens where grant_id = $1 and rotated_at is null and expires_at > $2`, g.ID, ceiling.Add(time.Second)); n != 0 {
		t.Fatalf("%d tokens outlive the ceiling", n)
	}
}

func TestRotateOAuthRefreshWithin_PastTheCeilingIsNotFound(t *testing.T) {
	f := newOAuthFixture(t)
	now := time.Now()
	_, _, refresh := f.grant(t, f.user.ID, "password", now.Add(90*24*time.Hour), time.Hour)
	_, err := f.st.RotateOAuthRefreshWithin(t.Context(), refresh.PublicID, refresh.SecretHash,
		f.token("oauth_access", now.Add(time.Hour)), f.token("oauth_refresh", now.Add(time.Hour)),
		now.Add(8*24*time.Hour), time.Minute, func(store.User) time.Duration { return 7 * 24 * time.Hour })
	if err != store.ErrNotFound {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// L1: "revoke everything" also kills codes approved but not yet exchanged.
func TestRevocation_AlsoDeletesCodes(t *testing.T) {
	f := newOAuthFixture(t)
	ctx := t.Context()
	other, err := f.st.CreateUser(ctx, "bob", "h", false, false)
	if err != nil {
		t.Fatal(err)
	}
	mk := func(hash string, userID int64, source string) {
		c := f.code(hash, time.Now().Add(time.Minute))
		c.UserID, c.AuthSource = userID, source
		if err := f.st.CreateOAuthCode(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	mk("pw-code", f.user.ID, "password")
	mk("saml-code", f.user.ID, "saml")
	mk("bob-saml-code", other.ID, "saml")

	if _, err := f.st.DeleteAPITokensForUser(ctx, f.user.ID, "saml"); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `select count(*) from oauth_codes where code_hash = 'saml-code'`); n != 0 {
		t.Fatal("per-user revoke left the user's saml code")
	}
	if n := f.count(t, `select count(*) from oauth_codes where code_hash in ('pw-code','bob-saml-code')`); n != 2 {
		t.Fatal("per-user revoke removed a code it should not")
	}
	if _, err := f.st.DeleteAPITokensBySource(ctx, "saml"); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `select count(*) from oauth_codes where code_hash = 'bob-saml-code'`); n != 0 {
		t.Fatal("by-source revoke left a saml code")
	}
	if _, err := f.st.DeleteAPITokensForUser(ctx, f.user.ID, ""); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `select count(*) from oauth_codes where user_id = $1`, f.user.ID); n != 0 {
		t.Fatal("revoke-all left a code")
	}
}

// L2: the grant's lifetime ceiling is recomputed from the user row as locked, not from what the caller loaded.
func TestCreateOAuthGrantWithTokensWithin_UsesTheLockedUserRow(t *testing.T) {
	f := newOAuthFixture(t)
	now := time.Now()
	// The caller "loaded" a non-admin; the user is promoted before the grant is written.
	if _, err := f.st.Pool.Exec(t.Context(), `update users set is_admin = true where id = $1`, f.user.ID); err != nil {
		t.Fatal(err)
	}
	far := now.Add(90 * 24 * time.Hour)
	g, err := f.st.CreateOAuthGrantWithTokensWithin(t.Context(), store.OAuthGrant{
		UserID: f.user.ID, ClientID: "https://claude.ai/c.json", ClientName: "Claude", Scope: "harbormaster:read",
		Resource: "r", AuthSource: "password", ExpiresAt: far,
	}, f.token("oauth_access", now.Add(time.Hour)), f.token("oauth_refresh", far),
		func(u store.User) time.Time {
			if u.Admin {
				return now.Add(7 * 24 * time.Hour)
			}
			return far
		})
	if err != nil {
		t.Fatal(err)
	}
	if limit := now.Add(7 * 24 * time.Hour); g.ExpiresAt.After(limit) {
		t.Fatalf("grant ends %v, past the admin ceiling %v", g.ExpiresAt, limit)
	}
	if n := f.count(t, `select count(*) from api_tokens where grant_id = $1 and expires_at > $2`, g.ID, now.Add(7*24*time.Hour)); n != 0 {
		t.Fatalf("%d tokens outlive the ceiling", n)
	}
	_, err = f.st.CreateOAuthGrantWithTokensWithin(t.Context(), store.OAuthGrant{UserID: 999999, ExpiresAt: far},
		f.token("oauth_access", far), f.token("oauth_refresh", far), func(store.User) time.Time { return far })
	if err != store.ErrNotFound {
		t.Fatalf("unknown user: err = %v, want ErrNotFound", err)
	}
}

// L7: rotated refresh rows are pruned once past the reuse window, but the most recent one per grant stays so a replay
// of it is still seen as reuse.
func TestRetention_PrunesOldRotatedRefreshRows(t *testing.T) {
	f := newOAuthFixture(t)
	ctx := t.Context()
	now := time.Now()
	g, _, refresh := f.grant(t, f.user.ID, "password", now.Add(7*24*time.Hour), time.Hour)
	cur := refresh
	for i := 0; i < 3; i++ {
		next := f.token("oauth_refresh", now.Add(7*24*time.Hour))
		if _, err := f.st.RotateOAuthRefresh(ctx, cur.PublicID, cur.SecretHash, f.token("oauth_access", now.Add(time.Hour)), next,
			now.Add(time.Duration(i+1)*time.Minute), time.Minute); err != nil {
			t.Fatal(err)
		}
		cur = next
	}
	if n := f.count(t, `select count(*) from api_tokens where grant_id = $1 and rotated_at is not null`, g.ID); n != 3 {
		t.Fatalf("%d rotated rows before retention, want 3", n)
	}
	if _, err := f.st.DeleteExpiredOAuth(ctx, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `select count(*) from api_tokens where grant_id = $1 and rotated_at is not null`, g.ID); n != 1 {
		t.Fatalf("%d rotated rows after retention, want only the latest", n)
	}
	if n := f.count(t, `select count(*) from api_tokens where grant_id = $1 and rotated_at is null and kind = 'oauth_refresh'`, g.ID); n != 1 {
		t.Fatal("the live refresh token was pruned")
	}
	// A rotation inside the window is untouched.
	if _, err := f.st.DeleteExpiredOAuth(ctx, now.Add(90*time.Second)); err != nil {
		t.Fatal(err)
	}
}
