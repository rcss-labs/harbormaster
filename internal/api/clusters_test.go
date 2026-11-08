package api

import (
	"math"
	"net/http"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/rcss-labs/harbormaster/internal/model"
)

func statusOf(s string) model.Status { return model.Status(s) }

type clusterList struct {
	Items    []ClusterCardDTO `json:"items"`
	Next     *string          `json:"next"`
	PolledAt string           `json:"polled_at"`
}

func listClusters(t *testing.T, c *apiClient, query string) clusterList {
	t.Helper()
	resp := c.get("/api/clusters" + query)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clusters %q: status %d", query, resp.StatusCode)
	}
	var out clusterList
	decode(t, resp, &out)
	return out
}

func clusterID(account, region, name string) string {
	return model.ClusterKey{AccountID: account, Region: region, Cluster: name}.ID()
}

func TestClusters_DefaultOrderProblemsFirst(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	got := listClusters(t, login(t, srv, "sam", testPassword), "")
	if len(got.Items) != 6 {
		t.Fatalf("items %d, want 6", len(got.Items))
	}
	rank := map[model.ClusterStatus]int{model.ClusterNeedsAttention: 0, model.ClusterDegraded: 1, model.ClusterDeploying: 2, model.ClusterHealthy: 3}
	for i := 1; i < len(got.Items); i++ {
		a, b := got.Items[i-1], got.Items[i]
		if rank[a.Status] > rank[b.Status] || (rank[a.Status] == rank[b.Status] && a.Name > b.Name) {
			t.Errorf("order broken at %d: %s(%s) before %s(%s)", i, a.Name, a.Status, b.Name, b.Status)
		}
	}
	if got.Items[0].Name != "background-jobs" || got.Items[0].Status != model.ClusterNeedsAttention {
		t.Errorf("first = %s %s, want background-jobs needs_attention", got.Items[0].Name, got.Items[0].Status)
	}
	if got.PolledAt == "" {
		t.Error("polled_at missing")
	}
}

func TestClusters_Pagination(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	c := login(t, srv, "sam", testPassword)
	first := listClusters(t, c, "?limit=4")
	if len(first.Items) != 4 || first.Next == nil {
		t.Fatalf("first page %d next %v", len(first.Items), first.Next)
	}
	second := listClusters(t, c, "?limit=4&cursor="+*first.Next)
	if len(second.Items) != 2 || second.Next != nil {
		t.Fatalf("second page %d next %v", len(second.Items), second.Next)
	}
}

func TestClusters_TagAndInheritance(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	for _, item := range listClusters(t, login(t, srv, "sam", testPassword), "").Items {
		switch item.Name {
		case "core-services":
			if item.Tag.Name != "Prod" || item.Tag.Inherited {
				t.Errorf("core-services tag %+v", item.Tag)
			}
		case "ci-tools":
			if item.Tag.Name != "Dev" || !item.Tag.Inherited {
				t.Errorf("ci-tools tag %+v, want inherited Dev", item.Tag)
			}
		}
	}
}

func TestCluster_InvisibleIs404(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	id := clusterID(homelabAccountID, "us-east-1", "core-services")
	if resp := login(t, srv, "omer", testPassword).get("/api/clusters/" + id); resp.StatusCode != http.StatusNotFound {
		t.Errorf("omer status %d, want 404", resp.StatusCode)
	}
	if resp := login(t, srv, "sam", testPassword).get("/api/clusters/not-an-id"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("bad id status %d, want 404", resp.StatusCode)
	}
}

func TestCluster_SlotsAndReserved(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	resp := login(t, srv, "sam", testPassword).get("/api/clusters/" + clusterID(homelabAccountID, "us-east-1", "core-services"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var got ClusterDTO
	decode(t, resp, &got)

	wantCPU, wantGiB, wantRunning := expectedReserved(fk, "core-services")
	if wantRunning != 9 {
		t.Fatalf("estate has %d running core-services tasks, want 9", wantRunning)
	}
	if math.Abs(got.ReservedVCPU-wantCPU) > 1e-9 || math.Abs(got.ReservedGiB-wantGiB) > 1e-9 {
		t.Errorf("reserved %v vCPU %v GiB, want %v and %v", got.ReservedVCPU, got.ReservedGiB, wantCPU, wantGiB)
	}
	if got.Running != 9 || got.Desired != 9 || got.Tag.Name != "Prod" || got.ConsoleURL != "https://us-east-1.console.aws.amazon.com/ecs/v2/clusters/core-services" {
		t.Errorf("cluster = running %d desired %d tag %s url %s", got.Running, got.Desired, got.Tag.Name, got.ConsoleURL)
	}
	if got.ARN == "" || got.PolledAt.IsZero() || got.TasksHistory72h == nil || len(got.StopsPerHour) != 24 {
		t.Errorf("arn %q polled %v history %v stops %d", got.ARN, got.PolledAt, got.TasksHistory72h, len(got.StopsPerHour))
	}
	if got.Status != model.ClusterDeploying {
		t.Errorf("status %s, want deploying", got.Status)
	}
	services := map[string]ClusterServiceDTO{}
	for _, s := range got.ServicesDetail {
		services[s.Name] = s
	}
	payments := services["payments-api"]
	if len(payments.Slots) != 3 || payments.ImageTag != "1.18.2" || payments.Revision != 42 || payments.Tag.Name != "Prod" || !payments.Tag.Inherited {
		t.Fatalf("payments-api = %+v", payments)
	}
	for _, slot := range payments.Slots {
		if slot.Health != "HEALTHY" || slot.RollingOut || slot.TaskID == "" || slot.ShortID == "" {
			t.Errorf("payments slot %+v", slot)
		}
	}
	rolling := 0
	for _, slot := range services["notifications"].Slots {
		if slot.RollingOut {
			rolling++
		}
	}
	if len(services["notifications"].Slots) != 2 || rolling != 1 {
		t.Errorf("notifications slots %d rolling %d, want 2 and 1", len(services["notifications"].Slots), rolling)
	}
}

func TestClusterDetail_IncludesVPC(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedAccounts(t, st)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	c := login(t, srv, "sam", testPassword)
	resp := c.get("/api/clusters/" + clusterID(homelabAccountID, "us-east-1", "core-services"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var got ClusterDTO
	decode(t, resp, &got)
	if got.VPC == nil || got.VPC.VPCID != vpcMain || got.VPC.Name != "homelab-main" {
		t.Errorf("vpc = %+v, want %s homelab-main", got.VPC, vpcMain)
	}
}

func TestClusterDetail_CountsFargateSpotAndOtherCapacity(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	fk.Estate.Mu.Lock()
	seen := 0
	for i := range fk.Estate.Tasks {
		task := &fk.Estate.Tasks[i]
		if task.ClusterName != "core-services" || aws.ToString(task.Task.LastStatus) != "RUNNING" {
			continue
		}
		provider := "FARGATE"
		task.Task.LaunchType = ecstypes.LaunchTypeFargate
		switch seen {
		case 0:
			provider = "FARGATE_SPOT"
		case 1:
			task.Task.LaunchType = ecstypes.LaunchTypeEc2
			task.Task.CapacityProviderName = nil
			provider = ""
		case 2:
			provider = "ec2-asg-provider"
			task.Task.LaunchType = ""
		}
		if provider != "" {
			task.Task.CapacityProviderName = aws.String(provider)
		}
		seen++
	}
	fk.Estate.Mu.Unlock()
	pollAll(t, st, fk)

	resp := login(t, srv, "sam", testPassword).get("/api/clusters/" + clusterID(homelabAccountID, "us-east-1", "core-services"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var got ClusterDTO
	decode(t, resp, &got)
	if got.SpotTasks != 1 || got.OtherTasks != 2 || got.FargateTasks != seen-3 {
		t.Errorf("fargate %d spot %d other %d of %d running, want %d, 1, 2", got.FargateTasks, got.SpotTasks, got.OtherTasks, seen, seen-3)
	}
}
