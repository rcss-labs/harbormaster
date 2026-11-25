package auth

import (
	"bytes"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/apitoken"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

// insertAgent adds the identity row directly, so a test does not depend on EnsureAgentIdentity.
func insertAgent(t *testing.T, st *store.Store) store.User {
	t.Helper()
	var id int64
	if err := st.Pool.QueryRow(t.Context(),
		`insert into users (username, kind) values ('harbormaster-agent', 'agent') returning id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	u, err := st.GetUser(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// insertTokenRow stores a token for userID without going through any store guard and returns its raw form.
func insertTokenRow(t *testing.T, st *store.Store, userID int64, kind apitoken.Kind, runID *uuid.UUID) string {
	t.Helper()
	raw, parsed, err := apitoken.Generate(kind)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(t.Context(),
		`insert into api_tokens (public_id, user_id, kind, name, secret_hash, scope, auth_source, expires_at, run_id)
		 values ($1, $2, $3, 'n', $4, 'read', 'password', now() + interval '1 hour', $5)`,
		apitoken.DisplayID(parsed), userID, string(kind), apitoken.Hash(parsed.Secret), runID); err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestValidateUsername_ReservedPrefix(t *testing.T) {
	for _, name := range []string{"harbormaster-agent", "harbormaster-agent-1", "harbormaster-agent2", "harbormaster-agents", "harbormaster.agent", "harbormaster_agent-2"} {
		var verr *ValidationError
		if err := ValidateUsername(name); !errors.As(err, &verr) {
			t.Errorf("ValidateUsername(%q) = %v, want a ValidationError", name, err)
		}
	}
	for _, name := range []string{"harbormaster", "harbormaster-ag", "my-harbormaster-agent", "alice"} {
		if err := ValidateUsername(name); err != nil {
			t.Errorf("ValidateUsername(%q) = %v, want nil", name, err)
		}
	}
}

func TestLogin_IdentityNameBehavesLikeUnknownUser(t *testing.T) {
	svc, st := newTestService(t)
	ctx := t.Context()
	insertAgent(t, st)
	var verified int
	svc.verify = func(string, string) (bool, error) { verified++; return true, nil } // accepts everything

	// Past the per-user limit, so a LockUser call would show.
	for range svc.cfg.LoginMaxPerUser + 2 {
		if _, err := svc.Login(ctx, "Harbormaster-Agent", "anything-at-all-1", "10.0.0.1", "ua"); !errors.Is(err, ErrBadCredentials) {
			t.Fatalf("Login = %v, want ErrBadCredentials", err)
		}
	}
	// The same number of attempts for a name that does not exist, for the timing and the accounting to compare.
	unknownVerified := 0
	svc.verify = func(string, string) (bool, error) { unknownVerified++; return true, nil }
	for range svc.cfg.LoginMaxPerUser + 2 {
		if _, err := svc.Login(ctx, "nobody-here", "anything-at-all-1", "10.0.0.2", "ua"); !errors.Is(err, ErrBadCredentials) {
			t.Fatalf("Login = %v, want ErrBadCredentials", err)
		}
	}
	if verified != unknownVerified {
		t.Errorf("password verifications: identity name %d, unknown name %d; want the same", verified, unknownVerified)
	}
	var locked int
	st.Pool.QueryRow(ctx, `select count(*) from users where locked_until is not null`).Scan(&locked)
	if locked != 0 {
		t.Error("a sign-in attempt locked the identity")
	}
	var sessions int
	st.Pool.QueryRow(ctx, `select count(*) from sessions`).Scan(&sessions)
	if sessions != 0 {
		t.Errorf("sessions = %d, want 0", sessions)
	}
	for _, e := range auditEntries(t, st, "sign_in_failed") {
		if e.ActorUserID != nil {
			t.Errorf("a failed sign-in of the identity's name names the user: %+v", e)
		}
	}
	if byUser, _, _ := st.FailedAttempts(ctx, "harbormaster-agent", "x", time.Now().Add(-time.Hour)); byUser != svc.cfg.LoginMaxPerUser+2 {
		t.Errorf("failed attempts recorded = %d, want %d", byUser, svc.cfg.LoginMaxPerUser+2)
	}
}

func TestStartSession_RefusesTheIdentity(t *testing.T) {
	svc, st := newTestService(t)
	agent := insertAgent(t, st)
	if _, err := svc.StartSession(t.Context(), agent, "ip", "ua"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("StartSession = %v, want ErrNotFound", err)
	}
	var sessions int
	st.Pool.QueryRow(t.Context(), `select count(*) from sessions`).Scan(&sessions)
	if sessions != 0 {
		t.Fatalf("sessions = %d, want 0", sessions)
	}
}

func TestPasswordRoutes_AnswerNotFoundForTheIdentity(t *testing.T) {
	svc, st := newTestService(t)
	agent := insertAgent(t, st)
	if err := svc.ChangePassword(t.Context(), agent.ID, "a", testPassword); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("ChangePassword = %v, want ErrNotFound", err)
	}
	if _, err := svc.AdminResetPassword(t.Context(), agent.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("AdminResetPassword = %v, want ErrNotFound", err)
	}
	if _, err := svc.StartLink(t.Context(), agent.ID, testPassword, "ip"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("StartLink = %v, want ErrNotFound", err)
	}
	if got, _ := st.GetUser(t.Context(), agent.ID); got.HasPassword() || got.MustChangePassword {
		t.Fatalf("the identity gained a credential: %+v", got)
	}
}

func TestAuthenticate_SessionOfNonHumanIsRefused(t *testing.T) {
	svc, st := newTestService(t)
	ctx := t.Context()
	agent := insertAgent(t, st)
	now := time.Now()
	const raw = "cookie-of-the-identity"
	if _, err := st.Pool.Exec(ctx,
		`insert into sessions (id_hash, user_id, csrf_token, created_at, last_seen_at, expires_at) values ($1, $2, 'c', $3, $3, $4)`,
		hashSessionID(svc.cfg.SessionKey, raw), agent.ID, now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Authenticate(ctx, raw); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("Authenticate = %v, want ErrUnauthenticated", err)
	}
	var left int
	st.Pool.QueryRow(ctx, `select count(*) from sessions`).Scan(&left)
	if left != 0 {
		t.Errorf("the session survived: %d rows", left)
	}
}

func TestAuthenticate_SessionOfUnknownKindIsRefused(t *testing.T) {
	svc, st := newTestService(t)
	ctx := t.Context()
	if _, err := st.Pool.Exec(ctx, `alter table users drop constraint users_kind_check, drop constraint users_agent_shape`); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := st.Pool.QueryRow(ctx,
		`insert into users (username, password_hash, kind) values ('robot', 'h', 'robot') returning id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, err := st.Pool.Exec(ctx,
		`insert into sessions (id_hash, user_id, csrf_token, created_at, last_seen_at, expires_at) values ($1, $2, 'c', $3, $3, $4)`,
		hashSessionID(svc.cfg.SessionKey, "robot-cookie"), id, now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Authenticate(ctx, "robot-cookie"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("Authenticate = %v, want ErrUnauthenticated", err)
	}
}

func TestAuthenticateToken_IdentityNeedsARunToken(t *testing.T) {
	svc, st := newAgentService(t)
	svc.cfg.APITokens = true
	ctx := t.Context()
	agent := insertAgent(t, st)

	pat := insertTokenRow(t, st, agent.ID, apitoken.KindPAT, nil)
	if _, _, err := svc.AuthenticateToken(ctx, pat, "ip"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("a personal token of the identity = %v, want ErrUnauthenticated", err)
	}
	runID := uuid.New()
	run := insertTokenRow(t, st, agent.ID, apitoken.KindRun, &runID)
	tok, user, err := svc.AuthenticateToken(ctx, run, "ip")
	if err != nil || user.ID != agent.ID || tok.RunID == nil {
		t.Fatalf("a run token of the identity = %+v, %+v, %v; want it accepted", tok, user, err)
	}
	// A human's tokens are unchanged.
	human := mustUser(t, st, "alice", false)
	if _, _, err := svc.AuthenticateToken(ctx, insertTokenRow(t, st, human.ID, apitoken.KindPAT, nil), "ip"); err != nil {
		t.Fatalf("a human's personal token = %v", err)
	}
}

func TestAuthenticateToken_UnknownKindIsRefused(t *testing.T) {
	svc, st := newAgentService(t)
	ctx := t.Context()
	if _, err := st.Pool.Exec(ctx, `alter table users drop constraint users_kind_check, drop constraint users_agent_shape`); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := st.Pool.QueryRow(ctx,
		`insert into users (username, password_hash, kind) values ('robot', 'h', 'robot') returning id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	runID := uuid.New()
	raw := insertTokenRow(t, st, id, apitoken.KindRun, &runID)
	if _, _, err := svc.AuthenticateToken(ctx, raw, "ip"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("a run token of an unknown kind = %v, want ErrUnauthenticated", err)
	}
}

func TestMintRunToken_RefusesTheIdentity(t *testing.T) {
	svc, st := newAgentService(t)
	agent := insertAgent(t, st)
	if raw, err := svc.MintRunToken(t.Context(), agent, uuid.New(), true); err == nil || raw != "" {
		t.Fatalf("MintRunToken for the identity = %q, %v; want a refusal", raw, err)
	}
	// A caller that built the user by hand and left the kind out is refused too, not treated as a human.
	if raw, err := svc.MintRunToken(t.Context(), store.User{ID: agent.ID, Username: agent.Username}, uuid.New(), true); err == nil || raw != "" {
		t.Fatalf("MintRunToken for a user of no kind = %q, %v; want a refusal", raw, err)
	}
	var n int
	st.Pool.QueryRow(t.Context(), `select count(*) from api_tokens`).Scan(&n)
	if n != 0 {
		t.Fatalf("api_tokens = %d, want 0", n)
	}
}

func TestMintAgentRunToken_ReadOnlySubject(t *testing.T) {
	svc, st := newAgentService(t)
	ctx := t.Context()
	root := mustUser(t, st, "root", true)
	prod, _ := st.CreateTag(ctx, "Prod", "red", false)
	id, err := st.EnsureAgentIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetAgentIdentityPermissions(ctx, map[authz.TagID]authz.ActionSet{prod.ID: authz.ActionSet(authz.View | authz.Logs)}, root.ID); err != nil {
		t.Fatal(err)
	}
	runID := uuid.New()
	raw, identity, err := svc.MintAgentRunToken(ctx, runID, true)
	if err != nil {
		t.Fatal(err)
	}
	if identity.ID != id.ID || !identity.IsAgent() {
		t.Fatalf("identity = %+v, want the agent identity %d", identity, id.ID)
	}
	parsed, ok := apitoken.Parse(raw)
	if !ok || parsed.Kind != apitoken.KindRun {
		t.Fatal("not a run token")
	}
	tok, user, err := svc.AuthenticateToken(ctx, raw, "ip")
	if err != nil || user.ID != id.ID || tok.Kind != "run" || tok.Scope != "read" || tok.AuthSource != "password" ||
		tok.RunID == nil || *tok.RunID != runID || !tok.RedactEnv {
		t.Fatalf("authenticate = %+v, %+v, %v", tok, user, err)
	}
	sub, err := st.LoadSubject(ctx, user.ID)
	if err != nil || sub.Admin {
		t.Fatalf("subject = %+v, %v", sub, err)
	}
	sub.ReadOnly = true
	if !authz.Can(sub, authz.View, prod.ID) || !authz.Can(sub, authz.Logs, prod.ID) || authz.Can(sub, authz.Operate, prod.ID) {
		t.Fatalf("subject perms = %+v", sub.Perms)
	}
	// Minting again finds the same identity and makes a second token.
	_, again, err := svc.MintAgentRunToken(ctx, uuid.New(), false)
	if err != nil || again.ID != id.ID {
		t.Fatalf("second mint = %+v, %v", again, err)
	}
}

func TestMintAgentRunToken_CreatesTheIdentityWhenAbsent(t *testing.T) {
	svc, st := newAgentService(t)
	_, identity, err := svc.MintAgentRunToken(t.Context(), uuid.New(), true)
	if err != nil || !identity.IsAgent() || identity.ID == 0 {
		t.Fatalf("mint = %+v, %v", identity, err)
	}
	if _, err := st.GetAgentIdentity(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestSetup_NeededWhenOnlyTheIdentityExists(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	insertAgent(t, st)
	var logs bytes.Buffer
	setup, err := NewSetup(ctx, st, slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if setup.token == "" {
		t.Fatal("no setup token although no human user exists")
	}
	if need, err := setup.Needed(ctx); err != nil || !need {
		t.Fatalf("Needed = %v, %v; want true", need, err)
	}
	u, err := setup.Complete(ctx, setup.token, "root", testPassword)
	if err != nil || !u.Admin || u.Kind != store.UserKindHuman {
		t.Fatalf("Complete = %+v, %v", u, err)
	}
	if need, _ := setup.Needed(ctx); need {
		t.Fatal("setup still needed after the first admin")
	}
}

func TestSetup_RefusesTheReservedName(t *testing.T) {
	st := storetest.Open(t)
	setup, err := NewSetup(t.Context(), st, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := setup.Complete(t.Context(), setup.token, "harbormaster-agent", testPassword); err == nil {
		t.Fatal("setup accepted the reserved name")
	}
}

func TestBootstrap_RunsWhenOnlyTheIdentityExists(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	insertAgent(t, st)
	log := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	if err := Bootstrap(ctx, st, stubSecrets{"bootstrap-secret-1"}, "arn:secret", log); err != nil {
		t.Fatal(err)
	}
	u, err := st.GetUserByUsername(ctx, "admin")
	if err != nil || !u.Admin {
		t.Fatalf("bootstrap admin = %+v, %v", u, err)
	}
}

func TestSSO_CannotLinkOrDeriveTheReservedName(t *testing.T) {
	for _, tc := range []struct {
		name          string
		identityFirst bool
	}{{"identity exists", true}, {"identity not created yet", false}} {
		t.Run(tc.name, func(t *testing.T) {
			svc, st := newTestService(t)
			ctx := t.Context()
			if tc.identityFirst {
				insertAgent(t, st)
			}
			cfg := ssoCfg()
			for _, nameID := range []string{"harbormaster-agent@example.test", "harbormaster-agent-2@example.test"} {
				id := ssoIdentity(nameID, nameID)
				if prev, err := svc.PreviewSSO(ctx, id, cfg); err == nil {
					t.Errorf("PreviewSSO(%s) = %+v, want a refusal: every name with the reserved prefix is off limits", nameID, prev)
				}
				if res, _, err := svc.LoginSSO(ctx, id, cfg, "ip", "ua"); err == nil {
					t.Errorf("LoginSSO(%s) created %q", nameID, res.User.Username)
				}
			}
			users, _ := st.ListUsers(ctx)
			if len(users) != 0 {
				t.Errorf("users created: %+v", users)
			}
			if tc.identityFirst {
				agent, _ := st.GetAgentIdentity(ctx)
				if agent.SSOLinked() {
					t.Error("the identity was linked")
				}
			}
		})
	}
}

func TestLinkSSO_RefusesTheIdentity(t *testing.T) {
	svc, st := newTestService(t)
	ctx := t.Context()
	agent := insertAgent(t, st)
	_, err := svc.LinkSSO(ctx, agent.ID, ssoIdentity("someone@example.test", "someone@example.test"), ssoCfg(), "ip")
	if err == nil {
		t.Fatal("LinkSSO bound an identity to the agent")
	}
	if got, _ := st.GetUser(ctx, agent.ID); got.SSOLinked() {
		t.Fatalf("the identity was linked: %+v", got)
	}
}

func TestSSO_LoginNeverResolvesToTheIdentity(t *testing.T) {
	svc, st := newTestService(t)
	insertAgent(t, st)
	// A sign-in whose derived name is free must create a human, and the identity must stay unlinked.
	res, outcome, err := svc.LoginSSO(t.Context(), ssoIdentity("dana@example.test", "dana@example.test"), ssoCfg(), "ip", "ua")
	if err != nil || outcome != SSOCreate || res.User.Kind != store.UserKindHuman {
		t.Fatalf("LoginSSO = %+v, %v, %v", res.User, outcome, err)
	}
}

func TestMintAgentRunToken_RefusesWhenTheAgentIsOff(t *testing.T) {
	svc, st := newTestService(t) // HM_AGENT_URL unset
	if raw, _, err := svc.MintAgentRunToken(t.Context(), uuid.New(), true); err == nil || raw != "" {
		t.Fatalf("MintAgentRunToken with the agent off = %q, %v; want a refusal", raw, err)
	}
	if _, err := st.GetAgentIdentity(t.Context()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("an identity row was created with the agent off: %v", err)
	}
}
