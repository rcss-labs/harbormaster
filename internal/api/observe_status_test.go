package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/poller"
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

	c := login(t, srv, "pavel", testPassword)
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
