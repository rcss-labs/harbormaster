package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func TestListAudit_NewestFirstWithOffset(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	base := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	for i, action := range []string{"first", "second", "third"} {
		if err := s.Audit(ctx, store.AuditEntry{
			TS:      base.Add(time.Duration(i) * time.Second),
			Action:  action,
			Summary: action,
		}); err != nil {
			t.Fatalf("Audit %s: %v", action, err)
		}
	}

	page1, err := s.ListAudit(ctx, 2, 0)
	if err != nil {
		t.Fatalf("ListAudit(2, 0): %v", err)
	}
	if len(page1) != 2 || page1[0].Action != "third" || page1[1].Action != "second" {
		t.Fatalf("ListAudit(2, 0) actions = %v, want [third second]", actionsOf(page1))
	}

	page2, err := s.ListAudit(ctx, 2, 2)
	if err != nil {
		t.Fatalf("ListAudit(2, 2): %v", err)
	}
	if len(page2) != 1 || page2[0].Action != "first" {
		t.Fatalf("ListAudit(2, 2) actions = %v, want [first]", actionsOf(page2))
	}
}

func actionsOf(entries []store.AuditEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Action)
	}
	return out
}
