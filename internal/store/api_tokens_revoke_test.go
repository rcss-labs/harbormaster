package store_test

import (
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func seedTokenWithSource(t *testing.T, st *store.Store, userID int64, publicID, source string) {
	t.Helper()
	tok := newAPIToken(userID, publicID)
	tok.AuthSource = source
	mustCreateToken(t, st, tok)
}

func TestRevokeTokens_AdminDemotionEndsAll(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	if _, err := st.CreateUser(ctx, "keeper", "h", true, false); err != nil {
		t.Fatal(err)
	}
	u, _ := st.CreateUser(ctx, "alice", "h", true, false)
	seedTokenWithSource(t, st, u.ID, "hmp_pwpwpwpwpwpw", "password")
	seedTokenWithSource(t, st, u.ID, "hmp_samlsamlsaml", "saml")
	seedSession(t, st, u.ID, "alice-sess", "password")

	if changed, err := st.SetAdminChecked(ctx, u.ID, false); err != nil || !changed {
		t.Fatalf("demote = %v, %v", changed, err)
	}
	if tokenExists(t, st, "hmp_pwpwpwpwpwpw") || tokenExists(t, st, "hmp_samlsamlsaml") {
		t.Fatal("a token survived demotion")
	}
	if sessionExists(t, st, "alice-sess") {
		t.Fatal("session survived demotion")
	}
}

func TestRevokeTokens_PromotionToAdminEndsAll(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	u, _ := st.CreateUser(ctx, "alice", "h", false, false)
	bystander, _ := st.CreateUser(ctx, "bob", "h", false, false)
	seedTokenWithSource(t, st, u.ID, "hmp_pwpwpwpwpwpw", "password")
	seedTokenWithSource(t, st, u.ID, "hmp_samlsamlsaml", "saml")
	seedTokenWithSource(t, st, bystander.ID, "hmp_bystanderbyst", "password")
	seedSession(t, st, u.ID, "alice-sess", "password")

	if changed, err := st.SetAdminChecked(ctx, u.ID, true); err != nil || !changed {
		t.Fatalf("promote = %v, %v", changed, err)
	}
	if tokenExists(t, st, "hmp_pwpwpwpwpwpw") || tokenExists(t, st, "hmp_samlsamlsaml") {
		t.Fatal("a token survived promotion")
	}
	if !sessionExists(t, st, "alice-sess") {
		t.Fatal("promotion must not end sessions")
	}
	if !tokenExists(t, st, "hmp_bystanderbyst") {
		t.Fatal("promotion removed another user's token")
	}
}

func TestRevokeTokens_UnchangedAdminFlagKeepsTokens(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	u, _ := st.CreateUser(ctx, "alice", "h", false, false)
	seedTokenWithSource(t, st, u.ID, "hmp_pwpwpwpwpwpw", "password")
	if changed, err := st.SetAdminChecked(ctx, u.ID, false); err != nil || changed {
		t.Fatalf("no-op = %v, %v", changed, err)
	}
	if !tokenExists(t, st, "hmp_pwpwpwpwpwpw") {
		t.Fatal("a no-op admin write removed the token")
	}
}

func TestRevokeTokens_UnlinkSSOEndsSAMLTokensOnly(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	u, _ := st.CreateUser(ctx, "alice", "orig", false, false)
	other, _ := st.CreateUser(ctx, "bob", "orig", false, false)
	for nameID, id := range map[string]int64{"name-a": u.ID, "name-b": other.ID} {
		if _, err := st.BindSSOIdentity(ctx, store.BindSSO{UserID: id, IdPEntityID: "e", ExternalID: nameID, ExternalIDFormat: "unspecified"}); err != nil {
			t.Fatal(err)
		}
	}
	seedTokenWithSource(t, st, u.ID, "hmp_pwpwpwpwpwpw", "password")
	seedTokenWithSource(t, st, u.ID, "hmp_samlsamlsaml", "saml")
	seedTokenWithSource(t, st, other.ID, "hmp_othersamlsam", "saml")

	if _, _, err := st.UnlinkSSO(ctx, u.ID, "ignored"); err != nil {
		t.Fatal(err)
	}
	if tokenExists(t, st, "hmp_samlsamlsaml") {
		t.Fatal("saml token survived unlink")
	}
	if !tokenExists(t, st, "hmp_pwpwpwpwpwpw") {
		t.Fatal("password token was removed by unlink")
	}
	if !tokenExists(t, st, "hmp_othersamlsam") {
		t.Fatal("another user's saml token was removed")
	}
}

func TestRevokeTokens_SAMLDisabledEndsSAMLTokens(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	a, _ := st.CreateUser(ctx, "alice", "h", false, false)
	b, _ := st.CreateUser(ctx, "bob", "h", false, false)
	seedTokenWithSource(t, st, a.ID, "hmp_alicesamlsaml", "saml")
	seedTokenWithSource(t, st, b.ID, "hmp_bobsamlsamlsa", "saml")
	seedTokenWithSource(t, st, a.ID, "hmp_alicepassword", "password")
	enableSAMLRaw(t, st, true)

	if _, err := st.SetSAMLEnabled(ctx, false); err != nil {
		t.Fatal(err)
	}
	if tokenExists(t, st, "hmp_alicesamlsaml") || tokenExists(t, st, "hmp_bobsamlsamlsa") {
		t.Fatal("a saml token survived SSO being turned off")
	}
	if !tokenExists(t, st, "hmp_alicepassword") {
		t.Fatal("password token removed when SSO was turned off")
	}
}

func TestRevokeTokens_SAMLEntityChangeEndsSAMLTokens(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	a, _ := st.CreateUser(ctx, "alice", "h", false, false)
	row := sampleSAMLConfig()
	if _, err := st.UpsertSAMLConfig(ctx, row); err != nil {
		t.Fatal(err)
	}
	seedTokenWithSource(t, st, a.ID, "hmp_alicesamlsaml", "saml")
	seedTokenWithSource(t, st, a.ID, "hmp_alicepassword", "password")
	// Unrelated edit: tokens stay.
	row.ButtonLabel = "Okta"
	if _, err := st.UpsertSAMLConfig(ctx, row); err != nil {
		t.Fatal(err)
	}
	if !tokenExists(t, st, "hmp_alicesamlsaml") {
		t.Fatal("a cosmetic SSO edit removed tokens")
	}
	row.IdPEntityID = "https://other-idp.example/entity"
	if _, err := st.UpsertSAMLConfig(ctx, row); err != nil {
		t.Fatal(err)
	}
	if tokenExists(t, st, "hmp_alicesamlsaml") {
		t.Fatal("saml token survived an IdP entity change")
	}
	if !tokenExists(t, st, "hmp_alicepassword") {
		t.Fatal("password token removed by an IdP entity change")
	}
}

func TestRevokeTokens_LockoutEndsNone(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	u, _ := st.CreateUser(ctx, "alice", "h", false, false)
	seedTokenWithSource(t, st, u.ID, "hmp_pwpwpwpwpwpw", "password")
	if err := st.LockUser(ctx, u.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if !tokenExists(t, st, "hmp_pwpwpwpwpwpw") {
		t.Fatal("lockout removed the token")
	}
}
