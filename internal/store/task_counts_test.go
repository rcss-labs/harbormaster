package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/store"
)

func TestTaskCounts_MatchesListTasksTotalPerFilter(t *testing.T) {
	fx := newReadFixture(t)
	upsertWindowTasks(t, fx)
	ctx := context.Background()
	admin := store.Visibility{Admin: true}
	for label, base := range map[string]store.TaskQuery{
		"no window":   {Vis: admin},
		"window":      {Vis: admin, StoppedAfter: fx.now.Add(-10 * time.Minute)},
		"cluster":     {Vis: admin, Cluster: "core-services"},
		"search":      {Vis: admin, Q: "p5"},
		"hidden tags": {},
	} {
		got, err := fx.s.TaskCounts(ctx, base)
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		for filter, gotN := range map[string]int{"all": got.All, "problems": got.Problems, "running": got.Running,
			"stopped": got.Stopped, "stopped_error": got.StoppedError} {
			probe := base
			probe.Filter, probe.Limit = filter, 1
			_, want, err := fx.s.ListTasks(ctx, probe)
			if err != nil {
				t.Fatalf("%s/%s: %v", label, filter, err)
			}
			if gotN != want {
				t.Errorf("%s: TaskCounts %s = %d, ListTasks total = %d", label, filter, gotN, want)
			}
		}
	}
}
