package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/store"
)

type searchResponse struct {
	Items []SearchItemDTO `json:"items"`
}

func search(t *testing.T, c *apiClient, q string) searchResponse {
	t.Helper()
	resp := c.get("/api/search?q=" + q)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("search %q: status %d", q, resp.StatusCode)
	}
	var out searchResponse
	decode(t, resp, &out)
	return out
}

func TestSearch_RespectsVisibility(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)

	if got := search(t, login(t, srv, "omer", testPassword), "pay"); len(got.Items) != 0 {
		t.Errorf("omer search pay = %+v, want none", got.Items)
	}
	got := search(t, login(t, srv, "pavel", testPassword), "pay")
	var found bool
	for _, item := range got.Items {
		if item.Kind == "service" && item.Label == "payments-api" {
			found = true
			if item.Href != "/services/"+paymentsID()+"/overview" {
				t.Errorf("href = %q", item.Href)
			}
		}
	}
	if !found {
		t.Errorf("admin search pay = %+v, want payments-api", got.Items)
	}
}

func TestSearch_ShortQueryAndClusterHref(t *testing.T) {
	srv, fk, st := newTestServer(t)
	pollAll(t, st, fk)
	c := login(t, srv, "pavel", testPassword)
	if got := search(t, c, "p"); got.Items == nil || len(got.Items) != 0 {
		t.Errorf("short query items = %#v, want empty non-nil", got.Items)
	}
	got := search(t, c, "core")
	if len(got.Items) == 0 || got.Items[0].Kind != "cluster" || got.Items[0].Href != "/clusters/"+clusterID(homelabAccountID, "us-east-1", "core-services") {
		t.Errorf("core search = %+v", got.Items)
	}
}

func TestOperations_ToggleECS(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	c := login(t, srv, "pavel", testPassword)
	path := "/api/services/" + paymentsID() + "/operations"

	var withECS, without ListResponse[EventDTO]
	decodeGet := func(p string, out *ListResponse[EventDTO]) {
		resp := c.get(p)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: status %d", p, resp.StatusCode)
		}
		decode(t, resp, out)
	}
	decodeGet(path, &withECS)
	decodeGet(path+"?include_ecs=false", &without)
	if len(withECS.Items) == 0 {
		t.Fatal("no operations with ECS events")
	}
	for _, e := range without.Items {
		if e.Source == "ecs" {
			t.Errorf("ecs event with include_ecs=false: %+v", e)
		}
	}
	if len(without.Items) >= len(withECS.Items) {
		t.Errorf("include_ecs=false returned %d, with ECS %d", len(without.Items), len(withECS.Items))
	}
	for _, e := range withECS.Items {
		if e.Source == "ecs" && e.Actor != "ECS" {
			t.Errorf("ecs actor = %q", e.Actor)
		}
	}

	// paging: limit 1 yields a cursor that moves strictly backwards in time
	var first, second ListResponse[EventDTO]
	decodeGet(path+"?limit=1", &first)
	if len(first.Items) != 1 || first.Next == nil {
		t.Fatalf("first page = %+v", first)
	}
	decodeGet(path+"?limit=1&cursor="+*first.Next, &second)
	if len(second.Items) != 1 || second.Items[0].TS.After(first.Items[0].TS) {
		t.Errorf("second page = %+v after %+v", second.Items, first.Items)
	}
	resp := c.get(path + "?cursor=!!!")
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("bad cursor status %d, want 422", resp.StatusCode)
	}
	resp.Body.Close()
	var last ListResponse[EventDTO]
	decodeGet(path+"?limit=500", &last)
	if last.Next != nil {
		t.Errorf("unfilled page next = %v, want null", *last.Next)
	}
}

func TestPoller_OnlyVisibleTargets(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedAccounts(t, st)
	seedMockupTags(t, st)
	pollAll(t, st, fk)

	var admin, omer struct {
		Items []PollerItemDTO `json:"items"`
	}
	resp := login(t, srv, "pavel", testPassword).get("/api/poller")
	decode(t, resp, &admin)
	resp = login(t, srv, "omer", testPassword).get("/api/poller")
	decode(t, resp, &omer)

	// the estate polls 4 targets: homelab in three regions, staging in us-east-1
	if len(admin.Items) != 4 {
		t.Fatalf("admin poller items = %+v, want 4", admin.Items)
	}
	for _, item := range admin.Items {
		if item.AccountName == "" || item.LastOKAt == nil || item.Failing || item.Throttled {
			t.Errorf("item = %+v", item)
		}
	}
	// omer sees every cluster except core-services, so all homelab regions remain, staging-web (Staging, view) too
	if len(omer.Items) != 4 {
		t.Errorf("omer poller items = %d, want 4", len(omer.Items))
	}
}

func TestPoller_HidesTargetsWithoutVisibleClusters(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	setPassword(t, st, "oncall")
	pollAll(t, st, fk)
	var got struct {
		Items []PollerItemDTO `json:"items"`
	}
	decode(t, login(t, srv, "oncall", testPassword).get("/api/poller"), &got)
	// oncall sees only core-services (homelab us-east-1) and staging-web (staging us-east-1)
	if len(got.Items) != 2 {
		t.Fatalf("oncall poller items = %+v, want 2", got.Items)
	}
	for _, item := range got.Items {
		if item.Region != "us-east-1" {
			t.Errorf("unexpected target %+v", item)
		}
	}
}

func TestPoller_LastErrorOnlyForAdminsOrFailingTargets(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedAccounts(t, st)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	ctx := t.Context()
	now := time.Now()
	target := awsx.Target{AccountID: homelabAccountID, Region: "us-east-1"}
	// not failing: one failure with an error text naming a resource
	if err := st.UpsertPollLoop(ctx, store.PollLoop{Target: target, Tier: "full_tasks", LastOKAt: &now,
		LastError: "AccessDenied on arn:aws:ecs:secret", Interval: time.Second, BaseInterval: time.Second, Failures: 1}); err != nil {
		t.Fatal(err)
	}
	errorFor := func(user string) (lastError string, failing bool) {
		var got struct {
			Items []PollerItemDTO `json:"items"`
		}
		decode(t, login(t, srv, user, testPassword).get("/api/poller"), &got)
		for _, item := range got.Items {
			if item.AccountID == target.AccountID && item.Region == target.Region {
				return item.LastError, item.Failing
			}
		}
		t.Fatalf("%s: target missing in %+v", user, got.Items)
		return "", false
	}
	if msg, failing := errorFor("pavel"); msg == "" || failing {
		t.Errorf("admin: last_error %q failing %v, want the error and not failing", msg, failing)
	}
	if msg, _ := errorFor("omer"); msg != "" {
		t.Errorf("omer sees last_error %q on a healthy target", msg)
	}
	if err := st.UpsertPollLoop(ctx, store.PollLoop{Target: target, Tier: "full_tasks", LastOKAt: &now,
		LastError: "AccessDenied on arn:aws:ecs:secret", Interval: time.Second, BaseInterval: time.Second, Failures: 5}); err != nil {
		t.Fatal(err)
	}
	if msg, failing := errorFor("omer"); msg == "" || !failing {
		t.Errorf("omer: last_error %q failing %v, want the error once failing", msg, failing)
	}
}
