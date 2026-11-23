package poller

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/snapcache"
)

// Each test here changes one kind of data and asserts that the cluster's token and the slice's token moved, and
// nothing else. A bump deleted from the poller makes exactly one of them fail, because every other path that could
// move the token is held still (the scenario changes one thing, and the tiers that would notice something else are
// not run).

func requireMovedExactly(t *testing.T, before map[snapcache.GenKey]string, cache *recordingCache, what string, want ...snapcache.GenKey) {
	t.Helper()
	moved := changedKeys(before, cache.snapshot())
	if len(moved) != len(want) {
		t.Fatalf("%s moved %v, want %v", what, moved, want)
	}
	for _, k := range want {
		if !hasKey(moved, k) {
			t.Fatalf("%s moved %v, want %v", what, moved, want)
		}
	}
}

func coreAndSlice() []snapcache.GenKey {
	return []snapcache.GenKey{genKeyOf("core-services"), sliceGenKey}
}

func coreRunningTasks(h *loopHarness) []int {
	h.estate.Mu.Lock()
	defer h.estate.Mu.Unlock()
	var idx []int
	for i, task := range h.estate.Tasks {
		if task.ClusterName == "core-services" && aws.ToString(task.Task.LastStatus) == "RUNNING" {
			idx = append(idx, i)
		}
	}
	return idx
}

func TestInvalidation_ChangedTaskBumpsTheCluster(t *testing.T) {
	cache := newRecordingCache()
	h := genHarness(t, cache)
	h.runOnce(t)
	idx := coreRunningTasks(h)
	h.estate.Mu.Lock()
	h.estate.Tasks[idx[0]].Task.HealthStatus = ecstypes.HealthStatusUnhealthy
	h.estate.Mu.Unlock()
	before := cache.snapshot()
	if err := h.loop.syncFullTasks(context.Background()); err != nil {
		t.Fatal(err)
	}
	requireMovedExactly(t, before, cache, "a task's health change", coreAndSlice()...)
}

func TestInvalidation_TaskMarkedMissingBumpsTheCluster(t *testing.T) {
	cache := newRecordingCache()
	h := genHarness(t, cache)
	h.runOnce(t)
	idx := coreRunningTasks(h)
	h.estate.Mu.Lock()
	h.estate.Tasks = append(h.estate.Tasks[:idx[0]:idx[0]], h.estate.Tasks[idx[0]+1:]...) // ECS no longer knows it
	h.estate.Mu.Unlock()
	before := cache.snapshot()
	if err := h.loop.syncRunningTasks(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := h.count(t, `select count(*) from tasks where cluster_name = 'core-services' and stop_code = 'Unknown'`); got != 1 {
		t.Fatalf("%d tasks were marked missing, want 1: the scenario does not exercise the step", got)
	}
	requireMovedExactly(t, before, cache, "a task marked missing", coreAndSlice()...)
}

func TestInvalidation_DeletedServiceBumpsTheCluster(t *testing.T) {
	cache := newRecordingCache()
	h := genHarness(t, cache)
	h.runOnce(t)
	h.estate.Mu.Lock()
	kept := h.estate.Services[:0:0]
	for _, svc := range h.estate.Services {
		if !(svc.ClusterName == "core-services" && aws.ToString(svc.Service.ServiceName) == "notifications") {
			kept = append(kept, svc)
		}
	}
	h.estate.Services = kept
	h.estate.Mu.Unlock()
	before := cache.snapshot()
	if err := h.loop.syncServiceList(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := h.count(t, `select count(*) from services where service_name = 'notifications'`); got != 0 {
		t.Fatal("the service was not deleted: the scenario does not exercise the step")
	}
	requireMovedExactly(t, before, cache, "a deleted service", coreAndSlice()...)
}

func TestInvalidation_NewServiceEventBumpsTheCluster(t *testing.T) {
	cache := newRecordingCache()
	h := genHarness(t, cache)
	h.runOnce(t)
	h.estate.Mu.Lock()
	for _, svc := range h.estate.Services {
		if svc.ClusterName == "core-services" && aws.ToString(svc.Service.ServiceName) == "payments-api" {
			arn := aws.ToString(svc.Service.ServiceArn)
			h.estate.Events[arn] = append([]ecstypes.ServiceEvent{{Id: aws.String("new-event-1"), CreatedAt: aws.Time(loopNow), Message: aws.String("a new event")}}, h.estate.Events[arn]...)
		}
	}
	h.estate.Mu.Unlock()
	before := cache.snapshot()
	if err := h.loop.syncServices(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := h.count(t, `select count(*) from service_events where event_id = 'new-event-1'`); got != 1 {
		t.Fatal("the event was not stored: the scenario does not exercise the step")
	}
	requireMovedExactly(t, before, cache, "a new service event", coreAndSlice()...)
}

// A focus cycle that finds only a changed target group health still tells the cache: the service's unhealthy flag is in
// the models.
func TestInvalidation_FocusTargetHealthChangeBumpsTheCluster(t *testing.T) {
	cache := newRecordingCache()
	h := genHarness(t, cache)
	extra := "arn:aws:elasticloadbalancing:us-east-1:" + sandboxAccount + ":targetgroup/payments-api-extra/0123456789abcdef"
	h.estate.AddTargetGroup(paymentsAPI(), fake.TargetGroup{ARN: extra, Healthy: 1})
	h.runOnce(t)
	if _, err := h.loop.focusService(context.Background(), paymentsAPI(), 1); err != nil { // settle everything else
		t.Fatal(err)
	}
	h.estate.Mu.Lock()
	groups := h.estate.ExtraTargetGroups[paymentsAPI()]
	groups[0].Unhealthy = 2
	h.estate.Mu.Unlock()
	before := cache.snapshot()
	res, err := h.loop.focusService(context.Background(), paymentsAPI(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !res.changed {
		t.Fatal("the cycle saw no change: the scenario does not exercise the step")
	}
	requireMovedExactly(t, before, cache, "a focus target health change", coreAndSlice()...)
}

// The three things that make a stored target group row differ from what AWS reports, one case each, and an order that
// does not.
func TestTargetHealthDiffers_EachDifference(t *testing.T) {
	row := func() model.TargetHealth {
		return model.TargetHealth{TargetGroupARN: "tg", LoadBalancerARN: "lb", Healthy: 2, Unhealthy: 1,
			Targets: []model.Target{{ID: "a", Port: 80, State: "healthy"}, {ID: "b", Port: 80, State: "unhealthy"}}}
	}
	same := func(fresh ...model.TargetHealth) bool { return targetHealthDiffers([]model.TargetHealth{row()}, fresh) }
	if same(row()) {
		t.Fatal("identical rows differ")
	}
	cases := map[string]func(*model.TargetHealth){
		"healthy count":   func(r *model.TargetHealth) { r.Healthy = 3 },
		"unhealthy count": func(r *model.TargetHealth) { r.Unhealthy = 2 },
		"load balancer":   func(r *model.TargetHealth) { r.LoadBalancerARN = "lb2" },
		"target state":    func(r *model.TargetHealth) { r.Targets[1].State = "healthy" },
		"target removed":  func(r *model.TargetHealth) { r.Targets = r.Targets[:1] },
	}
	for name, change := range cases {
		fresh := row()
		change(&fresh)
		if !same(fresh) {
			t.Errorf("a changed %s was not seen as a difference", name)
		}
	}
	if !targetHealthDiffers(nil, []model.TargetHealth{row()}) {
		t.Error("a group with no stored row was not seen as a difference")
	}
	// A new group whose first reading is all zeros differs from the stored rows only by being new.
	if !targetHealthDiffers([]model.TargetHealth{row()}, []model.TargetHealth{row(), {TargetGroupARN: "new-empty-group"}}) {
		t.Error("a new, empty group was not seen as a difference")
	}
	reordered := row()
	reordered.Targets[0], reordered.Targets[1] = reordered.Targets[1], reordered.Targets[0]
	if same(reordered) {
		t.Error("the same targets in another order were seen as a difference: AWS does not promise an order, and every cycle would invalidate the slice")
	}
}
