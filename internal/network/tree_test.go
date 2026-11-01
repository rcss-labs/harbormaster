package network

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/model"
)

func TestAssemble_MatchesScreenshotCounts(t *testing.T) {
	tree := Assemble(mainInput())
	root := tree.Root
	if root.Kind != "vpc" || root.Key != "vpc:"+mainVPCID || root.Label != "homelab-main" {
		t.Errorf("root: %+v", root)
	}
	if root.Tasks != 10 || root.VPC.Zones != 2 {
		t.Errorf("root tasks %d zones %d", root.Tasks, root.VPC.Zones)
	}
	if tree.Summary != (Summary{Subnets: 3, Zones: 2, RunningTasks: 10}) {
		t.Errorf("summary: %+v", tree.Summary)
	}
	zone := mustFind(t, root, "az:"+mainVPCID+":us-east-1a")
	if zone.AZ.Subnets != 2 || zone.Tasks != 7 {
		t.Errorf("az: %+v tasks %d", zone.AZ, zone.Tasks)
	}
	subnet := mustFind(t, root, "sn:"+private1a)
	if subnet.Subnet.IPsUsed != 27 || subnet.Subnet.IPsTotal != 251 || subnet.Tasks != 7 || subnet.Label != "private-1a" {
		t.Errorf("subnet: %+v tasks %d", subnet.Subnet, subnet.Tasks)
	}
	cluster := mustFind(t, root, "sn:"+private1a+":core-services")
	if cluster.Cluster.Services != 4 || cluster.Tasks != 5 {
		t.Errorf("cluster: %+v tasks %d", cluster.Cluster, cluster.Tasks)
	}
	if cluster.Cluster.Tag != (TagInfo{Name: "Prod", Color: "red"}) || cluster.Cluster.ClusterID != clusterKey("core-services").ID() {
		t.Errorf("cluster data: %+v", cluster.Cluster)
	}
	payments := mustFind(t, root, "sn:"+private1a+":core-services:payments-api")
	if payments.Service.Revision != 42 || payments.Service.Status != model.StatusSteady || payments.Tasks != 2 {
		t.Errorf("service: %+v", payments.Service)
	}
	if payments.Service.ServiceID != serviceKey("core-services", "payments-api").ID() {
		t.Errorf("service id: %s", payments.Service.ServiceID)
	}
	first := payments.Children[0]
	if first.Kind != "task" || first.Task.PrivateIP != "10.20.1.23" || len(first.Label) != 8 || first.Tasks != 1 {
		t.Errorf("task: %+v", first)
	}
	if first.Task.ServiceID != payments.Service.ServiceID {
		t.Errorf("task service id: %s", first.Task.ServiceID)
	}
}

func TestAssemble_OrdersAZsSubnetsClustersServicesTasks(t *testing.T) {
	in := mainInput()
	// Reverse inputs so any ordering must come from Assemble, not the fixture.
	for i, j := 0, len(in.Tasks)-1; i < j; i, j = i+1, j-1 {
		in.Tasks[i], in.Tasks[j] = in.Tasks[j], in.Tasks[i]
	}
	root := Assemble(in).Root
	if got := childKeys(root); !reflect.DeepEqual(got, []string{"az:" + mainVPCID + ":us-east-1a", "az:" + mainVPCID + ":us-east-1b"}) {
		t.Errorf("azs: %v", got)
	}
	zoneA := mustFind(t, root, "az:"+mainVPCID+":us-east-1a")
	if got := childKeys(zoneA); !reflect.DeepEqual(got, []string{"sn:" + private1a, "sn:" + public1a}) {
		t.Errorf("subnets: %v", got)
	}
	subnet := mustFind(t, root, "sn:"+private1a)
	if got := childKeys(subnet); !reflect.DeepEqual(got, []string{"sn:" + private1a + ":background-jobs", "sn:" + private1a + ":core-services"}) {
		t.Errorf("clusters: %v", got)
	}
	core := mustFind(t, root, "sn:"+private1a+":core-services")
	want := []string{"auth-api", "gateway", "notifier", "payments-api"}
	var got []string
	for _, child := range core.Children {
		got = append(got, child.Label)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("services: %v", got)
	}
	payments := mustFind(t, root, "sn:"+private1a+":core-services:payments-api")
	if payments.Children[0].Task.PrivateIP != "10.20.1.23" || payments.Children[1].Task.PrivateIP != "10.20.1.24" {
		t.Errorf("tasks must order by StartedAt: %+v", payments.Children)
	}
}

func TestAssemble_EmptySubnetHasNoChildren(t *testing.T) {
	subnet := mustFind(t, Assemble(mainInput()).Root, "sn:"+public1a)
	if subnet.Children != nil || subnet.Tasks != 0 || !subnet.Subnet.Public {
		t.Errorf("public-1a: %+v", subnet)
	}
}

func TestAssemble_StandaloneTasksNodeLast(t *testing.T) {
	root := Assemble(mainInput()).Root
	cluster := mustFind(t, root, "sn:"+private1a+":background-jobs")
	last := cluster.Children[len(cluster.Children)-1]
	if last.Kind != "standalone" || last.Label != "Standalone tasks" || !strings.HasSuffix(last.Key, ":_standalone") {
		t.Fatalf("last child: %+v", last)
	}
	if last.Tasks != 1 || last.Children[0].Task.Group != "family:report-backfill" || last.Children[0].Task.ServiceID != "" {
		t.Errorf("standalone task: %+v", last.Children[0])
	}
	if cluster.Cluster.Services != 1 || cluster.Tasks != 2 {
		t.Errorf("cluster: %+v tasks %d", cluster.Cluster, cluster.Tasks)
	}
}

func TestAssemble_NoSubnetTasksUnderNosubnetNode(t *testing.T) {
	in := mainInput()
	in.Tasks = append(in.Tasks,
		runningTask("core-services", "payments-api", "", "", 30),
		runningTask("edge-ec2", "proxy", "", "", 31),
	)
	tree := Assemble(in)
	root := tree.Root
	last := root.Children[len(root.Children)-1]
	if last.Kind != "nosubnet" || last.Key != "nosub:"+mainVPCID || last.Label != "No subnet (bridge or host network mode)" || last.Tasks != 1 {
		t.Fatalf("last child: %+v", last)
	}
	cluster := mustFind(t, root, "nosub:"+mainVPCID+":core-services")
	service := mustFind(t, root, "nosub:"+mainVPCID+":core-services:payments-api")
	if cluster.Kind != "cluster" || service.Kind != "service" || service.Children[0].Kind != "task" {
		t.Errorf("nesting: %+v", last)
	}
	if _, found := findNode(root, "nosub:"+mainVPCID+":edge-ec2"); found {
		t.Error("cluster with no awsvpc task must not be placed")
	}
	if tree.Summary.UnplacedTasks != 1 || tree.Summary.RunningTasks != 11 || root.Tasks != 11 {
		t.Errorf("summary: %+v root %d", tree.Summary, root.Tasks)
	}
}

func TestAssemble_UnknownSubnetIsUnplaced(t *testing.T) {
	in := mainInput()
	in.Tasks = append(in.Tasks, runningTask("core-services", "payments-api", "subnet-new", "10.20.9.9", 40))
	tree := Assemble(in)
	if tree.Summary.UnplacedTasks != 1 || tree.Summary.RunningTasks != 10 || tree.Root.Tasks != 10 {
		t.Errorf("summary: %+v", tree.Summary)
	}
	for _, child := range tree.Root.Children {
		if child.Kind == "nosubnet" {
			t.Error("unknown-subnet task must not appear under nosubnet")
		}
	}
}

func TestAssemble_CollapsedByDefaultAbove150(t *testing.T) {
	for _, row := range []struct {
		tasks     int
		collapsed bool
	}{{151, true}, {150, false}} {
		in := mainInput()
		in.Tasks = nil
		for i := 0; i < row.tasks; i++ {
			in.Tasks = append(in.Tasks, runningTask("core-services", "payments-api", private1a, "10.20.1.23", i))
		}
		if got := Assemble(in).Summary.CollapsedByDefault; got != row.collapsed {
			t.Errorf("%d tasks: collapsed=%v", row.tasks, got)
		}
	}
}

func TestAssemble_UnhealthyTaskFlag(t *testing.T) {
	in := mainInput()
	in.Tasks[0].HealthStatus = "UNHEALTHY"
	root := Assemble(in).Root
	payments := mustFind(t, root, "sn:"+private1a+":core-services:payments-api")
	if !payments.Children[0].Task.Unhealthy || payments.Children[1].Task.Unhealthy {
		t.Errorf("unhealthy flags: %+v", payments.Children)
	}
}

func TestAssemble_EdgesSortedAndDetailed(t *testing.T) {
	in := mainInput()
	in.Edges = []model.VPCEdge{
		{Kind: model.VPCEdgeEndpoint, ResourceID: "vpce-2", Detail: "logs"},
		{Kind: model.VPCEdgeNAT, ResourceID: "nat-1", SubnetID: public1a, Detail: "10.20.101.5"},
		{Kind: model.VPCEdgeEndpoint, ResourceID: "vpce-1", Detail: "ecr.api"},
		{Kind: model.VPCEdgeIGW, ResourceID: "igw-1"},
	}
	edges := Assemble(in).Edges
	want := []Edge{
		{Kind: model.VPCEdgeIGW, ID: "igw-1"},
		{Kind: model.VPCEdgeNAT, ID: "nat-1", SubnetID: public1a, Detail: "10.20.101.5"},
		{Kind: model.VPCEdgeEndpoint, ID: "vpce-1", Detail: "ecr.api"},
		{Kind: model.VPCEdgeEndpoint, ID: "vpce-2", Detail: "logs"},
	}
	if !reflect.DeepEqual(edges, want) {
		t.Errorf("got %+v", edges)
	}
}

func TestAssemble_NoEdgesIsEmptyJSONArray(t *testing.T) {
	data, err := json.Marshal(Assemble(mainInput()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"edges":[]`) || !strings.Contains(string(data), `"tasks":0`) {
		t.Errorf("json: %s", data)
	}
}

func TestAssemble_IgnoresTasksOfOtherVPCs(t *testing.T) {
	in := mainInput()
	in.Tasks = allTasks()
	tree := Assemble(in)
	if tree.Root.Tasks != 10 || tree.Summary.UnplacedTasks != 0 {
		t.Errorf("root %d summary %+v", tree.Root.Tasks, tree.Summary)
	}
	if _, found := findNode(tree.Root, "sn:"+shared1a); found {
		t.Error("shared-services subnet must not appear")
	}
}
