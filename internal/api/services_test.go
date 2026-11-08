package api

import (
	"net/http"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/model"
)

func serviceID(account, region, cluster, name string) string {
	return model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: account, Region: region, Cluster: cluster}, Service: name}.ID()
}

func paymentsID() string {
	return serviceID(homelabAccountID, "us-east-1", "core-services", "payments-api")
}

func getService(t *testing.T, c *apiClient, id string) ServiceDTO {
	t.Helper()
	resp := c.get("/api/services/" + id)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("service %s: status %d", id, resp.StatusCode)
	}
	var out ServiceDTO
	decode(t, resp, &out)
	return out
}

func TestService_DetailShape(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedAccounts(t, st)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	got := getService(t, login(t, srv, "sam", testPassword), paymentsID())

	if got.Name != "payments-api" || got.Cluster.Name != "core-services" || got.AccountID != homelabAccountID || got.Region != "us-east-1" {
		t.Errorf("identity = %s %s %s %s", got.Name, got.Cluster.Name, got.AccountID, got.Region)
	}
	if got.AccountName != "homelab-admin" || got.Tag.Name != "Prod" || got.Status != model.StatusSteady {
		t.Errorf("account %q tag %q status %q", got.AccountName, got.Tag.Name, got.Status)
	}
	if len(got.RunningTasks) != 3 || len(got.Slots) != 3 {
		t.Errorf("running tasks %d slots %d, want 3", len(got.RunningTasks), len(got.Slots))
	}
	for _, task := range got.RunningTasks {
		if task.Health != "HEALTHY" || task.ID == "" || task.AZ == "" {
			t.Errorf("running task = %+v", task)
		}
	}
	if len(got.Deployments) == 0 || got.Deployments[0].Status != "PRIMARY" || got.Deployments[0].RolloutState != "COMPLETED" {
		t.Fatalf("deployments = %+v, want PRIMARY COMPLETED first", got.Deployments)
	}
	if len(got.Networking.Subnets) != 2 {
		t.Errorf("subnets = %+v, want 2", got.Networking.Subnets)
	}
	tasksInSubnets := 0
	for _, s := range got.Networking.Subnets {
		tasksInSubnets += s.Tasks
	}
	if tasksInSubnets != 3 {
		t.Errorf("tasks across subnets = %d, want 3", tasksInSubnets)
	}
	if len(got.TaskDefinition.Containers) != 2 {
		t.Fatalf("containers = %+v, want 2", got.TaskDefinition.Containers)
	}
	names := map[string]bool{}
	for _, c := range got.TaskDefinition.Containers {
		names[c.Name] = true
	}
	if !names["otel-collector"] || !names["payments-api"] {
		t.Errorf("container names = %v", names)
	}
	if got.MinHealthyPercent != 100 || got.MaxPercent != 200 || !got.CircuitBreaker.Enable || !got.CircuitBreaker.Rollback {
		t.Errorf("deployment config = %d/%d %+v", got.MinHealthyPercent, got.MaxPercent, got.CircuitBreaker)
	}
	if !got.Can["view"] {
		t.Error("can.view = false for admin")
	}
	if got.ConsoleURL != "https://us-east-1.console.aws.amazon.com/ecs/v2/clusters/core-services/services/payments-api" {
		t.Errorf("console_url = %q", got.ConsoleURL)
	}
	if got.PolledAt.IsZero() || got.RecentEvents == nil || len(got.RecentEvents) > 6 {
		t.Errorf("polled_at %v recent_events %v", got.PolledAt, got.RecentEvents)
	}
}

func TestService_NotificationsRollout(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	got := getService(t, login(t, srv, "sam", testPassword),
		serviceID(homelabAccountID, "us-east-1", "core-services", "notifications"))

	if got.Status != model.StatusDeploying {
		t.Errorf("status = %q, want deploying", got.Status)
	}
	if len(got.Deployments) != 2 {
		t.Fatalf("deployments = %+v, want 2", got.Deployments)
	}
	primary, active := got.Deployments[0], got.Deployments[1]
	if primary.Status != "PRIMARY" || primary.RolloutState != "IN_PROGRESS" || primary.Revision != 10 || primary.RunningCount != 1 {
		t.Errorf("primary = %+v", primary)
	}
	if active.Status != "ACTIVE" || active.Revision != 9 {
		t.Errorf("active = %+v", active)
	}
	rolling := 0
	for _, slot := range got.Slots {
		if slot.RollingOut {
			rolling++
		}
	}
	if rolling != 1 || len(got.Slots) != 2 {
		t.Errorf("slots %d rolling_out %d, want 2 and 1", len(got.Slots), rolling)
	}
}

func TestService_InvisibleIs404(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	setPassword(t, st, "oncall")
	pollAll(t, st, fk)

	cases := []struct{ user, id string }{
		{"omer", paymentsID()},
		{"oncall", serviceID(homelabAccountID, "us-east-1", "background-jobs", "image-resizer")},
		{"sam", "not-a-service-id"},
	}
	for _, c := range cases {
		resp := login(t, srv, c.user, testPassword).get("/api/services/" + c.id)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s -> %s: status %d, want 404", c.user, c.id, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

func TestService_CanFlagsFollowPermissions(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	setPassword(t, st, "oncall")
	pollAll(t, st, fk)
	got := getService(t, login(t, srv, "oncall", testPassword), serviceID(stagingAccountID, "us-east-1", "staging-web", "web"))

	want := map[string]bool{"view": true, "logs": true, "operate": true, "deploy": false, "edit": false, "terminal": true}
	for action, allowed := range want {
		if got.Can[action] != allowed {
			t.Errorf("can[%s] = %v, want %v", action, got.Can[action], allowed)
		}
	}
	if got.Tag.Name != "Staging" {
		t.Errorf("tag = %q, want Staging", got.Tag.Name)
	}
}

func TestServiceDetail_IncludesVPCAndSecurityGroupNames(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedAccounts(t, st)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	pollAll(t, st, fk) // the first network run can precede services, so SG names need a second pass
	got := getService(t, login(t, srv, "sam", testPassword), paymentsID())
	if got.Networking.VPC == nil || got.Networking.VPC.VPCID != vpcMain || got.Networking.VPC.Name != "homelab-main" {
		t.Errorf("vpc = %+v, want %s homelab-main", got.Networking.VPC, vpcMain)
	}
	if got.Networking.SecurityGroupNames == nil || len(got.Networking.SecurityGroups) == 0 {
		t.Fatalf("security groups %v names %v", got.Networking.SecurityGroups, got.Networking.SecurityGroupNames)
	}
	for _, id := range got.Networking.SecurityGroups {
		if got.Networking.SecurityGroupNames[id] == "" {
			t.Errorf("no name for %s in %v", id, got.Networking.SecurityGroupNames)
		}
	}
}
