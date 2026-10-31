package auth

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

const testPassword = "a-good-password-1"

func testConfig() config.Config {
	return config.Config{
		SessionKey:      "test-key",
		SessionIdle:     12 * time.Hour,
		SessionAbsolute: 7 * 24 * time.Hour,
		LoginMaxPerUser: 5,
		LoginMaxPerIP:   30,
		LoginWindow:     15 * time.Minute,
	}
}

func newTestService(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	st := storetest.Open(t)
	return NewService(st, testConfig()), st
}

func mustUser(t *testing.T, st *store.Store, name string, admin bool) store.User {
	t.Helper()
	hash, err := HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	u, err := st.CreateUser(context.Background(), name, hash, admin, false)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func auditActions(t *testing.T, st *store.Store, action string) int {
	t.Helper()
	entries, err := st.ListAudit(context.Background(), 500, 0)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if e.Action == action {
			n++
		}
	}
	return n
}

func TestLogin_GenericErrorForUnknownUser(t *testing.T) {
	svc, _ := newTestService(t)
	_, err := svc.Login(context.Background(), "nobody", "whatever-password", "10.0.0.1", "ua")
	if !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("err = %v, want ErrBadCredentials", err)
	}
}

func TestLogin_SuccessCreatesSession(t *testing.T) {
	svc, st := newTestService(t)
	mustUser(t, st, "alice", false)
	res, err := svc.Login(context.Background(), "ALICE", testPassword, "10.0.0.1", "ua")
	if err != nil {
		t.Fatal(err)
	}
	if res.SessionID == "" || res.CSRFToken == "" || res.User.Username != "alice" {
		t.Fatalf("bad result %+v", res)
	}
	sess, user, err := svc.Authenticate(context.Background(), res.SessionID)
	if err != nil || user.ID != res.User.ID || sess.CSRFToken != res.CSRFToken {
		t.Fatalf("authenticate = %+v %+v %v", sess, user, err)
	}
	if sess.IDHash == res.SessionID {
		t.Fatal("raw session id stored")
	}
	if auditActions(t, st, "sign_in") != 1 {
		t.Fatal("expected one sign_in audit entry")
	}
}

func TestLogin_LocksAfterFiveFailures(t *testing.T) {
	svc, st := newTestService(t)
	u := mustUser(t, st, "bob", false)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if _, err := svc.Login(ctx, "bob", "wrong-password-x", "10.0.0.2", "ua"); !errors.Is(err, ErrBadCredentials) {
			t.Fatalf("attempt %d err = %v", i, err)
		}
	}
	if _, err := svc.Login(ctx, "bob", testPassword, "10.0.0.2", "ua"); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("login while locked = %v", err)
	}
	got, err := st.GetUser(ctx, u.ID)
	if err != nil || got.LockedUntil == nil {
		t.Fatalf("user not locked: %+v %v", got, err)
	}
	if d := time.Until(*got.LockedUntil); d < 14*time.Minute || d > 16*time.Minute {
		t.Fatalf("locked_until in %v, want about 15m", d)
	}
	if n := auditActions(t, st, "sign_in_failed"); n < 5 {
		t.Fatalf("sign_in_failed audit entries = %d, want >= 5", n)
	}
}

func TestLogin_PerIPLimit(t *testing.T) {
	svc, st := newTestService(t)
	mustUser(t, st, "carol", false)
	ctx := context.Background()
	for i := 0; i < 30; i++ {
		name := "ghost" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		_, _ = svc.Login(ctx, name, "wrong-password-x", "10.0.0.9", "ua")
	}
	if _, err := svc.Login(ctx, "carol", testPassword, "10.0.0.9", "ua"); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("31st login from throttled IP = %v, want ErrBadCredentials", err)
	}
	if _, err := svc.Login(ctx, "carol", testPassword, "10.0.0.10", "ua"); err != nil {
		t.Fatalf("other IP should still work: %v", err)
	}
}

func TestAuthenticate_IdleAndAbsoluteExpiry(t *testing.T) {
	svc, st := newTestService(t)
	mustUser(t, st, "dave", false)
	ctx := context.Background()

	idle, err := svc.Login(ctx, "dave", testPassword, "10.0.0.3", "ua")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(ctx, `update sessions set last_seen_at = now() - interval '13 hours' where id_hash = $1`,
		hashSessionID("test-key", idle.SessionID)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Authenticate(ctx, idle.SessionID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("idle session err = %v, want ErrNotFound", err)
	}

	old, err := svc.Login(ctx, "dave", testPassword, "10.0.0.3", "ua")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(ctx,
		`update sessions set created_at = now() - interval '8 days', expires_at = now() + interval '1 hour' where id_hash = $1`,
		hashSessionID("test-key", old.SessionID)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Authenticate(ctx, old.SessionID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("absolute-expired session err = %v, want ErrNotFound", err)
	}
}

func TestAuthenticate_TouchesAtMostOncePerMinute(t *testing.T) {
	svc, st := newTestService(t)
	mustUser(t, st, "erin", false)
	ctx := context.Background()
	res := mustLogin(t, svc, "erin", testPassword, "10.0.0.3", "ua")
	base := time.Now()
	svc.now = func() time.Time { return base.Add(30 * time.Second) }
	s1, _, err := svc.Authenticate(ctx, res.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	svc.now = func() time.Time { return base.Add(2 * time.Minute) }
	if _, _, err := svc.Authenticate(ctx, res.SessionID); err != nil {
		t.Fatal(err)
	}
	s2, err := st.GetSession(ctx, s1.IDHash)
	if err != nil {
		t.Fatal(err)
	}
	if !s2.LastSeenAt.After(s1.LastSeenAt.Add(time.Minute)) {
		t.Fatalf("last_seen not refreshed after 2m: %v -> %v", s1.LastSeenAt, s2.LastSeenAt)
	}
}

func TestChangePassword_EndsOtherSessions(t *testing.T) {
	svc, st := newTestService(t)
	u := mustUser(t, st, "frank", false)
	ctx := context.Background()
	a := mustLogin(t, svc, "frank", testPassword, "10.0.0.4", "ua")
	b := mustLogin(t, svc, "frank", testPassword, "10.0.0.4", "ua")
	sessA, _, err := svc.Authenticate(ctx, a.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	ctxA := context.WithValue(ctx, sessionKey{}, sessA)

	if err := svc.ChangePassword(ctxA, u.ID, "bad-current-pass", "new-password-123"); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("wrong current = %v", err)
	}
	if err := svc.ChangePassword(ctxA, u.ID, testPassword, "short"); err == nil || err.Error() != "Use at least 12 characters." {
		t.Fatalf("short new = %v", err)
	}
	if err := svc.ChangePassword(ctxA, u.ID, testPassword, "new-password-123"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Authenticate(ctx, a.SessionID); err != nil {
		t.Fatalf("current session should survive: %v", err)
	}
	if _, _, err := svc.Authenticate(ctx, b.SessionID); err == nil {
		t.Fatal("other session should have ended")
	}
	if _, err := svc.Login(ctx, "frank", "new-password-123", "10.0.0.4", "ua"); err != nil {
		t.Fatalf("login with new password: %v", err)
	}
}

func TestAdminResetPassword(t *testing.T) {
	svc, st := newTestService(t)
	u := mustUser(t, st, "gina", false)
	ctx := context.Background()
	res := mustLogin(t, svc, "gina", testPassword, "10.0.0.4", "ua")
	temp, err := svc.AdminResetPassword(ctx, u.ID)
	if err != nil || len(temp) != 14 {
		t.Fatalf("temp = %q, %v", temp, err)
	}
	got, _ := st.GetUser(ctx, u.ID)
	if !got.MustChangePassword {
		t.Fatal("must_change_password not set")
	}
	if ok, _ := VerifyPassword(got.PasswordHash, temp); !ok {
		t.Fatal("temp password does not verify")
	}
	if _, _, err := svc.Authenticate(ctx, res.SessionID); err == nil {
		t.Fatal("sessions should be gone")
	}
}

func TestDeleteUser_EndsSessions(t *testing.T) {
	svc, st := newTestService(t)
	u := mustUser(t, st, "hank", false)
	ctx := context.Background()
	res := mustLogin(t, svc, "hank", testPassword, "10.0.0.5", "ua")
	if err := st.DeleteUser(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Authenticate(ctx, res.SessionID); err == nil {
		t.Fatal("Authenticate after DeleteUser succeeded")
	}
}

func TestSetup_TokenRequiredOnce(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	var logs bytes.Buffer
	setup, err := NewSetup(ctx, st, slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "no users exist; open the app and create the first global admin with setup token ") {
		t.Fatalf("setup log line missing: %s", logs.String())
	}
	if need, _ := setup.Needed(ctx); !need {
		t.Fatal("setup should be needed")
	}
	if _, err := setup.Complete(ctx, "wrong", "root", testPassword); !errors.Is(err, ErrBadToken) {
		t.Fatalf("wrong token = %v", err)
	}
	u, err := setup.Complete(ctx, setup.token, "Root", testPassword)
	if err == nil && u.Username != "root" {
		t.Fatalf("username not normalized: %q", u.Username)
	}
	if err != nil || !u.Admin || u.MustChangePassword {
		t.Fatalf("complete = %+v, %v", u, err)
	}
	if _, err := setup.Complete(ctx, setup.token, "root2", testPassword); err == nil || err.Error() != "setup already done" {
		t.Fatalf("second complete = %v", err)
	}
	if need, _ := setup.Needed(ctx); need {
		t.Fatal("setup no longer needed")
	}
}

func TestSetup_NoTokenWhenUsersExist(t *testing.T) {
	st := storetest.Open(t)
	mustUser(t, st, "root", true)
	setup, err := NewSetup(context.Background(), st, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if setup.token != "" {
		t.Fatal("token generated although users exist")
	}
	if _, err := setup.Complete(context.Background(), "", "x1", testPassword); err == nil || err.Error() != "setup already done" {
		t.Fatalf("err = %v", err)
	}
}

type stubSecrets struct{ value string }

func (s stubSecrets) ReadSecret(context.Context, string) (string, error) { return s.value, nil }

func TestBootstrap_CreatesAdminFromSecret(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	if err := Bootstrap(ctx, st, stubSecrets{"bootstrap-secret-1"}, "arn:secret", log); err != nil {
		t.Fatal(err)
	}
	u, err := st.GetUserByUsername(ctx, "admin")
	if err != nil || !u.Admin || u.MustChangePassword {
		t.Fatalf("admin = %+v, %v", u, err)
	}
	if ok, _ := VerifyPassword(u.PasswordHash, "bootstrap-secret-1"); !ok {
		t.Fatal("password does not verify")
	}
	if strings.Contains(logs.String(), "bootstrap-secret-1") {
		t.Fatal("secret leaked to logs")
	}
	// Second run with users present is a no-op.
	if err := Bootstrap(ctx, st, stubSecrets{"another-secret-22"}, "arn:secret", log); err != nil {
		t.Fatal(err)
	}
}

func TestBootstrap_SkipsWithoutARN(t *testing.T) {
	st := storetest.Open(t)
	if err := Bootstrap(context.Background(), st, stubSecrets{"x"}, "", slog.Default()); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.CountUsers(context.Background()); n != 0 {
		t.Fatal("user created without ARN")
	}
}

func authedRequest(t *testing.T, svc *Service, st *store.Store, method, path string, header map[string]string) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	called := false
	h := svc.Middleware(RequireAuth(RequireCSRF(RequirePasswordChanged(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) { called = true })))))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	h.ServeHTTP(rec, req)
	return rec, called
}

func TestRequireCSRF_RejectsMissingHeader(t *testing.T) {
	svc, st := newTestService(t)
	mustUser(t, st, "ivy", false)
	res := mustLogin(t, svc, "ivy", testPassword, "10.0.0.6", "ua")
	cookie := CookieName + "=" + res.SessionID

	rec, called := authedRequest(t, svc, st, "POST", "/api/x", map[string]string{"Cookie": cookie})
	if called || rec.Code != 403 || !strings.Contains(rec.Body.String(), `"code":"csrf"`) {
		t.Fatalf("missing header: called=%v code=%d body=%s", called, rec.Code, rec.Body)
	}
	rec, called = authedRequest(t, svc, st, "POST", "/api/x", map[string]string{"Cookie": cookie, CSRFHeader: res.CSRFToken})
	if !called {
		t.Fatalf("valid CSRF rejected: %d %s", rec.Code, rec.Body)
	}
	if _, called = authedRequest(t, svc, st, "GET", "/api/x", map[string]string{"Cookie": cookie}); !called {
		t.Fatal("GET should not need CSRF")
	}
}

func TestRequireAuth_Unauthenticated(t *testing.T) {
	svc, st := newTestService(t)
	rec, called := authedRequest(t, svc, st, "GET", "/api/x", map[string]string{"Cookie": CookieName + "=garbage"})
	if called || rec.Code != 401 ||
		strings.TrimSpace(rec.Body.String()) != `{"error":{"code":"unauthenticated","message":"Sign in to continue."}}` {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
}

func TestRequirePasswordChanged_ExemptPaths(t *testing.T) {
	svc, st := newTestService(t)
	hash, _ := HashPassword(testPassword)
	if _, err := st.CreateUser(context.Background(), "jo", hash, false, true); err != nil {
		t.Fatal(err)
	}
	res, err := svc.Login(context.Background(), "jo", testPassword, "10.0.0.7", "ua")
	if err != nil {
		t.Fatal(err)
	}
	h := map[string]string{"Cookie": CookieName + "=" + res.SessionID}
	rec, called := authedRequest(t, svc, st, "GET", "/api/services", h)
	if called || rec.Code != 403 || !strings.Contains(rec.Body.String(), "password_change_required") {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	if _, called = authedRequest(t, svc, st, "GET", "/api/me", h); !called {
		t.Fatal("/api/me should be exempt")
	}
}

func TestRequireAdmin(t *testing.T) {
	svc, st := newTestService(t)
	mustUser(t, st, "kim", false)
	res := mustLogin(t, svc, "kim", testPassword, "10.0.0.8", "ua")
	called := false
	h := svc.Middleware(RequireAdmin(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Cookie", CookieName+"="+res.SessionID)
	h.ServeHTTP(rec, req)
	if called || rec.Code != 403 || !strings.Contains(rec.Body.String(), `"code":"forbidden"`) {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
}

func TestCookieAttributes(t *testing.T) {
	svc, _ := newTestService(t)
	svc.cfg.CookieSecure = true
	rec := httptest.NewRecorder()
	svc.SetCookie(rec, "abc")
	c := rec.Result().Cookies()[0]
	if c.Name != CookieName || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/" {
		t.Fatalf("cookie = %+v", c)
	}
	rec = httptest.NewRecorder()
	svc.ClearCookie(rec)
	if c := rec.Result().Cookies()[0]; c.MaxAge >= 0 {
		t.Fatalf("clear cookie = %+v", c)
	}
}

func mustLogin(t *testing.T, svc *Service, username, password, ip, userAgent string) LoginResult {
	t.Helper()
	res, err := svc.Login(context.Background(), username, password, ip, userAgent)
	if err != nil {
		t.Fatalf("login %s: %v", username, err)
	}
	return res
}

func countVerifies(svc *Service) *int {
	n := 0
	real := svc.verify
	svc.verify = func(hash, pw string) (bool, error) { n++; return real(hash, pw) }
	return &n
}

func TestLogin_LockedAccountStillPaysVerifyCost(t *testing.T) {
	svc, st := newTestService(t)
	mustUser(t, st, "lena", false)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		_, _ = svc.Login(ctx, "lena", "wrong-password-x", "10.0.1.1", "ua")
	}
	n := countVerifies(svc)
	if _, err := svc.Login(ctx, "lena", testPassword, "10.0.1.1", "ua"); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("err = %v", err)
	}
	if *n != 1 {
		t.Fatalf("locked path verify calls = %d, want 1 (timing must match unknown-user path)", *n)
	}
}

func TestLogin_PerUserCountPrecheck(t *testing.T) {
	svc, st := newTestService(t)
	mustUser(t, st, "mona", false)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		_, _ = svc.Login(ctx, "mona", "wrong-password-x", "10.0.1.2", "ua")
	}
	// Clear the lock: the failure count alone must still reject the login.
	if _, err := st.Pool.Exec(ctx, `update users set locked_until = null where username = 'mona'`); err != nil {
		t.Fatal(err)
	}
	n := countVerifies(svc)
	if _, err := svc.Login(ctx, "mona", testPassword, "10.0.1.3", "ua"); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("err = %v, want ErrBadCredentials", err)
	}
	if *n != 1 {
		t.Fatalf("verify calls = %d, want 1", *n)
	}
}

func TestMiddleware_StoreErrorIs500NotAnonymous(t *testing.T) {
	svc, st := newTestService(t)
	mustUser(t, st, "ivy", false)
	res := mustLogin(t, svc, "ivy", testPassword, "10.0.0.6", "ua")

	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	st.Pool.Close() // every query now fails with a non-NotFound error

	rec, called := authedRequest(t, svc, st, "GET", "/api/x", map[string]string{"Cookie": CookieName + "=" + res.SessionID})
	if called || rec.Code != 500 ||
		strings.TrimSpace(rec.Body.String()) != `{"error":{"code":"internal","message":"Something went wrong on our side."}}` {
		t.Fatalf("code=%d called=%v body=%s", rec.Code, called, rec.Body)
	}
	if !strings.Contains(logs.String(), "level=ERROR") || !strings.Contains(logs.String(), "/api/x") {
		t.Fatalf("store failure not logged with path: %q", logs.String())
	}
}
