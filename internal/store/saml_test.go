package store_test

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func sampleSAMLConfig() store.SAMLConfigRow {
	return store.SAMLConfigRow{
		IdPEntityID:       "https://idp.example/entity",
		IdPSSORedirectURL: "https://idp.example/sso",
		IdPCertsPEM:       []string{"CERT-A"},
		SPEntityID:        "https://hm.example/api/auth/saml/metadata",
		NameIDFormat:      "unspecified",
		ClockSkewSeconds:  120,
		ButtonLabel:       "Sign in with SSO",
	}
}

func seedSession(t *testing.T, st *store.Store, userID int64, hash, source string) {
	t.Helper()
	now := time.Now()
	err := st.CreateSession(t.Context(), store.Session{
		IDHash: hash, UserID: userID, CSRFToken: "c", CreatedAt: now, LastSeenAt: now,
		ExpiresAt: now.Add(time.Hour), AuthSource: source,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func sessionExists(t *testing.T, st *store.Store, hash string) bool {
	t.Helper()
	_, err := st.GetSession(t.Context(), hash)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	return err == nil
}

func TestSAMLConfig_UpsertAndGet_RoundTrip(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	if _, err := st.GetSAMLConfig(ctx); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unconfigured err = %v, want ErrNotFound", err)
	}
	if on, err := st.SAMLEnabled(ctx); err != nil || on {
		t.Fatalf("SAMLEnabled unconfigured = %v, %v", on, err)
	}
	admin, _ := st.CreateUser(ctx, "alpha", "h", true, false)
	row := sampleSAMLConfig()
	url := "https://idp.example/metadata"
	email := "mail"
	row.MetadataURL = &url
	row.AttrEmail = &email
	row.IdPSSOPostURL = "https://idp.example/post"
	row.IdPCertsPEM = []string{"CERT-A", "CERT-B"}
	row.UpdatedByUserID = &admin.ID
	if _, err := st.UpsertSAMLConfig(ctx, row); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetSAMLConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled || got.IdPEntityID != row.IdPEntityID || got.IdPSSORedirectURL != row.IdPSSORedirectURL ||
		got.IdPSSOPostURL != row.IdPSSOPostURL || len(got.IdPCertsPEM) != 2 || got.IdPCertsPEM[1] != "CERT-B" ||
		got.MetadataURL == nil || *got.MetadataURL != url || got.AttrEmail == nil || *got.AttrEmail != "mail" ||
		got.AttrDisplayName != nil || got.SPEntityID != row.SPEntityID || got.NameIDFormat != "unspecified" ||
		got.ClockSkewSeconds != 120 || got.ButtonLabel != "Sign in with SSO" || got.UpdatedAt.IsZero() ||
		got.UpdatedByUserID == nil || *got.UpdatedByUserID != admin.ID {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	row.ButtonLabel = "Okta"
	if _, err := st.UpsertSAMLConfig(ctx, row); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetSAMLConfig(ctx)
	if got.ButtonLabel != "Okta" {
		t.Fatalf("second upsert did not update: %+v", got)
	}
}

func TestUpsertSAMLConfig_DoesNotEnable(t *testing.T) {
	st := storetest.Open(t)
	row := sampleSAMLConfig()
	row.Enabled = true
	if _, err := st.UpsertSAMLConfig(t.Context(), row); err != nil {
		t.Fatal(err)
	}
	if on, _ := st.SAMLEnabled(t.Context()); on {
		t.Fatal("Upsert must not enable SSO; only SetSAMLEnabled has the guard")
	}
}

func TestUpsertSAMLConfig_EntityIDChangeEndsSAMLSessions(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	u, _ := st.CreateUser(ctx, "alpha", "h", false, false)
	if _, err := st.UpsertSAMLConfig(ctx, sampleSAMLConfig()); err != nil {
		t.Fatal(err)
	}
	seedSession(t, st, u.ID, "saml-1", "saml")
	seedSession(t, st, u.ID, "pw-1", "password")
	row := sampleSAMLConfig()
	row.IdPEntityID = "https://other.example/entity"
	ch, err := st.UpsertSAMLConfig(ctx, row)
	if err != nil {
		t.Fatal(err)
	}
	if ch.SessionsEnded != 1 {
		t.Fatalf("SessionsEnded = %d, want 1", ch.SessionsEnded)
	}
	if sessionExists(t, st, "saml-1") || !sessionExists(t, st, "pw-1") {
		t.Fatal("saml session must be gone, password session must remain")
	}
}

func TestUpsertSAMLConfig_DroppedCertEndsSessions_AddedCertDoesNot(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	u, _ := st.CreateUser(ctx, "alpha", "h", false, false)
	if _, err := st.UpsertSAMLConfig(ctx, sampleSAMLConfig()); err != nil {
		t.Fatal(err)
	}
	seedSession(t, st, u.ID, "saml-1", "saml")
	row := sampleSAMLConfig()
	row.IdPCertsPEM = []string{"CERT-A", "CERT-B"}
	row.ButtonLabel = "changed"
	ch, err := st.UpsertSAMLConfig(ctx, row)
	if err != nil || ch.SessionsEnded != 0 || !sessionExists(t, st, "saml-1") {
		t.Fatalf("superset of certs: change %+v err %v; session must remain", ch, err)
	}
	row.IdPCertsPEM = []string{"CERT-B"}
	ch, err = st.UpsertSAMLConfig(ctx, row)
	if err != nil || ch.SessionsEnded != 1 || sessionExists(t, st, "saml-1") {
		t.Fatalf("dropped cert: change %+v err %v; session must end", ch, err)
	}
}

func TestUpsertSAMLConfig_EntityIDChangeDetachesIdentitiesAndPendingLinks(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	admin, _ := st.CreateUser(ctx, "alpha", "h", true, false)
	linked, _ := st.CreateUser(ctx, "dana", "hash-dana", false, false)
	pending, _ := st.CreateUser(ctx, "omer", "hash-omer", false, false)
	bystander, _ := st.CreateUser(ctx, "plain", "hash-plain", false, false)
	if _, err := st.UpsertSAMLConfig(ctx, sampleSAMLConfig()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.BindSSOIdentity(ctx, store.BindSSO{UserID: linked.ID, IdPEntityID: "https://idp.example/entity", ExternalID: "n1", ExternalIDFormat: "unspecified"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetPendingLink(ctx, pending.ID, "n2", admin.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	row := sampleSAMLConfig()
	row.IdPEntityID = "https://other.example/entity"
	ch, err := st.UpsertSAMLConfig(ctx, row)
	if err != nil {
		t.Fatal(err)
	}
	if ch.IdentitiesDetached != 2 {
		t.Fatalf("IdentitiesDetached = %d, want 2 (one linked, one pending)", ch.IdentitiesDetached)
	}
	for _, id := range []int64{linked.ID, pending.ID, bystander.ID} {
		u, err := st.GetUser(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if u.ExternalID != "" || u.IdPEntityID != "" || u.ExternalIDFormat != "" || u.PendingLinkNameID != "" || u.PendingLinkExpiresAt != nil {
			t.Fatalf("user %s keeps identity columns: %+v", u.Username, u)
		}
		if u.PasswordHash == "" {
			t.Fatalf("user %s lost password", u.Username)
		}
	}
}

func TestSetSAMLEnabled_RequiresBreakGlassAdmin(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	if _, err := st.SetSAMLEnabled(ctx, true); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unconfigured err = %v, want ErrNotFound", err)
	}
	a, _ := st.CreateUser(ctx, "alpha", "h", true, false)
	if _, err := st.UpsertSAMLConfig(ctx, sampleSAMLConfig()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetSAMLEnabled(ctx, true); !errors.Is(err, store.ErrNoBreakGlassAdmin) {
		t.Fatalf("err = %v, want ErrNoBreakGlassAdmin", err)
	}
	if on, _ := st.SAMLEnabled(ctx); on {
		t.Fatal("must stay disabled after refusal")
	}
	if _, err := st.SetBreakGlassChecked(ctx, a.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetSAMLEnabled(ctx, true); err != nil {
		t.Fatalf("enable with break-glass admin: %v", err)
	}
	if on, _ := st.SAMLEnabled(ctx); !on {
		t.Fatal("want enabled")
	}
}

func TestSetSAMLEnabled_NeverTouchesPasswords(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	bg, _ := st.CreateUser(ctx, "alpha", "hash-bg", true, false)
	linked, _ := st.CreateUser(ctx, "dana", "hash-linked", false, false)
	plain, _ := st.CreateUser(ctx, "omer", "hash-plain", false, false)
	if _, err := st.UpsertSAMLConfig(ctx, sampleSAMLConfig()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{bg.ID, linked.ID} {
		if _, err := st.BindSSOIdentity(ctx, store.BindSSO{UserID: id, IdPEntityID: "https://idp.example/entity", ExternalID: "n" + string(rune('a'+id)), ExternalIDFormat: "unspecified"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.SetBreakGlassChecked(ctx, bg.ID, true); err != nil {
		t.Fatal(err)
	}
	want := map[int64]string{bg.ID: "hash-bg", linked.ID: "hash-linked", plain.ID: "hash-plain"}
	check := func(stage string) {
		t.Helper()
		for id, hash := range want {
			u, err := st.GetUser(ctx, id)
			if err != nil || u.PasswordHash != hash {
				t.Fatalf("%s: user %d hash = %q (%v), want %q", stage, id, u.PasswordHash, err, hash)
			}
		}
	}
	if _, err := st.SetSAMLEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	check("after enable")
	if _, err := st.SetSAMLEnabled(ctx, false); err != nil {
		t.Fatal(err)
	}
	check("after disable")
}

func TestSetSAMLEnabled_DisableEndsOnlySAMLSessions(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	a, _ := st.CreateUser(ctx, "alpha", "h", true, false)
	if _, err := st.SetBreakGlassChecked(ctx, a.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertSAMLConfig(ctx, sampleSAMLConfig()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetSAMLEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	seedSession(t, st, a.ID, "saml-1", "saml")
	seedSession(t, st, a.ID, "pw-1", "password")
	ch, err := st.SetSAMLEnabled(ctx, false)
	if err != nil || ch.SessionsEnded != 1 {
		t.Fatalf("disable: %+v, %v", ch, err)
	}
	if sessionExists(t, st, "saml-1") || !sessionExists(t, st, "pw-1") {
		t.Fatal("only the saml session must end")
	}
	// Disabling again is a no-op; enabling never ends sessions.
	seedSession(t, st, a.ID, "saml-2", "saml")
	if ch, err := st.SetSAMLEnabled(ctx, false); err != nil || ch.SessionsEnded != 0 {
		t.Fatalf("repeat disable: %+v, %v", ch, err)
	}
	if ch, err := st.SetSAMLEnabled(ctx, true); err != nil || ch.SessionsEnded != 0 || !sessionExists(t, st, "saml-2") {
		t.Fatalf("enable: %+v, %v", ch, err)
	}
}

func TestCreateSAMLRequest_LinkRequiresUserID(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	u, _ := st.CreateUser(ctx, "alpha", "h", false, false)
	exp := time.Now().Add(time.Minute)
	bad := []store.SAMLRequest{
		{ID: "_1", Purpose: "link", ReturnTo: "/", BindingHash: "b", ExpiresAt: exp},
		{ID: "_2", Purpose: "login", ReturnTo: "/", BindingHash: "b", UserID: &u.ID, ExpiresAt: exp},
	}
	for _, r := range bad {
		if err := st.CreateSAMLRequest(ctx, r); err == nil {
			t.Fatalf("request %+v accepted, want constraint failure", r)
		}
	}
	good := store.SAMLRequest{ID: "_3", Purpose: "link", ReturnTo: "/", BindingHash: "b", UserID: &u.ID, ExpiresAt: exp}
	if err := st.CreateSAMLRequest(ctx, good); err != nil {
		t.Fatal(err)
	}
	got, err := st.ConsumeSAMLRequest(ctx, "_3", time.Now())
	if err != nil || got.UserID == nil || *got.UserID != u.ID || got.Purpose != "link" || got.BindingHash != "b" || got.ReturnTo != "/" {
		t.Fatalf("consume = %+v, %v", got, err)
	}
}

func TestInsertSPKey_RetiresPrevious(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	first, err := st.InsertSPKey(ctx, "CERT1", []byte{1})
	if err != nil || first.RetiredAt != nil || first.ID == 0 {
		t.Fatalf("first = %+v, %v", first, err)
	}
	second, err := st.InsertSPKey(ctx, "CERT2", []byte{2})
	if err != nil {
		t.Fatal(err)
	}
	keys, err := st.ListSPKeys(ctx)
	if err != nil || len(keys) != 2 {
		t.Fatalf("keys = %+v, %v", keys, err)
	}
	if keys[0].ID != second.ID || keys[0].RetiredAt != nil || keys[1].ID != first.ID || keys[1].RetiredAt == nil {
		t.Fatalf("order/retirement wrong: %+v", keys)
	}
	if string(keys[0].KeyEnc) != "\x02" || keys[0].CertPEM != "CERT2" {
		t.Fatalf("payload wrong: %+v", keys[0])
	}
}

func TestConsumeSAMLRequest_OnceAndNotExpired(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	now := time.Now()
	req := store.SAMLRequest{ID: "_a", Purpose: "login", ReturnTo: "/x", BindingHash: "h", ExpiresAt: now.Add(time.Minute)}
	if err := st.CreateSAMLRequest(ctx, req); err != nil {
		t.Fatal(err)
	}
	got, err := st.ConsumeSAMLRequest(ctx, "_a", now)
	if err != nil || got.ReturnTo != "/x" || got.ID != "_a" {
		t.Fatalf("first consume = %+v, %v", got, err)
	}
	if _, err := st.ConsumeSAMLRequest(ctx, "_a", now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second consume err = %v, want ErrNotFound", err)
	}
	req.ID = "_b"
	if err := st.CreateSAMLRequest(ctx, req); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ConsumeSAMLRequest(ctx, "_b", now.Add(2*time.Minute)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired consume err = %v, want ErrNotFound", err)
	}
}

func TestConsumeSAMLRequest_Concurrent_ExactlyOneWins(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	req := store.SAMLRequest{ID: "_c", Purpose: "login", ReturnTo: "/", BindingHash: "h", ExpiresAt: time.Now().Add(time.Minute)}
	if err := st.CreateSAMLRequest(ctx, req); err != nil {
		t.Fatal(err)
	}
	var wins, misses atomic.Int32
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := st.ConsumeSAMLRequest(ctx, "_c", time.Now())
			switch {
			case err == nil:
				wins.Add(1)
			case errors.Is(err, store.ErrNotFound):
				misses.Add(1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 || misses.Load() != 19 {
		t.Fatalf("wins = %d, misses = %d, want 1 and 19", wins.Load(), misses.Load())
	}
}

func TestSAMLTestResult_PutAndTakeOnce(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	now := time.Now()
	req := store.SAMLRequest{ID: "_t", Purpose: "test", ReturnTo: "/settings/sso", BindingHash: "h", ExpiresAt: now.Add(time.Minute)}
	if err := st.CreateSAMLRequest(ctx, req); err != nil {
		t.Fatal(err)
	}
	// A pending (unconsumed) test row has no result yet.
	if _, err := st.TakeSAMLTestResult(ctx, "_t"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("take before put err = %v, want ErrNotFound", err)
	}
	consumed, err := st.ConsumeSAMLRequest(ctx, "_t", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PutSAMLTestResult(ctx, consumed, []byte(`{"ok":true}`), now.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, err := st.TakeSAMLTestResult(ctx, "_t")
	if err != nil || string(got) != `{"ok": true}` && string(got) != `{"ok":true}` {
		t.Fatalf("take = %q, %v", got, err)
	}
	if _, err := st.TakeSAMLTestResult(ctx, "_t"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second take err = %v, want ErrNotFound", err)
	}
}

func TestRememberAssertionID_DuplicateIsConflict(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	exp := time.Now().Add(time.Hour)
	if err := st.RememberAssertionID(ctx, "_as1", exp); err != nil {
		t.Fatal(err)
	}
	if err := st.RememberAssertionID(ctx, "_as1", exp); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate err = %v, want ErrConflict", err)
	}
	if err := st.RememberAssertionID(ctx, "_as2", exp); err != nil {
		t.Fatalf("different id: %v", err)
	}
}

func TestUpsertSAMLConfig_EntityIDChange_KeepsPasswordlessIdentity(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	const oldEntity = "https://idp.example/entity"
	admin, _ := st.CreateUser(ctx, "alpha", "h", true, false)
	jit, err := st.CreateSAMLUser(ctx, store.NewSAMLUser{Username: "jit", IdPEntityID: oldEntity, ExternalID: "jit-id", ExternalIDFormat: "unspecified"})
	if err != nil {
		t.Fatal(err)
	}
	linked, _ := st.CreateUser(ctx, "dana", "hash-dana", false, false)
	pending, _ := st.CreateUser(ctx, "omer", "hash-omer", false, false)
	if _, err := st.UpsertSAMLConfig(ctx, sampleSAMLConfig()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.BindSSOIdentity(ctx, store.BindSSO{UserID: linked.ID, IdPEntityID: oldEntity, ExternalID: "n1", ExternalIDFormat: "unspecified"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetPendingLink(ctx, pending.ID, "n2", admin.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	seedSession(t, st, jit.ID, "saml-jit", "saml")
	seedSession(t, st, linked.ID, "saml-linked", "saml")

	row := sampleSAMLConfig()
	row.IdPEntityID = "https://other.example/entity"
	ch, err := st.UpsertSAMLConfig(ctx, row)
	if err != nil {
		t.Fatalf("entity id change with a password-less identity: %v", err)
	}
	if ch.SessionsEnded != 2 || ch.IdentitiesDetached != 2 {
		t.Fatalf("change = %+v, want 2 sessions ended and 2 rows changed (linked + pending)", ch)
	}
	if sessionExists(t, st, "saml-jit") || sessionExists(t, st, "saml-linked") {
		t.Fatal("all saml sessions must end")
	}
	gotJIT, _ := st.GetUser(ctx, jit.ID)
	if gotJIT.IdPEntityID != oldEntity || gotJIT.ExternalID != "jit-id" {
		t.Fatalf("password-less identity must be kept: %+v", gotJIT)
	}
	if _, err := st.GetUserByExternalID(ctx, row.IdPEntityID, "jit-id"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("lookup under the new entity id err = %v, want ErrNotFound", err)
	}
	gotLinked, _ := st.GetUser(ctx, linked.ID)
	gotPending, _ := st.GetUser(ctx, pending.ID)
	if gotLinked.ExternalID != "" || gotLinked.PasswordHash != "hash-dana" || gotPending.PendingLinkNameID != "" {
		t.Fatalf("linked %+v pending %+v", gotLinked, gotPending)
	}
}
