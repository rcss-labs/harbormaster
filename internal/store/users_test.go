package store_test

import (
	"context"
	"errors"
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
