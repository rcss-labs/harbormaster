package store_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func savedSearch(userID int64, name, query string) store.SavedLogSearch {
	return store.SavedLogSearch{
		UserID:     userID,
		Name:       name,
		Query:      query,
		ServiceIDs: []string{"acct/eu-west-1/cluster/svc"},
		Range:      "1h",
	}
}

func TestSaveLogSearch_UpsertsByName(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	u, err := s.CreateUser(ctx, "alice", "h", false, false)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if _, err := s.SaveLogSearch(ctx, savedSearch(u.ID, "errors", "level=error")); err != nil {
		t.Fatalf("first save: %v", err)
	}
	if _, err := s.SaveLogSearch(ctx, savedSearch(u.ID, "errors", "level=warn")); err != nil {
		t.Fatalf("second save: %v", err)
	}
	list, err := s.ListSavedLogSearches(ctx, u.ID)
	if err != nil {
		t.Fatalf("ListSavedLogSearches: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d saved searches, want 1 after upsert by name", len(list))
	}
	if list[0].Query != "level=warn" {
		t.Fatalf("query = %q, want the replaced query level=warn", list[0].Query)
	}
}

func TestSavedLogSearches_ArePerUser(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	alice, err := s.CreateUser(ctx, "alice", "h", false, false)
	if err != nil {
		t.Fatalf("CreateUser alice: %v", err)
	}
	bob, err := s.CreateUser(ctx, "bob", "h", false, false)
	if err != nil {
		t.Fatalf("CreateUser bob: %v", err)
	}
	saved, err := s.SaveLogSearch(ctx, savedSearch(alice.ID, "mine", "x"))
	if err != nil {
		t.Fatalf("SaveLogSearch: %v", err)
	}

	bobList, err := s.ListSavedLogSearches(ctx, bob.ID)
	if err != nil {
		t.Fatalf("ListSavedLogSearches bob: %v", err)
	}
	if len(bobList) != 0 {
		t.Fatalf("bob sees %d saved searches, want 0", len(bobList))
	}
	if err := s.DeleteSavedLogSearch(ctx, bob.ID, saved.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("bob deleting alice's search error = %v, want ErrNotFound", err)
	}
	aliceList, err := s.ListSavedLogSearches(ctx, alice.ID)
	if err != nil {
		t.Fatalf("ListSavedLogSearches alice: %v", err)
	}
	if len(aliceList) != 1 {
		t.Fatalf("alice has %d saved searches after bob's delete attempt, want 1", len(aliceList))
	}
}

func TestSaveLogSearch_LimitPerUser(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	u, err := s.CreateUser(ctx, "alice", "h", false, false)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	for i := 0; i < store.MaxSavedLogSearches; i++ {
		if _, err := s.SaveLogSearch(ctx, savedSearch(u.ID, fmt.Sprintf("s-%02d", i), "q")); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}
	if _, err := s.SaveLogSearch(ctx, savedSearch(u.ID, "one-too-many", "q")); !errors.Is(err, store.ErrSavedLimit) {
		t.Fatalf("new name over the limit error = %v, want ErrSavedLimit", err)
	}
	if _, err := s.SaveLogSearch(ctx, savedSearch(u.ID, "s-00", "replaced")); err != nil {
		t.Fatalf("replacing an existing name at the limit: %v", err)
	}
}

func TestSavedLogSearches_DeletedWithUser(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	u, err := s.CreateUser(ctx, "alice", "h", false, false)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if _, err := s.SaveLogSearch(ctx, savedSearch(u.ID, "mine", "x")); err != nil {
		t.Fatalf("SaveLogSearch: %v", err)
	}
	if _, err := s.Pool.Exec(ctx, `delete from users where id = $1`, u.ID); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	var count int
	if err := s.Pool.QueryRow(ctx, `select count(*) from log_search_saved where user_id = $1`, u.ID).Scan(&count); err != nil {
		t.Fatalf("count saved searches: %v", err)
	}
	if count != 0 {
		t.Fatalf("%d saved searches remain after the user was deleted, want 0", count)
	}
}
