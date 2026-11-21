package poller

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/platforms"
	"github.com/rcss-labs/harbormaster/internal/registry"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

// countedRegistry counts the reads a resolver makes of the public registry.
type countedRegistry struct {
	inner *fake.Registry
	calls atomic.Int32
}

func (c *countedRegistry) Digest(ctx context.Context, ref registry.Ref) (string, error) {
	c.calls.Add(1)
	return c.inner.Digest(ctx, ref)
}

func (c *countedRegistry) Manifest(ctx context.Context, ref registry.Ref) ([]byte, string, error) {
	c.calls.Add(1)
	return c.inner.Manifest(ctx, ref)
}

func TestSupervisor_StartsImagePlatformsOnlyWhileLeading(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	key := model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: "482109375512", Region: "us-east-1", Cluster: "core-services"}, Service: "payments-api"}
	arn := "arn:aws:ecs:us-east-1:482109375512:task-definition/payments-api:1"
	raw, _ := json.Marshal(map[string]any{"ContainerDefinitions": []map[string]any{{"Name": "otel", "Image": "public.ecr.aws/aws-observability/aws-otel-collector:v0.40.0"}}})
	if err := st.UpsertTaskDefinition(ctx, model.TaskDefinitionRef{AccountID: key.AccountID, Region: key.Region, ARN: arn, Family: "payments-api", Revision: 1,
		RequiresCompatibilities: []string{"FARGATE"}, Raw: raw}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertServices(ctx, []model.Service{{Key: key, Status: "ACTIVE", Desired: 1, TaskDefinitionARN: arn, PolledAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	resolverFor := func() (*platforms.Resolver, *countedRegistry) {
		reader := &countedRegistry{inner: fake.NewRegistry()}
		clients := fake.New(fake.MockupEstate(time.Now))
		return &platforms.Resolver{Store: st, Clients: clients, Public: reader, Interval: 50 * time.Millisecond, Recheck: time.Hour, Now: time.Now}, reader
	}
	lock := randomLockKey()

	leaderResolver, leaderReader := resolverFor()
	leader := newTestPoller(st, Config{AccountsRefresh: time.Hour, RetentionEvery: time.Hour, ImagePlatforms: leaderResolver}, lock)
	leader.loopFactory = newLoopRecorder().factory
	startRun(t, leader)
	waitFor(t, 3*time.Second, "the first poller to lead", leader.IsLeader)

	followerResolver, followerReader := resolverFor()
	follower := newTestPoller(st, Config{AccountsRefresh: time.Hour, RetentionEvery: time.Hour, ImagePlatforms: followerResolver}, lock)
	follower.loopFactory = newLoopRecorder().factory
	startRun(t, follower)

	waitFor(t, 5*time.Second, "the leader's resolver to read the public image", func() bool {
		got, err := st.ImagePlatformsFor(ctx, []string{"public.ecr.aws/aws-observability/aws-otel-collector:v0.40.0"})
		return err == nil && got["public.ecr.aws/aws-observability/aws-otel-collector:v0.40.0"].Known
	})
	time.Sleep(300 * time.Millisecond)
	if n := followerReader.calls.Load(); n != 0 {
		t.Fatalf("the follower's resolver read the registry %d times", n)
	}
	if leaderReader.calls.Load() == 0 {
		t.Fatal("the leader's resolver never read the registry")
	}
	if leaderResolver.Idle == nil {
		t.Fatal("the supervisor left the resolver without the poller's idle flag")
	}
}
