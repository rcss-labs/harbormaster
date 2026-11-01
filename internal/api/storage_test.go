package api

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/metrics"
)

const (
	fsJenkins = "fs-0c91a4e7"
	fsCerts   = "fs-0a6b33f0"
	fsSearch  = "fs-0d18c2ab"
	fsUploads = "fs-07a2b9e1"
)

// failingFetcher makes every EFS metrics read fail with an internal-looking error.
type failingFetcher struct{}

func (failingFetcher) Fetch(context.Context, awsx.Target, metrics.Span, []metrics.Query) (*metrics.Result, error) {
	return nil, errors.New("dial tcp 10.1.2.3:443: connection refused (internal detail)")
}

func storageItem(t *testing.T, out StorageDTO, id string) StorageItemDTO {
	t.Helper()
	for _, item := range out.Items {
		if item.FileSystemID == id {
			return item
		}
	}
	t.Fatalf("file system %s missing from %d items", id, len(out.Items))
	return StorageItemDTO{}
}

func TestStorage_AdminSeesNotMounted(t *testing.T) {
	fx := newNetworkFixture(t)
	out := getJSON[StorageDTO](t, fx.admin, "/api/storage")
	if len(out.Items) != 5 || out.Totals.Mounted != 4 || out.Totals.NotMounted != 1 {
		t.Fatalf("items %d totals %+v, want 5 items, 4 mounted, 1 not mounted", len(out.Items), out.Totals)
	}
	uploads := storageItem(t, out, fsUploads)
	if uploads.Mounted || uploads.MountedBy == nil || len(uploads.MountedBy) != 0 || uploads.MountingTasks != 0 {
		t.Errorf("uploads-archive = %+v, want not mounted with empty mounted_by", uploads)
	}
	jenkins := storageItem(t, out, fsJenkins)
	if jenkins.Name != "jenkins-home" || jenkins.VPC == nil || jenkins.VPC.Name != "shared-services" {
		t.Errorf("jenkins-home = %+v, want vpc shared-services", jenkins)
	}
	if len(jenkins.AccessPoints) != 1 || jenkins.AccessPoints[0].Path != "/jenkins" || jenkins.AccessPoints[0].UID == nil || *jenkins.AccessPoints[0].UID != 1000 {
		t.Errorf("jenkins access points = %+v", jenkins.AccessPoints)
	}
	if len(jenkins.Size30d) == 0 || len(jenkins.Throughput24h) == 0 || jenkins.GrowthPct == nil {
		t.Errorf("jenkins metrics: size %d throughput %d growth %v", len(jenkins.Size30d), len(jenkins.Throughput24h), jenkins.GrowthPct)
	}
	if out.MetricsError != "" || out.PolledAt.IsZero() {
		t.Errorf("metrics_error %q polled_at %v", out.MetricsError, out.PolledAt)
	}
	var sum int64
	for _, item := range out.Items {
		sum += item.SizeBytes
	}
	if out.Totals.SizeBytes != sum || sum == 0 {
		t.Errorf("totals.size_bytes = %d, sum of items = %d", out.Totals.SizeBytes, sum)
	}
}

func TestStorage_DevUserSeesOnlyMounted(t *testing.T) {
	fx := newNetworkFixture(t)
	out := getJSON[StorageDTO](t, fx.devOnly, "/api/storage")
	if len(out.Items) != 4 || out.Totals.Mounted != 4 || out.Totals.NotMounted != 0 {
		t.Fatalf("items %d totals %+v, want 4 mounted", len(out.Items), out.Totals)
	}
	for _, item := range out.Items {
		if item.FileSystemID == fsUploads || !item.Mounted {
			t.Errorf("dev user sees %+v", item)
		}
	}
}

func TestStorage_ProdOnlySeesNothing(t *testing.T) {
	fx := newNetworkFixture(t)
	for name, c := range map[string]*apiClient{"prodOnly": fx.prodOnly, "none": fx.none} {
		out := getJSON[StorageDTO](t, c, "/api/storage")
		if out.Items == nil || len(out.Items) != 0 || out.Totals.Mounted != 0 || out.Totals.NotMounted != 0 {
			t.Errorf("%s: items %v totals %+v, want empty", name, out.Items, out.Totals)
		}
	}
}

func TestStorage_MountedByHasPathAndTaskCount(t *testing.T) {
	fx := newNetworkFixture(t)
	out := getJSON[StorageDTO](t, fx.admin, "/api/storage")
	certs := storageItem(t, out, fsCerts)
	if len(certs.MountedBy) != 2 {
		t.Fatalf("shared-certs mounted_by = %+v", certs.MountedBy)
	}
	nginx, renewer := certs.MountedBy[0], certs.MountedBy[1]
	if nginx.Service != "edge-nginx" || nginx.ContainerPath != "/etc/nginx/certs" || nginx.Tasks != 2 || nginx.Cluster != "edge-proxy" || nginx.ServiceID == "" {
		t.Errorf("first = %+v, want edge-nginx /etc/nginx/certs 2 tasks", nginx)
	}
	if renewer.Service != "cert-renewer" || renewer.ContainerPath != "/certs" || renewer.Tasks != 1 {
		t.Errorf("second = %+v, want cert-renewer /certs 1 task", renewer)
	}
	if certs.MountingTasks != 3 {
		t.Errorf("mounting_tasks = %d, want 3", certs.MountingTasks)
	}
	if certs.ClientConnections == nil || *certs.ClientConnections != 3 {
		t.Errorf("client_connections = %v, want 3 (fake CloudWatch)", certs.ClientConnections)
	}
}

func TestStorage_MetricsFailureStillListsFileSystems(t *testing.T) {
	fx := newNetworkFixtureWith(t, func(s *Server) { s.efsFetcher = failingFetcher{} })
	out := getJSON[StorageDTO](t, fx.admin, "/api/storage")
	if len(out.Items) != 5 {
		t.Fatalf("items = %d, want 5", len(out.Items))
	}
	if out.MetricsError == "" {
		t.Error("metrics_error is empty")
	}
	if strings.Contains(out.MetricsError, "10.1.2.3") {
		t.Errorf("metrics_error leaks internals: %q", out.MetricsError)
	}
	for _, item := range out.Items {
		if item.Size30d != nil || item.Throughput24h != nil || item.GrowthPct != nil || item.ClientConnections != nil {
			t.Errorf("%s has metrics despite failure: %+v", item.FileSystemID, item)
		}
		if item.SizeBytes == 0 {
			t.Errorf("%s lost its stored size", item.FileSystemID)
		}
	}
}

func TestStorage_MountOfUnknownFileSystemIgnored(t *testing.T) {
	srv, fk, st, _ := newServerCustom(t, true, nil)
	seedAccounts(t, st)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	if _, err := st.Pool.Exec(t.Context(),
		`update task_definitions set raw = replace(raw::text, $1, 'fs-deadbeef')::jsonb`, fsJenkins); err != nil {
		t.Fatal(err)
	}
	out := getJSON[StorageDTO](t, login(t, srv, "pavel", testPassword), "/api/storage")
	if len(out.Items) != 5 {
		t.Fatalf("items = %d, want 5", len(out.Items))
	}
	for _, item := range out.Items {
		if item.FileSystemID == "fs-deadbeef" {
			t.Fatal("phantom item for an unknown file system")
		}
	}
	if jenkins := storageItem(t, out, fsJenkins); jenkins.Mounted {
		t.Errorf("jenkins-home still mounted: %+v", jenkins)
	}
}

func TestStorage_FilterByRegion(t *testing.T) {
	fx := newNetworkFixture(t)
	out := getJSON[StorageDTO](t, fx.admin, "/api/storage?region=eu-west-1")
	if len(out.Items) != 1 || out.Items[0].FileSystemID != fsSearch || out.Items[0].Name != "search-index" {
		t.Fatalf("items = %+v, want only search-index", out.Items)
	}
	if out.Totals.Mounted != 1 || out.Totals.NotMounted != 0 {
		t.Errorf("totals = %+v", out.Totals)
	}
}
