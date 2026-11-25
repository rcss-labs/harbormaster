package store_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

// nameIDs are n different service ids.
func nameIDs(n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: "482109375512", Region: "eu-west-1", Cluster: "Infra-ClusterEB0386A7-wD3kQ9sLmN2v"},
			Service: fmt.Sprintf("CheckoutStack-CheckoutServiceF3A9B2C1-Xy7KpQ2mNv%02d", i)}.ID()
	}
	return ids
}

// finishers finish one investigation and one review run with a result, and read back the ids stored with it.
func finishers(t *testing.T, s *store.Store) map[string]struct {
	finish func(state, reason string, res *store.InvestigationResult) error
	read   func() []string
	state  func() string
} {
	t.Helper()
	ctx := context.Background()
	invSeed(t, s, []string{"a"}, invNow)
	inv := claimed(t, s, invNow)
	run, c := claimedReview(t, s)
	return map[string]struct {
		finish func(state, reason string, res *store.InvestigationResult) error
		read   func() []string
		state  func() string
	}{
		"investigation": {
			func(state, reason string, res *store.InvestigationResult) error {
				return s.FinishInvestigation(ctx, inv.Claim(), state, reason, res, invNow)
			},
			func() []string {
				one, err := s.GetInvestigation(ctx, inv.ID)
				if err != nil {
					t.Fatal(err)
				}
				list, err := s.ListInvestigations(ctx, store.InvestigationFilter{})
				if err != nil || len(list) != 1 || !slices.Equal(list[0].SummaryNameIDs, one.SummaryNameIDs) {
					t.Errorf("listed %+v, %v; read %q", list, err, one.SummaryNameIDs)
				}
				return one.SummaryNameIDs
			},
			func() string { v, _ := s.GetInvestigation(ctx, inv.ID); return v.State },
		},
		"review": {
			func(state, reason string, res *store.InvestigationResult) error {
				return s.FinishReviewRun(ctx, c, state, reason, res, nil, invNow)
			},
			func() []string {
				one := reviewRunState(t, s, run.ID)
				list, err := s.ListReviewRuns(ctx, store.ReviewRunFilter{})
				if err != nil || len(list) != 1 || !slices.Equal(list[0].SummaryNameIDs, one.SummaryNameIDs) {
					t.Errorf("listed %+v, %v; read %q", list, err, one.SummaryNameIDs)
				}
				return one.SummaryNameIDs
			},
			func() string { return reviewRunState(t, s, run.ID).State },
		},
	}
}

func TestSummaryNameIDs_RoundTrip(t *testing.T) {
	long := strings.Repeat("a", 512)
	for label, ids := range map[string][]string{"none": nil, "an empty list": {}, "some, in order": {nameIDs(3)[2], nameIDs(3)[0]}, "sixty-four": nameIDs(64), "the longest": {long}} {
		t.Run(label, func(t *testing.T) {
			for name, f := range finishers(t, storetest.Open(t)) {
				if got := f.read(); len(got) != 0 {
					t.Errorf("%s: before the end: %q", name, got)
				}
				if err := f.finish("done", "", &store.InvestigationResult{Summary: "s", NameIDs: ids}); err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				if got := f.read(); !slices.Equal(got, ids) {
					t.Errorf("%s: read %q, want %q", name, got, ids)
				}
			}
		})
	}
	// A run that ends without a result stores none.
	for name, f := range finishers(t, storetest.Open(t)) {
		if err := f.finish("failed", "error", nil); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := f.read(); len(got) != 0 {
			t.Errorf("%s: read %q", name, got)
		}
	}
}

func TestSummaryNameIDs_BoundsAreRefused(t *testing.T) {
	good := nameIDs(1)[0]
	for label, c := range map[string]struct {
		ids  []string
		text bool // refused as text
	}{
		"sixty-five":    {nameIDs(65), false},
		"too long":      {[]string{good, strings.Repeat("a", 513)}, true},
		"a NUL":         {[]string{good, "a\x00b"}, true},
		"not UTF-8":     {[]string{"a\xff"}, true},
		"too many long": {append(nameIDs(65), strings.Repeat("a", 513)), false},
	} {
		t.Run(label, func(t *testing.T) {
			s := storetest.Open(t)
			for name, f := range finishers(t, s) {
				err := f.finish("done", "", &store.InvestigationResult{Summary: "s", NameIDs: c.ids})
				if err == nil || errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrInvestigationText) != c.text {
					t.Errorf("%s: err = %v, want a refusal (of text: %v)", name, err, c.text)
				}
				if f.state() != "running" || len(f.read()) != 0 {
					t.Errorf("%s: a refused result was written: %s %q", name, f.state(), f.read())
				}
			}
			for _, table := range []string{"agent_investigations", "agent_review_runs"} {
				if len(c.ids) < 65 {
					continue
				}
				// The schema refuses it too, for a writer that skips the Go check.
				if _, err := s.Pool.Exec(context.Background(), `update `+table+` set summary_name_ids = $1`, c.ids); err == nil {
					t.Errorf("%s: the schema accepted %d ids", table, len(c.ids))
				}
			}
		})
	}
}

// stepBackTo0223 turns a migrated schema into what the release before names were kept left.
func stepBackTo0223(t *testing.T, s *store.Store) {
	t.Helper()
	invExec(t, s, `alter table agent_investigations drop column if exists summary_name_ids;
		alter table agent_review_runs drop column if exists summary_name_ids`)
}

func apply0224(s *store.Store) error {
	body, err := os.ReadFile("migrations/0224_agent_summary_names.up.sql")
	if err == nil {
		_, err = s.Pool.Exec(context.Background(), string(body))
	}
	return err
}

// Rows of the release before get the default, and its statements, which never name the column, work before and after.
func TestMigrate_0224KeepsOldRowsAndStatements(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	stepBackTo0223(t, s)
	old := func(n int) {
		t.Helper()
		invExec(t, s, `insert into agent_investigations
			(subject_kind, subject_key, account_id, alert_kind, alert_title, alert_text, alert_opened_at, requested_at, state, attempts)
			values ('service', $1, '482109375512', 'service_failing', 't', 'x', now(), now(), 'running', 1)`, fmt.Sprint("service/", n))
		invExec(t, s, `insert into agent_review_runs (review_name, review_type, period_key, period_label, scheduled_for, state, attempts)
			values ('n', 'cost', $1, 'l', now(), 'running', 1)`, fmt.Sprint("d:", n))
		// How an older task ends a run.
		invExec(t, s, `update agent_investigations set state = 'done', reason = '', summary = 'old', summary_replaced = false,
			evidence_dropped = 0, finished_at = now(), claimed_until = null where subject_key = $1`, fmt.Sprint("service/", n))
		invExec(t, s, `update agent_review_runs set state = 'done', reason = '', summary = 'old', summary_replaced = false,
			evidence_dropped = 0, finished_at = now(), claimed_until = null where period_key = $1`, fmt.Sprint("d:", n))
	}
	old(1)
	if err := apply0224(s); err != nil {
		t.Fatalf("apply 0224 over existing rows: %v", err)
	}
	old(2)
	invs, err := s.ListInvestigations(ctx, store.InvestigationFilter{})
	if err != nil || len(invs) != 2 {
		t.Fatalf("investigations = %+v, %v", invs, err)
	}
	runs, err := s.ListReviewRuns(ctx, store.ReviewRunFilter{})
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs = %+v, %v", runs, err)
	}
	for i := range 2 {
		if len(invs[i].SummaryNameIDs) != 0 || invs[i].Summary != "old" || len(runs[i].SummaryNameIDs) != 0 || runs[i].Summary != "old" {
			t.Errorf("rows = %+v and %+v, want no ids", invs[i], runs[i])
		}
	}
	var nulls int
	if err := s.Pool.QueryRow(ctx, `select (select count(*) from agent_investigations where summary_name_ids is null or summary_name_ids <> '{}')
		+ (select count(*) from agent_review_runs where summary_name_ids is null or summary_name_ids <> '{}')`).Scan(&nulls); err != nil || nulls != 0 {
		t.Errorf("%d rows without the default, %v", nulls, err)
	}
	if _, err := s.Pool.Exec(ctx, `update agent_investigations set summary_name_ids = null`); err == nil {
		t.Error("null was accepted")
	}

	// Rolled back by hand and forward again.
	stepBackTo0223(t, s)
	old(3)
	if err := apply0224(s); err != nil {
		t.Fatalf("apply 0224 again: %v", err)
	}
	old(4)
}

// As for 0223: an open reader must make the migration give up, not queue every later statement behind it.
func TestMigrate_0224GivesUpOnALock(t *testing.T) {
	for _, table := range []string{"agent_investigations", "agent_review_runs"} {
		t.Run(table, func(t *testing.T) {
			s, ctx := storetest.Open(t), context.Background()
			stepBackTo0223(t, s)
			reader, err := s.Pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			release := func() { reader.Rollback(ctx) } //nolint:errcheck // nothing was written
			defer release()
			if _, err := reader.Exec(ctx, `select count(*) from `+table); err != nil {
				t.Fatal(err)
			}
			migrated := make(chan error, 1)
			go func() { migrated <- apply0224(s) }()
			select {
			case err := <-migrated:
				if err == nil || !strings.Contains(err.Error(), "55P03") {
					t.Fatalf("migration under an open reader = %v, want a lock timeout (55P03)", err)
				}
			case <-time.After(4 * time.Second):
				release()
				<-migrated
				t.Fatal("the migration was still waiting after 4 seconds: it must time out on its lock within 2")
			}
			release()
			if err := apply0224(s); err != nil {
				t.Fatalf("retry once the reader is gone: %v", err)
			}
		})
	}
}
