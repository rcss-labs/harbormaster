package store_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

// oauthFixture is a user and a way to mint grants and codes for it.
type oauthFixture struct {
	st   *store.Store
	user store.User
	seq  int
}

func newOAuthFixture(t *testing.T) *oauthFixture {
	t.Helper()
	st := storetest.Open(t)
	user, err := st.CreateUser(t.Context(), "alice", "h", false, false)
	if err != nil {
		t.Fatal(err)
	}
	return &oauthFixture{st: st, user: user}
}

func (f *oauthFixture) token(kind string, expires time.Time) store.APIToken {
	f.seq++
	prefix := map[string]string{"oauth_access": "hmo_", "oauth_refresh": "hmr_"}[kind]
	return store.APIToken{
		PublicID: fmt.Sprintf("%s%010d", prefix, f.seq), Kind: kind, SecretHash: fmt.Sprintf("hash-%d", f.seq),
		Scope: "read", ExpiresAt: expires,
	}
}

func (f *oauthFixture) code(hash string, expires time.Time) store.OAuthCode {
	return store.OAuthCode{CodeHash: hash, UserID: f.user.ID, ClientID: "https://claude.ai/c.json", ClientName: "Claude",
		RedirectURI: "https://claude.ai/api/mcp/auth_callback", CodeChallenge: "challenge", Scope: "harbormaster:read",
		Resource: "https://mcp.example.com/mcp", AuthSource: "password", ExpiresAt: expires}
}

// grant creates a grant ending at end with an access token of accessTTL and a refresh token ending with the grant.
func (f *oauthFixture) grant(t *testing.T, userID int64, source string, end time.Time, accessTTL time.Duration) (store.OAuthGrant, store.APIToken, store.APIToken) {
	t.Helper()
	access := f.token("oauth_access", time.Now().Add(accessTTL))
	refresh := f.token("oauth_refresh", end)
	g, err := f.st.CreateOAuthGrantWithTokens(t.Context(), store.OAuthGrant{
		UserID: userID, ClientID: "https://claude.ai/c.json", ClientName: "Claude", Scope: "harbormaster:read",
		Resource: "https://mcp.example.com/mcp", AuthSource: source, ExpiresAt: end,
	}, access, refresh)
	if err != nil {
		t.Fatal(err)
	}
	return g, access, refresh
}

func (f *oauthFixture) tokenRows(t *testing.T, grantID int64) map[string]int {
	t.Helper()
	rows, err := f.st.Pool.Query(t.Context(), `select kind from api_tokens where grant_id = $1`, grantID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var kind string
		if err := rows.Scan(&kind); err != nil {
			t.Fatal(err)
		}
		out[kind]++
	}
	return out
}

func (f *oauthFixture) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := f.st.Pool.QueryRow(t.Context(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestOAuthCode_ConsumeIsSingleUse(t *testing.T) {
	f := newOAuthFixture(t)
	ctx := t.Context()
	if err := f.st.CreateOAuthCode(ctx, f.code("hash-1", time.Now().Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	got, err := f.st.ConsumeOAuthCode(ctx, "hash-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.UserID != f.user.ID || got.ClientID != "https://claude.ai/c.json" || got.RedirectURI == "" ||
		got.CodeChallenge != "challenge" || got.Resource == "" || got.AuthSource != "password" {
		t.Fatalf("consumed code lost its binding: %+v", got)
	}
	if _, err := f.st.ConsumeOAuthCode(ctx, "hash-1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second consume err = %v, want ErrNotFound", err)
	}
}

func TestOAuthCode_ConcurrentConsumeHasOneWinner(t *testing.T) {
	f := newOAuthFixture(t)
	if err := f.st.CreateOAuthCode(t.Context(), f.code("race", time.Now().Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 8)
	for range 8 {
		go func() { _, err := f.st.ConsumeOAuthCode(t.Context(), "race"); results <- err }()
	}
	wins := 0
	for range 8 {
		if err := <-results; err == nil {
			wins++
		} else if !errors.Is(err, store.ErrNotFound) {
			t.Fatal(err)
		}
	}
	if wins != 1 {
		t.Fatalf("%d consumers won, want exactly 1", wins)
	}
}

func TestOAuthCode_ExpiredIsNotFoundAndGone(t *testing.T) {
	f := newOAuthFixture(t)
	if err := f.st.CreateOAuthCode(t.Context(), f.code("old", time.Now().Add(-time.Second))); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.ConsumeOAuthCode(t.Context(), "old"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if n := f.count(t, `select count(*) from oauth_codes where code_hash = 'old'`); n != 0 {
		t.Fatalf("expired code still stored (%d rows)", n)
	}
}

func TestOAuthCode_UnknownUserIsNotFound(t *testing.T) {
	f := newOAuthFixture(t)
	c := f.code("x", time.Now().Add(time.Minute))
	c.UserID = f.user.ID + 1000
	if err := f.st.CreateOAuthCode(t.Context(), c); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestOAuthGrant_CreateWithTokens(t *testing.T) {
	f := newOAuthFixture(t)
	end := time.Now().Add(7 * 24 * time.Hour)
	g, access, refresh := f.grant(t, f.user.ID, "saml", end, time.Hour)
	if g.ID == 0 || g.UserID != f.user.ID || g.AuthSource != "saml" || g.Scope != "harbormaster:read" {
		t.Fatalf("grant = %+v", g)
	}
	if rows := f.tokenRows(t, g.ID); rows["oauth_access"] != 1 || rows["oauth_refresh"] != 1 || len(rows) != 2 {
		t.Fatalf("token rows = %v, want one access and one refresh", rows)
	}
	// The tokens take the grant's user and auth source.
	got, err := f.st.GetAPITokenByPublicID(t.Context(), access.PublicID)
	if err != nil {
		t.Fatal(err)
	}
	if got.UserID != f.user.ID || got.AuthSource != "saml" || got.GrantID == nil || *got.GrantID != g.ID || got.Name != "Claude" {
		t.Fatalf("access token = %+v", got)
	}
	if _, err := f.st.GetAPITokenByPublicID(t.Context(), refresh.PublicID); err != nil {
		t.Fatal(err)
	}
	grants, err := f.st.ListOAuthGrantsForUser(t.Context(), f.user.ID)
	if err != nil || len(grants) != 1 || grants[0].ID != g.ID {
		t.Fatalf("list = %v, %v", grants, err)
	}
	// A failed transaction leaves no grant behind: a duplicate refresh public id fails the second insert.
	dup := f.token("oauth_refresh", end)
	dup.PublicID = access.PublicID
	_, err = f.st.CreateOAuthGrantWithTokens(t.Context(), store.OAuthGrant{
		UserID: f.user.ID, ClientID: "c", ClientName: "n", Scope: "harbormaster:read", Resource: "r", ExpiresAt: end,
	}, f.token("oauth_access", end), dup)
	if err == nil {
		t.Fatal("duplicate token id accepted")
	}
	if n := f.count(t, `select count(*) from oauth_grants where client_id = 'c'`); n != 0 {
		t.Fatalf("failed creation left %d grant rows", n)
	}
}

func TestRotateOAuthRefresh_Table(t *testing.T) {
	const grace = 60 * time.Second
	type step struct {
		at    time.Duration // offset from the base time
		hash  string        // "" means the right one
		token string        // which refresh token to present: "first" or "second"
		want  error
	}
	cases := []struct {
		name  string
		steps []step
		check func(t *testing.T, f *oauthFixture, g store.OAuthGrant)
	}{
		{"happy path", []step{{at: time.Minute, want: nil}}, func(t *testing.T, f *oauthFixture, g store.OAuthGrant) {
			if rows := f.tokenRows(t, g.ID); rows["oauth_access"] != 1 || rows["oauth_refresh"] != 2 {
				t.Fatalf("rows after rotation = %v, want 1 access (new) and 2 refresh (old marked, new)", rows)
			}
			if n := f.count(t, `select count(*) from api_tokens where grant_id = $1 and rotated_at is not null`, g.ID); n != 1 {
				t.Fatalf("%d rotated refresh tokens, want 1", n)
			}
		}},
		{"wrong hash", []step{{at: time.Minute, hash: "not-the-hash", want: store.ErrNotFound}}, func(t *testing.T, f *oauthFixture, g store.OAuthGrant) {
			if n := f.count(t, `select count(*) from api_tokens where grant_id = $1 and rotated_at is not null`, g.ID); n != 0 {
				t.Fatal("a wrong hash marked the token rotated")
			}
		}},
		{"grant expired", []step{{at: 8 * 24 * time.Hour, want: store.ErrNotFound}}, nil},
		{"retry inside grace", []step{{at: time.Minute}, {at: time.Minute + 30*time.Second, want: store.ErrRefreshRetry}},
			func(t *testing.T, f *oauthFixture, g store.OAuthGrant) {
				if rows := f.tokenRows(t, g.ID); rows["oauth_refresh"] != 2 || rows["oauth_access"] != 1 {
					t.Fatalf("a retry changed the tokens: %v", rows)
				}
			}},
		{"retry exactly at the grace edge", []step{{at: time.Minute}, {at: time.Minute + grace, want: store.ErrRefreshRetry}}, nil},
		{"reuse after grace revokes the grant", []step{{at: time.Minute}, {at: time.Minute + grace + time.Second, want: store.ErrRefreshReused}},
			func(t *testing.T, f *oauthFixture, g store.OAuthGrant) {
				if n := f.count(t, `select count(*) from oauth_grants where id = $1`, g.ID); n != 0 {
					t.Fatal("grant survived reuse")
				}
				if n := f.count(t, `select count(*) from api_tokens where grant_id = $1`, g.ID); n != 0 {
					t.Fatalf("%d tokens survived reuse (the issued access token must stop working)", n)
				}
			}},
		{"the new refresh token still works after the old was reused-within-grace", []step{
			{at: time.Minute}, {at: time.Minute + 10*time.Second, want: store.ErrRefreshRetry},
			{at: 2 * time.Minute, token: "second"},
		}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newOAuthFixture(t)
			// Sub-microsecond nanoseconds, as Linux clocks give: Postgres stores rotated_at at microsecond precision,
			// so the grace edge must hold even when "now" carries more precision than the stored time.
			base := time.Now().Truncate(time.Microsecond).Add(789 * time.Nanosecond)
			end := base.Add(7 * 24 * time.Hour)
			g, _, refresh := f.grant(t, f.user.ID, "password", end, time.Hour)
			original, latest := refresh, refresh
			for i, s := range tc.steps {
				presented := original
				if s.token == "second" {
					presented = latest
				}
				hash := presented.SecretHash
				if s.hash != "" {
					hash = s.hash
				}
				newAccess, newRefresh := f.token("oauth_access", base.Add(s.at+time.Hour)), f.token("oauth_refresh", base.Add(s.at+time.Hour))
				_, err := f.st.RotateOAuthRefresh(t.Context(), presented.PublicID, hash, newAccess, newRefresh, base.Add(s.at), grace)
				if !errors.Is(err, s.want) && (s.want != nil || err != nil) {
					t.Fatalf("step %d: err = %v, want %v", i, err, s.want)
				}
				if err == nil {
					latest = newRefresh
				}
			}
			if tc.check != nil {
				tc.check(t, f, g)
			}
		})
	}
}

func TestRotateOAuthRefresh_RefusesNonRefreshTokens(t *testing.T) {
	f := newOAuthFixture(t)
	_, access, _ := f.grant(t, f.user.ID, "password", time.Now().Add(24*time.Hour), time.Hour)
	_, err := f.st.RotateOAuthRefresh(t.Context(), access.PublicID, access.SecretHash,
		f.token("oauth_access", time.Now().Add(time.Hour)), f.token("oauth_refresh", time.Now().Add(time.Hour)),
		time.Now(), time.Minute)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("rotating an access token: err = %v, want ErrNotFound", err)
	}
	_, err = f.st.RotateOAuthRefresh(t.Context(), "hmr_unknown00000", "x",
		f.token("oauth_access", time.Now().Add(time.Hour)), f.token("oauth_refresh", time.Now().Add(time.Hour)),
		time.Now(), time.Minute)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown token: err = %v, want ErrNotFound", err)
	}
}

func TestRotateOAuthRefresh_NewTokensNeverOutliveTheGrant(t *testing.T) {
	f := newOAuthFixture(t)
	base := time.Now()
	end := base.Add(2 * time.Hour)
	g, _, refresh := f.grant(t, f.user.ID, "password", end, time.Hour)
	// Ask for tokens that would end long after the grant.
	far := base.Add(30 * 24 * time.Hour)
	newAccess, newRefresh := f.token("oauth_access", far), f.token("oauth_refresh", far)
	if _, err := f.st.RotateOAuthRefresh(t.Context(), refresh.PublicID, refresh.SecretHash, newAccess, newRefresh,
		base.Add(time.Minute), time.Minute); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `select count(*) from api_tokens where grant_id = $1 and expires_at > $2`, g.ID, end); n != 0 {
		t.Fatalf("%d tokens outlive the grant", n)
	}
	// Creation caps the same way.
	g2, _, _ := f.grant(t, f.user.ID, "password", end, 30*24*time.Hour)
	if n := f.count(t, `select count(*) from api_tokens where grant_id = $1 and expires_at > $2`, g2.ID, end); n != 0 {
		t.Fatalf("%d created tokens outlive the grant", n)
	}
}

func TestRevocation_AlsoDeletesGrants(t *testing.T) {
	f := newOAuthFixture(t)
	end := time.Now().Add(24 * time.Hour)
	samlGrant, samlAccess, _ := f.grant(t, f.user.ID, "saml", end, time.Hour)
	pwGrant, pwAccess, _ := f.grant(t, f.user.ID, "password", end, time.Hour)

	if _, err := f.st.DeleteAPITokensForUser(t.Context(), f.user.ID, "saml"); err != nil {
		t.Fatal(err)
	}
	grants, err := f.st.ListOAuthGrantsForUser(t.Context(), f.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) != 1 || grants[0].ID != pwGrant.ID {
		t.Fatalf("grants after saml revocation = %+v, want only the password grant", grants)
	}
	if _, err := f.st.GetAPITokenByPublicID(t.Context(), samlAccess.PublicID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("saml access token survived: %v", err)
	}
	if _, err := f.st.GetAPITokenByPublicID(t.Context(), pwAccess.PublicID); err != nil {
		t.Fatalf("password access token lost: %v", err)
	}
	if n := f.count(t, `select count(*) from api_tokens where grant_id = $1`, samlGrant.ID); n != 0 {
		t.Fatal("saml grant tokens remain")
	}

	// By source, across users.
	other, err := f.st.CreateUser(t.Context(), "bob", "h", false, false)
	if err != nil {
		t.Fatal(err)
	}
	bobSaml, _, _ := f.grant(t, other.ID, "saml", end, time.Hour)
	if _, err := f.st.DeleteAPITokensBySource(t.Context(), "saml"); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `select count(*) from oauth_grants where id = $1`, bobSaml.ID); n != 0 {
		t.Fatal("DeleteAPITokensBySource left a saml grant")
	}
	if n := f.count(t, `select count(*) from oauth_grants where id = $1`, pwGrant.ID); n != 1 {
		t.Fatal("DeleteAPITokensBySource removed a password grant")
	}
	// Everything for the user.
	if _, err := f.st.DeleteAPITokensForUser(t.Context(), f.user.ID, ""); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `select count(*) from oauth_grants where user_id = $1`, f.user.ID); n != 0 {
		t.Fatal("revoking all left a grant")
	}
}

func TestOAuthGrant_DeleteIsPerUserAndCascades(t *testing.T) {
	f := newOAuthFixture(t)
	g, access, _ := f.grant(t, f.user.ID, "password", time.Now().Add(24*time.Hour), time.Hour)
	if err := f.st.DeleteOAuthGrant(t.Context(), g.ID, f.user.ID+1); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleting another user's grant: err = %v, want ErrNotFound", err)
	}
	if err := f.st.DeleteOAuthGrant(t.Context(), g.ID, f.user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.GetAPITokenByPublicID(t.Context(), access.PublicID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("access token survived its grant: %v", err)
	}
}

func TestAPIToken_SchemaTiesGrantToKind(t *testing.T) {
	f := newOAuthFixture(t)
	g, _, _ := f.grant(t, f.user.ID, "password", time.Now().Add(24*time.Hour), time.Hour)
	ctx := t.Context()
	_, err := f.st.Pool.Exec(ctx,
		`insert into api_tokens (public_id, user_id, kind, name, secret_hash, scope, expires_at, grant_id)
		 values ('hmp_aaaaaaaaaaaa', $1, 'pat', 'n', 'h', 'read', now() + interval '1 day', $2)`, f.user.ID, g.ID)
	if err == nil {
		t.Fatal("a pat with a grant_id was accepted")
	}
	_, err = f.st.Pool.Exec(ctx,
		`insert into api_tokens (public_id, user_id, kind, name, secret_hash, scope, expires_at)
		 values ('hmo_aaaaaaaaaaaa', $1, 'oauth_access', 'n', 'h', 'read', now() + interval '1 day')`, f.user.ID)
	if err == nil {
		t.Fatal("an oauth_access token without a grant_id was accepted")
	}
	_, err = f.st.Pool.Exec(ctx,
		`insert into api_tokens (public_id, user_id, kind, name, secret_hash, scope, expires_at, grant_id)
		 values ('hmx_aaaaaaaaaaaa', $1, 'bogus', 'n', 'h', 'read', now() + interval '1 day', $2)`, f.user.ID, g.ID)
	if err == nil {
		t.Fatal("an unknown kind was accepted")
	}
	// The grant's scope is fixed too.
	_, err = f.st.Pool.Exec(ctx,
		`insert into oauth_grants (user_id, client_id, client_name, scope, resource, auth_source, expires_at)
		 values ($1, 'c', 'n', 'admin', 'r', 'password', now() + interval '1 day')`, f.user.ID)
	if err == nil {
		t.Fatal("a grant with another scope was accepted")
	}
}

func TestRetention_RemovesExpiredCodesAndGrants(t *testing.T) {
	f := newOAuthFixture(t)
	now := time.Now()
	ctx := t.Context()
	for hash, expires := range map[string]time.Time{
		"stale": now.Add(-11 * time.Minute), "recent": now.Add(-5 * time.Minute), "live": now.Add(time.Minute),
	} {
		if err := f.st.CreateOAuthCode(ctx, f.code(hash, expires)); err != nil {
			t.Fatal(err)
		}
	}
	oldGrant, _, _ := f.grant(t, f.user.ID, "password", now.Add(-25*time.Hour), time.Hour)
	recentGrant, _, _ := f.grant(t, f.user.ID, "password", now.Add(-23*time.Hour), time.Hour)
	liveGrant, _, _ := f.grant(t, f.user.ID, "password", now.Add(24*time.Hour), time.Hour)

	result, err := f.st.RunRetention(ctx, now, store.Retention{})
	if err != nil {
		t.Fatal(err)
	}
	if result["oauth"] != 2 { // the stale code and the stale grant; the grant's tokens go by cascade
		t.Fatalf("oauth removed = %d, want 2", result["oauth"])
	}
	if f.count(t, `select count(*) from oauth_codes where code_hash = 'stale'`) != 0 {
		t.Fatal("stale code kept")
	}
	if f.count(t, `select count(*) from oauth_codes where code_hash in ('recent', 'live')`) != 2 {
		t.Fatal("a code within retention was removed")
	}
	if f.count(t, `select count(*) from oauth_grants where id = $1`, oldGrant.ID) != 0 {
		t.Fatal("grant expired over a day ago kept")
	}
	if f.count(t, `select count(*) from api_tokens where grant_id = $1`, oldGrant.ID) != 0 {
		t.Fatal("tokens of the removed grant kept")
	}
	if f.count(t, `select count(*) from oauth_grants where id in ($1, $2)`, recentGrant.ID, liveGrant.ID) != 2 {
		t.Fatal("a grant within retention was removed")
	}
}

// seedAllCredentials creates a personal token, an OAuth grant with its two tokens and a pending code: 3 tokens, 1 grant, 1 code.
func (f *oauthFixture) seedAllCredentials(t *testing.T) {
	t.Helper()
	end := time.Now().Add(time.Hour)
	mustCreateToken(t, f.st, newAPIToken(f.user.ID, "hmp_revokeall001"))
	f.grant(t, f.user.ID, "password", end, 30*time.Minute)
	if err := f.st.CreateOAuthCode(t.Context(), f.code("code-revokeall", end)); err != nil {
		t.Fatal(err)
	}
}

func (f *oauthFixture) credentialRows(t *testing.T) (tokens, grants, codes int) {
	t.Helper()
	return f.count(t, `select count(*) from api_tokens`), f.count(t, `select count(*) from oauth_grants`),
		f.count(t, `select count(*) from oauth_codes`)
}

func TestDeleteAllAPICredentials_EmptiesAllThreeTablesAndCounts(t *testing.T) {
	f := newOAuthFixture(t)
	f.seedAllCredentials(t)
	bob, err := f.st.CreateUser(t.Context(), "bob", "h", false, false)
	if err != nil {
		t.Fatal(err)
	}
	f.grant(t, bob.ID, "saml", time.Now().Add(time.Hour), 30*time.Minute)

	got, err := f.st.DeleteAllAPICredentials(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if want := (store.RevokedCredentials{Tokens: 5, Grants: 2, Codes: 1}); got != want {
		t.Errorf("counts = %+v, want %+v", got, want)
	}
	if tk, g, c := f.credentialRows(t); tk+g+c != 0 {
		t.Errorf("rows left: tokens=%d grants=%d codes=%d", tk, g, c)
	}
	if n := f.count(t, `select count(*) from users`); n != 2 {
		t.Errorf("users = %d, want 2 untouched", n)
	}
	again, err := f.st.DeleteAllAPICredentials(t.Context())
	if err != nil || again != (store.RevokedCredentials{}) {
		t.Errorf("second call = %+v, %v; want zero counts", again, err)
	}
}

// A failure on the last table must roll back the earlier deletes: revoke is all or nothing.
func TestDeleteAllAPICredentials_FailureLeavesEverythingIntact(t *testing.T) {
	f := newOAuthFixture(t)
	f.seedAllCredentials(t)
	ctx := t.Context()
	for _, stmt := range []string{
		`create function fail_revoke() returns trigger language plpgsql as $$ begin raise exception 'forced failure'; end $$`,
		`create trigger fail_revoke_grants before delete on oauth_grants for each statement execute function fail_revoke()`,
		`create trigger fail_revoke_codes before delete on oauth_codes for each statement execute function fail_revoke()`,
	} {
		if _, err := f.st.Pool.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.st.DeleteAllAPICredentials(ctx); err == nil || !strings.Contains(err.Error(), "forced failure") {
		t.Fatalf("err = %v, want the forced failure", err)
	}
	if tk, g, c := f.credentialRows(t); tk != 3 || g != 1 || c != 1 {
		t.Errorf("after failed revoke tokens=%d grants=%d codes=%d, want 3/1/1", tk, g, c)
	}
}
