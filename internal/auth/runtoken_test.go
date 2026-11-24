package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/apitoken"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const testAgentURL = "https://agent.internal:8484"

// newAgentService is newTestService with the agent on and HM_API_TOKENS off, the case run tokens must work in.
func newAgentService(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	svc, st := newTestService(t)
	svc.cfg.AgentURL = testAgentURL
	svc.cfg.AgentRunTokenTTL = 15 * time.Minute
	return svc, st
}

func TestMintRunToken_StoresAReadOnlyRunRow(t *testing.T) {
	svc, st := newAgentService(t)
	u := mustUser(t, st, "alice", false)
	runID := uuid.New()
	raw, err := svc.MintRunToken(t.Context(), u, runID, false)
	if err != nil {
		t.Fatal(err)
	}
	parsed, ok := apitoken.Parse(raw)
	if !ok || parsed.Kind != apitoken.KindRun {
		t.Fatalf("minted %q, want a run token", raw[:4])
	}
	tok, err := st.GetAPITokenByPublicID(t.Context(), apitoken.DisplayID(parsed))
	if err != nil {
		t.Fatal(err)
	}
	if tok.Kind != "run" || tok.Name != "agent run" || tok.Scope != "read" || tok.AuthSource != "password" ||
		tok.UserID != u.ID || tok.RunID == nil || *tok.RunID != runID || tok.RedactEnv || tok.GrantID != nil {
		t.Fatalf("stored token = %+v", tok)
	}
	if want := svc.now().Add(15 * time.Minute); tok.ExpiresAt.Sub(want) > time.Second || want.Sub(tok.ExpiresAt) > time.Second {
		t.Errorf("expires_at = %v, want about %v", tok.ExpiresAt, want)
	}
}

func TestAuthenticateToken_RunTokenNeedsAgentEnabled(t *testing.T) {
	svc, st := newAgentService(t)
	u := mustUser(t, st, "alice", false)
	raw, err := svc.MintRunToken(t.Context(), u, uuid.New(), true)
	if err != nil {
		t.Fatal(err)
	}
	if svc.cfg.APITokens {
		t.Fatal("setup: HM_API_TOKENS must be off")
	}

	svc.cfg.AgentURL = ""
	if _, _, err := svc.AuthenticateToken(t.Context(), raw, "10.0.0.1"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("agent off: err = %v, want ErrUnauthenticated", err)
	}
	svc.cfg.AgentURL = testAgentURL
	tok, user, err := svc.AuthenticateToken(t.Context(), raw, "10.0.0.1")
	if err != nil || user.ID != u.ID || tok.Kind != "run" {
		t.Fatalf("agent on: user %d, token %+v, err %v", user.ID, tok, err)
	}
}

func TestAuthenticateToken_ExpiredRunToken(t *testing.T) {
	svc, st := newAgentService(t)
	u := mustUser(t, st, "alice", false)
	raw, err := svc.MintRunToken(t.Context(), u, uuid.New(), true)
	if err != nil {
		t.Fatal(err)
	}
	svc.now = func() time.Time { return time.Now().Add(16 * time.Minute) }
	if _, _, err := svc.AuthenticateToken(t.Context(), raw, ""); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("err = %v, want ErrUnauthenticated after the run token lifetime", err)
	}
}

// A run token's request carries the run id and the redaction flag, and an admin's token still cannot operate.
func TestRunToken_SubjectIsReadOnly(t *testing.T) {
	svc, st := newAgentService(t)
	admin := mustUser(t, st, "root", true)
	tag, err := st.CreateTag(t.Context(), "payments", "neutral", false)
	if err != nil {
		t.Fatal(err)
	}
	runID := uuid.New()
	raw, err := svc.MintRunToken(t.Context(), admin, runID, true)
	if err != nil {
		t.Fatal(err)
	}

	var subject authz.Subject
	var cred Credential
	h := svc.Middleware(RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		subject = SubjectFrom(r.Context())
		cred, _ = CredentialFrom(r.Context())
	})))
	req := httptest.NewRequest("GET", "/api/x", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	if !subject.Admin || !subject.ReadOnly {
		t.Fatalf("subject = %+v, want an admin marked read-only", subject)
	}
	id := authz.TagID(tag.ID)
	if !authz.Can(subject, authz.View, id) || !authz.Can(subject, authz.Logs, id) {
		t.Error("a run token should still View and read Logs")
	}
	for _, a := range []authz.Action{authz.Operate, authz.Deploy, authz.Edit, authz.Terminal} {
		if authz.Can(subject, a, id) {
			t.Errorf("an admin's run token may perform action %v", a)
		}
	}
	if cred.Kind != CredentialToken || cred.TokenKind != apitoken.KindRun || cred.RunID == nil || *cred.RunID != runID || !cred.RedactEnv {
		t.Errorf("credential = %+v, want a run token credential carrying the run id and redaction", cred)
	}
}

func serveBearer(svc *Service, header string) (code int, called bool) {
	h := svc.Middleware(RequireAuth(okHandler(&called)))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/x", nil)
	req.Header.Set("Authorization", header)
	h.ServeHTTP(rec, req)
	return rec.Code, called
}

// With only the agent on, a personal or OAuth bearer is not a token request: it is ignored like any other header
// (no cookie here, so the request is anonymous 401, not a token 401 with WWW-Authenticate).
func TestMiddleware_PATIgnoredWhenOnlyAgentEnabled(t *testing.T) {
	svc, st := newAgentService(t)
	u := mustUser(t, st, "alice", false)
	pat, _ := issueToken(t, st, u.ID, time.Now().Add(time.Hour))
	oauthRaw, _, _ := apitoken.Generate(apitoken.KindOAuthAccess)
	for name, raw := range map[string]string{"pat": pat, "oauth access": oauthRaw} {
		h := svc.Middleware(RequireAuth(okHandler(new(bool))))
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/x", nil)
		req.Header.Set("Authorization", "Bearer "+raw)
		h.ServeHTTP(rec, req)
		if rec.Code != 401 || rec.Header().Get("WWW-Authenticate") != "" {
			t.Errorf("%s: code=%d www=%q, want the header ignored (plain anonymous 401)", name, rec.Code, rec.Header().Get("WWW-Authenticate"))
		}
	}
	// The cookie still rules: a signed-in session is served with the PAT header present.
	res := mustLogin(t, svc, "alice", testPassword, "10.0.0.1", "ua")
	var called bool
	h := svc.Middleware(RequireAuth(okHandler(&called)))
	req := httptest.NewRequest("GET", "/api/x", nil)
	req.Header.Set("Authorization", "Bearer "+pat)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: res.SessionID})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || !called {
		t.Fatalf("session with an ignored PAT header: code=%d called=%v", rec.Code, called)
	}
}

func TestMiddleware_RunTokenIgnoredWhenAgentDisabled(t *testing.T) {
	svc, st := newAgentService(t)
	u := mustUser(t, st, "alice", false)
	raw, err := svc.MintRunToken(t.Context(), u, uuid.New(), true)
	if err != nil {
		t.Fatal(err)
	}
	svc.cfg.APITokens = false
	svc.cfg.AgentURL = ""
	if code, called := serveBearer(svc, "Bearer "+raw); code != 401 || called {
		t.Fatalf("agent off, tokens off: code=%d called=%v, want anonymous 401", code, called)
	}
	// Tokens on but agent off: the run token takes the token path and is refused there.
	svc.cfg.APITokens = true
	if code, called := serveBearer(svc, "Bearer "+raw); code != 401 || called {
		t.Fatalf("agent off, tokens on: code=%d called=%v, want 401", code, called)
	}
	svc.cfg.AgentURL = testAgentURL
	svc.cfg.APITokens = false
	if code, called := serveBearer(svc, "Bearer "+raw); code != 200 || !called {
		t.Fatalf("agent on, tokens off: code=%d called=%v, want 200", code, called)
	}
}

func TestRunToken_DeletedWithUserSessions(t *testing.T) {
	svc, st := newAgentService(t)
	u := mustUser(t, st, "frank", false)
	bystander := mustUser(t, st, "gina", false)
	ctx := context.Background()
	mine, err := svc.MintRunToken(ctx, u, uuid.New(), true)
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := svc.MintRunToken(ctx, bystander, uuid.New(), true)
	if err != nil {
		t.Fatal(err)
	}
	res := mustLogin(t, svc, "frank", testPassword, "10.0.0.4", "ua")
	sess, _, err := svc.Authenticate(ctx, res.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ChangePassword(context.WithValue(ctx, sessionKey{}, sess), u.ID, testPassword, "new-password-123"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.AuthenticateToken(ctx, mine, ""); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("run token after a password change: err = %v, want ErrUnauthenticated", err)
	}
	if _, _, err := svc.AuthenticateToken(ctx, theirs, ""); err != nil {
		t.Fatalf("another user's run token: %v", err)
	}
}
