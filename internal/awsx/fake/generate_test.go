package fake

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"

	"github.com/rcss-labs/harbormaster/internal/awsx"
)

// plan 06

func generatedTargets(e *Estate) []awsx.Target {
	var out []awsx.Target
	for _, a := range e.Accounts {
		for _, r := range a.Regions {
			out = append(out, awsx.Target{AccountID: a.ID, Region: r})
		}
	}
	return out
}

func TestGenerate_TargetScaleCounts(t *testing.T) {
	start := time.Now()
	e := Generate(1, TargetScale)
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("Generate took %v, want a couple of seconds at most", d)
	}
	f := New(e)
	targets := generatedTargets(e)
	if len(targets) != 20 {
		t.Fatalf("targets = %d, want 20", len(targets))
	}
	for _, tg := range targets {
		api, err := f.ECS(context.Background(), tg)
		if err != nil {
			t.Fatal(err)
		}
		out, err := api.ListClusters(context.Background(), &ecs.ListClustersInput{})
		if err != nil {
			t.Fatal(err)
		}
		if len(out.ClusterArns) != 20 {
			t.Fatalf("%v: clusters = %d, want 20", tg, len(out.ClusterArns))
		}
	}
	if len(e.Services) != 6000 {
		t.Errorf("services = %d, want 6000", len(e.Services))
	}
	running := 0
	for _, task := range e.Tasks {
		if aws.ToString(task.Task.LastStatus) == "RUNNING" {
			running++
		}
	}
	if len(e.Tasks) != 18000 || running != 18000 {
		t.Errorf("tasks/running = %d/%d, want 18000/18000", len(e.Tasks), running)
	}
	if e.Accounts[0].ID != "100000000001" {
		t.Errorf("first account id = %s, want 100000000001", e.Accounts[0].ID)
	}
	// Every service must reference a task definition the poller can fetch.
	api, _ := f.ECS(context.Background(), targets[0])
	svc := e.Services[0]
	if _, err := api.DescribeTaskDefinition(context.Background(), &ecs.DescribeTaskDefinitionInput{TaskDefinition: svc.Service.TaskDefinition}); err != nil {
		t.Errorf("DescribeTaskDefinition: %v", err)
	}
}

func TestGenerate_Deterministic(t *testing.T) {
	s := Scale{Accounts: 2, RegionsPerAccount: 2, ClustersPerRegion: 2, ServicesPerCluster: 3, TasksPerService: 2}
	arns := func(e *Estate) []string {
		var out []string
		for _, c := range e.Clusters {
			out = append(out, aws.ToString(c.Cluster.ClusterArn))
		}
		for _, sv := range e.Services {
			out = append(out, aws.ToString(sv.Service.ServiceArn))
		}
		for _, tk := range e.Tasks {
			out = append(out, aws.ToString(tk.Task.TaskArn))
		}
		return out
	}
	a, b := arns(Generate(7, s)), arns(Generate(7, s))
	if len(a) == 0 || !reflect.DeepEqual(a, b) {
		t.Fatalf("same seed produced different ARNs")
	}
	if reflect.DeepEqual(a, arns(Generate(8, s))) {
		t.Errorf("different seeds produced identical task ARNs")
	}
}

func TestSetRateLimit_ThrottlesBeyondBurst(t *testing.T) {
	s := Scale{Accounts: 1, RegionsPerAccount: 2, ClustersPerRegion: 1, ServicesPerCluster: 1, TasksPerService: 1}
	e := Generate(1, s)
	f := New(e)
	targets := generatedTargets(e)
	limited, other := targets[0], targets[1]
	f.SetRateLimit(limited, 0.0001, 2) // refill positive but negligible; the bucket starts full
	describe := func(tg awsx.Target) error {
		api, err := f.ECS(context.Background(), tg)
		if err != nil {
			t.Fatal(err)
		}
		_, err = api.DescribeServices(context.Background(), &ecs.DescribeServicesInput{Cluster: aws.String("cluster-00"), Services: []string{"svc-000"}})
		return err
	}
	for i := 0; i < 2; i++ {
		if err := describe(limited); err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
	}
	if err := describe(limited); err == nil || !awsx.IsThrottle(err) {
		t.Fatalf("third call err = %v, want throttle", err)
	}
	// Every API of the target shares the bucket, not just ECS.
	ec2, _ := f.EC2(context.Background(), limited)
	if _, err := ec2.DescribeVpcs(context.Background(), &ec2sdk.DescribeVpcsInput{}); !awsx.IsThrottle(err) {
		t.Errorf("EC2 on limited target err = %v, want throttle", err)
	}
	for i := 0; i < 5; i++ {
		if err := describe(other); err != nil {
			t.Fatalf("other target call %d: %v", i+1, err)
		}
	}
	f.SetRateLimit(limited, 0, 0) // refill <= 0 removes the limit
	if err := describe(limited); err != nil {
		t.Errorf("after removing limit: %v", err)
	}
}

func TestCallCountsAndMaxInFlight(t *testing.T) {
	e := Generate(1, Scale{Accounts: 1, RegionsPerAccount: 1, ClustersPerRegion: 1, ServicesPerCluster: 1, TasksPerService: 1})
	f := New(e)
	f.CallLatency = 50 * time.Millisecond
	tg := generatedTargets(e)[0]
	api, _ := f.ECS(context.Background(), tg)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = api.ListClusters(context.Background(), &ecs.ListClustersInput{})
		}()
	}
	wg.Wait()
	elb, _ := f.ELB(context.Background(), tg)
	_, _ = elb.DescribeTargetGroups(context.Background(), &elasticloadbalancingv2.DescribeTargetGroupsInput{})

	counts := f.CallCounts()
	if got := counts[tg.AccountID+"|"+tg.Region+"|ecs|ListClusters"]; got != 4 {
		t.Errorf("ecs ListClusters = %d, want 4 (counts %v)", got, counts)
	}
	if got := counts[tg.AccountID+"|"+tg.Region+"|elasticloadbalancing|DescribeTargetGroups"]; got != 1 {
		t.Errorf("elb DescribeTargetGroups = %d, want 1 (counts %v)", got, counts)
	}
	if got := f.MaxInFlight(tg); got < 2 || got > 4 {
		t.Errorf("MaxInFlight = %d, want 2..4", got)
	}
	f.ResetCallCounts()
	if len(f.CallCounts()) != 0 {
		t.Errorf("counts after reset = %v", f.CallCounts())
	}
}

func TestMutateTasks_ChangesDescribeOutput(t *testing.T) {
	s := Scale{Accounts: 1, RegionsPerAccount: 2, ClustersPerRegion: 2, ServicesPerCluster: 3, TasksPerService: 3}
	e := Generate(3, s)
	f := New(e)
	targets := generatedTargets(e)
	tg, other := targets[0], targets[1]
	snapshot := func(tg awsx.Target) map[string]string {
		api, _ := f.ECS(context.Background(), tg)
		out := map[string]string{}
		for _, cl := range []string{"cluster-00", "cluster-01"} {
			lt, err := api.ListTasks(context.Background(), &ecs.ListTasksInput{Cluster: aws.String(cl)})
			if err != nil {
				t.Fatal(err)
			}
			dt, err := api.DescribeTasks(context.Background(), &ecs.DescribeTasksInput{Cluster: aws.String(cl), Tasks: lt.TaskArns})
			if err != nil {
				t.Fatal(err)
			}
			for _, task := range dt.Tasks {
				out[aws.ToString(task.TaskArn)] = string(task.HealthStatus) + "/" + aws.ToString(task.Cpu) + "/" + string(task.Containers[0].HealthStatus)
			}
		}
		return out
	}
	before, otherBefore := snapshot(tg), snapshot(other)
	e.MutateTasks(tg, 5)
	after := snapshot(tg)
	changed := 0
	for arn, v := range before {
		if after[arn] != v {
			changed++
		}
	}
	if changed != 5 {
		t.Errorf("changed tasks = %d, want 5", changed)
	}
	if !reflect.DeepEqual(otherBefore, snapshot(other)) {
		t.Errorf("MutateTasks touched another target")
	}
}
