package api

import (
	"net/http"
	"slices"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/authz"
)

const (
	vpcMain   = "vpc-0a41c7e29b3d" // homelab-main
	vpcShared = "vpc-07de19a4c011" // shared-services
	vpcLegacy = "vpc-5c1e0f2a"     // legacy-default (no tasks)
)

// networkFixture polls the mockup estate (all tiers, so VPCs are stored) and returns clients for
// admin, devOnly ({Dev: View}), prodOnly ({Prod: View}) and none (no permissions).
type networkFixture struct {
	admin, devOnly, prodOnly, none *apiClient
}

func newNetworkFixture(t *testing.T) networkFixture {
	t.Helper()
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	tags, err := st.ListTags(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	tagID := map[string]authz.TagID{}
	for _, tag := range tags {
		tagID[tag.Name] = tag.ID
	}
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	addUser := func(name string, perms map[authz.TagID]authz.ActionSet) *apiClient {
		user, err := st.CreateUser(t.Context(), name, hash, false, false)
		if err != nil {
			t.Fatal(err)
		}
		for tag, actions := range perms {
			if err := st.SetPermission(t.Context(), user.ID, tag, actions); err != nil {
				t.Fatal(err)
			}
		}
		return login(t, srv, name, testPassword)
	}
	view := authz.Presets["Viewer"]
	return networkFixture{
		admin:    login(t, srv, "pavel", testPassword),
		devOnly:  addUser("devonly", map[authz.TagID]authz.ActionSet{tagID["Dev"]: view}),
		prodOnly: addUser("prodonly", map[authz.TagID]authz.ActionSet{tagID["Prod"]: view}),
		none:     addUser("nobody", nil),
	}
}

func getJSON[T any](t *testing.T, c *apiClient, path string) T {
	t.Helper()
	resp := c.get(path)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", path, resp.StatusCode)
	}
	var out T
	decode(t, resp, &out)
	return out
}

func expectError(t *testing.T, c *apiClient, path string, status int, code string) {
	t.Helper()
	resp := c.get(path)
	if resp.StatusCode != status {
		t.Fatalf("GET %s: status %d, want %d", path, resp.StatusCode, status)
	}
	if got, _ := errorCode(t, resp); got != code {
		t.Fatalf("GET %s: error code %q, want %q", path, got, code)
	}
}

func accountEntry(t *testing.T, out NetworksOverviewDTO, accountID string) NetworkAccountDTO {
	t.Helper()
	for _, item := range out.Items {
		if item.AccountID == accountID {
			return item
		}
	}
	t.Fatalf("account %s missing from %+v", accountID, out.Items)
	return NetworkAccountDTO{}
}

func regionEntry(t *testing.T, acct NetworkAccountDTO, region string) NetworkRegionDTO {
	t.Helper()
	for _, item := range acct.Regions {
		if item.Region == region {
			return item
		}
	}
	t.Fatalf("region %s missing from %+v", region, acct.Regions)
	return NetworkRegionDTO{}
}

// Expected counts are derived from fake.MockupEstate (one poll): homelab-admin us-east-1 runs 14 tasks
// in 3 clusters. homelab-main (3 subnets) hosts background-jobs and core-services (11 tasks, of which
// core-services is the Prod one with 9); shared-services (2 subnets) hosts ci-tools (3 tasks incl. the
// standalone release-build); legacy-default runs nothing and is hidden. The brief's 12/10/2 come from the
// mockup screenshots and differ from the seeded estate.
const (
	wantEastTasks         = 14
	wantEastClusters      = 3
	wantCoreServicesTasks = 9
	wantMainTasks         = 11
	wantSharedTasks       = 3
)

func TestNetworks_AdminSeesAllAccountsAndDimmedRegions(t *testing.T) {
	fx := newNetworkFixture(t)
	out := getJSON[NetworksOverviewDTO](t, fx.admin, "/api/networks")
	homelab := accountEntry(t, out, homelabAccountID)
	if homelab.AccountName != "homelab-admin" || len(homelab.Regions) != 3 {
		t.Fatalf("homelab = %+v", homelab)
	}
	east := regionEntry(t, homelab, "us-east-1")
	if east.ActiveVPCs != 2 || east.HiddenVPCs != 1 || east.UnplacedTasks != 0 {
		t.Errorf("us-east-1 = %+v", east)
	}
	if east.Tasks != wantEastTasks || east.Clusters != wantEastClusters {
		t.Errorf("us-east-1 tasks/clusters = %d/%d, want %d/%d", east.Tasks, east.Clusters, wantEastTasks, wantEastClusters)
	}
	staging := accountEntry(t, out, stagingAccountID)
	if len(staging.Regions) != 1 || staging.Regions[0].Region != "us-east-1" {
		t.Errorf("staging regions = %+v", staging.Regions)
	}
	if out.PolledAt.IsZero() {
		t.Error("polled_at is zero")
	}
}

func TestNetworks_DevUserSeesOnlyDevTasks(t *testing.T) {
	fx := newNetworkFixture(t)
	admin := regionEntry(t, accountEntry(t, getJSON[NetworksOverviewDTO](t, fx.admin, "/api/networks"), homelabAccountID), "us-east-1")
	dev := regionEntry(t, accountEntry(t, getJSON[NetworksOverviewDTO](t, fx.devOnly, "/api/networks"), homelabAccountID), "us-east-1")
	if dev.Tasks >= admin.Tasks || dev.Tasks == 0 {
		t.Errorf("dev tasks = %d, admin = %d; want 0 < dev < admin (core-services is Prod)", dev.Tasks, admin.Tasks)
	}
	if dev.Tasks != admin.Tasks-wantCoreServicesTasks {
		t.Errorf("dev tasks = %d, want %d", dev.Tasks, admin.Tasks-wantCoreServicesTasks)
	}
	if dev.HiddenVPCs != admin.HiddenVPCs {
		t.Errorf("hidden_vpcs changed: dev %d, admin %d", dev.HiddenVPCs, admin.HiddenVPCs)
	}
}

func TestNetworks_NoPermissionsIsEmptyRegions(t *testing.T) {
	fx := newNetworkFixture(t)
	out := getJSON[NetworksOverviewDTO](t, fx.none, "/api/networks")
	if len(out.Items) == 0 {
		t.Fatal("no accounts listed")
	}
	for _, acct := range out.Items {
		if len(acct.Regions) == 0 {
			t.Errorf("%s has no regions", acct.AccountName)
		}
		for _, region := range acct.Regions {
			if region.ActiveVPCs != 0 || region.Tasks != 0 || region.Clusters != 0 {
				t.Errorf("%s %s = %+v, want empty", acct.AccountName, region.Region, region)
			}
		}
	}
}

func TestNetworks_FilterByAccountAndRegion(t *testing.T) {
	fx := newNetworkFixture(t)
	out := getJSON[NetworksOverviewDTO](t, fx.admin, "/api/networks?account="+stagingAccountID+"&region=us-east-1")
	if len(out.Items) != 1 || out.Items[0].AccountID != stagingAccountID || len(out.Items[0].Regions) != 1 || out.Items[0].Regions[0].Region != "us-east-1" {
		t.Fatalf("filtered = %+v", out.Items)
	}
}

func TestNetworkRegion_ListsActiveVPCsOnly(t *testing.T) {
	fx := newNetworkFixture(t)
	out := getJSON[NetworkRegionVPCsDTO](t, fx.admin, "/api/networks/"+homelabAccountID+"/us-east-1")
	byID := map[string]NetworkVPCCardDTO{}
	for _, item := range out.Items {
		byID[item.VPCID] = item
	}
	if _, ok := byID[vpcLegacy]; ok {
		t.Error("legacy-default (no tasks) must be hidden")
	}
	main, shared := byID[vpcMain], byID[vpcShared]
	if main.Name != "homelab-main" || main.CIDR != "10.20.0.0/16" || main.Subnets != 3 || main.Tasks != wantMainTasks {
		t.Errorf("homelab-main = %+v", main)
	}
	if len(main.Clusters) != 2 || main.Clusters[0] != "background-jobs" || main.Clusters[1] != "core-services" {
		t.Errorf("homelab-main clusters = %v", main.Clusters)
	}
	if shared.Name != "shared-services" || shared.Subnets != 2 || shared.Tasks != wantSharedTasks {
		t.Errorf("shared-services = %+v", shared)
	}
}

func TestNetworkRegion_UnknownAccountIs404(t *testing.T) {
	fx := newNetworkFixture(t)
	expectError(t, fx.admin, "/api/networks/000000000000/us-east-1", http.StatusNotFound, "not_found")
}

func TestNetworkRegion_RegionNotScannedIs404(t *testing.T) {
	fx := newNetworkFixture(t)
	expectError(t, fx.admin, "/api/networks/"+stagingAccountID+"/eu-west-1", http.StatusNotFound, "not_found")
}

func TestVPCTree_RequiresAccountAndRegion(t *testing.T) {
	fx := newNetworkFixture(t)
	expectError(t, fx.admin, "/api/vpcs/"+vpcMain+"/tree", http.StatusUnprocessableEntity, "invalid")
	expectError(t, fx.admin, "/api/vpcs/"+vpcMain+"/tree?account="+homelabAccountID, http.StatusUnprocessableEntity, "invalid")
}

func TestVPCTree_InvisibleVPCIs404(t *testing.T) {
	fx := newNetworkFixture(t)
	q := "?account=" + homelabAccountID + "&region=us-east-1"
	expectError(t, fx.prodOnly, "/api/vpcs/"+vpcShared+"/tree"+q, http.StatusNotFound, "not_found")
	expectError(t, fx.none, "/api/vpcs/"+vpcMain+"/tree"+q, http.StatusNotFound, "not_found")
	expectError(t, fx.admin, "/api/vpcs/"+vpcLegacy+"/tree"+q, http.StatusNotFound, "not_found")
	expectError(t, fx.admin, "/api/vpcs/vpc-doesnotexist/tree"+q, http.StatusNotFound, "not_found")
}

func TestVPCTree_ReturnsTreeEdgesSummary(t *testing.T) {
	fx := newNetworkFixture(t)
	out := getJSON[VPCTreeDTO](t, fx.admin, "/api/vpcs/"+vpcMain+"/tree?account="+homelabAccountID+"&region=us-east-1")
	if out.VPC.VPCID != vpcMain || out.VPC.AccountName != "homelab-admin" || out.VPC.Region != "us-east-1" || out.VPC.CIDR != "10.20.0.0/16" {
		t.Errorf("vpc = %+v", out.VPC)
	}
	endpoints := 0
	for _, edge := range out.Edges {
		if edge.Kind == "endpoint" {
			endpoints++
		}
	}
	if endpoints != 4 {
		t.Errorf("endpoints = %d, want 4 (edges %+v)", endpoints, out.Edges)
	}
	if out.Summary.RunningTasks != wantMainTasks || out.Summary.Subnets != 3 || out.Summary.CollapsedByDefault {
		t.Errorf("summary = %+v", out.Summary)
	}
	if kind := treeRootKind(t, out); kind != "vpc" {
		t.Errorf("root kind = %q", kind)
	}
	if out.PolledAt.IsZero() {
		t.Error("polled_at is zero")
	}
}

func TestVPCTree_ProdOnlySeesOnlyCoreServices(t *testing.T) {
	fx := newNetworkFixture(t)
	out := getJSON[VPCTreeDTO](t, fx.prodOnly, "/api/vpcs/"+vpcMain+"/tree?account="+homelabAccountID+"&region=us-east-1")
	clusters := clusterLabels(t, out)
	if len(clusters) != 1 || clusters[0] != "core-services" {
		t.Errorf("clusters = %v, want only core-services", clusters)
	}
	if out.Summary.RunningTasks != wantCoreServicesTasks {
		t.Errorf("running_tasks = %d, want %d", out.Summary.RunningTasks, wantCoreServicesTasks)
	}
}

// treeRootKind and clusterLabels read the root through its JSON-decoded form.
func treeRootKind(t *testing.T, out VPCTreeDTO) string {
	t.Helper()
	root, ok := out.Root.(map[string]any)
	if !ok {
		t.Fatalf("root = %T", out.Root)
	}
	kind, _ := root["kind"].(string)
	return kind
}

func clusterLabels(t *testing.T, out VPCTreeDTO) []string {
	t.Helper()
	var labels []string
	var walk func(node any)
	walk = func(node any) {
		m, ok := node.(map[string]any)
		if !ok {
			return
		}
		// A cluster node repeats under every subnet it runs in; list each name once.
		if label, _ := m["label"].(string); m["kind"] == "cluster" && !slices.Contains(labels, label) {
			labels = append(labels, label)
		}
		if children, ok := m["children"].([]any); ok {
			for _, child := range children {
				walk(child)
			}
		}
	}
	walk(out.Root)
	return labels
}
