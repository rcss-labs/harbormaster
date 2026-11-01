package api

import (
	"net/http"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

type tagIDs struct{ Dev, Staging, Prod string }

// lookupTagIDs resolves the seeded tag ids by name (never hard-code them).
func lookupTagIDs(t *testing.T, st *store.Store) tagIDs {
	t.Helper()
	tags, err := st.ListTags(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var ids tagIDs
	for _, tag := range tags {
		id := "tag=" + strconv.FormatInt(int64(tag.ID), 10)
		switch tag.Name {
		case "Dev":
			ids.Dev = id
		case "Staging":
			ids.Staging = id
		case "Prod":
			ids.Prod = id
		}
	}
	if ids.Dev == "" || ids.Staging == "" || ids.Prod == "" {
		t.Fatalf("seed tags missing: %+v", ids)
	}
	return ids
}

// estateServices returns the estate's services whose effective tag has the given query value ("tag=N").
func estateServices(t *testing.T, st *store.Store, f *fake.Fake, wantTag string) []fake.Service {
	t.Helper()
	tags, err := st.TagsSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	f.Estate.Mu.Lock()
	defer f.Estate.Mu.Unlock()
	var out []fake.Service
	for _, svc := range f.Estate.Services {
		key := model.ServiceKey{
			ClusterKey: model.ClusterKey{AccountID: svc.AccountID, Region: svc.Region, Cluster: svc.ClusterName},
			Service:    aws.ToString(svc.Service.ServiceName),
		}
		if "tag="+strconv.FormatInt(int64(authz.EffectiveServiceTag(tags, key)), 10) == wantTag {
			out = append(out, svc)
		}
	}
	return out
}

func sumCounts(services []fake.Service) (running, desired int) {
	for _, svc := range services {
		running += int(svc.Service.RunningCount)
		desired += int(svc.Service.DesiredCount)
	}
	return running, desired
}

func clusterNames(items []ClusterCardDTO) []string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.Name)
	}
	return names
}

func serviceNames(refs []ServiceRef) []string {
	names := make([]string, 0, len(refs))
	for _, ref := range refs {
		names = append(names, ref.Name)
	}
	return names
}

func clusterByName(t *testing.T, items []ClusterCardDTO, name string) ClusterCardDTO {
	t.Helper()
	for _, item := range items {
		if item.Name == name {
			return item
		}
	}
	t.Fatalf("cluster %q not in %v", name, clusterNames(items))
	return ClusterCardDTO{}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestTagFilter_ClustersAndServiceCounts(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	ids := lookupTagIDs(t, st)
	pavel := login(t, srv, "pavel", testPassword)

	prod := listClusters(t, pavel, "?"+ids.Prod)
	if len(prod.Items) != 2 {
		t.Fatalf("Prod clusters = %v, want core-services and ci-tools", clusterNames(prod.Items))
	}
	prodRunning, prodDesired := sumCounts(estateServices(t, st, fk, ids.Prod))
	core := clusterByName(t, prod.Items, "core-services")
	ci := clusterByName(t, prod.Items, "ci-tools")
	if len(core.Services) != 4 {
		t.Errorf("core-services services = %v, want 4", serviceNames(core.Services))
	}
	if !equalStrings(serviceNames(ci.Services), []string{"artifact-proxy"}) {
		t.Errorf("ci-tools under Prod = %v, want [artifact-proxy]", serviceNames(ci.Services))
	}
	if core.Running+ci.Running != prodRunning || core.Desired+ci.Desired != prodDesired {
		t.Errorf("Prod running/desired = %d/%d, want %d/%d from the estate", core.Running+ci.Running, core.Desired+ci.Desired, prodRunning, prodDesired)
	}
	if core.Running != 9 || core.Desired != 9 || ci.Running != 1 {
		t.Errorf("core-services %d/%d ci-tools running %d, want 9/9 and 1", core.Running, core.Desired, ci.Running)
	}

	if got := listClusters(t, pavel, "?"+ids.Staging); !equalStrings(clusterNames(got.Items), []string{"staging-web"}) {
		t.Errorf("Staging clusters = %v, want [staging-web]", clusterNames(got.Items))
	}
	if got := listClusters(t, pavel, "?"+ids.Prod+"&"+ids.Staging); len(got.Items) != 3 {
		t.Errorf("Prod+Staging clusters = %v, want 3", clusterNames(got.Items))
	}
	dev := listClusters(t, pavel, "?"+ids.Dev)
	if len(dev.Items) != 4 {
		t.Errorf("Dev clusters = %v, want 4", clusterNames(dev.Items))
	}
	if got := serviceNames(clusterByName(t, dev.Items, "ci-tools").Services); !equalStrings(got, []string{"jenkins-controller"}) {
		t.Errorf("ci-tools under Dev = %v, want [jenkins-controller]", got)
	}
}

func TestTagFilter_OverviewAndTasks(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	ids := lookupTagIDs(t, st)
	pavel := login(t, srv, "pavel", testPassword)

	overview := getOverview(t, pavel, "?"+ids.Prod)
	if len(overview.Clusters) != 2 || overview.Services.Total != 5 || overview.Tasks.Running != 10 {
		t.Errorf("Prod overview clusters %d services %d running %d, want 2, 5, 10",
			len(overview.Clusters), overview.Services.Total, overview.Tasks.Running)
	}

	prod := getTasks(t, pavel, "/api/tasks?"+ids.Prod+"&filter=running&limit=500")
	if prod.Counts.Running != 10 {
		t.Errorf("Prod running tasks = %d, want 10", prod.Counts.Running)
	}
	for _, task := range prod.Items {
		if task.Cluster.Name == "background-jobs" || (task.Service != nil && task.Service.Name == "release-build") {
			t.Errorf("Prod tasks include a Dev task: %s/%v", task.Cluster.Name, task.Service)
		}
	}

	dev := getTasks(t, pavel, "/api/tasks?"+ids.Dev+"&filter=running&limit=500")
	if dev.Counts.Running != 10 {
		t.Errorf("Dev running tasks = %d, want 10", dev.Counts.Running)
	}
	var standalone bool
	for _, task := range dev.Items {
		if task.Family == "release-build" || (task.Service != nil && task.Service.Name == "release-build") {
			standalone = true
		}
	}
	if !standalone {
		t.Error("Dev running tasks do not include the standalone release-build task")
	}
}

func TestTagFilter_NeverWidens(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	ids := lookupTagIDs(t, st)
	omer := login(t, srv, "omer", testPassword)

	if got := listClusters(t, omer, "?"+ids.Prod); len(got.Items) != 0 {
		t.Errorf("omer clusters under Prod = %v, want none", clusterNames(got.Items))
	}
	if got := getOverview(t, omer, "?"+ids.Prod); len(got.Clusters) != 0 {
		t.Errorf("omer overview under Prod has %d clusters, want 0", len(got.Clusters))
	}
	if got := listClusters(t, omer, "?"+ids.Dev); len(got.Items) != 4 {
		t.Errorf("omer clusters under Dev = %v, want 4", clusterNames(got.Items))
	}
	if got := listClusters(t, omer, "?tag=99999"); len(got.Items) != 0 {
		t.Errorf("omer clusters under unknown tag = %v, want none", clusterNames(got.Items))
	}
}

func TestTagFilter_InvalidIs422(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	resp := login(t, srv, "pavel", testPassword).get("/api/clusters?tag=abc")
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status %d, want 422", resp.StatusCode)
	}
	if code, _ := errorCode(t, resp); code != "invalid" {
		t.Errorf("code %q, want invalid", code)
	}
}

func TestTagFilter_Alerts(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	ids := lookupTagIDs(t, st)
	replaceAlerts(t, st, fk,
		serviceAlert(homelabAccountID, "us-east-1", "background-jobs", "image-resizer", model.AlertDanger, "image-resizer danger"),
		serviceAlert(homelabAccountID, "us-east-1", "core-services", "payments-api", model.AlertWarning, "payments-api warning"),
	)
	pavel := login(t, srv, "pavel", testPassword)

	// Account-level alerts are visible to an unfiltered admin but, under a tag filter, only where a
	// matching cluster exists in that account x region. This pins the current narrowing behaviour.
	allRegions := []string{"us-east-1", "eu-west-1", "eu-central-1"}
	var accountAlerts []targetAlert
	for _, region := range allRegions {
		accountAlerts = append(accountAlerts, accountAlert(homelabAccountID, region, model.AlertInfo, "account notice "+region))
	}
	replaceAlerts(t, st, fk, accountAlerts...)
	for _, region := range allRegions {
		title := "account notice " + region
		if got := itemTitles(getAlertsBody(t, pavel, "")); !slices.Contains(got, title) {
			t.Errorf("unfiltered admin alerts = %v, want %q", got, title)
		}
		for _, tag := range []string{ids.Dev, ids.Staging, ids.Prod} {
			hasCluster := false
			for _, cluster := range listClusters(t, pavel, "?"+tag).Items {
				hasCluster = hasCluster || (cluster.AccountID == homelabAccountID && cluster.Region == region)
			}
			if got := itemTitles(getAlertsBody(t, pavel, "?"+tag)); slices.Contains(got, title) != hasCluster {
				t.Errorf("%s under %s: alerts %v, visible want %v (a matching cluster exists: %v)", title, tag, got, hasCluster, hasCluster)
			}
		}
	}
	replaceAlerts(t, st, fk,
		serviceAlert(homelabAccountID, "us-east-1", "background-jobs", "image-resizer", model.AlertDanger, "image-resizer danger"),
		serviceAlert(homelabAccountID, "us-east-1", "core-services", "payments-api", model.AlertWarning, "payments-api warning"),
	)

	staging := getAlertsBody(t, pavel, "?"+ids.Staging)
	for _, item := range staging.Items {
		if item.Service == "image-resizer" {
			t.Errorf("Staging alerts include image-resizer: %v", itemTitles(staging))
		}
	}
	dev := getAlertsBody(t, pavel, "?"+ids.Dev)
	if got := itemTitles(dev); !equalStrings(got, []string{"image-resizer danger"}) {
		t.Errorf("Dev alerts = %v, want only the image-resizer alert", got)
	}
	if dev.Items[0].Kind != "service_failing" {
		t.Errorf("Dev alert kind = %q, want service_failing", dev.Items[0].Kind)
	}
	if dev.Counts.Danger != 1 || dev.Counts.Warning != 0 || dev.Counts.Info != 0 {
		t.Errorf("Dev alert counts = %+v, want danger 1 only", dev.Counts)
	}
}

func TestTagFilter_Search(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	ids := lookupTagIDs(t, st)
	pavel := login(t, srv, "pavel", testPassword)

	if got := search(t, pavel, "image&"+ids.Prod); len(got.Items) != 0 {
		t.Errorf("search image under Prod = %+v, want none", got.Items)
	}
	if got := search(t, pavel, "image&"+ids.Dev); len(got.Items) < 1 {
		t.Error("search image under Dev found nothing, want image-resizer")
	}
}

// accountAlert builds an alert that belongs to an account x region, not to any service.
func accountAlert(account, region string, severity model.AlertSeverity, title string) targetAlert {
	return targetAlert{
		target: awsx.Target{AccountID: account, Region: region},
		alert: model.Alert{
			Key:      model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: account, Region: region}},
			Kind:     "account_notice",
			Severity: severity,
			Title:    title,
			Text:     "detail",
			At:       time.Now().UTC(),
		},
	}
}
