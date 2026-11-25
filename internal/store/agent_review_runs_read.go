package store

import (
	"context"
	"fmt"
	"time"
)

// ReadableReviewRunFilter pages the runs one reader may read, newest first. Admin readers see every run of a known
// state and type; others only runs that covered at least one tag and whose covered tags are all in ViewableTags.
// BeforeAt (when not zero) and BeforeID are the keyset cursor.
type ReadableReviewRunFilter struct {
	Admin        bool
	ViewableTags []int64
	BeforeAt     time.Time
	BeforeID     int64
	Limit        int
}

// ListReadableReviewRuns applies the reader rule in SQL, so a page is filled from the readable runs alone however many
// unreadable ones lie between them. The caller still checks each row; this is the same rule, not a second one.
func (s *Store) ListReadableReviewRuns(ctx context.Context, f ReadableReviewRunFilter) ([]ReviewRun, error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	var before *time.Time
	if !f.BeforeAt.IsZero() {
		before = &f.BeforeAt
	}
	viewable := f.ViewableTags
	if viewable == nil {
		viewable = []int64{}
	}
	rows, err := s.Pool.Query(ctx, `select `+reviewRunColumns+` from agent_review_runs
		where state in ('requested', 'running', 'done', 'failed', 'skipped')
		  and review_type in ('cost', 'security', 'reliability', 'capacity')
		  and ($1 or (covered_tags <> '{}' and covered_tags <@ $2::bigint[]))
		  and ($3::timestamptz is null or (requested_at, id) < ($3, $4))
		order by requested_at desc, id desc limit $5`, f.Admin, viewable, before, f.BeforeID, f.Limit)
	if err != nil {
		return nil, fmt.Errorf("list readable review runs: %w", err)
	}
	out, err := collectReviewRuns(rows)
	if err != nil {
		return nil, fmt.Errorf("list readable review runs: %w", err)
	}
	return out, nil
}
