package api

import (
	"net/http"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/store"
)

func TestOperationsAggregate_KPIsSeriesAndTables(t *testing.T) {
	w := clampWindow(Range7d, 0, analyticsNow)
	buckets := []store.OperationBucketCount{
		{Bucket: w.from, Kind: "deploy", Count: 3},
		{Bucket: w.from, Kind: "restart", Count: 1},
		{Bucket: w.from.Add(w.bucket), Kind: "rollback", Count: 1},
		{Bucket: w.from.Add(w.bucket), Kind: "restart", Count: 3},
		{Bucket: w.from.Add(2 * w.bucket), Kind: "task_definition", Count: 2},
		{Bucket: w.from.Add(2 * w.bucket), Kind: "scale", Count: 4},
		{Bucket: w.from.Add(2 * w.bucket), Kind: "stop_task", Count: 5},
		{Bucket: w.from.Add(2 * w.bucket), Kind: "terminal", Count: 6},
	}
	operators := []store.OperatorCounts{
		{Username: "sam", Counts: map[string]int{"deploy": 3, "rollback": 1, "restart": 2}, LastAt: analyticsNow},
		{Username: "omer", Counts: map[string]int{"scale": 4, "terminal": 6}, LastAt: analyticsNow},
	}
	got := aggregateOperations(buckets, operators, w)

	want := OperationsKPIsDTO{Operations: 25, People: 2, RevisionsRegistered: 2, ScaleChanges: 4, Restarts: 4, TasksStopped: 5, TerminalSessions: 6}
	if got.kpis != want {
		t.Errorf("kpis = %+v, want %+v", got.kpis, want)
	}
	line := func(s SeriesDTO, key string) SeriesLineDTO {
		for _, l := range s.Series {
			if l.Key == key {
				return l
			}
		}
		t.Fatalf("series has no %s line", key)
		return SeriesLineDTO{}
	}
	if v := line(got.byType, "deploy").Values; *v[0] != 3 || *v[1] != 0 {
		t.Errorf("deploy values = %v %v", *v[0], *v[1])
	}
	if len(got.byType.Series) != 7 || len(got.handsOn.Series) != 3 {
		t.Errorf("lines = %d/%d, want 7/3", len(got.byType.Series), len(got.handsOn.Series))
	}
	ratio := line(got.restartsPer100, "restarts_per_100_deploys").Values
	// bucket 0: 1 restart / 3 deploys; bucket 1: 3 restarts / 1 rollback; bucket 2: no deploys -> null.
	if ratio[0] == nil || *ratio[0] < 33.3 || *ratio[0] > 33.4 || ratio[1] == nil || *ratio[1] != 300 || ratio[2] != nil {
		t.Errorf("restarts per 100 deploys = %v %v %v", ratio[0], ratio[1], ratio[2])
	}
}

func TestOperationsRows_CapAndColumns(t *testing.T) {
	operators := []store.OperatorCounts{{Username: "sam", Counts: map[string]int{"deploy": 3, "rollback": 1, "restart": 2, "scale": 1, "terminal": 1, "stop_task": 1}, LastAt: analyticsNow}}
	table := operatorRows(operators)
	row := table.Rows[0]
	if table.Total != 1 || row.Total != 9 || row.Deploy != 4 || row.Restart != 2 || row.Scale != 1 || row.Terminal != 1 {
		t.Errorf("row = %+v total %d", row, table.Total)
	}
	many := make([]store.OperatorCounts, 60)
	for i := range many {
		many[i] = store.OperatorCounts{Username: "u", Counts: map[string]int{"deploy": 1}}
	}
	if capped := operatorRows(many); len(capped.Rows) != 50 || capped.Total != 60 {
		t.Errorf("capped rows/total = %d/%d, want 50/60", len(capped.Rows), capped.Total)
	}
}

func TestAnalyticsOperations_RevisionsRegisteredFromOperationLog(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	setPassword(t, st, "omer")

	sam, err := st.GetUserByUsername(t.Context(), "sam")
	if err != nil {
		t.Fatal(err)
	}
	services, err := st.ListServices(t.Context(), store.TargetFilter{}, "", store.Visibility{Admin: true})
	if err != nil || len(services) == 0 {
		t.Fatalf("services = %d, %v", len(services), err)
	}
	svc := services[0].Key
	for _, details := range []map[string]any{
		{"op": "task_definition", "revision": 9, "task_definition_arn": "arn"},
		{"op": "restart", "deployment_id": "d1"},
		{"op": "restart", "deployment_id": "d2"},
		{"op": "outcome", "deployment_id": "d2"},
	} {
		if err := st.AppendOperation(t.Context(), store.Operation{
			AccountID: svc.AccountID, Region: svc.Region, Cluster: svc.Cluster, Service: &svc.Service,
			ActorUserID: &sam.ID, Source: "harbormaster", Text: "x", Details: details,
		}); err != nil {
			t.Fatal(err)
		}
	}

	// One scale per service, to compare what a tag-limited user sees with the admin.
	for _, other := range services {
		key := other.Key
		if err := st.AppendOperation(t.Context(), store.Operation{
			AccountID: key.AccountID, Region: key.Region, Cluster: key.Cluster, Service: &key.Service,
			ActorUserID: &sam.ID, Source: "harbormaster", Text: "x", Details: map[string]any{"op": "scale"},
		}); err != nil {
			t.Fatal(err)
		}
	}

	// The window ends at "now" truncated to 5 minutes, so move the rows into the past.
	if _, err := st.Pool.Exec(t.Context(), `update operation_log set ts = ts - interval '1 hour'`); err != nil {
		t.Fatal(err)
	}

	admin := login(t, srv, "sam", testPassword)
	resp := admin.get("/api/analytics/operations?range=7d")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	etag := resp.Header.Get("ETag")
	var out OperationsDTO
	decode(t, resp, &out)
	if out.Tab != "operations" || out.KPIs.RevisionsRegistered != 1 || out.KPIs.Restarts != 2 || out.KPIs.Operations != 3+len(services) || out.KPIs.ScaleChanges != len(services) || out.KPIs.People != 1 {
		t.Fatalf("kpis = %+v tab %q", out.KPIs, out.Tab)
	}
	if len(out.Who.Rows) != 1 || out.Who.Rows[0].Username != "sam" {
		t.Errorf("who = %+v", out.Who)
	}
	if len(out.MostRestarted.Rows) != 1 || out.MostRestarted.Rows[0].Restarts != 2 || out.MostRestarted.Rows[0].Service != svc.Service {
		t.Errorf("most restarted = %+v", out.MostRestarted)
	}
	tables := map[string]bool{}
	for _, s := range out.Sources {
		tables[s.Table] = true
	}
	if !tables["operation_log"] || !tables["revision_records"] {
		t.Errorf("sources = %+v", out.Sources)
	}

	if again := admin.do(http.MethodGet, "/api/analytics/operations?range=7d", nil, map[string]string{"If-None-Match": etag}); again.StatusCode != http.StatusNotModified {
		t.Errorf("repeat = %d, want 304", again.StatusCode)
	}

	omer := login(t, srv, "omer", testPassword)
	var narrow OperationsDTO
	decode(t, omer.get("/api/analytics/operations?range=7d"), &narrow)
	if narrow.KPIs.ScaleChanges >= out.KPIs.ScaleChanges {
		t.Errorf("omer sees %d scale changes, admin %d; omer must see only his tags", narrow.KPIs.ScaleChanges, out.KPIs.ScaleChanges)
	}
	bad := admin.get("/api/analytics/operations?range=1y")
	if code, _ := errorCode(t, bad); code != "invalid" || bad.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("range=1y -> %d %q", bad.StatusCode, code)
	}
}
