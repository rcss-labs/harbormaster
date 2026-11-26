package store_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

// seedReadableRun writes a run row directly; covered tag 7.
func seedReadableRun(t *testing.T, s *store.Store, typ, state string, test bool, at time.Time) int64 {
	t.Helper()
	var id int64
	if err := s.Pool.QueryRow(context.Background(), `insert into agent_review_runs
		(review_name, review_type, period_key, period_label, scheduled_for, manual, state, requested_at, finished_at, covered_tags, test, delivery_planned_at)
		values ('r', $1, 'x:' || gen_random_uuid()::text, 'l', $4::timestamptz, $3, $2, $4::timestamptz,
		 case when $2::text in ('done', 'failed', 'skipped') then $4::timestamptz end, '{7}', $3, case when $3 then $4::timestamptz end)
		returning id`, typ, state, test, at).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func readableRunIDs(t *testing.T, s *store.Store, admin bool) []int64 {
	t.Helper()
	runs, err := s.ListReadableReviewRuns(context.Background(), store.ReadableReviewRunFilter{Admin: admin, ViewableTags: []int64{7}})
	if err != nil {
		t.Fatal(err)
	}
	ids := []int64{}
	for _, run := range runs {
		ids = append(ids, run.ID)
	}
	return ids
}

// The sets the API opens runs by and the sets the list is filtered by are one: a value one of them knows alone would
// open by id and never be listed, or the other way round.
func TestReadableReviewRuns_EveryKnownTypeAndStateIsListed(t *testing.T) {
	s := storetest.Open(t)
	invExec(t, s, `alter table agent_review_runs drop constraint agent_review_runs_state_check`)
	types := map[string]bool{"cost": true, "security": true, "reliability": true, "capacity": true, "custom": true, "tailored": false, "Custom": false, "": false}
	states := map[string]bool{"requested": true, "running": true, "done": true, "failed": true, "skipped": true, "archived": false, "": false}
	for typ, known := range types {
		if got := store.KnownReviewType(typ); got != known {
			t.Errorf("KnownReviewType(%q) = %v, want %v", typ, got, known)
		}
	}
	for state, known := range states {
		if got := store.KnownReviewRunState(state); got != known {
			t.Errorf("KnownReviewRunState(%q) = %v, want %v", state, got, known)
		}
	}
	want := []int64{}
	at := revNow
	for typ, typeKnown := range types {
		for state, stateKnown := range states {
			at = at.Add(time.Second)
			id := seedReadableRun(t, s, typ, state, false, at)
			if typeKnown && stateKnown {
				want = append(want, id)
			}
		}
	}
	slices.Reverse(want) // newest first
	for _, admin := range []bool{true, false} {
		if got := readableRunIDs(t, s, admin); !slices.Equal(got, want) {
			t.Errorf("admin %v: listed %v, want %v", admin, got, want)
		}
	}
}

// Test runs are for admins only even when the row holds covered tags (a row StartReviewRun did not write).
func TestReadableReviewRuns_TestRunsAreForAdminsOnly(t *testing.T) {
	s := storetest.Open(t)
	// The schema now forbids a test row that holds covered tags (agent_review_runs_test_no_tags), so a row like this one
	// cannot exist in production. The predicate is still tested: it is the second line of defence if a row slips in.
	if _, err := s.Pool.Exec(context.Background(), `alter table agent_review_runs drop constraint agent_review_runs_test_no_tags`); err != nil {
		t.Fatal(err)
	}
	normal := seedReadableRun(t, s, "custom", "done", false, revNow)
	test := seedReadableRun(t, s, "cost", "done", true, revNow.Add(time.Minute))
	if got := readableRunIDs(t, s, false); !slices.Equal(got, []int64{normal}) {
		t.Errorf("reader with View on the covered tag: %v, want only the normal run %d", got, normal)
	}
	if got := readableRunIDs(t, s, true); !slices.Equal(got, []int64{test, normal}) {
		t.Errorf("admin: %v, want [%d %d]", got, test, normal)
	}
}
