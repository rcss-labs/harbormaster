package auth

import (
	"context"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/store"
)

func seedAuthToken(t *testing.T, st *store.Store, userID int64, publicID string) store.APIToken {
	t.Helper()
	tok, err := st.CreateAPIToken(context.Background(), store.APIToken{
		PublicID: publicID, UserID: userID, Kind: "pat", Name: "t", SecretHash: "h", Scope: "read",
		AuthSource: "password", ExpiresAt: time.Now().Add(time.Hour),
	}, 10)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func tokenCount(t *testing.T, st *store.Store, userID int64) int {
	t.Helper()
	list, err := st.ListAPITokensForUser(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	return len(list)
}

func TestRevokeTokens_PasswordChangeEndsAll(t *testing.T) {
	svc, st := newTestService(t)
	u := mustUser(t, st, "frank", false)
	bystander := mustUser(t, st, "gina", false)
	ctx := context.Background()
	seedAuthToken(t, st, u.ID, "hmp_aaaaaaaaaaaa")
	seedAuthToken(t, st, u.ID, "hmp_bbbbbbbbbbbb")
	seedAuthToken(t, st, bystander.ID, "hmp_cccccccccccc")
	res := mustLogin(t, svc, "frank", testPassword, "10.0.0.4", "ua")
	sess, _, err := svc.Authenticate(ctx, res.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	ctxSess := context.WithValue(ctx, sessionKey{}, sess)

	if err := svc.ChangePassword(ctxSess, u.ID, testPassword, "new-password-123"); err != nil {
		t.Fatal(err)
	}
	if n := tokenCount(t, st, u.ID); n != 0 {
		t.Fatalf("tokens after password change = %d, want 0", n)
	}
	if n := tokenCount(t, st, bystander.ID); n != 1 {
		t.Fatalf("another user's tokens = %d, want 1", n)
	}
	if _, _, err := svc.Authenticate(ctx, res.SessionID); err != nil {
		t.Fatalf("current session should survive: %v", err)
	}
}

func TestRevokeTokens_FailedPasswordChangeKeepsTokens(t *testing.T) {
	svc, st := newTestService(t)
	u := mustUser(t, st, "frank", false)
	seedAuthToken(t, st, u.ID, "hmp_aaaaaaaaaaaa")
	if err := svc.ChangePassword(context.Background(), u.ID, "wrong-current-pw", "new-password-123"); err == nil {
		t.Fatal("expected wrong password error")
	}
	if n := tokenCount(t, st, u.ID); n != 1 {
		t.Fatalf("tokens = %d, want 1: a rejected change must not revoke", n)
	}
}

func TestRevokeTokens_AdminResetEndsAll(t *testing.T) {
	svc, st := newTestService(t)
	u := mustUser(t, st, "gina", false)
	seedAuthToken(t, st, u.ID, "hmp_aaaaaaaaaaaa")
	seedAuthToken(t, st, u.ID, "hmp_bbbbbbbbbbbb")
	if _, err := svc.AdminResetPassword(context.Background(), u.ID); err != nil {
		t.Fatal(err)
	}
	if n := tokenCount(t, st, u.ID); n != 0 {
		t.Fatalf("tokens after admin reset = %d, want 0", n)
	}
}
