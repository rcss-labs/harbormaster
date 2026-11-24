package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func newRunToken(userID int64, publicID string, runID uuid.UUID) store.APIToken {
	return store.APIToken{
		PublicID: publicID, UserID: userID, Kind: "run", Name: "agent run", SecretHash: "hash-" + publicID,
		Scope: "read", AuthSource: "password", ExpiresAt: time.Now().Add(15 * time.Minute), RunID: &runID, RedactEnv: true,
	}
}

func TestRunToken_CreateAndDelete(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	u, _ := st.CreateUser(ctx, "alice", "h", false, false)
	runID := uuid.New()
	created, err := st.CreateRunToken(ctx, newRunToken(u.ID, "hma_aaaaaaaaaaaa", runID))
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == 0 || created.RunID == nil || *created.RunID != runID || !created.RedactEnv || created.GrantID != nil {
		t.Fatalf("created = %+v", created)
	}
	got, err := st.GetAPITokenByPublicID(ctx, "hma_aaaaaaaaaaaa")
	if err != nil || got.RunID == nil || *got.RunID != runID || !got.RedactEnv {
		t.Fatalf("get = %+v, %v", got, err)
	}
	other, _ := st.CreateRunToken(ctx, newRunToken(u.ID, "hma_bbbbbbbbbbbb", uuid.New()))
	if err := st.DeleteRunToken(ctx, runID); err != nil {
		t.Fatal(err)
	}
	if tokenExists(t, st, "hma_aaaaaaaaaaaa") || !tokenExists(t, st, other.PublicID) {
		t.Fatal("DeleteRunToken must remove only that run's token")
	}
	if err := st.DeleteRunToken(ctx, runID); err != nil {
		t.Errorf("deleting an absent run token should be a no-op, got %v", err)
	}
}

func TestRunToken_SchemaShape(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	u, _ := st.CreateUser(ctx, "alice", "h", false, false)
	exec := func(sql string, args ...any) error { _, err := st.Pool.Exec(ctx, sql, args...); return err }
	base := `insert into api_tokens (public_id, user_id, kind, name, secret_hash, scope, auth_source, expires_at, run_id)
		values ($1, $2, $3, 'n', 'h', 'read', 'password', now() + interval '1 day', $4)`
	if err := exec(base, "hma_cccccccccccc", u.ID, "run", nil); err == nil {
		t.Error("a run token without run_id was accepted")
	}
	if err := exec(base, "hmp_cccccccccccc", u.ID, "pat", uuid.New()); err == nil {
		t.Error("a personal token with run_id was accepted")
	}
	if err := exec(`insert into oauth_grants (id, user_id, client_id, client_name, scope, resource, auth_source, expires_at)
		values (900, $1, 'c', 'c', 'harbormaster:read', 'r', 'password', now() + interval '1 day')`, u.ID); err != nil {
		t.Fatal(err)
	}
	if err := exec(`insert into api_tokens (public_id, user_id, kind, name, secret_hash, scope, auth_source, expires_at, run_id, grant_id)
		values ('hma_dddddddddddd', $1, 'run', 'n', 'h', 'read', 'password', now() + interval '1 day', $2, 900)`, u.ID, uuid.New()); err == nil {
		t.Error("a run token with a grant was accepted")
	}
	if err := exec(base, "hma_eeeeeeeeeeee", u.ID, "run", uuid.New()); err != nil {
		t.Errorf("valid run row refused: %v", err)
	}
}

func TestRunToken_NotListedNorCounted(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	u, _ := st.CreateUser(ctx, "alice", "h", false, false)
	mustCreateToken(t, st, newAPIToken(u.ID, "hmp_aaaaaaaaaaaa"))
	run, err := st.CreateRunToken(ctx, newRunToken(u.ID, "hma_aaaaaaaaaaaa", uuid.New()))
	if err != nil {
		t.Fatal(err)
	}

	list, err := st.ListAPITokensForUser(ctx, u.ID)
	if err != nil || len(list) != 1 || list[0].PublicID != "hmp_aaaaaaaaaaaa" {
		t.Fatalf("list = %+v, %v; want only the personal token", list, err)
	}
	counts, err := st.CountAPITokensByUser(ctx)
	if err != nil || counts[u.ID] != 1 {
		t.Fatalf("counts = %v, %v; want 1", counts, err)
	}
	// The per-user limit counts personal tokens only: a run token does not use up the slot.
	if _, err := st.CreateAPIToken(ctx, newAPIToken(u.ID, "hmp_bbbbbbbbbbbb"), 2); err != nil {
		t.Fatalf("second personal token with a limit of 2 and one run token held: %v", err)
	}
	if _, err := st.CreateAPIToken(ctx, newAPIToken(u.ID, "hmp_cccccccccccc"), 2); !errors.Is(err, store.ErrTokenLimit) {
		t.Fatalf("third personal token: err = %v, want ErrTokenLimit", err)
	}
	// The personal revoke path cannot reach a run token.
	if err := st.DeleteAPIToken(ctx, run.ID, u.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("DeleteAPIToken on a run token: err = %v, want ErrNotFound", err)
	}
	if !tokenExists(t, st, "hma_aaaaaaaaaaaa") {
		t.Fatal("the run token was revoked through the personal path")
	}
}

func TestRunToken_BulkRevokesAndRetentionCoverRunKind(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	u, _ := st.CreateUser(ctx, "alice", "h", false, false)
	keep, _ := st.CreateUser(ctx, "bob", "h", false, false)
	mk := func(userID int64, id, source string, expires time.Time) {
		tok := newRunToken(userID, id, uuid.New())
		tok.AuthSource, tok.ExpiresAt = source, expires
		if _, err := st.CreateRunToken(ctx, tok); err != nil {
			t.Fatal(err)
		}
	}
	soon := time.Now().Add(time.Hour)
	mk(u.ID, "hma_aaaaaaaaaaaa", "password", soon)
	mk(u.ID, "hma_bbbbbbbbbbbb", "saml", soon)
	mk(keep.ID, "hma_cccccccccccc", "saml", soon)

	if n, err := st.DeleteAPITokensForUser(ctx, u.ID, "saml"); err != nil || n != 1 {
		t.Fatalf("by user and source: %d, %v", n, err)
	}
	if tokenExists(t, st, "hma_bbbbbbbbbbbb") || !tokenExists(t, st, "hma_aaaaaaaaaaaa") {
		t.Fatal("DeleteAPITokensForUser(saml) must remove only that source's run token")
	}
	if n, err := st.DeleteAPITokensBySource(ctx, "saml"); err != nil || n != 1 || tokenExists(t, st, "hma_cccccccccccc") {
		t.Fatalf("by source: %d, %v", n, err)
	}
	if n, err := st.DeleteAPITokensForUser(ctx, u.ID, ""); err != nil || n != 1 || tokenExists(t, st, "hma_aaaaaaaaaaaa") {
		t.Fatalf("all of a user's: %d, %v", n, err)
	}

	// Expired run tokens go with the expired-token retention.
	mk(u.ID, "hma_dddddddddddd", "password", time.Now().Add(-48*time.Hour))
	mk(u.ID, "hma_eeeeeeeeeeee", "password", soon)
	res, err := st.RunRetention(ctx, time.Now(), store.Retention{})
	if err != nil {
		t.Fatal(err)
	}
	if res["api_tokens"] != 1 || tokenExists(t, st, "hma_dddddddddddd") || !tokenExists(t, st, "hma_eeeeeeeeeeee") {
		t.Fatalf("retention result %v: want only the long-expired run token removed", res)
	}
	if n, err := st.DeleteAllAPICredentials(ctx); err != nil || n.Tokens != 1 {
		t.Fatalf("revoke everything: %+v, %v", n, err)
	}
}
