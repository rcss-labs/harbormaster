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

	user, err := s.CreateUser(ctx, "sam", "hash", false, false)
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

func TestSession_AuthSourceAndNotAfterRoundTrip(t *testing.T) {
	s := storetest.Open(t)
	ctx := t.Context()
	user, _ := s.CreateUser(ctx, "sam", "hash", false, false)
	now := time.Now().UTC().Truncate(time.Microsecond)
	notAfter := now.Add(30 * time.Minute)
	base := store.Session{UserID: user.ID, CSRFToken: "c", CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)}
	pw := base
	pw.IDHash = "pw"
	saml := base
	saml.IDHash, saml.AuthSource, saml.NotAfter = "saml", "saml", &notAfter
	for _, sess := range []store.Session{pw, saml} {
		if err := s.CreateSession(ctx, sess); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.GetSession(ctx, "pw")
	if err != nil || got.AuthSource != "password" || got.NotAfter != nil {
		t.Fatalf("pw = %+v, %v; want default password source and nil NotAfter", got, err)
	}
	got, err = s.GetSession(ctx, "saml")
	if err != nil || got.AuthSource != "saml" || got.NotAfter == nil || !got.NotAfter.Equal(notAfter) {
		t.Fatalf("saml = %+v, %v", got, err)
	}
}

func seedMixedSessions(t *testing.T, s *store.Store) (a, b store.User) {
	t.Helper()
	ctx := t.Context()
	a, _ = s.CreateUser(ctx, "alpha", "h", false, false)
	b, _ = s.CreateUser(ctx, "bravo", "h", false, false)
	now := time.Now()
	for _, sess := range []store.Session{
		{IDHash: "a-pw", UserID: a.ID, AuthSource: "password"},
		{IDHash: "a-saml", UserID: a.ID, AuthSource: "saml"},
		{IDHash: "b-pw", UserID: b.ID},
		{IDHash: "b-saml", UserID: b.ID, AuthSource: "saml"},
	} {
		sess.CSRFToken, sess.CreatedAt, sess.LastSeenAt, sess.ExpiresAt = "c", now, now, now.Add(time.Hour)
		if err := s.CreateSession(ctx, sess); err != nil {
			t.Fatal(err)
		}
	}
	return a, b
}

func TestDeleteSAMLSessions_LeavesPasswordSessions(t *testing.T) {
	s := storetest.Open(t)
	seedMixedSessions(t, s)
	n, err := s.DeleteSAMLSessions(t.Context())
	if err != nil || n != 2 {
		t.Fatalf("deleted %d, %v; want 2", n, err)
	}
	for _, id := range []string{"a-saml", "b-saml"} {
		if _, err := s.GetSession(t.Context(), id); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s still present: %v", id, err)
		}
	}
	for _, id := range []string{"a-pw", "b-pw"} {
		if _, err := s.GetSession(t.Context(), id); err != nil {
			t.Errorf("%s gone: %v", id, err)
		}
	}
}

func TestDeleteSAMLSessionsForUser(t *testing.T) {
	s := storetest.Open(t)
	a, _ := seedMixedSessions(t, s)
	n, err := s.DeleteSAMLSessionsForUser(t.Context(), a.ID)
	if err != nil || n != 1 {
		t.Fatalf("deleted %d, %v; want 1", n, err)
	}
	if _, err := s.GetSession(t.Context(), "a-saml"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a-saml still present: %v", err)
	}
	for _, id := range []string{"a-pw", "b-pw", "b-saml"} {
		if _, err := s.GetSession(t.Context(), id); err != nil {
			t.Errorf("%s gone: %v", id, err)
		}
	}
}
