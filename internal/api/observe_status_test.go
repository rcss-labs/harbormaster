package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/poller"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// markServiceTasksHealthy sets the ECS health of every running task of the service to HEALTHY,
// so that only the target group decides whether the service is unhealthy_task.
func markServiceTasksHealthy(fk *fake.Fake, cluster, service string) {
	fk.Estate.Mu.Lock()
	defer fk.Estate.Mu.Unlock()
	for i := range fk.Estate.Tasks {
		task := &fk.Estate.Tasks[i]
		if task.ClusterName != cluster || aws.ToString(task.Task.Group) != "service:"+service {
			continue
		}
		if aws.ToString(task.Task.LastStatus) == "RUNNING" {
			task.Task.HealthStatus = ecstypes.HealthStatusHealthy
		}
	}
}

func TestServiceStatus_TargetUnhealthyWithoutTaskHealth(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	target := awsx.Target{AccountID: homelabAccountID, Region: "eu-west-1"}
	key := model.ServiceKey{
		ClusterKey: model.ClusterKey{AccountID: target.AccountID, Region: target.Region, Cluster: "eu-apps"},
		Service:    "catalog-api",
	}
	markServiceTasksHealthy(fk, key.Cluster, key.Service)
	pollTargetHealth := func() {
		t.Helper()
		if err := poller.PollOnce(context.Background(), st, fk, poller.Config{MaxInFlight: 4}, target); err != nil {
			t.Fatalf("poll: %v", err)
		}
	}

	c := login(t, srv, "sam", testPassword)
	// The seed leaves one target unhealthy; clear it so the baseline is steady.
	fk.Estate.SetTargetHealth(key, 2, 0)
	pollTargetHealth()
	baseline := getOverview(t, c, "").Services.ByStatus[model.StatusUnhealthyTask]
	if got := serviceStatusOf(t, c, key); got != model.StatusSteady {
		t.Fatalf("before target unhealthy: status %q, want %q", got, model.StatusSteady)
	}

	fk.Estate.SetTargetHealth(key, 1, 1)
	pollTargetHealth()
	if got := serviceStatusOf(t, c, key); got != model.StatusUnhealthyTask {
		t.Fatalf("with unhealthy target: status %q, want %q", got, model.StatusUnhealthyTask)
	}
	if got := getOverview(t, c, "").Services.ByStatus[model.StatusUnhealthyTask]; got != baseline+1 {
		t.Fatalf("overview by_status[unhealthy_task] = %d, want %d", got, baseline+1)
	}

	fk.Estate.SetTargetHealth(key, 2, 0)
	pollTargetHealth()
	if got := serviceStatusOf(t, c, key); got != model.StatusSteady {
		t.Fatalf("after targets recover: status %q, want %q", got, model.StatusSteady)
	}
}

func serviceStatusOf(t *testing.T, c *apiClient, key model.ServiceKey) model.Status {
	t.Helper()
	resp := c.get("/api/services/" + key.ID())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("service %s: status %d", key.ID(), resp.StatusCode)
	}
	var dto ServiceDTO
	decode(t, resp, &dto)
	return dto.Status
}

type pollerBody struct {
	Items []PollerItemDTO `json:"items"`
}

func pollerItemFor(t *testing.T, got pollerBody, target awsx.Target) PollerItemDTO {
	t.Helper()
	for _, item := range got.Items {
		if item.AccountID == target.AccountID && item.Region == target.Region {
			return item
		}
	}
	t.Fatalf("target %+v missing in %+v", target, got.Items)
	return PollerItemDTO{}
}

func tierByLabel(t *testing.T, item PollerItemDTO, label string) PollerTierDTO {
	t.Helper()
	for _, tier := range item.Tiers {
		if tier.Label == label {
			return tier
		}
	}
	t.Fatalf("no tier labelled %q in %+v", label, item.Tiers)
	return PollerTierDTO{}
}

func TestPollerStatus_TiersListed(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedAccounts(t, st)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	var got pollerBody
	decode(t, login(t, srv, "sam", testPassword).get("/api/poller"), &got)
	item := pollerItemFor(t, got, awsx.Target{AccountID: homelabAccountID, Region: "eu-west-1"})
	if len(item.Tiers) == 0 {
		t.Fatalf("tiers empty: %+v", item)
	}
	if tier := tierByLabel(t, item, "Clusters and services"); !tier.Core {
		t.Errorf("clusters and services tier = %+v, want core", tier)
	}
	if tier := tierByLabel(t, item, "Auto scaling"); tier.Core {
		t.Errorf("auto scaling tier = %+v, want non-core", tier)
	}
}

func TestPollerStatus_NonCoreTierFailureKeepsCoreCurrent(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedAccounts(t, st)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	target := awsx.Target{AccountID: homelabAccountID, Region: "eu-west-1"}
	if err := st.UpsertPollLoop(t.Context(), store.PollLoop{Target: target, Tier: "scaling",
		LastError: "DescribeScheduledActions: UnsupportedOperation", Interval: time.Minute, BaseInterval: time.Minute, Failures: 5}); err != nil {
		t.Fatal(err)
	}
	var got pollerBody
	decode(t, login(t, srv, "sam", testPassword).get("/api/poller"), &got)
	item := pollerItemFor(t, got, target)
	if item.Failing || item.Throttled || item.LastError != "" || item.LastOKAt == nil {
		t.Errorf("item = %+v, want core data current", item)
	}
	if tier := tierByLabel(t, item, "Auto scaling"); !tier.Failing || tier.LastError == "" {
		t.Errorf("auto scaling tier = %+v, want failing with its error", tier)
	}
}

func TestPollerStatus_TierLabelsMerge(t *testing.T) {
	srv, _, st := newTestServer(t)
	seedAccounts(t, st)
	target := awsx.Target{AccountID: homelabAccountID, Region: "eu-west-1"}
	older := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	newer := time.Now().UTC().Truncate(time.Second)
	for _, loop := range []store.PollLoop{
		{Target: target, Tier: "services", LastOKAt: &newer, Interval: 5 * time.Second, BaseInterval: 5 * time.Second},
		{Target: target, Tier: "clusters", LastOKAt: &older, Interval: 30 * time.Second, BaseInterval: 30 * time.Second},
	} {
		if err := st.UpsertPollLoop(t.Context(), loop); err != nil {
			t.Fatal(err)
		}
	}
	var got pollerBody
	decode(t, login(t, srv, "sam", testPassword).get("/api/poller"), &got)
	item := pollerItemFor(t, got, target)
	var merged []PollerTierDTO
	for _, tier := range item.Tiers {
		if tier.Label == "Clusters and services" {
			merged = append(merged, tier)
		}
	}
	if len(merged) != 1 {
		t.Fatalf("Clusters and services entries = %+v, want exactly one", merged)
	}
	if merged[0].LastOKAt == nil || !merged[0].LastOKAt.Equal(older) {
		t.Errorf("last_ok_at = %v, want the older %v", merged[0].LastOKAt, older)
	}
}

func TestPollerStatus_NonAdminTierErrorHidden(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedAccounts(t, st)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	target := awsx.Target{AccountID: homelabAccountID, Region: "eu-west-1"}
	now := time.Now()
	// one non-failing blip names a resource, a failing tier names another
	if err := st.UpsertPollLoop(t.Context(), store.PollLoop{Target: target, Tier: "network", LastOKAt: &now,
		LastError: "AccessDenied on arn:aws:ec2:secret", Interval: time.Minute, BaseInterval: time.Minute, Failures: 1}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertPollLoop(t.Context(), store.PollLoop{Target: target, Tier: "scaling", LastOKAt: &now,
		LastError: "scaling failure text", Interval: time.Minute, BaseInterval: time.Minute, Failures: 5}); err != nil {
		t.Fatal(err)
	}
	var admin, omer pollerBody
	decode(t, login(t, srv, "sam", testPassword).get("/api/poller"), &admin)
	decode(t, login(t, srv, "omer", testPassword).get("/api/poller"), &omer)
	if tier := tierByLabel(t, pollerItemFor(t, admin, target), "Networks"); tier.LastError == "" {
		t.Errorf("admin should see the network error, got %+v", tier)
	}
	omerItem := pollerItemFor(t, omer, target)
	if tier := tierByLabel(t, omerItem, "Networks"); tier.LastError != "" {
		t.Errorf("omer sees a healthy tier's error %q", tier.LastError)
	}
	if tier := tierByLabel(t, omerItem, "Auto scaling"); tier.LastError == "" {
		t.Errorf("omer should see the error of a failing tier, got %+v", tier)
	}
}
