package store_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

// insertAgentRow adds an agent-kind row directly, so a test does not depend on EnsureAgentIdentity.
func insertAgentRow(t *testing.T, st *store.Store, username string) int64 {
	t.Helper()
	var id int64
	if err := st.Pool.QueryRow(t.Context(),
		`insert into users (username, kind) values ($1, 'agent') returning id`, username).Scan(&id); err != nil {
		t.Fatalf("insert agent row: %v", err)
	}
	return id
}

func TestEnsureAgentIdentity_CreatesOnceUnderConcurrency(t *testing.T) {
	st := storetest.Open(t)
	const callers = 20
	ids := make([]int64, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			u, err := st.EnsureAgentIdentity(t.Context())
			ids[i], errs[i] = u.ID, err
		}()
	}
	wg.Wait()
	for i := range callers {
		if errs[i] != nil || ids[i] == 0 || ids[i] != ids[0] {
			t.Fatalf("caller %d got id %d, err %v; want every caller to get id %d", i, ids[i], errs[i], ids[0])
		}
	}
	var n int
	if err := st.Pool.QueryRow(t.Context(), `select count(*) from users where kind = 'agent'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("agent rows = %d (%v), want 1", n, err)
	}
	u, err := st.GetAgentIdentity(t.Context())
	if err != nil || u.ID != ids[0] || !u.IsAgent() || u.Kind != store.UserKindAgent || u.Username != "harbormaster-agent" {
		t.Fatalf("GetAgentIdentity = %+v, %v", u, err)
	}
	if u.Admin || u.HasPassword() || u.BreakGlass || u.SSOLinked() || u.AIChat || u.MustChangePassword {
		t.Fatalf("identity carries a credential or privilege: %+v", u)
	}
}

func TestGetAgentIdentity_NotFoundWhenNone(t *testing.T) {
	st := storetest.Open(t)
	if _, err := st.GetAgentIdentity(t.Context()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestEnsureAgentIdentity_SkipsATakenUsername(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	// A human already holds the first two reserved names (an older task, or a row created before the reservation).
	if _, err := st.CreateUser(ctx, "harbormaster-agent", "h", false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser(ctx, "Harbormaster-Agent-1", "h", false, false); err != nil {
		t.Fatal(err)
	}
	u, err := st.EnsureAgentIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if u.Username != "harbormaster-agent-2" || !u.IsAgent() {
		t.Fatalf("identity = %q (agent %v), want harbormaster-agent-2", u.Username, u.IsAgent())
	}
	again, err := st.EnsureAgentIdentity(ctx)
	if err != nil || again.ID != u.ID {
		t.Fatalf("second Ensure = %+v, %v; want the same row", again, err)
	}
}

func TestEnsureAgentIdentity_FailsWhenEveryReservedNameIsTaken(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	names := []string{"harbormaster-agent"}
	for i := 1; i <= 9; i++ {
		names = append(names, "harbormaster-agent-"+string(rune('0'+i)))
	}
	for _, name := range names {
		if _, err := st.CreateUser(ctx, name, "h", false, false); err != nil {
			t.Fatal(err)
		}
	}
	if u, err := st.EnsureAgentIdentity(ctx); err == nil {
		t.Fatalf("Ensure returned %+v with no free name, want an error", u)
	}
	if _, err := st.GetAgentIdentity(ctx); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetAgentIdentity = %v, want ErrNotFound", err)
	}
}

func TestAgentShapeConstraint_RefusesEachForbiddenField(t *testing.T) {
	for _, tc := range []struct{ name, set string }{
		{"admin", `is_admin = true`},
		{"password", `password_hash = 'x'`},
		{"break-glass", `break_glass = true`},
		{"must change password", `must_change_password = true`},
		{"AI chat", `ai_chat = true`},
		{"SSO identity", `idp_entity_id = 'e', external_id = 'n', external_id_format = 'f'`},
		{"pending link", `pending_link_name_id = 'n', pending_link_expires_at = now() + interval '1 hour'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := storetest.Open(t)
			id := insertAgentRow(t, st, "harbormaster-agent")
			if _, err := st.Pool.Exec(t.Context(), `update users set `+tc.set+` where id = $1`, id); err == nil {
				t.Fatal("update of the agent row was accepted, want the shape check to refuse it")
			}
		})
	}
}

func TestAgentShapeConstraint_OnlyOneAgentRow(t *testing.T) {
	st := storetest.Open(t)
	insertAgentRow(t, st, "harbormaster-agent")
	if _, err := st.Pool.Exec(t.Context(), `insert into users (username, kind) values ('harbormaster-agent-1', 'agent')`); err == nil {
		t.Fatal("a second agent row was accepted")
	}
}

func TestListAndCountUsers_ExcludeTheIdentity(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	insertAgentRow(t, st, "harbormaster-agent")
	n, err := st.CountUsers(ctx)
	if err != nil || n != 0 {
		t.Fatalf("CountUsers with only the identity = %d, %v; want 0", n, err)
	}
	users, err := st.ListUsers(ctx)
	if err != nil || len(users) != 0 {
		t.Fatalf("ListUsers with only the identity = %+v, %v; want none", users, err)
	}
	alice, _ := st.CreateUser(ctx, "alice", "h", false, false)
	n, _ = st.CountUsers(ctx)
	users, _ = st.ListUsers(ctx)
	if n != 1 || len(users) != 1 || users[0].ID != alice.ID {
		t.Fatalf("CountUsers = %d, ListUsers = %+v; want only alice", n, users)
	}
	if users[0].Kind != store.UserKindHuman || users[0].IsAgent() {
		t.Fatalf("human kind = %q", users[0].Kind)
	}
}

func TestGetUserByUsername_DoesNotFindTheIdentity(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	id := insertAgentRow(t, st, "harbormaster-agent")
	if u, err := st.GetUserByUsername(ctx, "Harbormaster-Agent"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetUserByUsername = %+v, %v; want ErrNotFound", u, err)
	}
	u, err := st.GetUser(ctx, id)
	if err != nil || !u.IsAgent() {
		t.Fatalf("GetUser = %+v, %v; want the identity, which the token path and audit names need", u, err)
	}
}

func TestUsernameTaken_EveryKind(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	insertAgentRow(t, st, "harbormaster-agent")
	st.CreateUser(ctx, "alice", "h", false, false)
	for name, want := range map[string]bool{
		"harbormaster-agent": true, "HARBORMASTER-AGENT": true, "alice": true, "Alice": true, "bob": false,
	} {
		got, err := st.UsernameTaken(ctx, name)
		if err != nil || got != want {
			t.Errorf("UsernameTaken(%q) = %v, %v; want %v", name, got, err, want)
		}
	}
}

func TestCreators_AlwaysCreateHumans(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	a, err1 := st.CreateUser(ctx, "alice", "h", false, false)
	b, err2 := st.CreateSAMLUser(ctx, store.NewSAMLUser{Username: "bob", IdPEntityID: "e", ExternalID: "n", ExternalIDFormat: "f"})
	c, err3 := st.CreateBreakGlassAdmin(ctx, "carol", "h")
	if err1 != nil || err2 != nil || err3 != nil {
		t.Fatalf("errors: %v %v %v", err1, err2, err3)
	}
	for _, u := range []store.User{a, b, c} {
		if u.Kind != store.UserKindHuman {
			t.Errorf("%s kind = %q, want human", u.Username, u.Kind)
		}
	}
	insertAgentRow(t, st, "harbormaster-agent")
	if _, err := st.CreateUser(ctx, "harbormaster-agent", "h", false, false); !errors.Is(err, store.ErrConflict) {
		t.Errorf("CreateUser over the identity's name = %v, want ErrConflict", err)
	}
}

// Every writer that is for humans answers ErrNotFound for the identity and leaves its row as it was.
func TestHumanOnlyWrites_AnswerNotFoundForTheIdentity(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	tag, _ := st.CreateTag(ctx, "Prod", "red", false)
	st.CreateUser(ctx, "root", "h", true, false) // an admin, so "last admin" is not what refuses
	id := insertAgentRow(t, st, "harbormaster-agent")
	exp := time.Now().Add(time.Hour)
	for _, tc := range []struct {
		name string
		call func() error
	}{
		{"SetPassword", func() error { return st.SetPassword(ctx, id, "h", false) }},
		{"SetPasswordEndingCredentials", func() error { return st.SetPasswordEndingCredentials(ctx, id, "h", true, "") }},
		{"LockUser", func() error { return st.LockUser(ctx, id, exp) }},
		{"TouchSignIn", func() error { return st.TouchSignIn(ctx, id, time.Now()) }},
		{"SetUserAIChat", func() error { return st.SetUserAIChat(ctx, id, true) }},
		{"ChangeUserAIChat", func() error { _, err := st.ChangeUserAIChat(ctx, id, true); return err }},
		{"SetAdminChecked", func() error { _, err := st.SetAdminChecked(ctx, id, true); return err }},
		{"SetBreakGlassChecked", func() error { _, err := st.SetBreakGlassChecked(ctx, id, true); return err }},
		{"DeleteUserChecked", func() error { return st.DeleteUserChecked(ctx, id) }},
		{"BindSSOIdentity", func() error {
			_, err := st.BindSSOIdentity(ctx, store.BindSSO{UserID: id, IdPEntityID: "e", ExternalID: "n", ExternalIDFormat: "f"})
			return err
		}},
		{"SetPendingLink", func() error { return st.SetPendingLink(ctx, id, "n", id, exp) }},
		{"ClearPendingLink", func() error { return st.ClearPendingLink(ctx, id) }},
		{"UnlinkSSO", func() error { _, _, err := st.UnlinkSSO(ctx, id, "h"); return err }},
		{"UpdateSAMLProfile", func() error { e := "a@example.com"; return st.UpdateSAMLProfile(ctx, id, &e, nil) }},
		{"SetPermission", func() error { return st.SetPermission(ctx, id, tag.ID, authz.ActionSet(authz.View)) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("err = %v, want ErrNotFound", err)
			}
		})
	}
	u, err := st.GetUser(ctx, id)
	if err != nil || u.Admin || u.BreakGlass || u.AIChat || u.HasPassword() || u.LockedUntil != nil || u.LastSignInAt != nil ||
		u.SSOLinked() || u.Email != nil {
		t.Fatalf("the identity changed: %+v, %v", u, err)
	}
	if perms, _ := st.PermissionsOf(ctx, id); len(perms) != 0 {
		t.Fatalf("SetPermission wrote rows for the identity: %v", perms)
	}
}

func TestSSOLookups_DoNotFindTheIdentity(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	insertAgentRow(t, st, "harbormaster-agent")
	// The lookups key on columns the shape check keeps null; a row the check let through would still not be found.
	if _, err := st.GetUserByExternalID(ctx, "e", "n"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetUserByExternalID = %v", err)
	}
	if _, err := st.GetUserByPendingLink(ctx, "e", "n", time.Now()); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetUserByPendingLink = %v", err)
	}
}

func TestLastAdmin_IdentityNeverCounts(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	root, _ := st.CreateUser(ctx, "root", "h", true, false)
	insertAgentRow(t, st, "harbormaster-agent")
	if _, err := st.SetAdminChecked(ctx, root.ID, false); !errors.Is(err, store.ErrLastAdmin) {
		t.Fatalf("demoting the only human admin = %v, want ErrLastAdmin", err)
	}
	if err := st.DeleteUserChecked(ctx, root.ID); !errors.Is(err, store.ErrLastAdmin) {
		t.Fatalf("deleting the only human admin = %v, want ErrLastAdmin", err)
	}
	if n, _ := st.CountBreakGlassAdmins(ctx); n != 0 {
		t.Errorf("break-glass admins = %d, want 0", n)
	}
	if n, _ := st.CountPasswordUsersNotBreakGlass(ctx); n != 1 {
		t.Errorf("password users = %d, want 1 (root only)", n)
	}
}

func TestLoadSubject_IdentityIsMaskedToViewAndLogs(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	prod, _ := st.CreateTag(ctx, "Prod", "red", false)
	dev, _ := st.CreateTag(ctx, "Staging", "blue", false)
	only, _ := st.CreateTag(ctx, "Only-Operate", "green", false)
	id := insertAgentRow(t, st, "harbormaster-agent")
	// Rows an older task's permission editor could have stored: the check on users does not cover them.
	for tag, actions := range map[authz.TagID]authz.ActionSet{
		prod.ID: authz.ActionSet(authz.View | authz.Logs | authz.Operate | authz.Terminal | authz.Deploy),
		dev.ID:  authz.ActionSet(authz.View),
		only.ID: authz.ActionSet(authz.View | authz.Operate),
	} {
		if _, err := st.Pool.Exec(ctx, `insert into permissions (user_id, tag_id, actions) values ($1, $2, $3)`,
			id, int64(tag), int16(actions)); err != nil {
			t.Fatal(err)
		}
	}
	sub, err := st.LoadSubject(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if sub.Admin || sub.UserID != id {
		t.Fatalf("subject = %+v", sub)
	}
	if got := sub.Perms[prod.ID]; got != authz.ActionSet(authz.View|authz.Logs) {
		t.Errorf("prod = %v, want View and Logs only", got)
	}
	if got := sub.Perms[dev.ID]; got != authz.ActionSet(authz.View) {
		t.Errorf("dev = %v, want View", got)
	}
	if got := sub.Perms[only.ID]; got != authz.ActionSet(authz.View) {
		t.Errorf("only-operate = %v, want View", got)
	}
	for _, a := range []authz.Action{authz.Operate, authz.Deploy, authz.Edit, authz.Terminal, authz.AcceptRisk} {
		if authz.Can(sub, a, prod.ID) {
			t.Errorf("the identity can perform action %v", a)
		}
	}
	// A human with the same rows keeps them.
	human, _ := st.CreateUser(ctx, "alice", "h", false, false)
	if _, err := st.Pool.Exec(ctx, `insert into permissions (user_id, tag_id, actions) values ($1, $2, $3)`,
		human.ID, int64(prod.ID), int16(authz.View|authz.Operate)); err != nil {
		t.Fatal(err)
	}
	hsub, err := st.LoadSubject(ctx, human.ID)
	if err != nil || !hsub.Perms[prod.ID].Has(authz.Operate) {
		t.Fatalf("human subject = %+v, %v", hsub, err)
	}
}

func TestLoadSubject_UnknownKindIsNotFound(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	// The kind and shape checks are dropped in this test's own schema so a row of an unknown kind can exist.
	if _, err := st.Pool.Exec(ctx, `alter table users drop constraint users_kind_check, drop constraint users_agent_shape`); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := st.Pool.QueryRow(ctx,
		`insert into users (username, password_hash, is_admin, kind) values ('robot', 'h', true, 'robot') returning id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if sub, err := st.LoadSubject(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("LoadSubject of an unknown kind = %+v, %v; want ErrNotFound", sub, err)
	}
}

func TestSetAgentIdentityPermissions_ReplacesRows(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	root, _ := st.CreateUser(ctx, "root", "h", true, false)
	prod, _ := st.CreateTag(ctx, "Prod", "red", false)
	dev, _ := st.CreateTag(ctx, "Staging", "blue", false)
	id, err := st.EnsureAgentIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetAgentIdentityPermissions(ctx, map[authz.TagID]authz.ActionSet{
		prod.ID: authz.ActionSet(authz.View | authz.Logs), dev.ID: authz.ActionSet(authz.View)}, root.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.SetAgentIdentityPermissions(ctx, map[authz.TagID]authz.ActionSet{
		dev.ID: authz.ActionSet(authz.View | authz.Logs)}, root.ID); err != nil {
		t.Fatal(err)
	}
	perms, _ := st.PermissionsOf(ctx, id.ID)
	if len(perms) != 1 || perms[dev.ID] != authz.ActionSet(authz.View|authz.Logs) {
		t.Fatalf("perms = %v, want only dev with View and Logs", perms)
	}
	if err := st.SetAgentIdentityPermissions(ctx, nil, root.ID); err != nil {
		t.Fatal(err)
	}
	if perms, _ = st.PermissionsOf(ctx, id.ID); len(perms) != 0 {
		t.Fatalf("perms after an empty set = %v", perms)
	}
	entries, _ := st.ListAudit(ctx, 10, 0)
	found := 0
	for _, e := range entries {
		if e.Action == "agent_identity_update" && e.ActorUserID != nil && *e.ActorUserID == root.ID {
			found++
		}
	}
	if found != 3 {
		t.Errorf("agent_identity_update audit rows = %d, want 3", found)
	}
}

func TestSetAgentIdentityPermissions_RefusesOtherActions(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	root, _ := st.CreateUser(ctx, "root", "h", true, false)
	prod, _ := st.CreateTag(ctx, "Prod", "red", false)
	dev, _ := st.CreateTag(ctx, "Staging", "blue", false)
	id, _ := st.EnsureAgentIdentity(ctx)
	if err := st.SetAgentIdentityPermissions(ctx, map[authz.TagID]authz.ActionSet{
		prod.ID: authz.ActionSet(authz.View | authz.Logs)}, root.ID); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		set  authz.ActionSet
	}{
		{"operate", authz.ActionSet(authz.View | authz.Operate)},
		{"terminal", authz.ActionSet(authz.View | authz.Logs | authz.Terminal)},
		{"deploy", authz.ActionSet(authz.View | authz.Deploy)},
		{"edit", authz.ActionSet(authz.View | authz.Edit)},
		{"accept risk", authz.ActionSet(authz.View | authz.AcceptRisk)},
		{"logs without view", authz.ActionSet(authz.Logs)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The refused entry comes second in a map of two, so a partial write would show.
			err := st.SetAgentIdentityPermissions(ctx, map[authz.TagID]authz.ActionSet{
				dev.ID: authz.ActionSet(authz.View), prod.ID: tc.set}, root.ID)
			if err == nil {
				t.Fatal("want an error")
			}
			perms, _ := st.PermissionsOf(ctx, id.ID)
			if len(perms) != 1 || perms[prod.ID] != authz.ActionSet(authz.View|authz.Logs) {
				t.Fatalf("a refused call changed the rows: %v", perms)
			}
		})
	}
}

func TestCreateSession_RefusesTheIdentity(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	id := insertAgentRow(t, st, "harbormaster-agent")
	now := time.Now()
	err := st.CreateSession(ctx, store.Session{IDHash: "s", UserID: id, CSRFToken: "c", CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("CreateSession = %v, want ErrNotFound", err)
	}
	var n int
	st.Pool.QueryRow(ctx, `select count(*) from sessions`).Scan(&n)
	if n != 0 {
		t.Fatalf("sessions = %d, want 0", n)
	}
	human, _ := st.CreateUser(ctx, "alice", "h", false, false)
	if err := st.CreateSession(ctx, store.Session{IDHash: "h", UserID: human.ID, CSRFToken: "c", CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatalf("a human's session: %v", err)
	}
}

func TestCreateAPIToken_RefusesTheIdentity(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	id := insertAgentRow(t, st, "harbormaster-agent")
	tok := store.APIToken{PublicID: "hmp_aaaaaaaaaaaa", UserID: id, Kind: "pat", Name: "n", SecretHash: "h", Scope: "read",
		AuthSource: "password", ExpiresAt: time.Now().Add(time.Hour)}
	if _, err := st.CreateAPIToken(ctx, tok, 10); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("CreateAPIToken = %v, want ErrNotFound", err)
	}
	if _, err := st.CreateAPITokenWithin(ctx, tok, 10, func(store.User) time.Time { return time.Now().Add(time.Hour) }); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("CreateAPITokenWithin = %v, want ErrNotFound", err)
	}
	grant := store.OAuthGrant{UserID: id, ClientID: "c", ClientName: "n", Scope: "read", ExpiresAt: time.Now().Add(time.Hour)}
	access := store.APIToken{PublicID: "hmo_aaaaaaaaaaaa", Kind: "oauth_access", Name: "n", SecretHash: "h", Scope: "read", ExpiresAt: time.Now().Add(time.Minute)}
	refresh := store.APIToken{PublicID: "hmr_aaaaaaaaaaaa", Kind: "oauth_refresh", Name: "n", SecretHash: "h2", Scope: "read", ExpiresAt: time.Now().Add(time.Hour)}
	if _, err := st.CreateOAuthGrantWithTokens(ctx, grant, access, refresh); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("CreateOAuthGrantWithTokens = %v, want ErrNotFound", err)
	}
	var n int
	st.Pool.QueryRow(ctx, `select count(*) from api_tokens`).Scan(&n)
	if n != 0 {
		t.Fatalf("api_tokens = %d, want 0", n)
	}
}

func TestCreateRunToken_AcceptsBothKinds(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	id := insertAgentRow(t, st, "harbormaster-agent")
	human, _ := st.CreateUser(ctx, "alice", "h", false, false)
	for i, uid := range []int64{id, human.ID} {
		if _, err := st.CreateRunToken(ctx, newRunToken(uid, "hma_aaaaaaaaaaa"+string(rune('a'+i)), uuid.New())); err != nil {
			t.Fatalf("CreateRunToken for user %d: %v", uid, err)
		}
	}
}

func TestUsersWithView_IncludesTheIdentity(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	tag, _ := st.CreateTag(ctx, "Prod", "red", false)
	root, _ := st.CreateUser(ctx, "root", "h", true, false)
	id, err := st.EnsureAgentIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetAgentIdentityPermissions(ctx, map[authz.TagID]authz.ActionSet{tag.ID: authz.ActionSet(authz.View)}, root.ID); err != nil {
		t.Fatal(err)
	}
	users, err := st.UsersWithView(ctx, tag.ID)
	if err != nil || len(users) != 1 || users[0].Username != id.Username {
		t.Fatalf("UsersWithView = %+v, %v; want the identity named", users, err)
	}
}
