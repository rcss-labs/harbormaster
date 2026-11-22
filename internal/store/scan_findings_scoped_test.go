package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
)

func TestOpenFindingCountsByScope_MatchesPerDigestCalls(t *testing.T) {
	fx := newReadFixture(t)
	s, ctx := fx.s, context.Background()
	a, b := serviceKey("core-services", "payments-api"), serviceKey("core-services", "notifications")
	for _, digest := range []string{"sha256:one", "sha256:two", "sha256:three"} {
		openFindingFixture(t, fx, digest)
	}
	acceptCritical(t, s, "sha256:one", fx.now.Add(time.Hour), a)
	acceptCritical(t, s, "sha256:two", fx.now.Add(time.Hour), a)
	scopes := map[string][]model.ServiceKey{
		"sha256:one":   {a},    // accepted
		"sha256:two":   {a, b}, // b has not accepted
		"sha256:three": {b},
	}
	gotCounts, gotAccepted, err := s.OpenFindingCountsByScope(ctx, scopes, fx.now)
	if err != nil {
		t.Fatal(err)
	}
	for digest, scope := range scopes {
		counts, accepted, err := s.OpenFindingCounts(ctx, []string{digest}, scope, fx.now)
		if err != nil {
			t.Fatal(err)
		}
		if gotCounts[digest] != counts[digest] || gotAccepted[digest] != accepted[digest] {
			t.Errorf("%s: batch counts=%+v accepted=%d, per-digest counts=%+v accepted=%d",
				digest, gotCounts[digest], gotAccepted[digest], counts[digest], accepted[digest])
		}
	}
	if gotAccepted["sha256:one"] != 1 || gotCounts["sha256:two"].Critical != 1 {
		t.Errorf("unexpected fixture result: %+v %+v", gotCounts, gotAccepted)
	}
}
