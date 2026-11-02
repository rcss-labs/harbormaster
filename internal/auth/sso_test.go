package auth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/saml"
	"github.com/rcss-labs/harbormaster/internal/saml/fakeidp"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const testIdP = "https://idp.example/entity"

func ssoCfg() saml.Config {
	return saml.Config{IdPEntityID: testIdP, NameIDFormat: "unspecified", ClockSkew: 120 * time.Second}
}

func ssoIdentity(nameID, email string) saml.Identity {
	attrs := map[string][]string{}
	if email != "" {
		attrs["email"] = []string{email}
	}
	return saml.Identity{
		NameID: nameID, NameIDFormat: "urn:oasis:names:tc:SAML:1.1:nameid-format:unspecified",
		Attributes: attrs, AssertionID: "_a" + nameID, ResponseID: "_r" + nameID,
		NotOnOrAfter: time.Now().Add(5 * time.Minute),
	}
}

// enableSSO flags a new password admin as break-glass, saves a configuration and turns SSO on.
func enableSSO(t *testing.T, st *store.Store) store.User {
	t.Helper()
	ctx := context.Background()
	admin := mustUser(t, st, "pavel", true)
	if _, err := st.SetBreakGlassChecked(ctx, admin.ID, true); err != nil {
		t.Fatal(err)
	}
	configureSSO(t, st)
	if _, err := st.SetSAMLEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	return admin
}

func configureSSO(t *testing.T, st *store.Store) {
	t.Helper()
	_, err := st.UpsertSAMLConfig(context.Background(), store.SAMLConfigRow{
		IdPEntityID: testIdP, IdPSSORedirectURL: "https://idp.example/sso", IdPCertsPEM: []string{"CERT"},
		SPEntityID: "https://hm.example/api/auth/saml/metadata", NameIDFormat: "unspecified",
		ClockSkewSeconds: 120, ButtonLabel: "Sign in with SSO",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func auditEntries(t *testing.T, st *store.Store, action string) []store.AuditEntry {
	t.Helper()
	all, err := st.ListAudit(context.Background(), 500, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.AuditEntry
	for _, e := range all {
		if e.Action == action {
			out = append(out, e)
		}
	}
	return out
}

func TestLoginSSO_CreatesUserWithNoPermissions(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()
	res, outcome, err := svc.LoginSSO(ctx, ssoIdentity("alex@example.test", "alex@example.test"), ssoCfg(), "10.0.0.1", "ua")
	if err != nil {
		t.Fatal(err)
	}
	if outcome != SSOOutcome("create") {
		t.Fatalf("outcome = %q", outcome)
	}
	u := res.User
	if u.Username != "alex" || u.Admin || u.HasPassword() || !u.SSOLinked() || u.MustChangePassword || u.BreakGlass {
		t.Fatalf("unexpected user %+v", u)
	}
	perms, err := st.PermissionsOf(ctx, u.ID)
	if err != nil || len(perms) != 0 {
		t.Fatalf("permissions = %v, %v", perms, err)
	}
	sess, _, err := svc.Authenticate(ctx, res.SessionID)
	if err != nil || sess.AuthSource != "saml" {
		t.Fatalf("session = %+v, %v", sess, err)
	}
	if len(auditEntries(t, st, "sso_user_created")) != 1 {
		t.Fatal("missing sso_user_created audit")
	}
	signIn := auditEntries(t, st, "sso_sign_in")
	if len(signIn) != 1 || signIn[0].Details["jit"] != true {
		t.Fatalf("sso_sign_in audit = %+v", signIn)
	}
}

func TestLoginSSO_ExistingUserSyncsProfile(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()
	if _, _, err := svc.LoginSSO(ctx, ssoIdentity("n1", "old@example.test"), ssoCfg(), "ip", "ua"); err != nil {
		t.Fatal(err)
	}
	res, outcome, err := svc.LoginSSO(ctx, ssoIdentity("n1", "new@example.test"), ssoCfg(), "ip", "ua")
	if err != nil || outcome != SSOOutcome("sign_in") {
		t.Fatalf("outcome = %q, err = %v", outcome, err)
	}
	u, _ := st.GetUser(ctx, res.User.ID)
	if u.Email == nil || *u.Email != "new@example.test" {
		t.Fatalf("email = %v", u.Email)
	}
	n, _ := st.CountUsers(ctx)
	if n != 1 {
		t.Fatalf("users = %d", n)
	}
	if u.LastSignInAt == nil {
		t.Fatal("last sign-in not recorded")
	}
}

func TestLoginSSO_UsernameCollisionGetsSuffix(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()
	local := mustUser(t, st, "alex", false)
	res, _, err := svc.LoginSSO(ctx, ssoIdentity("alex@example.test", "alex@example.test"), ssoCfg(), "ip", "ua")
	if err != nil {
		t.Fatal(err)
	}
	if res.User.Username != "alex-2" || res.User.ID == local.ID {
		t.Fatalf("user = %+v", res.User)
	}
	after, _ := st.GetUser(ctx, local.ID)
	if after.SSOLinked() {
		t.Fatal("local alex was auto-linked by username")
	}
}

func TestLoginSSO_NeverMatchesExistingUserByEmail(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()
	local := mustUser(t, st, "dana", false)
	res, _, err := svc.LoginSSO(ctx, ssoIdentity("someone-else", "dana"), ssoCfg(), "ip", "ua")
	if err != nil {
		t.Fatal(err)
	}
	if res.User.ID == local.ID {
		t.Fatal("signed in as existing user without a link")
	}
}

func TestLoginSSO_PendingLinkBindsExistingUser(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()
	admin := enableSSO(t, st)
	dana := mustUser(t, st, "dana", false)
	if err := st.SetPendingLink(ctx, dana.ID, "dana@example.test", admin.ID, time.Now().Add(14*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	before, _ := st.GetUser(ctx, dana.ID)

	res, outcome, err := svc.LoginSSO(ctx, ssoIdentity("dana@example.test", "dana@example.test"), ssoCfg(), "ip", "ua")
	if err != nil {
		t.Fatal(err)
	}
	if outcome != SSOOutcome("link_pending") || res.User.ID != dana.ID {
		t.Fatalf("outcome = %q user = %+v", outcome, res.User)
	}
	after, _ := st.GetUser(ctx, dana.ID)
	if !after.SSOLinked() || after.PendingLinkNameID != "" {
		t.Fatalf("after = %+v", after)
	}
	if after.PasswordHash != before.PasswordHash {
		t.Fatal("password hash changed")
	}
	linked := auditEntries(t, st, "sso_linked")
	if len(linked) != 1 || linked[0].Details["method"] != "pending" || linked[0].Details["has_password"] != true {
		t.Fatalf("sso_linked audit = %+v", linked)
	}
	if got := linked[0].Details["pending_set_by_user_id"]; got == nil {
		t.Fatalf("pending_set_by_user_id missing: %+v", linked[0].Details)
	}
	if n, _ := st.CountUsers(ctx); n != 2 {
		t.Fatalf("users = %d, want 2 (no JIT user)", n)
	}
}

func TestLoginSSO_ExpiredPendingLinkFallsBackToJIT(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()
	admin := mustUser(t, st, "pavel", true)
	dana := mustUser(t, st, "dana", false)
	if err := st.SetPendingLink(ctx, dana.ID, "dana@example.test", admin.ID, time.Now().Add(14*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	svc.now = func() time.Time { return time.Now().Add(15 * 24 * time.Hour) }
	res, outcome, err := svc.LoginSSO(ctx, ssoIdentity("dana@example.test", "dana@example.test"), ssoCfg(), "ip", "ua")
	if err != nil {
		t.Fatal(err)
	}
	if outcome != SSOOutcome("create") || res.User.ID == dana.ID {
		t.Fatalf("outcome = %q user = %+v", outcome, res.User)
	}
	if after, _ := st.GetUser(ctx, dana.ID); after.SSOLinked() {
		t.Fatal("expired pending link was consumed")
	}
}

func TestLoginSSO_LockedUserRefused(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()
	res, _, err := svc.LoginSSO(ctx, ssoIdentity("n1", "a@example.test"), ssoCfg(), "ip", "ua")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.LockUser(ctx, res.User.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.LoginSSO(ctx, ssoIdentity("n1", "a@example.test"), ssoCfg(), "ip", "ua"); err == nil {
		t.Fatal("locked user signed in via SSO")
	}
}

func TestLoginSSO_SessionNotAfterHonoured(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	id := ssoIdentity("n1", "a@example.test")
	notAfter := time.Now().Add(10 * time.Minute)
	id.SessionNotOnOrAfter = &notAfter
	res, _, err := svc.LoginSSO(ctx, id, ssoCfg(), "ip", "ua")
	if err != nil {
		t.Fatal(err)
	}
	if sess, _, err := svc.Authenticate(ctx, res.SessionID); err != nil || sess.NotAfter == nil {
		t.Fatalf("authenticate = %+v, %v", sess, err)
	}
	svc.now = func() time.Time { return time.Now().Add(11 * time.Minute) }
	if _, _, err := svc.Authenticate(ctx, res.SessionID); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("err = %v, want ErrUnauthenticated", err)
	}
}

func TestLoginSSO_ConcurrentFirstSignIn_OneUser(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	results := make([]LoginResult, 4)
	errs := make([]error, 4)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], _, errs[i] = svc.LoginSSO(ctx, ssoIdentity("racer@example.test", "racer@example.test"), ssoCfg(), "ip", "ua")
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}
	if n, _ := st.CountUsers(ctx); n != 1 {
		t.Fatalf("users = %d, want 1", n)
	}
	for _, r := range results {
		if _, _, err := svc.Authenticate(ctx, r.SessionID); err != nil {
			t.Fatalf("session invalid: %v", err)
		}
	}
}

func TestPreviewSSO_WouldCreateAndWouldSignIn(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()
	admin := mustUser(t, st, "pavel", true)
	p, err := svc.PreviewSSO(ctx, ssoIdentity("alex@example.test", "alex@example.test"), ssoCfg())
	if err != nil || p.Outcome != "would_create" || p.Username != "alex" || p.ExistingUser != nil {
		t.Fatalf("preview = %+v, %v", p, err)
	}
	if n, _ := st.CountUsers(ctx); n != 1 {
		t.Fatal("preview created a user")
	}
	dana := mustUser(t, st, "dana", false)
	if err := st.SetPendingLink(ctx, dana.ID, "dana@example.test", admin.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	p, err = svc.PreviewSSO(ctx, ssoIdentity("dana@example.test", ""), ssoCfg())
	if err != nil || p.Outcome != "would_link" || p.ExistingUser == nil || *p.ExistingUser != "dana" {
		t.Fatalf("pending preview = %+v, %v", p, err)
	}
	if after, _ := st.GetUser(ctx, dana.ID); after.SSOLinked() {
		t.Fatal("preview consumed the pending link")
	}
	if _, _, err := svc.LoginSSO(ctx, ssoIdentity("n9", "n9@example.test"), ssoCfg(), "ip", "ua"); err != nil {
		t.Fatal(err)
	}
	p, err = svc.PreviewSSO(ctx, ssoIdentity("n9", "n9@example.test"), ssoCfg())
	if err != nil || p.Outcome != "would_sign_in" || p.ExistingUser == nil || *p.ExistingUser != "n9" {
		t.Fatalf("sign-in preview = %+v, %v", p, err)
	}
}

func TestStartLink_RequiresPasswordAndUnlinked(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()
	dana := mustUser(t, st, "dana", false)
	if _, err := svc.StartLink(ctx, dana.ID, "wrong-password-1", "10.0.0.9"); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("wrong password: %v", err)
	}
	if u, err := svc.StartLink(ctx, dana.ID, testPassword, "10.0.0.9"); err != nil || u.ID != dana.ID {
		t.Fatalf("ok: %+v %v", u, err)
	}
	res, _, err := svc.LoginSSO(ctx, ssoIdentity("jit", "jit@example.test"), ssoCfg(), "ip", "ua")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.StartLink(ctx, res.User.ID, "", "10.0.0.9"); !errors.Is(err, ErrNoPassword) {
		t.Fatalf("passwordless: %v", err)
	}
	if _, err := svc.LinkSSO(ctx, dana.ID, ssoIdentity("d1", ""), ssoCfg(), "ip"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.StartLink(ctx, dana.ID, testPassword, "10.0.0.9"); !errors.Is(err, store.ErrAlreadyLinked) {
		t.Fatalf("linked: %v", err)
	}
}

func TestStartLink_PasswordlessIgnoresVerifier(t *testing.T) {
	svc, _ := newTestService(t)
	svc.verify = func(string, string) (bool, error) { return true, nil }
	res, _, err := svc.LoginSSO(context.Background(), ssoIdentity("jit", "jit@example.test"), ssoCfg(), "ip", "ua")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.StartLink(context.Background(), res.User.ID, "anything-at-all-1", "10.0.0.9"); !errors.Is(err, ErrNoPassword) {
		t.Fatalf("err = %v, want ErrNoPassword", err)
	}
}

func TestLinkSSO_PasswordUntouched_SignInGatedBySSOState(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()
	admin := mustUser(t, st, "pavel", true)
	dana := mustUser(t, st, "dana", false)
	before, _ := st.GetUser(ctx, dana.ID)

	if _, err := st.SetBreakGlassChecked(ctx, admin.ID, true); err != nil {
		t.Fatal(err)
	}
	linked, err := svc.LinkSSO(ctx, dana.ID, ssoIdentity("dana@example.test", "dana@example.test"), ssoCfg(), "ip")
	if err != nil || !linked.SSOLinked() {
		t.Fatalf("link: %+v %v", linked, err)
	}
	if linked.PasswordHash != before.PasswordHash {
		t.Fatal("link changed the hash")
	}
	if _, err := svc.Login(ctx, "dana", testPassword, "ip", "ua"); err != nil {
		t.Fatalf("SSO off: %v", err)
	}
	configureSSO(t, st)
	if _, err := st.SetSAMLEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Login(ctx, "dana", testPassword, "ip", "ua"); !errors.Is(err, ErrSSORequired) {
		t.Fatalf("SSO on: %v", err)
	}
	if _, err := st.SetBreakGlassChecked(ctx, dana.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Login(ctx, "dana", testPassword, "ip", "ua"); err != nil {
		t.Fatalf("break-glass: %v", err)
	}
	if _, err := st.SetBreakGlassChecked(ctx, dana.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetSAMLEnabled(ctx, false); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Login(ctx, "dana", testPassword, "ip", "ua"); err != nil {
		t.Fatalf("SSO off again: %v", err)
	}
	after, _ := st.GetUser(ctx, dana.ID)
	if after.PasswordHash != before.PasswordHash {
		t.Fatal("hash changed")
	}
	if len(auditEntries(t, st, "sso_linked")) != 1 {
		t.Fatal("missing sso_linked audit")
	}
	if e := auditEntries(t, st, "sso_linked")[0]; e.Details["method"] != "self" || e.Details["has_password"] != true {
		t.Fatalf("details = %+v", e.Details)
	}
}

func TestLinkSSO_IdentityTakenAndConcurrent(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()
	a := mustUser(t, st, "alice", false)
	b := mustUser(t, st, "bob", false)
	c := mustUser(t, st, "carol", false)

	if _, err := svc.LinkSSO(ctx, b.ID, ssoIdentity("shared", ""), ssoCfg(), "ip"); err != nil {
		t.Fatal(err)
	}
	_, err := svc.LinkSSO(ctx, a.ID, ssoIdentity("shared", ""), ssoCfg(), "ip")
	if saml.ReasonOf(err) != ReasonSSOIdentityTaken {
		t.Fatalf("reason = %q (%v)", saml.ReasonOf(err), err)
	}
	if bAfter, _ := st.GetUser(ctx, b.ID); bAfter.ExternalID != "shared" {
		t.Fatal("B was changed")
	}
	if aAfter, _ := st.GetUser(ctx, a.ID); aAfter.SSOLinked() {
		t.Fatal("A got linked")
	}
	if len(auditEntries(t, st, "sso_link_failed")) != 1 {
		t.Fatal("missing sso_link_failed audit")
	}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, uid := range []int64{a.ID, c.ID} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = svc.LinkSSO(ctx, uid, ssoIdentity("race", ""), ssoCfg(), "ip")
		}()
	}
	wg.Wait()
	wins := 0
	for _, e := range errs {
		if e == nil {
			wins++
		} else if saml.ReasonOf(e) != ReasonSSOIdentityTaken {
			t.Fatalf("loser reason = %q", saml.ReasonOf(e))
		}
	}
	if wins != 1 {
		t.Fatalf("wins = %d, want 1", wins)
	}
}

func TestLinkSSO_IdentityPendingElsewhereIsTaken(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()
	admin := mustUser(t, st, "pavel", true)
	a := mustUser(t, st, "alice", false)
	b := mustUser(t, st, "bob", false)
	if err := st.SetPendingLink(ctx, b.ID, "pending-id", admin.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	_, err := svc.LinkSSO(ctx, a.ID, ssoIdentity("pending-id", ""), ssoCfg(), "ip")
	if saml.ReasonOf(err) != ReasonSSOIdentityTaken {
		t.Fatalf("reason = %q", saml.ReasonOf(err))
	}
}

func TestLinkSSO_UserGoneAndHasIdentity(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()
	mustUser(t, st, "pavel", true)
	a := mustUser(t, st, "alice", false)
	if err := st.DeleteUser(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	_, err := svc.LinkSSO(ctx, a.ID, ssoIdentity("x", ""), ssoCfg(), "ip")
	if saml.ReasonOf(err) != ReasonLinkUserGone {
		t.Fatalf("reason = %q", saml.ReasonOf(err))
	}
	b := mustUser(t, st, "bob", false)
	if _, err := svc.LinkSSO(ctx, b.ID, ssoIdentity("y", ""), ssoCfg(), "ip"); err != nil {
		t.Fatal(err)
	}
	_, err = svc.LinkSSO(ctx, b.ID, ssoIdentity("z", ""), ssoCfg(), "ip")
	if saml.ReasonOf(err) != ReasonLinkUserHasIdentity {
		t.Fatalf("reason = %q", saml.ReasonOf(err))
	}
}

func TestLogin_SSOOnRequiresBreakGlass(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()
	enableSSO(t, st)
	mustUser(t, st, "omer", false)

	if _, err := svc.Login(ctx, "omer", testPassword, "10.0.0.1", "ua"); !errors.Is(err, ErrSSORequired) {
		t.Fatalf("right password: %v", err)
	}
	failed := auditEntries(t, st, "sign_in_failed")
	if len(failed) != 1 || failed[0].Details["reason"] != "sso_required" {
		t.Fatalf("audit = %+v", failed)
	}
	if _, err := svc.Login(ctx, "omer", "wrong-password-xx", "10.0.0.1", "ua"); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("wrong password: %v", err)
	}
	res, err := svc.Login(ctx, "pavel", testPassword, "10.0.0.1", "ua")
	if err != nil || res.SessionID == "" {
		t.Fatalf("break-glass: %v", err)
	}
	signIns := auditEntries(t, st, "sign_in")
	if len(signIns) != 1 || signIns[0].Details["break_glass"] != true || signIns[0].Summary != "Signed in with password (break-glass)." {
		t.Fatalf("sign_in audit = %+v", signIns)
	}
	sess, _, err := svc.Authenticate(ctx, res.SessionID)
	if err != nil || sess.AuthSource != "password" || sess.NotAfter != nil {
		t.Fatalf("session = %+v, %v", sess, err)
	}
}

func TestLogin_SSOOff_NonBreakGlassStillSignsIn(t *testing.T) {
	svc, st := newTestService(t)
	mustUser(t, st, "omer", false)
	if _, err := svc.Login(context.Background(), "omer", testPassword, "ip", "ua"); err != nil {
		t.Fatal(err)
	}
	if e := auditEntries(t, st, "sign_in"); len(e) != 1 || e[0].Summary != "Signed in." {
		t.Fatalf("audit = %+v", e)
	}
}

func TestLogin_PasswordlessUserIsAlwaysWrong(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()
	// A verifier that accepts everything must not matter: the refusal is explicit.
	svc.verify = func(string, string) (bool, error) { return true, nil }
	u, err := st.CreateSAMLUser(ctx, store.NewSAMLUser{Username: "jit", IdPEntityID: testIdP, ExternalID: "n1", ExternalIDFormat: "x"})
	if err != nil {
		t.Fatal(err)
	}
	for _, pw := range []string{"", "anything-at-all-1"} {
		if _, err := svc.Login(ctx, "jit", pw, "10.0.0.1", "ua"); !errors.Is(err, ErrBadCredentials) {
			t.Fatalf("password %q: %v", pw, err)
		}
	}
	byUser, byIP, _ := st.FailedAttempts(ctx, "jit", "10.0.0.1", time.Now().Add(-time.Hour))
	if byUser != 2 || byIP != 2 {
		t.Fatalf("attempts = %d/%d, want 2/2 (same accounting as a wrong password)", byUser, byIP)
	}
	for i := 0; i < 3; i++ {
		_, _ = svc.Login(ctx, "jit", "x", "10.0.0.1", "ua")
	}
	after, _ := st.GetUser(ctx, u.ID)
	if after.LockedUntil == nil {
		t.Fatal("repeated attempts did not lock the account like wrong passwords do")
	}
	if len(auditEntries(t, st, "sign_in_failed")) != 5 {
		t.Fatal("failures not audited")
	}
}

func TestChangePassword_PasswordlessRefused(t *testing.T) {
	svc, _ := newTestService(t)
	svc.verify = func(string, string) (bool, error) { return true, nil }
	res, _, err := svc.LoginSSO(context.Background(), ssoIdentity("jit", "jit@example.test"), ssoCfg(), "ip", "ua")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ChangePassword(context.Background(), res.User.ID, "", "a-brand-new-password-1"); !errors.Is(err, ErrNoPassword) {
		t.Fatalf("err = %v, want ErrNoPassword", err)
	}
}

func TestAdminResetPassword_PasswordlessUserGetsOne(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()
	enableSSO(t, st)
	res, _, err := svc.LoginSSO(ctx, ssoIdentity("jit", "jit@example.test"), ssoCfg(), "ip", "ua")
	if err != nil {
		t.Fatal(err)
	}
	temp, err := svc.AdminResetPassword(ctx, res.User.ID)
	if err != nil || temp == "" {
		t.Fatalf("reset: %q %v", temp, err)
	}
	u, _ := st.GetUser(ctx, res.User.ID)
	if !u.HasPassword() || !u.MustChangePassword {
		t.Fatalf("user = %+v", u)
	}
	if _, err := svc.Login(ctx, "jit", temp, "ip", "ua"); !errors.Is(err, ErrSSORequired) {
		t.Fatalf("non-break-glass with SSO on: %v", err)
	}
}

func TestLoginSSO_FakeIdPEndToEnd(t *testing.T) {
	svc, _ := newTestService(t)
	const base = "https://harbor.example.test"
	idp, err := fakeidp.New(fakeidp.Options{BaseURL: "http://127.0.0.1:8481"})
	if err != nil {
		t.Fatal(err)
	}
	md, err := saml.ParseIdPMetadata(idp.Metadata(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	cfg := saml.Config{
		Enabled: true, IdPEntityID: md.EntityID, IdPSSORedirectURL: md.SSORedirectURL, IdPCerts: md.SigningCerts,
		SPEntityID: saml.SPEntityID(base), NameIDFormat: "email", ClockSkew: 120 * time.Second,
	}
	reqID, _ := saml.NewRequestID()
	_, reqXML, err := saml.BuildRedirect(cfg, base, reqID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	b64, err := idp.Response(reqXML, "casey", saml.ACSURL(base), fakeidp.Tamper{})
	if err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM, err := saml.GenerateSPKey("harbor.example.test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	key, err := saml.ParseSPKey(1, certPEM, keyPEM, nil)
	if err != nil {
		t.Fatal(err)
	}
	id, err := (&saml.Verifier{Cfg: cfg, BaseURL: base}).Verify(saml.VerifyInput{ResponseB64: b64, RequestID: reqID, Now: time.Now(), Keys: []saml.SPKey{key}})
	if err != nil {
		t.Fatal(err)
	}
	res, outcome, err := svc.LoginSSO(context.Background(), id, cfg, "ip", "ua")
	if err != nil || outcome != SSOOutcome("create") || res.User.Username != "casey" {
		t.Fatalf("res = %+v outcome = %q err = %v", res.User, outcome, err)
	}
}

func TestStartLink_WrongPasswordsCountAndLockLikeSignIn(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()
	dana := mustUser(t, st, "dana", false)
	for i := 0; i < testConfig().LoginMaxPerUser; i++ {
		if _, err := svc.StartLink(ctx, dana.ID, "wrong-password-1", "10.0.0.9"); !errors.Is(err, ErrWrongPassword) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if n := len(auditEntries(t, st, "sign_in_failed")); n != testConfig().LoginMaxPerUser {
		t.Fatalf("sign_in_failed audits = %d, want %d", n, testConfig().LoginMaxPerUser)
	}
	byUser, byIP, err := st.FailedAttempts(ctx, "dana", "10.0.0.9", time.Now().Add(-time.Hour))
	if err != nil || byUser != testConfig().LoginMaxPerUser || byIP != byUser {
		t.Fatalf("failed attempts = %d/%d, %v", byUser, byIP, err)
	}
	// Locked: even the right password is refused, exactly as Login refuses it.
	if _, err := svc.StartLink(ctx, dana.ID, testPassword, "10.0.0.9"); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("locked user with right password: %v", err)
	}
	if _, err := svc.Login(ctx, "dana", testPassword, "10.0.0.8", "ua"); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("Login for the same user must be locked too: %v", err)
	}
}
