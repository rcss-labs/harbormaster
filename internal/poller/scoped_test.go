package poller

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/model"
)

// recordingECS notes which cluster and service each read named, so a test can show a scoped run stayed inside its scope.
type recordingECS struct {
	awsx.ECSAPI
	mu sync.Mutex

	describeServices []describeServicesCall
	listTasks        []listTasksCall
	describeTasks    []string // cluster of each call
	describedTasks   []string // every task ARN asked for
	describeClusters [][]string
	listServices     []string // cluster of each call
}

type describeServicesCall struct {
	cluster  string
	services []string
}

type listTasksCall struct {
	cluster, service string
	desired          ecstypes.DesiredStatus
}

func (r *recordingECS) DescribeServices(ctx context.Context, in *ecs.DescribeServicesInput, opts ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error) {
	r.mu.Lock()
	r.describeServices = append(r.describeServices, describeServicesCall{aws.ToString(in.Cluster), slices.Clone(in.Services)})
	r.mu.Unlock()
	return r.ECSAPI.DescribeServices(ctx, in, opts...)
}

func (r *recordingECS) ListTasks(ctx context.Context, in *ecs.ListTasksInput, opts ...func(*ecs.Options)) (*ecs.ListTasksOutput, error) {
	r.mu.Lock()
	r.listTasks = append(r.listTasks, listTasksCall{aws.ToString(in.Cluster), aws.ToString(in.ServiceName), in.DesiredStatus})
	r.mu.Unlock()
	return r.ECSAPI.ListTasks(ctx, in, opts...)
}

func (r *recordingECS) DescribeTasks(ctx context.Context, in *ecs.DescribeTasksInput, opts ...func(*ecs.Options)) (*ecs.DescribeTasksOutput, error) {
	r.mu.Lock()
	r.describeTasks = append(r.describeTasks, aws.ToString(in.Cluster))
	r.describedTasks = append(r.describedTasks, in.Tasks...)
	r.mu.Unlock()
	return r.ECSAPI.DescribeTasks(ctx, in, opts...)
}

func (r *recordingECS) DescribeClusters(ctx context.Context, in *ecs.DescribeClustersInput, opts ...func(*ecs.Options)) (*ecs.DescribeClustersOutput, error) {
	r.mu.Lock()
	r.describeClusters = append(r.describeClusters, slices.Clone(in.Clusters))
	r.mu.Unlock()
	return r.ECSAPI.DescribeClusters(ctx, in, opts...)
}

func (r *recordingECS) ListServices(ctx context.Context, in *ecs.ListServicesInput, opts ...func(*ecs.Options)) (*ecs.ListServicesOutput, error) {
	r.mu.Lock()
	r.listServices = append(r.listServices, aws.ToString(in.Cluster))
	r.mu.Unlock()
	return r.ECSAPI.ListServices(ctx, in, opts...)
}

// recording wraps the harness's fake ECS client for the loop's target.
func (h *loopHarness) recording(t *testing.T) *recordingECS {
	t.Helper()
	api, err := h.fake.ECS(context.Background(), loopTarget)
	if err != nil {
		t.Fatal(err)
	}
	return &recordingECS{ECSAPI: api}
}

func coreServices() model.ClusterKey {
	return model.ClusterKey{AccountID: sandboxAccount, Region: "us-east-1", Cluster: "core-services"}
}

func TestSyncServicesIn_TouchesOnlyTheNamedCluster(t *testing.T) {
	h := mockupHarness(t)
	h.runOnce(t)
	rec := h.recording(t)

	if _, err := h.loop.syncServicesIn(context.Background(), rec, coreServices(), nil); err != nil {
		t.Fatal(err)
	}
	if len(rec.describeServices) == 0 {
		t.Fatal("no DescribeServices call")
	}
	for _, c := range rec.describeServices {
		if c.cluster != "core-services" {
			t.Errorf("DescribeServices for cluster %q, want only core-services", c.cluster)
		}
	}
}

func TestSyncServicesIn_NamedServiceOnlyAndKeepsTheRestOfTheSnapshot(t *testing.T) {
	h := mockupHarness(t)
	h.runOnce(t)
	rec := h.recording(t)

	if _, err := h.loop.syncServicesIn(context.Background(), rec, coreServices(), []string{"payments-api"}); err != nil {
		t.Fatal(err)
	}
	if len(rec.describeServices) != 1 || !slices.Equal(rec.describeServices[0].services, []string{"payments-api"}) {
		t.Fatalf("DescribeServices calls = %+v, want one for payments-api", rec.describeServices)
	}
	h.loop.mu.Lock()
	defer h.loop.mu.Unlock()
	var inCluster int
	for key := range h.loop.latest {
		if key.Cluster == "core-services" {
			inCluster++
		}
	}
	if inCluster < 2 {
		t.Fatalf("latest snapshot holds %d services of core-services after a one-service run, want the others kept", inCluster)
	}
}

func TestSyncServicesIn_ReportsChangeOnlyWhenRowsChanged(t *testing.T) {
	h := mockupHarness(t)
	ctx := context.Background()
	api, err := h.loop.api(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.loop.syncClusters(ctx); err != nil {
		t.Fatal(err)
	}
	if err := h.loop.listServicesIn(ctx, api, coreServices()); err != nil {
		t.Fatal(err)
	}

	changed, err := h.loop.syncServicesIn(ctx, api, coreServices(), nil)
	if err != nil || !changed {
		t.Fatalf("first run on an empty store = changed %v, err %v; want changed", changed, err)
	}
	changed, err = h.loop.syncServicesIn(ctx, api, coreServices(), nil)
	if err != nil || changed {
		t.Fatalf("second run with nothing new = changed %v, err %v; want unchanged", changed, err)
	}

	h.estate.Mu.Lock()
	for i := range h.estate.Services {
		if aws.ToString(h.estate.Services[i].Service.ServiceName) == "payments-api" {
			h.estate.Services[i].Service.DesiredCount = 9
		}
	}
	h.estate.Mu.Unlock()
	changed, err = h.loop.syncServicesIn(ctx, api, coreServices(), []string{"payments-api"})
	if err != nil || !changed {
		t.Fatalf("run after a scale = changed %v, err %v; want changed", changed, err)
	}
}

func TestDescribeClustersNamed_DescribesOnlyTheNamedClustersAndDeletesNothing(t *testing.T) {
	h := mockupHarness(t)
	h.runOnce(t)
	rec := h.recording(t)
	before := h.count(t, `select count(*) from clusters`)

	if _, err := h.loop.describeClustersNamed(context.Background(), rec, []string{"core-services"}); err != nil {
		t.Fatal(err)
	}
	if len(rec.describeClusters) != 1 || !slices.Equal(rec.describeClusters[0], []string{"core-services"}) {
		t.Fatalf("DescribeClusters calls = %v, want one for core-services", rec.describeClusters)
	}
	if got := h.count(t, `select count(*) from clusters`); got != before {
		t.Fatalf("clusters stored = %d, want %d: a scoped describe must not delete others", got, before)
	}
}

func TestListServicesIn_ListsOnlyTheCluster(t *testing.T) {
	h := mockupHarness(t)
	h.runOnce(t)
	rec := h.recording(t)

	if err := h.loop.listServicesIn(context.Background(), rec, coreServices()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rec.listServices, []string{"core-services"}) {
		t.Fatalf("ListServices clusters = %v, want [core-services]", rec.listServices)
	}
}

// stateOf returns the task's stored last_status.
func (h *loopHarness) stateOf(t *testing.T, arn string) string {
	t.Helper()
	var status string
	if err := h.store.Pool.QueryRow(context.Background(), `select last_status from tasks where task_arn = $1`, arn).Scan(&status); err != nil {
		t.Fatalf("task %s: %v", arn, err)
	}
	return status
}

func (h *loopHarness) runningARNOf(service string) string {
	for _, task := range h.estateTasks("RUNNING") {
		if aws.ToString(task.Task.Group) == "service:"+service {
			return aws.ToString(task.Task.TaskArn)
		}
	}
	return ""
}

func TestSyncRunningTasksIn_ServiceScopeListsOnlyThatService(t *testing.T) {
	h := mockupHarness(t)
	h.runOnce(t)
	h.estate.AddRunningTasks("core-services", "payments-api", 2)
	h.estate.AddRunningTasks("core-services", "web-frontend", 2)
	paymentsBefore := h.count(t, `select count(*) from tasks where service_name = 'payments-api'`)
	webBefore := h.count(t, `select count(*) from tasks where service_name = 'web-frontend'`)
	rec := h.recording(t)

	changed, err := h.loop.syncRunningTasksIn(context.Background(), rec, coreServices(), "payments-api", false)
	if err != nil || !changed {
		t.Fatalf("scoped run = changed %v, err %v; want changed", changed, err)
	}
	if len(rec.listTasks) == 0 {
		t.Fatal("no ListTasks call")
	}
	for _, c := range rec.listTasks {
		if c.service != "payments-api" || c.desired != ecstypes.DesiredStatusRunning {
			t.Errorf("ListTasks %+v, want RUNNING for payments-api only", c)
		}
	}
	if got := h.count(t, `select count(*) from tasks where service_name = 'payments-api'`); got != paymentsBefore+2 {
		t.Errorf("payments-api tasks = %d, want %d", got, paymentsBefore+2)
	}
	if got := h.count(t, `select count(*) from tasks where service_name = 'web-frontend'`); got != webBefore {
		t.Errorf("web-frontend tasks = %d, want %d: another service must not be touched", got, webBefore)
	}
}

func TestSyncRunningTasksIn_DescribeAllDescribesEveryListedTask(t *testing.T) {
	h := mockupHarness(t)
	h.runOnce(t)
	rec := h.recording(t)

	if _, err := h.loop.syncRunningTasksIn(context.Background(), rec, coreServices(), "payments-api", false); err != nil {
		t.Fatal(err)
	}
	if len(rec.describedTasks) != 0 {
		t.Fatalf("an unchanged service described %d tasks without describeAll", len(rec.describedTasks))
	}
	if _, err := h.loop.syncRunningTasksIn(context.Background(), rec, coreServices(), "payments-api", true); err != nil {
		t.Fatal(err)
	}
	if len(rec.describedTasks) == 0 {
		t.Fatal("describeAll described nothing")
	}
}

func TestSyncRunningTasksIn_ServiceScopeLeavesOtherDirtyFlags(t *testing.T) {
	h := mockupHarness(t)
	h.runOnce(t)
	h.loop.restoreDirty("core-services", []string{"payments-api", "web-frontend"})
	rec := h.recording(t)

	if _, err := h.loop.syncRunningTasksIn(context.Background(), rec, coreServices(), "payments-api", false); err != nil {
		t.Fatal(err)
	}
	h.loop.mu.Lock()
	got := h.loop.dirty["core-services"]
	h.loop.mu.Unlock()
	if len(got) != 1 || !got["web-frontend"] {
		t.Fatalf("dirty flags after a payments-api run = %v, want only web-frontend left", got)
	}
}

func TestSyncRunningTasksIn_DirtyServiceIsRedescribed(t *testing.T) {
	h := mockupHarness(t)
	h.runOnce(t)
	h.loop.restoreDirty("core-services", []string{"payments-api"})
	rec := h.recording(t)

	if _, err := h.loop.syncRunningTasksIn(context.Background(), rec, coreServices(), "payments-api", false); err != nil {
		t.Fatal(err)
	}
	if len(rec.describedTasks) == 0 {
		t.Fatal("a dirty service's tasks were not described")
	}
}

func TestSyncStoppedTasksIn_ServiceScope(t *testing.T) {
	h := mockupHarness(t)
	h.runOnce(t)
	payments, web := h.runningARNOf("payments-api"), h.runningARNOf("web-frontend")
	h.estate.StopTask(payments, "Essential container exited", 1)
	h.estate.StopTask(web, "Essential container exited", 1)
	rec := h.recording(t)

	changed, err := h.loop.syncStoppedTasksIn(context.Background(), rec, coreServices(), "payments-api")
	if err != nil || !changed {
		t.Fatalf("scoped stopped run = changed %v, err %v; want changed", changed, err)
	}
	for _, c := range rec.listTasks {
		if c.service != "payments-api" || c.desired != ecstypes.DesiredStatusStopped {
			t.Errorf("ListTasks %+v, want STOPPED for payments-api only", c)
		}
	}
	if got := h.stateOf(t, payments); got != "STOPPED" {
		t.Errorf("payments-api task = %s, want STOPPED", got)
	}
	if got := h.stateOf(t, web); got != "RUNNING" {
		t.Errorf("web-frontend task = %s, want it untouched (RUNNING)", got)
	}
}

// recordingELB notes the target groups whose health was read.
type recordingELB struct {
	awsx.ELBAPI
	mu            sync.Mutex
	healthGroups  []string
	describedARNs [][]string
}

func (r *recordingELB) DescribeTargetHealth(ctx context.Context, in *elasticloadbalancingv2.DescribeTargetHealthInput, opts ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeTargetHealthOutput, error) {
	r.mu.Lock()
	r.healthGroups = append(r.healthGroups, aws.ToString(in.TargetGroupArn))
	r.mu.Unlock()
	return r.ELBAPI.DescribeTargetHealth(ctx, in, opts...)
}

func (r *recordingELB) DescribeTargetGroups(ctx context.Context, in *elasticloadbalancingv2.DescribeTargetGroupsInput, opts ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeTargetGroupsOutput, error) {
	r.mu.Lock()
	r.describedARNs = append(r.describedARNs, slices.Clone(in.TargetGroupArns))
	r.mu.Unlock()
	return r.ELBAPI.DescribeTargetGroups(ctx, in, opts...)
}

type recordingClients struct {
	awsx.Clients
	elb *recordingELB
}

func (c recordingClients) ELB(ctx context.Context, t awsx.Target) (awsx.ELBAPI, error) {
	inner, err := c.Clients.ELB(ctx, t)
	if err != nil {
		return nil, err
	}
	c.elb.ELBAPI = inner
	return c.elb, nil
}

func TestTargetHealth_RunServiceDescribesOnlyItsGroups(t *testing.T) {
	target := awsx.Target{AccountID: sandboxAccount, Region: euWestAccountRegion}
	h := newObserveHarness(t, target)
	key := serviceKey(target, "eu-apps", "catalog-api")
	ctx := context.Background()
	loop := h.targetHealthLoop()
	if err := loop.Run(ctx, target); err != nil { // every service's rows, so the scoped run can be shown to keep them
		t.Fatal(err)
	}
	rowsBefore := h.count(t, `select count(*) from target_health`)

	rec := &recordingELB{}
	loop.Clients = recordingClients{Clients: h.fake, elb: rec}
	changed, err := loop.RunService(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("RunService reported a change when the stored health already matched")
	}
	rows, err := h.store.TargetHealthForService(ctx, key)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows for the service = %d, %v; want 1", len(rows), err)
	}
	if want := []string{rows[0].TargetGroupARN}; !slices.Equal(rec.healthGroups, want) {
		t.Fatalf("DescribeTargetHealth groups = %v, want only %v", rec.healthGroups, want)
	}
	if got := h.count(t, `select count(*) from target_health`); got != rowsBefore {
		t.Fatalf("target_health rows = %d, want %d: a scoped run must not delete others", got, rowsBefore)
	}

	h.estate.SetTargetHealth(key, 0, 2)
	changed, err = loop.RunService(ctx, key)
	if err != nil || !changed {
		t.Fatalf("RunService after the health changed = changed %v, err %v; want changed", changed, err)
	}
}

func (h *observeHarness) count(t *testing.T, query string) int {
	t.Helper()
	var n int
	if err := h.store.Pool.QueryRow(context.Background(), query).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
