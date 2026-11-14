package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func TestLastSessionActivity(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	if _, ok, err := s.LastSessionActivity(ctx, now); err != nil || ok {
		t.Fatalf("no sessions: ok=%v err=%v, want false, nil", ok, err)
	}

	user, err := s.CreateUser(ctx, "sam", "hash", false, false)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	add := func(id string, lastSeen, expires time.Time) {
		t.Helper()
		if err := s.CreateSession(ctx, store.Session{
			IDHash: id, UserID: user.ID, CSRFToken: "c",
			CreatedAt: lastSeen, LastSeenAt: lastSeen, ExpiresAt: expires,
		}); err != nil {
			t.Fatalf("CreateSession %s: %v", id, err)
		}
	}
	// The newest last_seen_at belongs to an expired session and must be ignored.
	add("expired-newest", now.Add(-time.Minute), now.Add(-time.Second))
	if _, ok, err := s.LastSessionActivity(ctx, now); err != nil || ok {
		t.Fatalf("only expired: ok=%v err=%v, want false, nil", ok, err)
	}
	older := now.Add(-30 * time.Minute)
	newer := now.Add(-5 * time.Minute)
	add("live-old", older, now.Add(time.Hour))
	add("live-new", newer, now.Add(time.Hour))

	got, ok, err := s.LastSessionActivity(ctx, now)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v, want true, nil", ok, err)
	}
	if !got.Equal(newer) {
		t.Errorf("last activity = %v, want %v", got, newer)
	}
}
