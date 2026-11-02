package api

import (
	"math"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
)

func getCapacity(t *testing.T, c *apiClient, query string) CapacityDTO {
	t.Helper()
	resp := c.get("/api/analytics/capacity" + query)
	if resp.StatusCode != http.StatusOK {
		code, msg := errorCode(t, resp)
		t.Fatalf("capacity %q: status %d %s %s", query, resp.StatusCode, code, msg)
	}
	var out CapacityDTO
	decode(t, resp, &out)
	return out
}

func TestAnalyticsCapacity_IgnoresRangeAndReportsIt(t *testing.T) {
	srv, fk, st, _ := newServerCustom(t, true, withAnalyticsRetention)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	c := login(t, srv, "pavel", testPassword)
	got := getCapacity(t, c, "?range=24h")

	w := got.Window
	if w.Requested != "24h" || w.Used != "7d" || w.Clamped || w.RangeApplies || w.Bucket != "4h" {
		t.Fatalf("window = %+v, want requested 24h, used 7d, unclamped, range_applies false, bucket 4h", w)
	}
	if got.Tab != "capacity" || len(got.ReservedVCPU7d.Timestamps) != 42 {
		t.Errorf("tab %q with %d timestamps, want capacity and 42", got.Tab, len(got.ReservedVCPU7d.Timestamps))
	}
	if other := getCapacity(t, c, "?range=90d"); other.Window.Used != "7d" || !reflect.DeepEqual(other.KPIs, got.KPIs) {
		t.Errorf("range=90d changed the answer: window %+v", other.Window)
	}
	if resp := c.get("/api/analytics/capacity?range=1y"); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("range=1y status %d, want 422", resp.StatusCode)
	}
	for _, p := range []struct {
		name      string
		available bool
		table     string
	}{
		{"reserved_vs_used_90d", got.ReservedVsUsed90d.Available, got.ReservedVsUsed90d.Needs.Table},
		{"spot_share_90d", got.SpotShare90d.Available, got.SpotShare90d.Needs.Table},
		{"memory_90d", got.Memory90d.Available, got.Memory90d.Needs.Table},
		{"over_reserved", got.OverReserved.Available, got.OverReserved.Needs.Table},
	} {
		if p.available || p.table == "" {
			t.Errorf("%s = available %v needs %q, want unavailable with a needed table", p.name, p.available, p.table)
		}
	}
}

func TestAnalyticsCapacity_KPIsMatchEstateAndDonutsAddUp(t *testing.T) {
	srv, fk, st, _ := newServerCustom(t, true, withAnalyticsRetention)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	got := getCapacity(t, login(t, srv, "pavel", testPassword), "")

	var wantVCPU, wantGiB float64
	var wantTasks int
	for _, row := range got.Clusters.Rows {
		vcpu, gib, running := expectedReserved(fk, row.Cluster)
		wantVCPU, wantGiB, wantTasks = wantVCPU+vcpu, wantGiB+gib, wantTasks+running
		if row.Tasks != running || math.Abs(row.VCPU-vcpu) > 1e-9 || math.Abs(row.MemGiB-gib) > 1e-9 {
			t.Errorf("cluster %s = %d tasks %.3f vCPU %.3f GiB, want %d %.3f %.3f", row.Cluster, row.Tasks, row.VCPU, row.MemGiB, running, vcpu, gib)
		}
	}
	k := got.KPIs
	if k.RunningTasks != wantTasks || math.Abs(k.ReservedVCPU-wantVCPU) > 1e-9 || math.Abs(k.ReservedMemGiB-wantGiB) > 1e-9 {
		t.Errorf("kpis = %+v, want %d tasks %.3f vCPU %.3f GiB", k, wantTasks, wantVCPU, wantGiB)
	}
	if k.Services != 15 || wantTasks == 0 {
		t.Errorf("services %d tasks %d, want 15 services and running tasks", k.Services, wantTasks)
	}
	if math.Abs(got.VCPUByAccount.Total-wantVCPU) > 1e-9 || got.TasksByCapacityProvider.Total != float64(wantTasks) {
		t.Errorf("donut totals %v / %v, want %v / %d", got.VCPUByAccount.Total, got.TasksByCapacityProvider.Total, wantVCPU, wantTasks)
	}
	var sliceSum float64
	for _, slice := range got.VCPUByAccount.Slices {
		sliceSum += slice.Value
		if slice.Label == "" || slice.Key == "" {
			t.Errorf("account slice %+v needs a key and label", slice)
		}
	}
	if math.Abs(sliceSum-wantVCPU) > 1e-9 {
		t.Errorf("account slices sum to %v, want %v", sliceSum, wantVCPU)
	}
	var share float64
	for i, row := range got.Clusters.Rows {
		share += row.Share
		if i > 0 && row.VCPU > got.Clusters.Rows[i-1].VCPU {
			t.Errorf("clusters not sorted by vCPU descending at %d", i)
		}
	}
	if math.Abs(share-1) > 1e-9 {
		t.Errorf("cluster shares sum to %v, want 1", share)
	}
}

func TestAnalyticsCapacity_ReservedTrendStackedByTag(t *testing.T) {
	srv, fk, st, _ := newServerCustom(t, true, withAnalyticsRetention)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	var rows []model.Service
	for _, svc := range visibleServices(t, st) {
		rows = append(rows, svc.Service)
	}
	if err := st.InsertCountsHistory(t.Context(), time.Now().UTC().Add(-time.Hour), rows); err != nil {
		t.Fatal(err)
	}
	got := getCapacity(t, login(t, srv, "pavel", testPassword), "")
	if len(got.ReservedVCPU7d.Series) == 0 {
		t.Fatal("reserved_vcpu_7d has no lines")
	}
	var total float64
	for _, line := range got.ReservedVCPU7d.Series {
		if len(line.Values) != 42 || line.Label == "" {
			t.Fatalf("line %+v", line)
		}
		for _, v := range line.Values {
			if v == nil {
				t.Fatalf("line %q has a null value, want zero-filled", line.Key)
			}
			total += *v
		}
	}
	if total <= 0 {
		t.Errorf("reserved trend sums to %v, want some reserved vCPU", total)
	}
}

func TestAnalyticsCapacity_NonAdminSeesOnlyVisibleClusters(t *testing.T) {
	srv, fk, st, _ := newServerCustom(t, true, withAnalyticsRetention)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	admin := getCapacity(t, login(t, srv, "pavel", testPassword), "")
	omerClient := login(t, srv, "omer", testPassword)
	omer := getCapacity(t, omerClient, "")

	var visible struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	decode(t, omerClient.get("/api/clusters"), &visible)
	allowed := map[string]bool{}
	for _, item := range visible.Items {
		allowed[item.ID] = true
	}
	if len(omer.Clusters.Rows) == 0 || len(omer.Clusters.Rows) >= len(admin.Clusters.Rows) {
		t.Fatalf("omer sees %d clusters, admin %d; want a non-empty strict subset", len(omer.Clusters.Rows), len(admin.Clusters.Rows))
	}
	for _, row := range omer.Clusters.Rows {
		if !allowed[row.ClusterID] {
			t.Errorf("omer sees cluster %s that /api/clusters hides", row.ClusterID)
		}
	}
	if omer.KPIs.RunningTasks >= admin.KPIs.RunningTasks || omer.KPIs.Services >= admin.KPIs.Services {
		t.Errorf("omer kpis %+v not smaller than admin %+v", omer.KPIs, admin.KPIs)
	}
	if omer.Clusters.Total != len(omer.Clusters.Rows) {
		t.Errorf("clusters total %d for %d rows", omer.Clusters.Total, len(omer.Clusters.Rows))
	}
}
