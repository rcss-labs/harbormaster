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

// activityToken stores a token for the user and, when lastUsed is set, records its use.
func activityToken(t *testing.T, s *store.Store, userID int64, id string, expires time.Time, lastUsed *time.Time) {
	t.Helper()
	tok := mustCreateToken(t, s, store.APIToken{PublicID: id, UserID: userID, Kind: "pat", Name: "n", SecretHash: "h",
		Scope: "read", AuthSource: "password", ExpiresAt: expires})
	if lastUsed != nil {
		if err := s.TouchAPIToken(context.Background(), tok.ID, *lastUsed, ""); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLastSessionActivity_CountsTokenUse(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	user, _ := s.CreateUser(ctx, "sam", "hash", false, false)
	used := now.Add(-3 * time.Minute)
	activityToken(t, s, user.ID, "hmp_aaaaaaaaaaaa", now.Add(time.Hour), &used)
	got, ok, err := s.LastSessionActivity(ctx, now)
	if err != nil || !ok || !got.Equal(used) {
		t.Fatalf("got %v, %v, %v; want %v, true", got, ok, err, used)
	}
}

func TestLastSessionActivity_NewerOfSessionAndToken(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	for name, tc := range map[string]struct{ session, token time.Time }{
		"session newer": {now.Add(-time.Minute), now.Add(-10 * time.Minute)},
		"token newer":   {now.Add(-10 * time.Minute), now.Add(-time.Minute)},
	} {
		t.Run(name, func(t *testing.T) {
			s := storetest.Open(t)
			ctx := context.Background()
			user, _ := s.CreateUser(ctx, "sam", "hash", false, false)
			if err := s.CreateSession(ctx, store.Session{IDHash: "s", UserID: user.ID, CSRFToken: "c",
				CreatedAt: tc.session, LastSeenAt: tc.session, ExpiresAt: now.Add(time.Hour)}); err != nil {
				t.Fatal(err)
			}
			token := tc.token
			activityToken(t, s, user.ID, "hmp_aaaaaaaaaaaa", now.Add(time.Hour), &token)
			want := tc.session
			if tc.token.After(want) {
				want = tc.token
			}
			got, ok, err := s.LastSessionActivity(ctx, now)
			if err != nil || !ok || !got.Equal(want) {
				t.Fatalf("got %v, %v, %v; want %v", got, ok, err, want)
			}
		})
	}
}

func TestLastSessionActivity_IgnoresExpiredTokensAndNeverUsedOnes(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	user, _ := s.CreateUser(ctx, "sam", "hash", false, false)
	used := now.Add(-time.Minute)
	activityToken(t, s, user.ID, "hmp_expiredexpir", now.Add(-time.Second), &used)
	activityToken(t, s, user.ID, "hmp_neverusedneve", now.Add(time.Hour), nil)
	if got, ok, err := s.LastSessionActivity(ctx, now); err != nil || ok {
		t.Fatalf("got %v, %v, %v; want no activity", got, ok, err)
	}
}
