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

	"github.com/rcss-labs/harbormaster/internal/awsx"
)

const (
	homelabID = "482109375512"
	stagingID = "731022849906"
)

var fixedNow = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

func newFake() *Fake { return New(MockupEstate(func() time.Time { return fixedNow })) }

func ecsFor(t *testing.T, f *Fake, account, region string) awsx.ECSAPI {
	t.Helper()
	api, err := f.ECS(context.Background(), awsx.Target{AccountID: account, Region: region})
	if err != nil {
		t.Fatalf("ECS(%s,%s): %v", account, region, err)
	}
	return api
}

func TestMockupEstate_Counts(t *testing.T) {
	e := MockupEstate(func() time.Time { return fixedNow })
	if len(e.Accounts) != 2 || len(e.Clusters) != 6 || len(e.Services) != 15 {
		t.Fatalf("accounts/clusters/services = %d/%d/%d, want 2/6/15", len(e.Accounts), len(e.Clusters), len(e.Services))
	}
	var runningService, runningStandalone, stoppedResizer, desired int
	for _, s := range e.Services {
		desired += int(s.Service.DesiredCount)
	}
	for _, task := range e.Tasks {
		group := aws.ToString(task.Task.Group)
		switch {
		case task.Task.LastStatus != nil && *task.Task.LastStatus == "RUNNING" && strings.HasPrefix(group, "service:"):
			runningService++
		case task.Task.LastStatus != nil && *task.Task.LastStatus == "RUNNING":
			runningStandalone++
		case aws.ToString(task.Task.LastStatus) == "STOPPED" && group == "service:image-resizer":
			stoppedResizer++
		}
	}
	if runningService != 21 || runningStandalone != 1 || desired != 23 || stoppedResizer != 6 {
		t.Fatalf("running service/standalone/desired/resizer-stopped = %d/%d/%d/%d, want 21/1/23/6",
			runningService, runningStandalone, desired, stoppedResizer)
	}
	for _, td := range e.TaskDefinitions {
		if aws.ToString(td.Definition.Family) == "image-resizer" {
			if aws.ToString(td.Definition.Cpu) != "256" || aws.ToString(td.Definition.Memory) != "512" {
				t.Fatalf("image-resizer size = %s/%s, want 256/512", aws.ToString(td.Definition.Cpu), aws.ToString(td.Definition.Memory))
			}
		}
	}
}

func TestFakeECS_ListTasksPaginates(t *testing.T) {
	f := newFake()
	api := ecsFor(t, f, homelabID, "us-east-1")
	all, err := api.ListTasks(context.Background(), &ecs.ListTasksInput{Cluster: aws.String("core-services")})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.TaskArns) < 7 {
		t.Fatalf("expected at least 7 running core-services tasks, got %d", len(all.TaskArns))
	}
	seen := map[string]bool{}
	var token *string
	pages := 0
	for {
		out, err := api.ListTasks(context.Background(), &ecs.ListTasksInput{Cluster: aws.String("core-services"), MaxResults: aws.Int32(3), NextToken: token})
		if err != nil {
			t.Fatal(err)
		}
		pages++
		if len(out.TaskArns) > 3 {
			t.Fatalf("page of %d exceeds MaxResults 3", len(out.TaskArns))
		}
		for _, a := range out.TaskArns {
			if seen[a] {
				t.Fatalf("duplicate %s across pages", a)
			}
			seen[a] = true
		}
		if out.NextToken == nil {
			break
		}
		token = out.NextToken
	}
	if len(seen) != len(all.TaskArns) || pages < 3 {
		t.Fatalf("paged union %d (%d pages), want %d", len(seen), pages, len(all.TaskArns))
	}
}

func TestFakeECS_DescribeTasksRejectsOver100(t *testing.T) {
	api := ecsFor(t, newFake(), homelabID, "us-east-1")
	arns := make([]string, 101)
	for i := range arns {
		arns[i] = "arn:aws:ecs:us-east-1:" + homelabID + ":task/core-services/missing"
	}
	_, err := api.DescribeTasks(context.Background(), &ecs.DescribeTasksInput{Cluster: aws.String("core-services"), Tasks: arns})
	if err == nil || !strings.Contains(err.Error(), "at most 100") {
		t.Fatalf("err = %v, want 'at most 100' validation error", err)
	}
	_, err = api.DescribeServices(context.Background(), &ecs.DescribeServicesInput{Cluster: aws.String("core-services"), Services: make([]string, 11)})
	if err == nil || !strings.Contains(err.Error(), "at most 10") {
		t.Fatalf("err = %v, want 'at most 10' validation error", err)
	}
	out, err := api.DescribeTasks(context.Background(), &ecs.DescribeTasksInput{Cluster: aws.String("core-services"), Tasks: arns[:1]})
	if err != nil || len(out.Failures) != 1 || aws.ToString(out.Failures[0].Reason) != "MISSING" {
		t.Fatalf("unknown ARN: out=%+v err=%v, want one MISSING failure", out, err)
	}
}

func TestFake_ThrottleKnobAndCalls(t *testing.T) {
	f := newFake()
	api := ecsFor(t, f, homelabID, "us-east-1")
	f.Throttle = 2
	in := &ecs.DescribeServicesInput{Cluster: aws.String("core-services"), Services: []string{"payments-api"}}
	for i := 0; i < 2; i++ {
		if _, err := api.DescribeServices(context.Background(), in); !awsx.IsThrottle(err) {
			t.Fatalf("call %d: err = %v, want throttle", i, err)
		}
	}
	out, err := api.DescribeServices(context.Background(), in)
	if err != nil || len(out.Services) != 1 {
		t.Fatalf("third call: %v %+v", err, out)
	}
	if got := f.Calls()["DescribeServices"]; got != 3 {
		t.Fatalf("Calls()[DescribeServices] = %d, want 3 (throttled calls count)", got)
	}
}

func TestFakeECS_ScopedToTarget(t *testing.T) {
	f := newFake()
	out, err := ecsFor(t, f, homelabID, "eu-west-1").ListClusters(context.Background(), &ecs.ListClustersInput{})
	if err != nil || len(out.ClusterArns) != 1 || !strings.HasSuffix(out.ClusterArns[0], ":cluster/eu-apps") {
		t.Fatalf("eu-west-1 clusters = %v err=%v, want only eu-apps", out, err)
	}
	staging, err := ecsFor(t, f, stagingID, "us-east-1").ListClusters(context.Background(), &ecs.ListClustersInput{})
	if err != nil || len(staging.ClusterArns) != 1 {
		t.Fatalf("staging clusters = %v err=%v", staging, err)
	}
	empty, err := ecsFor(t, f, stagingID, "eu-west-1").ListClusters(context.Background(), &ecs.ListClustersInput{})
	if err != nil || len(empty.ClusterArns) != 0 {
		t.Fatalf("staging eu-west-1 = %v err=%v, want empty", empty, err)
	}
	if _, err := f.ECS(context.Background(), awsx.Target{AccountID: "000000000000", Region: "us-east-1"}); !errors.Is(err, awsx.ErrAccountUnknown) {
		t.Fatalf("unknown account err = %v", err)
	}
}

func TestFake_CredentialsForKnownAccount(t *testing.T) {
	f := newFake()
	p, err := f.Credentials(context.Background(), awsx.Target{AccountID: homelabID, Region: "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	creds, err := p.Retrieve(context.Background())
	if err != nil || creds.AccessKeyID != "FAKEKEY" || creds.SecretAccessKey != "FAKESECRET" {
		t.Fatalf("creds = %+v err=%v", creds, err)
	}
	if _, err := f.Credentials(context.Background(), awsx.Target{AccountID: "000000000000"}); !errors.Is(err, awsx.ErrAccountUnknown) {
		t.Fatalf("unknown account err = %v", err)
	}
}

func TestFakeECS_NotificationsMidRolloutAndHelpers(t *testing.T) {
	f := newFake()
	api := ecsFor(t, f, homelabID, "us-east-1")
	out, err := api.DescribeServices(context.Background(), &ecs.DescribeServicesInput{Cluster: aws.String("core-services"), Services: []string{"notifications"}})
	if err != nil || len(out.Services) != 1 {
		t.Fatalf("describe: %v", err)
	}
	svc := out.Services[0]
	var primary, active *ecstypes.Deployment
	for i := range svc.Deployments {
		switch aws.ToString(svc.Deployments[i].Status) {
		case "PRIMARY":
			primary = &svc.Deployments[i]
		case "ACTIVE":
			active = &svc.Deployments[i]
		}
	}
	if primary == nil || active == nil || primary.RolloutState != ecstypes.DeploymentRolloutStateInProgress ||
		primary.RunningCount != 1 || active.RunningCount != 1 || svc.RunningCount != 2 ||
		!strings.HasSuffix(aws.ToString(primary.TaskDefinition), ":10") || !strings.HasSuffix(aws.ToString(active.TaskDefinition), ":9") {
		t.Fatalf("unexpected rollout shape: %+v", svc.Deployments)
	}
	if len(svc.Events) == 0 {
		t.Fatal("expected service events")
	}

	tasks, _ := api.ListTasks(context.Background(), &ecs.ListTasksInput{Cluster: aws.String("core-services"), ServiceName: aws.String("notifications")})
	if len(tasks.TaskArns) != 2 {
		t.Fatalf("notifications running tasks = %d, want 2", len(tasks.TaskArns))
	}
	f.Estate.StopTask(tasks.TaskArns[0], "Essential container in task exited", 1)
	after, _ := api.ListTasks(context.Background(), &ecs.ListTasksInput{Cluster: aws.String("core-services"), ServiceName: aws.String("notifications")})
	if len(after.TaskArns) != 1 {
		t.Fatalf("after StopTask running = %d, want 1", len(after.TaskArns))
	}
	f.Estate.AddRunningTasks("core-services", "web-frontend", 250)
	big, _ := api.ListTasks(context.Background(), &ecs.ListTasksInput{Cluster: aws.String("core-services"), ServiceName: aws.String("web-frontend")})
	if big.NextToken == nil || len(big.TaskArns) != 100 {
		t.Fatalf("expected a full first page of 100 with a next token, got %d", len(big.TaskArns))
	}
}

func TestFake_ProbeAuthorisesKnownAccounts(t *testing.T) {
	f := newFake()
	ok := f.Probe(context.Background(), "arn:aws:iam::"+homelabID+":role/harbormaster-access", "ext", []string{"us-east-1", "eu-west-1"})
	if len(ok) != 2 || !ok[0].OK || ok[0].Clusters != 3 || !ok[1].OK || ok[1].Clusters != 1 {
		t.Fatalf("probe = %+v, want us-east-1:3 eu-west-1:1", ok)
	}
	for _, role := range []string{"arn:aws:iam::999999999999:role/x", "arn:aws:iam::" + homelabID + ":role/x"} {
		extID := "ext"
		if strings.Contains(role, homelabID) {
			extID = ""
		}
		bad := f.Probe(context.Background(), role, extID, []string{"us-east-1"})
		if len(bad) != 1 || bad[0].OK || !strings.Contains(bad[0].Error, "AccessDenied: not authorized to perform sts:AssumeRole") {
			t.Fatalf("probe(%s) = %+v, want AccessDenied", role, bad)
		}
	}
}
