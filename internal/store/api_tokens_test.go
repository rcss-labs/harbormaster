package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

// newAPIToken is a valid personal token row for userID; callers override fields they care about.
func newAPIToken(userID int64, publicID string) store.APIToken {
	return store.APIToken{
		PublicID: publicID, UserID: userID, Kind: "pat", Name: "laptop", SecretHash: "hash-" + publicID,
		Scope: "read", AuthSource: "password", ExpiresAt: time.Now().Add(24 * time.Hour),
	}
}

func mustCreateToken(t *testing.T, st *store.Store, tok store.APIToken) store.APIToken {
	t.Helper()
	created, err := st.CreateAPIToken(t.Context(), tok, 100)
	if err != nil {
		t.Fatalf("CreateAPIToken %s: %v", tok.PublicID, err)
	}
	return created
}

func tokenExists(t *testing.T, st *store.Store, publicID string) bool {
	t.Helper()
	_, err := st.GetAPITokenByPublicID(t.Context(), publicID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	return err == nil
}

func TestAPIToken_CreateGetList(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	u, _ := st.CreateUser(ctx, "alice", "h", false, false)
	created := mustCreateToken(t, st, newAPIToken(u.ID, "hmp_aaaaaaaaaaaa"))
	if created.ID == 0 || created.CreatedAt.IsZero() {
		t.Fatalf("created = %+v, want id and created_at", created)
	}
	got, err := st.GetAPITokenByPublicID(ctx, "hmp_aaaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	if got.SecretHash != "hash-hmp_aaaaaaaaaaaa" || got.UserID != u.ID || got.Name != "laptop" || got.Scope != "read" ||
		got.AuthSource != "password" || got.LastUsedAt != nil {
		t.Fatalf("got = %+v", got)
	}
	mustCreateToken(t, st, newAPIToken(u.ID, "hmp_bbbbbbbbbbbb"))
	list, err := st.ListAPITokensForUser(ctx, u.ID)
	if err != nil || len(list) != 2 {
		t.Fatalf("list = %v, %v", list, err)
	}
	if list[0].PublicID != "hmp_bbbbbbbbbbbb" {
		t.Errorf("list is not newest first: %s", list[0].PublicID)
	}
	if err := st.TouchAPIToken(ctx, created.ID, time.Now(), "10.1.2.3"); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetAPITokenByPublicID(ctx, "hmp_aaaaaaaaaaaa")
	if got.LastUsedAt == nil || got.LastUsedIP != "10.1.2.3" {
		t.Errorf("touch not recorded: %+v", got)
	}
}

func TestAPIToken_ExpiredIsNotFound(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	u, _ := st.CreateUser(ctx, "alice", "h", false, false)
	tok := newAPIToken(u.ID, "hmp_aaaaaaaaaaaa")
	tok.ExpiresAt = time.Now().Add(-time.Minute)
	mustCreateToken(t, st, tok)
	if _, err := st.GetAPITokenByPublicID(ctx, tok.PublicID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired get err = %v, want ErrNotFound", err)
	}
	list, err := st.ListAPITokensForUser(ctx, u.ID)
	if err != nil || len(list) != 0 {
		t.Fatalf("list = %v, %v; want empty", list, err)
	}
}

func TestAPIToken_LimitPerUser(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	u, _ := st.CreateUser(ctx, "alice", "h", false, false)
	other, _ := st.CreateUser(ctx, "bob", "h", false, false)
	expired := newAPIToken(u.ID, "hmp_expiredexpir")
	expired.ExpiresAt = time.Now().Add(-time.Hour)
	if _, err := st.CreateAPIToken(ctx, expired, 2); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"hmp_aaaaaaaaaaaa", "hmp_bbbbbbbbbbbb"} {
		if _, err := st.CreateAPIToken(ctx, newAPIToken(u.ID, id), 2); err != nil {
			t.Fatalf("create %s (expired must not count): %v", id, err)
		}
	}
	if _, err := st.CreateAPIToken(ctx, newAPIToken(u.ID, "hmp_cccccccccccc"), 2); !errors.Is(err, store.ErrTokenLimit) {
		t.Fatalf("third create err = %v, want ErrTokenLimit", err)
	}
	if _, err := st.CreateAPIToken(ctx, newAPIToken(other.ID, "hmp_dddddddddddd"), 2); err != nil {
		t.Fatalf("other user affected by the limit: %v", err)
	}
}

func TestAPIToken_DeleteIsScopedToOwner(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	u, _ := st.CreateUser(ctx, "alice", "h", false, false)
	other, _ := st.CreateUser(ctx, "bob", "h", false, false)
	created := mustCreateToken(t, st, newAPIToken(u.ID, "hmp_aaaaaaaaaaaa"))
	if err := st.DeleteAPIToken(ctx, created.ID, other.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("delete as other err = %v, want ErrNotFound", err)
	}
	if !tokenExists(t, st, "hmp_aaaaaaaaaaaa") {
		t.Fatal("token was removed by a non-owner")
	}
	if err := st.DeleteAPIToken(ctx, created.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	if tokenExists(t, st, "hmp_aaaaaaaaaaaa") {
		t.Fatal("token survived its owner's delete")
	}
}

func TestAPIToken_DeleteForUserBySource(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	u, _ := st.CreateUser(ctx, "alice", "h", false, false)
	other, _ := st.CreateUser(ctx, "bob", "h", false, false)
	mk := func(userID int64, id, source string) {
		tok := newAPIToken(userID, id)
		tok.AuthSource = source
		mustCreateToken(t, st, tok)
	}
	mk(u.ID, "hmp_pwpwpwpwpwpw", "password")
	mk(u.ID, "hmp_samlsamlsaml", "saml")
	mk(other.ID, "hmp_othersamlsam", "saml")

	n, err := st.DeleteAPITokensForUser(ctx, u.ID, "saml")
	if err != nil || n != 1 {
		t.Fatalf("delete saml = %d, %v; want 1", n, err)
	}
	if tokenExists(t, st, "hmp_samlsamlsaml") || !tokenExists(t, st, "hmp_pwpwpwpwpwpw") || !tokenExists(t, st, "hmp_othersamlsam") {
		t.Fatal("source-scoped delete removed the wrong rows")
	}
	n, err = st.DeleteAPITokensForUser(ctx, u.ID, "")
	if err != nil || n != 1 {
		t.Fatalf("delete all = %d, %v; want 1", n, err)
	}
	if tokenExists(t, st, "hmp_pwpwpwpwpwpw") || !tokenExists(t, st, "hmp_othersamlsam") {
		t.Fatal("delete-all removed the wrong rows")
	}
}

func TestAPIToken_CascadesWithUser(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	u, _ := st.CreateUser(ctx, "alice", "h", false, false)
	mustCreateToken(t, st, newAPIToken(u.ID, "hmp_aaaaaaaaaaaa"))
	if err := storetest.DeleteUser(ctx, st, u.ID); err != nil {
		t.Fatal(err)
	}
	if tokenExists(t, st, "hmp_aaaaaaaaaaaa") {
		t.Fatal("token outlived its user")
	}
}

func TestAPIToken_SchemaRefusesOtherScopes(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	u, _ := st.CreateUser(ctx, "alice", "h", false, false)
	insert := func(kind, scope, source string) error {
		_, err := st.Pool.Exec(ctx, `insert into api_tokens (public_id, user_id, kind, name, secret_hash, scope, auth_source, expires_at)
			values ('hmp_rawrawrawraw', $1, $2, 'n', 'h', $3, $4, now() + interval '1 day')`, u.ID, kind, scope, source)
		return err
	}
	if err := insert("pat", "write", "password"); err == nil {
		t.Error("scope = 'write' was accepted")
	}
	if err := insert("oauth_access", "read", "password"); err == nil {
		t.Error("kind = 'oauth_access' was accepted")
	}
	if err := insert("pat", "read", "ldap"); err == nil {
		t.Error("auth_source = 'ldap' was accepted")
	}
	if err := insert("pat", "read", "password"); err != nil {
		t.Errorf("valid row refused: %v", err)
	}
}
