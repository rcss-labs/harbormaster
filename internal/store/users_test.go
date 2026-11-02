package store_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func TestCreateUser_CaseInsensitiveUnique(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	if _, err := s.CreateUser(ctx, "pavel", "hash", false, false); err != nil {
		t.Fatalf("CreateUser pavel: %v", err)
	}
	_, err := s.CreateUser(ctx, "Pavel", "hash", false, false)
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("CreateUser Pavel error = %v, want ErrConflict", err)
	}
}

func TestGetUserByUsername_IgnoresCase(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	created, err := s.CreateUser(ctx, "pavel", "hash", true, true)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	got, err := s.GetUserByUsername(ctx, "PAVEL")
	if err != nil {
		t.Fatalf("GetUserByUsername(PAVEL): %v", err)
	}
	if got.ID != created.ID || got.Username != "pavel" || !got.Admin || !got.MustChangePassword {
		t.Fatalf("GetUserByUsername(PAVEL) = %+v, want the created user", got)
	}
}

func TestDeleteUser_CascadesSessionsAndPermissions(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	user, err := s.CreateUser(ctx, "pavel", "hash", false, false)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	now := time.Now()
	if err := s.CreateSession(ctx, store.Session{
		IDHash: "hash-1", UserID: user.ID, CSRFToken: "csrf",
		CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := s.Pool.Exec(ctx,
		`insert into permissions(user_id, tag_id, actions) values ($1, (select id from tags where name='Dev'), 1)`,
		user.ID); err != nil {
		t.Fatalf("insert permission: %v", err)
	}

	if err := s.DeleteUser(ctx, user.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	var sessions, permissions int
	if err := s.Pool.QueryRow(ctx, `select count(*) from sessions where user_id=$1`, user.ID).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if err := s.Pool.QueryRow(ctx, `select count(*) from permissions where user_id=$1`, user.ID).Scan(&permissions); err != nil {
		t.Fatal(err)
	}
	if sessions != 0 || permissions != 0 {
		t.Fatalf("after DeleteUser: sessions=%d permissions=%d, want 0 and 0", sessions, permissions)
	}
}

func TestFailedAttempts_CountsOnlyFailuresInWindow(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	const username, ip = "pavel", "203.0.113.7"

	for i := 0; i < 3; i++ {
		if err := s.RecordLoginAttempt(ctx, username, ip, false); err != nil {
			t.Fatalf("RecordLoginAttempt failed: %v", err)
		}
	}
	if err := s.RecordLoginAttempt(ctx, username, ip, true); err != nil {
		t.Fatalf("RecordLoginAttempt ok: %v", err)
	}
	if _, err := s.Pool.Exec(ctx,
		`insert into login_attempts(username, ip, ts, ok) values ($1, $2, now() - interval '2 hours', false)`,
		username, ip); err != nil {
		t.Fatalf("insert old attempt: %v", err)
	}

	byUser, byIP, err := s.FailedAttempts(ctx, username, ip, time.Now().Add(-15*time.Minute))
	if err != nil {
		t.Fatalf("FailedAttempts: %v", err)
	}
	if byUser != 3 || byIP != 3 {
		t.Fatalf("FailedAttempts = (%d, %d), want (3, 3)", byUser, byIP)
	}
}

func strp(s string) *string { return &s }

func newSAMLUser(name, entity, nameID string) store.NewSAMLUser {
	return store.NewSAMLUser{Username: name, IdPEntityID: entity, ExternalID: nameID, ExternalIDFormat: "unspecified"}
}

func TestCreateSAMLUser_NoPasswordAndShape(t *testing.T) {
	s := storetest.Open(t)
	ctx := t.Context()
	u, err := s.CreateSAMLUser(ctx, store.NewSAMLUser{
		Username: "ada", IdPEntityID: "https://idp/x", ExternalID: "ada@example.test",
		ExternalIDFormat: "email", Email: strp("ada@example.test"), DisplayName: strp("Ada"),
	})
	if err != nil {
		t.Fatalf("CreateSAMLUser: %v", err)
	}
	if u.PasswordHash != "" || u.HasPassword() || !u.SSOLinked() || u.Admin || u.MustChangePassword || u.BreakGlass {
		t.Fatalf("user = %+v, want password-less linked non-admin", u)
	}
	if u.IdPEntityID != "https://idp/x" || u.ExternalID != "ada@example.test" || u.ExternalIDFormat != "email" ||
		u.Email == nil || *u.Email != "ada@example.test" || u.DisplayName == nil || *u.DisplayName != "Ada" {
		t.Fatalf("round trip lost fields: %+v", u)
	}
	if _, err := s.CreateSAMLUser(ctx, newSAMLUser("ada2", "https://idp/x", "ada@example.test")); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("same external id err = %v, want ErrConflict", err)
	}
	if _, err := s.CreateSAMLUser(ctx, newSAMLUser("ADA", "https://idp/x", "other")); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("same username err = %v, want ErrConflict", err)
	}
	got, err := s.GetUserByExternalID(ctx, "https://idp/x", "ada@example.test")
	if err != nil || got.ID != u.ID {
		t.Fatalf("GetUserByExternalID = %+v, %v", got, err)
	}
	if _, err := s.GetUserByExternalID(ctx, "https://other", "ada@example.test"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("other entity err = %v, want ErrNotFound", err)
	}
	local, err := s.CreateUser(ctx, "local", "hash", false, false)
	if err != nil || !local.HasPassword() || local.SSOLinked() {
		t.Fatalf("local = %+v, %v", local, err)
	}
}

func TestUpdateSAMLProfile_OnlyNonNil(t *testing.T) {
	s := storetest.Open(t)
	ctx := t.Context()
	n := store.NewSAMLUser{Username: "ada", IdPEntityID: "e", ExternalID: "n", ExternalIDFormat: "unspecified",
		Email: strp("a@x"), DisplayName: strp("Ada")}
	u, err := s.CreateSAMLUser(ctx, n)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateSAMLProfile(ctx, u.ID, strp("b@x"), nil); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetUser(ctx, u.ID)
	if *got.Email != "b@x" || *got.DisplayName != "Ada" {
		t.Fatalf("after email-only update: %v / %v", *got.Email, *got.DisplayName)
	}
	if err := s.UpdateSAMLProfile(ctx, u.ID, nil, strp("Ada L")); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetUser(ctx, u.ID)
	if *got.Email != "b@x" || *got.DisplayName != "Ada L" {
		t.Fatalf("after name-only update: %v / %v", *got.Email, *got.DisplayName)
	}
	if err := s.UpdateSAMLProfile(ctx, 9999, strp("x"), nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing user err = %v, want ErrNotFound", err)
	}
}

func TestCountBreakGlassAdmins_CountsAdminsOnly(t *testing.T) {
	s := storetest.Open(t)
	ctx := t.Context()
	a, _ := s.CreateUser(ctx, "alpha", "h", true, false)
	b, _ := s.CreateUser(ctx, "bravo", "h", false, false)
	c, _ := s.CreateUser(ctx, "charlie", "h", true, false)
	if n, err := s.CountBreakGlassAdmins(ctx); err != nil || n != 0 {
		t.Fatalf("initial = %d, %v", n, err)
	}
	for _, id := range []int64{a.ID, b.ID} {
		if _, err := s.SetBreakGlassChecked(ctx, id, true); err != nil {
			t.Fatal(err)
		}
	}
	_ = c
	if n, err := s.CountBreakGlassAdmins(ctx); err != nil || n != 1 {
		t.Fatalf("count = %d, %v; want 1 (non-admin break-glass excluded)", n, err)
	}
}

func TestCountPasswordUsersNotBreakGlass(t *testing.T) {
	s := storetest.Open(t)
	ctx := t.Context()
	a, _ := s.CreateUser(ctx, "alpha", "h", true, false)
	_, _ = s.CreateUser(ctx, "bravo", "h", false, false)
	_, _ = s.CreateSAMLUser(ctx, newSAMLUser("jit", "e", "n1"))
	if _, err := s.SetBreakGlassChecked(ctx, a.ID, true); err != nil {
		t.Fatal(err)
	}
	if n, err := s.CountPasswordUsersNotBreakGlass(ctx); err != nil || n != 1 {
		t.Fatalf("count = %d, %v; want 1", n, err)
	}
}

func TestBindSSOIdentity_LinksLocalUser_PasswordUntouched(t *testing.T) {
	s := storetest.Open(t)
	ctx := t.Context()
	admin, _ := s.CreateUser(ctx, "root", "h", true, false)
	u, _ := s.CreateUser(ctx, "bob", "secret-hash", false, false)
	if err := s.SetPendingLink(ctx, u.ID, "bob@x", admin.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, err := s.BindSSOIdentity(ctx, store.BindSSO{UserID: u.ID, IdPEntityID: "e", ExternalID: "bob@x",
		ExternalIDFormat: "email", Email: strp("bob@x"), DisplayName: strp("Bob")})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if got.PasswordHash != "secret-hash" || !got.SSOLinked() || got.ExternalID != "bob@x" {
		t.Fatalf("bound = %+v", got)
	}
	if got.PendingLinkNameID != "" || got.PendingLinkExpiresAt != nil || got.PendingLinkSetAt != nil || got.PendingLinkSetByUserID != nil {
		t.Fatalf("pending columns not cleared: %+v", got)
	}
	if got.Email == nil || *got.Email != "bob@x" || got.DisplayName == nil || *got.DisplayName != "Bob" {
		t.Fatalf("profile not synced: %+v", got)
	}
	again, _ := s.GetUser(ctx, u.ID)
	if again.PasswordHash != "secret-hash" {
		t.Fatalf("hash changed: %q", again.PasswordHash)
	}
}

func TestBindSSOIdentity_AlreadyLinkedAndTaken(t *testing.T) {
	s := storetest.Open(t)
	ctx := t.Context()
	a, _ := s.CreateUser(ctx, "alpha", "h", false, false)
	b, _ := s.CreateUser(ctx, "bravo", "h", false, false)
	bind := func(id int64, nameID string) error {
		_, err := s.BindSSOIdentity(ctx, store.BindSSO{UserID: id, IdPEntityID: "e", ExternalID: nameID, ExternalIDFormat: "unspecified"})
		return err
	}
	if err := bind(a.ID, "n1"); err != nil {
		t.Fatal(err)
	}
	if err := bind(a.ID, "n2"); !errors.Is(err, store.ErrAlreadyLinked) {
		t.Fatalf("second bind err = %v, want ErrAlreadyLinked", err)
	}
	if err := bind(b.ID, "n1"); !errors.Is(err, store.ErrSSOIdentityTaken) {
		t.Fatalf("same identity err = %v, want ErrSSOIdentityTaken", err)
	}
	if err := bind(9999, "n3"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing user err = %v, want ErrNotFound", err)
	}
}

func TestBindSSOIdentity_Concurrent_OneWins(t *testing.T) {
	s := storetest.Open(t)
	ctx := t.Context()
	a, _ := s.CreateUser(ctx, "alpha", "h", false, false)
	b, _ := s.CreateUser(ctx, "bravo", "h", false, false)
	const n = 20
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := a.ID
			if i%2 == 1 {
				id = b.ID
			}
			_, errs[i] = s.BindSSOIdentity(ctx, store.BindSSO{UserID: id, IdPEntityID: "e", ExternalID: "same", ExternalIDFormat: "unspecified"})
		}()
	}
	wg.Wait()
	ok := 0
	for _, e := range errs {
		if e == nil {
			ok++
		} else if !errors.Is(e, store.ErrAlreadyLinked) && !errors.Is(e, store.ErrSSOIdentityTaken) {
			t.Fatalf("unexpected error %v", e)
		}
	}
	if ok != 1 {
		t.Fatalf("successes = %d, want 1", ok)
	}
}

func TestPendingLink_SetClearGet(t *testing.T) {
	s := storetest.Open(t)
	ctx := t.Context()
	admin, _ := s.CreateUser(ctx, "root", "h", true, false)
	u, _ := s.CreateUser(ctx, "bob", "h", false, false)
	now := time.Now()
	if err := s.ClearPendingLink(ctx, u.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("clear none err = %v, want ErrNotFound", err)
	}
	if err := s.SetPendingLink(ctx, u.ID, "bob@x", admin.ID, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetUserByPendingLink(ctx, "e", "bob@x", now)
	if err != nil || got.ID != u.ID || !got.PendingLinkActive(now) {
		t.Fatalf("GetUserByPendingLink = %+v, %v", got, err)
	}
	if got.PendingLinkSetByUserID == nil || *got.PendingLinkSetByUserID != admin.ID || got.PendingLinkSetAt == nil {
		t.Fatalf("set-by/set-at missing: %+v", got)
	}
	if _, err := s.GetUserByPendingLink(ctx, "e", "bob@x", now.Add(2*time.Hour)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired err = %v, want ErrNotFound", err)
	}
	if _, err := s.GetUserByPendingLink(ctx, "e", "BOB@x", now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("case-different err = %v, want ErrNotFound (exact match)", err)
	}
	if err := s.ClearPendingLink(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetUserByPendingLink(ctx, "e", "bob@x", now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("after clear err = %v, want ErrNotFound", err)
	}
}

func TestPendingLink_ExpiryAndUniqueness(t *testing.T) {
	s := storetest.Open(t)
	ctx := t.Context()
	admin, _ := s.CreateUser(ctx, "root", "h", true, false)
	a, _ := s.CreateUser(ctx, "alpha", "h", false, false)
	b, _ := s.CreateUser(ctx, "bravo", "h", false, false)
	linked, _ := s.CreateSAMLUser(ctx, newSAMLUser("jit", "e", "taken"))
	exp := time.Now().Add(time.Hour)
	if err := s.SetPendingLink(ctx, a.ID, "n1", admin.ID, exp); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPendingLink(ctx, b.ID, "n1", admin.ID, exp); !errors.Is(err, store.ErrSSOIdentityTaken) {
		t.Fatalf("duplicate pending err = %v, want ErrSSOIdentityTaken", err)
	}
	if err := s.SetPendingLink(ctx, b.ID, "taken", admin.ID, exp); !errors.Is(err, store.ErrSSOIdentityTaken) {
		t.Fatalf("already-linked nameid err = %v, want ErrSSOIdentityTaken", err)
	}
	if err := s.SetPendingLink(ctx, linked.ID, "n9", admin.ID, exp); !errors.Is(err, store.ErrAlreadyLinked) {
		t.Fatalf("set on linked user err = %v, want ErrAlreadyLinked", err)
	}
	// Re-setting on the same user replaces the previous pending link.
	if err := s.SetPendingLink(ctx, a.ID, "n2", admin.ID, exp); err != nil {
		t.Fatalf("re-set err = %v", err)
	}
	if err := s.SetPendingLink(ctx, 9999, "n5", admin.ID, exp); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing user err = %v, want ErrNotFound", err)
	}
}

func TestUnlinkSSO_SetsPasswordWhenNone(t *testing.T) {
	s := storetest.Open(t)
	ctx := t.Context()
	jit, _ := s.CreateSAMLUser(ctx, newSAMLUser("jit", "e", "n1"))
	local, _ := s.CreateUser(ctx, "bob", "orig-hash", false, false)
	if _, err := s.BindSSOIdentity(ctx, store.BindSSO{UserID: local.ID, IdPEntityID: "e", ExternalID: "n2", ExternalIDFormat: "unspecified"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, sess := range []store.Session{
		{IDHash: "jit-saml", UserID: jit.ID, AuthSource: "saml"},
		{IDHash: "jit-pw", UserID: jit.ID, AuthSource: "password"},
		{IDHash: "bob-saml", UserID: local.ID, AuthSource: "saml"},
		{IDHash: "bob-pw", UserID: local.ID, AuthSource: "password"},
	} {
		sess.CSRFToken, sess.CreatedAt, sess.LastSeenAt, sess.ExpiresAt = "c", now, now, now.Add(time.Hour)
		if err := s.CreateSession(ctx, sess); err != nil {
			t.Fatal(err)
		}
	}

	set, ended, err := s.UnlinkSSO(ctx, jit.ID, "temp-hash")
	if err != nil || !set || ended != 1 {
		t.Fatalf("unlink jit = %v, %d, %v; want true, 1, nil", set, ended, err)
	}
	got, _ := s.GetUser(ctx, jit.ID)
	if got.PasswordHash != "temp-hash" || !got.MustChangePassword || got.SSOLinked() || got.IdPEntityID != "" || got.ExternalIDFormat != "" {
		t.Fatalf("jit after unlink = %+v", got)
	}
	if _, err := s.GetSession(ctx, "jit-pw"); err != nil {
		t.Fatalf("password session gone: %v", err)
	}

	set, ended, err = s.UnlinkSSO(ctx, local.ID, "ignored")
	if err != nil || set || ended != 1 {
		t.Fatalf("unlink local = %v, %d, %v; want false, 1, nil", set, ended, err)
	}
	got, _ = s.GetUser(ctx, local.ID)
	if got.PasswordHash != "orig-hash" || got.MustChangePassword || got.SSOLinked() {
		t.Fatalf("local after unlink = %+v", got)
	}
	if _, err := s.GetSession(ctx, "bob-pw"); err != nil {
		t.Fatalf("bob password session gone: %v", err)
	}
	if _, _, err := s.UnlinkSSO(ctx, local.ID, "x"); !errors.Is(err, store.ErrNotLinked) {
		t.Fatalf("not linked err = %v, want ErrNotLinked", err)
	}
	if _, _, err := s.UnlinkSSO(ctx, 9999, "x"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing err = %v, want ErrNotFound", err)
	}
}

func TestUsersConstraints_RejectBadShapes(t *testing.T) {
	s := storetest.Open(t)
	ctx := t.Context()
	cases := map[string]string{
		"identity half set":          `insert into users (username, password_hash, idp_entity_id) values ('c1', 'h', 'e')`,
		"external id without format": `insert into users (username, password_hash, idp_entity_id, external_id) values ('c2', 'h', 'e', 'n')`,
		"break-glass no password":    `insert into users (username, break_glass, idp_entity_id, external_id, external_id_format) values ('c3', true, 'e', 'n', 'u')`,
		"pending on linked user": `insert into users (username, password_hash, idp_entity_id, external_id, external_id_format, pending_link_name_id, pending_link_expires_at)
			values ('c4', 'h', 'e', 'n', 'u', 'p', now())`,
		"pending without expiry": `insert into users (username, password_hash, pending_link_name_id) values ('c5', 'h', 'p')`,
		"expiry without pending": `insert into users (username, password_hash, pending_link_expires_at) values ('c6', 'h', now())`,
		"no credential":          `insert into users (username) values ('c7')`,
	}
	for name, sql := range cases {
		if _, err := s.Pool.Exec(ctx, sql); err == nil {
			t.Errorf("%s: insert succeeded, want a constraint error", name)
		}
	}
}

func TestBindSSOIdentity_RefusesNameIDPendingOnAnotherUser(t *testing.T) {
	s := storetest.Open(t)
	ctx := t.Context()
	admin, _ := s.CreateUser(ctx, "root", "h", true, false)
	a, _ := s.CreateUser(ctx, "alpha", "h", false, false)
	b, _ := s.CreateUser(ctx, "bravo", "h", false, false)
	if err := s.SetPendingLink(ctx, b.ID, "shared@x", admin.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	_, err := s.BindSSOIdentity(ctx, store.BindSSO{UserID: a.ID, IdPEntityID: "e", ExternalID: "shared@x", ExternalIDFormat: "email"})
	if !errors.Is(err, store.ErrSSOIdentityTaken) {
		t.Fatalf("bind err = %v, want ErrSSOIdentityTaken", err)
	}
	// The pending owner can still bind their own pre-link.
	if _, err := s.BindSSOIdentity(ctx, store.BindSSO{UserID: b.ID, IdPEntityID: "e", ExternalID: "shared@x", ExternalIDFormat: "email"}); err != nil {
		t.Fatalf("owner bind: %v", err)
	}
}

func TestBindSSOIdentity_IgnoresExpiredPendingOnAnotherUser(t *testing.T) {
	s := storetest.Open(t)
	ctx := t.Context()
	admin, _ := s.CreateUser(ctx, "root", "h", true, false)
	a, _ := s.CreateUser(ctx, "alpha", "h", false, false)
	b, _ := s.CreateUser(ctx, "bravo", "h", false, false)
	if err := s.SetPendingLink(ctx, b.ID, "old@x", admin.ID, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BindSSOIdentity(ctx, store.BindSSO{UserID: a.ID, IdPEntityID: "e", ExternalID: "old@x", ExternalIDFormat: "email"}); err != nil {
		t.Fatalf("bind over expired pending: %v", err)
	}
}

// Binding a NameID and pre-linking it for a different user must never both succeed.
func TestBindVersusSetPendingLink_NeverBothSucceed(t *testing.T) {
	s := storetest.Open(t)
	ctx := t.Context()
	admin, _ := s.CreateUser(ctx, "root", "h", true, false)
	for round := range 40 {
		a, _ := s.CreateUser(ctx, fmt.Sprintf("a%d", round), "h", false, false)
		b, _ := s.CreateUser(ctx, fmt.Sprintf("b%d", round), "h", false, false)
		nameID := fmt.Sprintf("race%d@x", round)
		var bindErr, pendErr error
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, bindErr = s.BindSSOIdentity(ctx, store.BindSSO{UserID: a.ID, IdPEntityID: "e", ExternalID: nameID, ExternalIDFormat: "email"})
		}()
		go func() {
			defer wg.Done()
			<-start
			pendErr = s.SetPendingLink(ctx, b.ID, nameID, admin.ID, time.Now().Add(time.Hour))
		}()
		close(start)
		wg.Wait()
		if bindErr == nil && pendErr == nil {
			t.Fatalf("round %d: both bind and set-pending succeeded for %s", round, nameID)
		}
		for _, err := range []error{bindErr, pendErr} {
			if err != nil && !errors.Is(err, store.ErrSSOIdentityTaken) {
				t.Fatalf("round %d: unexpected error %v", round, err)
			}
		}
	}
}

func TestCreateBreakGlassAdmin(t *testing.T) {
	s := storetest.Open(t)
	ctx := t.Context()
	u, err := s.CreateBreakGlassAdmin(ctx, "Rescue", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if !u.Admin || !u.BreakGlass || !u.MustChangePassword || u.PasswordHash != "hash" || u.Username != "Rescue" {
		t.Fatalf("user = %+v", u)
	}
	if _, err := s.CreateBreakGlassAdmin(ctx, "rescue", "hash"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate err = %v, want ErrConflict", err)
	}
}
