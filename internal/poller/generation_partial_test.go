package poller

import (
	"context"
	"testing"

	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
)

// blockStatement makes one kind of statement fail, so that a step that follows a committed write fails and the test
// can see whether the committed write still told the cache.
func (h *loopHarness) blockStatement(t *testing.T, name, when string) {
	t.Helper()
	ctx := context.Background()
	for _, sql := range []string{
		`create function ` + name + `() returns trigger language plpgsql as $$ begin raise exception 'blocked by the test'; end $$`,
		`create trigger ` + name + ` ` + when + ` execute function ` + name + `()`,
	} {
		if _, err := h.store.Pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
}

// The task upsert commits, then marking the missing tasks fails: the changed task must still move the token.
func TestInvalidation_CommittedTaskWriteBumpsEvenWhenMarkingMissingFails(t *testing.T) {
	cache := newRecordingCache()
	h := genHarness(t, cache)
	h.runOnce(t)
	idx := coreRunningTasks(h)
	h.estate.Mu.Lock()
	h.estate.Tasks[idx[0]].Task.HealthStatus = ecstypes.HealthStatusUnhealthy             // changes: the upsert writes it
	h.estate.Tasks = append(h.estate.Tasks[:idx[1]:idx[1]], h.estate.Tasks[idx[1]+1:]...) // goes missing: the next step runs
	h.estate.Mu.Unlock()
	h.blockStatement(t, "hm_block_missing", `before update on tasks for each row when (new.stop_code = 'Unknown')`)
	before := cache.snapshot()
	if err := h.loop.syncFullTasks(context.Background()); err == nil {
		t.Fatal("the blocked step did not fail: the scenario does not exercise the path")
	}
	if got := h.count(t, `select count(*) from tasks where health_status = 'UNHEALTHY' and cluster_name = 'core-services'`); got == 0 {
		t.Fatal("the upsert did not commit: the scenario does not exercise the path")
	}
	requireMovedExactly(t, before, cache, "a committed task write followed by a failure", coreAndSlice()...)
}

// The target health upsert commits, then deleting the rows of detached groups fails: the slice must still move.
func TestInvalidation_CommittedTargetHealthWriteBumpsEvenWhenTheDeleteFails(t *testing.T) {
	cache := newRecordingCache()
	h := genHarness(t, cache)
	extra := "arn:aws:elasticloadbalancing:us-east-1:" + sandboxAccount + ":targetgroup/payments-api-extra/0123456789abcdef"
	h.estate.AddTargetGroup(paymentsAPI(), fake.TargetGroup{ARN: extra, Healthy: 1})
	h.runOnce(t)
	h.estate.RemoveTargetGroup(extra)
	if err := h.loop.syncServices(context.Background()); err != nil { // the service detaches the group; its row is now stale
		t.Fatal(err)
	}
	h.estate.Mu.Lock()
	primary := h.estate.TargetGroups[paymentsAPI()]
	primary.Unhealthy += 2 // the upsert changes a row
	h.estate.TargetGroups[paymentsAPI()] = primary
	h.estate.Mu.Unlock()
	h.blockStatement(t, "hm_block_th_delete", `before delete on target_health for each row`)
	before := cache.snapshot()
	var run func(context.Context) error
	for _, d := range h.loop.defs {
		if d.tier.name == tierTargetHealth {
			run = d.run
		}
	}
	if err := run(context.Background()); err == nil {
		t.Fatal("the blocked delete did not fail: the scenario does not exercise the path")
	}
	if got := h.count(t, `select unhealthy from target_health where target_group_arn not like '%extra%' and service_name = 'payments-api'`); got < 2 {
		t.Fatal("the upsert did not commit: the scenario does not exercise the path")
	}
	moved := changedKeys(before, cache.snapshot())
	if !hasKey(moved, sliceGenKey) {
		t.Fatalf("a committed target health write followed by a failure moved %v, want the slice", moved)
	}
}
