package network

import (
	"reflect"
	"testing"
)

func TestActiveVPCs_CountsOnlyGivenTasks(t *testing.T) {
	got := ActiveVPCs(testSubnets(), mainTasks()[:3])
	want := map[string]int{mainVPCID: 3}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestSummarizeRegion_HiddenVPCs(t *testing.T) {
	got := SummarizeRegion(testRegion, testVPCs(), testSubnets(), allTasks(), allTasks())
	want := RegionSummary{Region: testRegion, ActiveVPCs: 2, HiddenVPCs: 1, Clusters: 3, Tasks: 12}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// A VPC whose tasks the caller cannot see is neither active nor hidden: hidden only counts
// VPCs with no running task at all, so the number is the same for every caller.
func TestSummarizeRegion_HiddenIgnoresVisibility(t *testing.T) {
	got := SummarizeRegion(testRegion, testVPCs(), testSubnets(), nil, allTasks())
	if got.ActiveVPCs != 0 || got.HiddenVPCs != 1 {
		t.Errorf("got %+v, want 0 active and 1 hidden", got)
	}
}

func TestSummarizeRegion_UnplacedTasks(t *testing.T) {
	tasks := allTasks()
	tasks = append(tasks,
		runningTask("core-services", "payments-api", "subnet-new", "10.20.9.9", 20),
		runningTask("edge-ec2", "proxy", "", "", 21), // bridge task, cluster has no awsvpc task
	)
	got := SummarizeRegion(testRegion, testVPCs(), testSubnets(), tasks, tasks)
	if got.UnplacedTasks != 2 || got.Tasks != 12 || got.Clusters != 3 {
		t.Errorf("got %+v", got)
	}
}

func TestSummarizeVPCs_ActiveOnlySortedByName(t *testing.T) {
	got := SummarizeVPCs(testVPCs(), testSubnets(), allTasks())
	if len(got) != 2 {
		t.Fatalf("want 2 active VPCs, got %d", len(got))
	}
	main, shared := got[0], got[1]
	if main.VPC.Name != "homelab-main" || shared.VPC.Name != "shared-services" {
		t.Fatalf("order: %s, %s", main.VPC.Name, shared.VPC.Name)
	}
	if main.Subnets != 3 || main.Tasks != 10 || !reflect.DeepEqual(main.Clusters, []string{"background-jobs", "core-services"}) {
		t.Errorf("homelab-main: %+v", main)
	}
	if shared.Subnets != 2 || shared.Tasks != 2 || !reflect.DeepEqual(shared.Clusters, []string{"ci-tools"}) {
		t.Errorf("shared-services: %+v", shared)
	}
}
