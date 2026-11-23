package fake

import (
	"context"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"

	"github.com/rcss-labs/harbormaster/internal/awsx"
)

type recordedCall struct {
	target      awsx.Target
	service, op string
	result      string
	source      awsx.Source
}

func TestFake_OnCallReportsEveryCallWithItsSource(t *testing.T) {
	f := newFake()
	var mu sync.Mutex
	var got []recordedCall
	f.OnCall = func(ctx context.Context, target awsx.Target, service, op, result string) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, recordedCall{target, service, op, result, awsx.SourceFrom(ctx)})
	}
	target := awsx.Target{AccountID: sandboxID, Region: "eu-west-1"}
	ecsAPI := ecsFor(t, f, sandboxID, "eu-west-1")
	if _, err := ecsAPI.ListClusters(awsx.WithSource(context.Background(), awsx.SourceFocus), &ecs.ListClustersInput{}); err != nil {
		t.Fatal(err)
	}
	elbAPI, err := f.ELB(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := elbAPI.DescribeTargetGroups(context.Background(), &elasticloadbalancingv2.DescribeTargetGroupsInput{}); err != nil {
		t.Fatal(err)
	}
	f.Throttle = 1
	_, _ = ecsAPI.ListClusters(awsx.WithSource(context.Background(), awsx.SourceTier), &ecs.ListClustersInput{})

	mu.Lock()
	defer mu.Unlock()
	want := []recordedCall{
		{target, "ecs", "ListClusters", "ok", awsx.SourceFocus},
		{target, "elasticloadbalancing", "DescribeTargetGroups", "ok", awsx.SourceRequest},
		{target, "ecs", "ListClusters", "throttled", awsx.SourceTier},
	}
	if len(got) != len(want) {
		t.Fatalf("OnCall saw %d calls, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("call %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}
