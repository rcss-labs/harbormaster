package fake

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

const (
	paymentsRev43 = "arn:aws:ecs:us-east-1:482109375512:task-definition/payments-api:43"
	resizerRev6   = "arn:aws:ecs:us-east-1:482109375512:task-definition/image-resizer:6"
)

func advance(f *Fake, n int) {
	for i := 0; i < n; i++ {
		f.Estate.Advance()
	}
}

// describe returns the service as the API reports it (with events).
func describe(t *testing.T, f *Fake, cluster, name string) ecstypes.Service {
	t.Helper()
	out, err := ecsFor(t, f, homelabID, "us-east-1").DescribeServices(context.Background(),
		&ecs.DescribeServicesInput{Cluster: aws.String(cluster), Services: []string{name}})
	if err != nil || len(out.Services) != 1 {
		t.Fatalf("DescribeServices %s: %v %+v", name, err, out)
	}
	return out.Services[0]
}

func primaryOf(s ecstypes.Service) ecstypes.Deployment {
	for _, d := range s.Deployments {
		if aws.ToString(d.Status) == "PRIMARY" {
			return d
		}
	}
	return ecstypes.Deployment{}
}

// tasksOf returns the service's tasks with the given last status.
func tasksOf(f *Fake, service, status string) []ecstypes.Task {
	f.Estate.Mu.Lock()
	defer f.Estate.Mu.Unlock()
	var out []ecstypes.Task
	for _, t := range f.Estate.Tasks {
		if aws.ToString(t.Task.Group) == "service:"+service && aws.ToString(t.Task.LastStatus) == status {
			out = append(out, t.Task)
		}
	}
	return out
}

func registerRevision(t *testing.T, f *Fake, account, region, family string, memory string) string {
	t.Helper()
	out, err := ecsFor(t, f, account, region).RegisterTaskDefinition(context.Background(), &ecs.RegisterTaskDefinitionInput{
		Family: aws.String(family), Cpu: aws.String("256"), Memory: aws.String(memory),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{Name: aws.String(family), Image: aws.String(family + ":x")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return aws.ToString(out.TaskDefinition.TaskDefinitionArn)
}

func TestRollout_DeployCompletesAfterDesiredSteps(t *testing.T) {
	f := newFake()
	api := ecsFor(t, f, homelabID, "us-east-1")
	if got := registerRevision(t, f, homelabID, "us-east-1", "payments-api", "1024"); got != paymentsRev43 {
		t.Fatalf("registered %s, want %s", got, paymentsRev43)
	}
	out, err := api.UpdateService(context.Background(), &ecs.UpdateServiceInput{
		Cluster: aws.String("core-services"), Service: aws.String("payments-api"), TaskDefinition: aws.String(paymentsRev43)})
	if err != nil {
		t.Fatal(err)
	}
	p := primaryOf(*out.Service)
	if p.RolloutState != ecstypes.DeploymentRolloutStateInProgress || !strings.HasPrefix(aws.ToString(p.Id), "ecs-svc/") ||
		len(strings.TrimPrefix(aws.ToString(p.Id), "ecs-svc/")) != 19 || p.DesiredCount != 3 || p.RunningCount != 0 {
		t.Fatalf("new PRIMARY = %+v", p)
	}
	if aws.ToString(p.RolloutStateReason) != "ECS deployment "+aws.ToString(p.Id)+" in progress." {
		t.Fatalf("reason = %q", aws.ToString(p.RolloutStateReason))
	}
	if len(out.Service.Deployments) != 2 {
		t.Fatalf("deployments = %d, want PRIMARY + ACTIVE", len(out.Service.Deployments))
	}

	advance(f, 3)
	svc := describe(t, f, "core-services", "payments-api")
	if len(svc.Deployments) != 1 || primaryOf(svc).RolloutState != ecstypes.DeploymentRolloutStateCompleted {
		t.Fatalf("deployments after 3 steps = %+v", svc.Deployments)
	}
	if aws.ToString(svc.TaskDefinition) != paymentsRev43 || svc.RunningCount != 3 {
		t.Fatalf("taskDefinition/running = %s/%d", aws.ToString(svc.TaskDefinition), svc.RunningCount)
	}
	for _, task := range tasksOf(f, "payments-api", "RUNNING") {
		if aws.ToString(task.TaskDefinitionArn) != paymentsRev43 {
			t.Fatalf("running task on %s, want :43", aws.ToString(task.TaskDefinitionArn))
		}
	}
	if got := aws.ToString(svc.Events[0].Message); got != "(service payments-api) has reached a steady state." {
		t.Fatalf("latest event = %q", got)
	}
	if aws.ToString(primaryOf(svc).RolloutStateReason) != "ECS deployment "+aws.ToString(p.Id)+" completed." {
		t.Fatalf("completed reason = %q", aws.ToString(primaryOf(svc).RolloutStateReason))
	}
	var started bool
	for _, ev := range svc.Events {
		started = started || strings.HasPrefix(aws.ToString(ev.Message), "(service payments-api) has started deployment "+aws.ToString(p.Id)+" for task definition payments-api:43.")
	}
	if !started {
		t.Fatal("no 'has started deployment' event")
	}
}

func TestRollout_ForceNewDeploymentKeepsRevision(t *testing.T) {
	f := newFake()
	api := ecsFor(t, f, homelabID, "us-east-1")
	before := describe(t, f, "core-services", "web-frontend")
	if _, err := api.UpdateService(context.Background(), &ecs.UpdateServiceInput{
		Cluster: aws.String("core-services"), Service: aws.String("web-frontend"), ForceNewDeployment: true}); err != nil {
		t.Fatal(err)
	}
	mid := describe(t, f, "core-services", "web-frontend")
	if aws.ToString(primaryOf(mid).Id) == aws.ToString(primaryOf(before).Id) {
		t.Fatal("restart did not create a new deployment id")
	}
	advance(f, 2)
	after := describe(t, f, "core-services", "web-frontend")
	if aws.ToString(after.TaskDefinition) != aws.ToString(before.TaskDefinition) || len(after.Deployments) != 1 ||
		primaryOf(after).RolloutState != ecstypes.DeploymentRolloutStateCompleted {
		t.Fatalf("after restart: %+v", after.Deployments)
	}
	for _, task := range tasksOf(f, "web-frontend", "RUNNING") {
		if aws.ToString(task.StartedBy) != aws.ToString(primaryOf(mid).Id) {
			t.Fatalf("task started by %s, want the new deployment", aws.ToString(task.StartedBy))
		}
	}
}

func TestRollout_OOMFailsAndCircuitBreakerRollsBack(t *testing.T) {
	f := newFake()
	lowMemory := registerRevision(t, f, homelabID, "us-east-1", "image-resizer", "512")
	api := ecsFor(t, f, homelabID, "us-east-1")
	if !strings.HasSuffix(lowMemory, ":7") {
		t.Fatalf("registered %s, want revision 7", lowMemory)
	}
	if _, err := api.UpdateService(context.Background(), &ecs.UpdateServiceInput{
		Cluster: aws.String("background-jobs"), Service: aws.String("image-resizer"), TaskDefinition: aws.String(lowMemory)}); err != nil {
		t.Fatal(err)
	}
	stoppedBefore := len(tasksOf(f, "image-resizer", "STOPPED"))
	advance(f, 3)

	svc := describe(t, f, "background-jobs", "image-resizer")
	var failed *ecstypes.Deployment
	for i, d := range svc.Deployments {
		if d.RolloutState == ecstypes.DeploymentRolloutStateFailed {
			failed = &svc.Deployments[i]
		}
	}
	if failed == nil || aws.ToString(failed.RolloutStateReason) != "ECS deployment circuit breaker: tasks failed to start." ||
		aws.ToString(failed.Status) != "ACTIVE" || failed.FailedTasks != 3 {
		t.Fatalf("deployments = %+v", svc.Deployments)
	}
	if aws.ToString(svc.TaskDefinition) != resizerRev6 {
		t.Fatalf("service taskDefinition = %s, want :6", aws.ToString(svc.TaskDefinition))
	}
	p := primaryOf(svc)
	if aws.ToString(p.TaskDefinition) != resizerRev6 || p.RolloutState != ecstypes.DeploymentRolloutStateInProgress || p.FailedTasks != 0 {
		t.Fatalf("rollback PRIMARY = %+v", p)
	}
	stopped := tasksOf(f, "image-resizer", "STOPPED")
	if len(stopped)-stoppedBefore != 3 {
		t.Fatalf("new stopped tasks = %d, want 3", len(stopped)-stoppedBefore)
	}
	for _, task := range stopped[stoppedBefore:] {
		if aws.ToString(task.TaskDefinitionArn) != lowMemory || task.StopCode != ecstypes.TaskStopCodeTaskFailedToStart ||
			aws.ToString(task.StoppedReason) != "OutOfMemoryError: Container killed due to memory usage" ||
			aws.ToInt32(task.Containers[0].ExitCode) != 137 {
			t.Fatalf("failed task = %+v", task)
		}
	}
	wantEvent := "(service image-resizer) deployment failed: tasks failed to start. Circuit breaker rolled back to image-resizer:6."
	found := false
	for _, ev := range svc.Events {
		found = found || aws.ToString(ev.Message) == wantEvent
	}
	if !found {
		t.Fatalf("missing event %q", wantEvent)
	}

	// The rollback deployment is exempt from the OOM path and completes normally,
	// dropping the FAILED ACTIVE deployment with it.
	advance(f, 2)
	svc = describe(t, f, "background-jobs", "image-resizer")
	if primaryOf(svc).RolloutState != ecstypes.DeploymentRolloutStateCompleted || svc.RunningCount != 2 {
		t.Fatalf("after rollback: running=%d deployments=%+v", svc.RunningCount, svc.Deployments)
	}

	// The FAILED deployment stays observable for 5 more steps (longer than a
	// poll interval), then is dropped.
	failedCount := func() int {
		n := 0
		for _, d := range describe(t, f, "background-jobs", "image-resizer").Deployments {
			if d.RolloutState == ecstypes.DeploymentRolloutStateFailed {
				n++
			}
		}
		return n
	}
	if failedCount() != 1 {
		t.Fatal("FAILED deployment must survive the rollback completing")
	}
	for step := 1; step <= 5; step++ {
		advance(f, 1)
		if failedCount() != 1 {
			t.Fatalf("FAILED deployment gone after %d steps, want it kept for 5", step)
		}
	}
	advance(f, 1)
	if failedCount() != 0 || len(describe(t, f, "background-jobs", "image-resizer").Deployments) != 1 {
		t.Fatalf("FAILED deployment still present after 6 steps: %+v", describe(t, f, "background-jobs", "image-resizer").Deployments)
	}
}

func TestRollout_LoweringDesiredMidRolloutStillCompletes(t *testing.T) {
	f := newFake()
	api := ecsFor(t, f, homelabID, "us-east-1")
	ctx := context.Background()
	registerRevision(t, f, homelabID, "us-east-1", "payments-api", "1024")
	if _, err := api.UpdateService(ctx, &ecs.UpdateServiceInput{
		Cluster: aws.String("core-services"), Service: aws.String("payments-api"), TaskDefinition: aws.String(paymentsRev43)}); err != nil {
		t.Fatal(err)
	}
	advance(f, 2)
	if _, err := api.UpdateService(ctx, &ecs.UpdateServiceInput{
		Cluster: aws.String("core-services"), Service: aws.String("payments-api"), DesiredCount: aws.Int32(1)}); err != nil {
		t.Fatal(err)
	}
	advance(f, 4)
	svc := describe(t, f, "core-services", "payments-api")
	if primaryOf(svc).RolloutState != ecstypes.DeploymentRolloutStateCompleted || svc.RunningCount != 1 || len(svc.Deployments) != 1 {
		t.Fatalf("running=%d deployments=%+v", svc.RunningCount, svc.Deployments)
	}
}

func TestRollout_DeploymentIDsAreUniqueWithinOneInstant(t *testing.T) {
	f := newFake()
	api := ecsFor(t, f, homelabID, "us-east-1")
	restart := func() string {
		out, err := api.UpdateService(context.Background(), &ecs.UpdateServiceInput{
			Cluster: aws.String("core-services"), Service: aws.String("web-frontend"), ForceNewDeployment: true})
		if err != nil {
			t.Fatal(err)
		}
		return aws.ToString(primaryOf(*out.Service).Id)
	}
	if first, second := restart(), restart(); first == second {
		t.Fatalf("two restarts at the same instant share deployment id %s", first)
	}
}

func TestRollout_DesiredCountReconciles(t *testing.T) {
	f := newFake()
	api := ecsFor(t, f, homelabID, "us-east-1")
	update := func(n int32) {
		t.Helper()
		out, err := api.UpdateService(context.Background(), &ecs.UpdateServiceInput{
			Cluster: aws.String("core-services"), Service: aws.String("payments-api"), DesiredCount: aws.Int32(n)})
		if err != nil {
			t.Fatal(err)
		}
		if len(out.Service.Deployments) != 1 || primaryOf(*out.Service).DesiredCount != n {
			t.Fatalf("desired-only update must not start a deployment: %+v", out.Service.Deployments)
		}
	}
	update(5)
	advance(f, 1)
	if got := len(tasksOf(f, "payments-api", "RUNNING")); got != 4 {
		t.Fatalf("running after one step = %d, want 4", got)
	}
	advance(f, 1)
	svc := describe(t, f, "core-services", "payments-api")
	if svc.RunningCount != 5 || aws.ToString(svc.Events[0].Message) != "(service payments-api) has reached a steady state." {
		t.Fatalf("running=%d latest=%q", svc.RunningCount, aws.ToString(svc.Events[0].Message))
	}

	update(0)
	advance(f, 1)
	if got := len(tasksOf(f, "payments-api", "RUNNING")); got != 4 {
		t.Fatalf("running after first scale-in step = %d, want 4", got)
	}
	advance(f, 4)
	scaledIn := 0
	for _, task := range tasksOf(f, "payments-api", "STOPPED") {
		if aws.ToString(task.StoppedReason) == "Scaling activity initiated by (desired count change)" {
			scaledIn++
		}
	}
	if running := len(tasksOf(f, "payments-api", "RUNNING")); running != 0 || scaledIn != 5 {
		t.Fatalf("running=%d stopped by desired count change=%d, want 0 and 5", running, scaledIn)
	}
}

func TestRollout_DesiredZeroCompletesImmediately(t *testing.T) {
	f := newFake()
	api := ecsFor(t, f, homelabID, "us-east-1")
	ctx := context.Background()
	in := func() *ecs.UpdateServiceInput {
		return &ecs.UpdateServiceInput{Cluster: aws.String("core-services"), Service: aws.String("auth-service")}
	}
	zero := in()
	zero.DesiredCount = aws.Int32(0)
	if _, err := api.UpdateService(ctx, zero); err != nil {
		t.Fatal(err)
	}
	advance(f, 2)
	restart := in()
	restart.ForceNewDeployment = true
	if _, err := api.UpdateService(ctx, restart); err != nil {
		t.Fatal(err)
	}
	if primaryOf(describe(t, f, "core-services", "auth-service")).RolloutState != ecstypes.DeploymentRolloutStateInProgress {
		t.Fatal("restart should start IN_PROGRESS")
	}
	advance(f, 1)
	svc := describe(t, f, "core-services", "auth-service")
	if primaryOf(svc).RolloutState != ecstypes.DeploymentRolloutStateCompleted || len(svc.Deployments) != 1 {
		t.Fatalf("deployments = %+v", svc.Deployments)
	}
}

func TestRollout_NotificationsFinishesSeededRollout(t *testing.T) {
	f := newFake()
	advance(f, 1)
	svc := describe(t, f, "core-services", "notifications")
	if primaryOf(svc).RolloutState != ecstypes.DeploymentRolloutStateCompleted || len(svc.Deployments) != 1 || svc.RunningCount != 2 {
		t.Fatalf("running=%d deployments=%+v", svc.RunningCount, svc.Deployments)
	}
}

func TestRollout_UpdateServiceRejectsUnknownService(t *testing.T) {
	api := ecsFor(t, newFake(), homelabID, "us-east-1")
	_, err := api.UpdateService(context.Background(), &ecs.UpdateServiceInput{
		Cluster: aws.String("core-services"), Service: aws.String("nope"), ForceNewDeployment: true})
	var notFound *ecstypes.ServiceNotFoundException
	if !errors.As(err, &notFound) {
		t.Fatalf("err = %v, want ServiceNotFoundException", err)
	}
}

func TestRunRollouts_TicksUntilCancelled(t *testing.T) {
	f := newFake()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.Estate.RunRollouts(ctx, 5*time.Millisecond); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for primaryOf(describe(t, f, "core-services", "notifications")).RolloutState != ecstypes.DeploymentRolloutStateCompleted {
		if time.Now().After(deadline) {
			t.Fatal("notifications rollout never completed under the ticker")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunRollouts did not return after cancel")
	}
}
