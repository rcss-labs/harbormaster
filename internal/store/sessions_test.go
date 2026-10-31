package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func TestGetSession_ExpiredIsNotFound(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	user, err := s.CreateUser(ctx, "pavel", "hash", false, false)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	now := time.Now()
	if err := s.CreateSession(ctx, store.Session{
		IDHash: "live", UserID: user.ID, CSRFToken: "csrf",
		CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("CreateSession live: %v", err)
	}
	if err := s.CreateSession(ctx, store.Session{
		IDHash: "expired", UserID: user.ID, CSRFToken: "csrf",
		CreatedAt: now.Add(-2 * time.Hour), LastSeenAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Minute),
	}); err != nil {
		t.Fatalf("CreateSession expired: %v", err)
	}

	if _, err := s.GetSession(ctx, "live"); err != nil {
		t.Fatalf("GetSession(live): %v", err)
	}
	if _, err := s.GetSession(ctx, "expired"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetSession(expired) error = %v, want ErrNotFound", err)
	}
}
