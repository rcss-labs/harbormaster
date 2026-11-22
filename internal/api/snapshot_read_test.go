package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/deploy"
	"github.com/rcss-labs/harbormaster/internal/kv"
	"github.com/rcss-labs/harbormaster/internal/kv/kvtest"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/poller"
	"github.com/rcss-labs/harbormaster/internal/snapcache"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
	"github.com/rcss-labs/harbormaster/internal/telemetry"
)

// snapPair is two API servers over one database: one reads through the snapshot cache and one reads Postgres as it
// always did. Sessions live in the database, so a user signs in on each separately and sees the same data.
type snapPair struct {
	on, off *httptest.Server
	st      *store.Store
	fk      *fake.Fake
	cache   snapcache.Cache
	reader  *snapcache.Reader
	tel     *telemetry.Registry
}

const snapUserPassword = testPassword

func newSnapPair(t *testing.T, c kv.Client) *snapPair {
	t.Helper()
	ctx := t.Context()
	st := storetest.Open(t)
	hash, err := auth.HashPassword(snapUserPassword)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []struct {
		name  string
		admin bool
	}{{"sam", true}, {"omer", false}, {"viewer", false}, {"operator", false}} {
		if _, err := st.CreateUser(ctx, u.name, hash, u.admin, false); err != nil {
			t.Fatal(err)
		}
	}
	seedMockupTags(t, st)
	fk := fake.New(fake.MockupEstate(time.Now))
	p := &snapPair{st: st, fk: fk, tel: telemetry.New(), cache: snapcache.NewKV(c)}
	p.setPermission(t, "viewer", "Prod", "Viewer")
	p.setPermission(t, "operator", "Staging", "Operator")
	p.poll(t)
	p.reader = &snapcache.Reader{Cache: p.cache, Store: st, TTL: time.Minute, Telemetry: p.tel}
	p.on = p.serve(t, p.reader)
	p.off = p.serve(t, nil)
	return p
}

func (p *snapPair) serve(t *testing.T, reader *snapcache.Reader) *httptest.Server {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := testConfig()
	setup, err := auth.NewSetup(t.Context(), p.st, log)
	if err != nil {
		t.Fatal(err)
	}
	api := New(Server{
		Store: p.st, Auth: auth.NewService(p.st, cfg), Setup: setup,
		Clients: p.fk, Prober: p.fk, Cfg: cfg, Log: log, Now: time.Now, Snap: reader,
		Deploy: &deploy.Service{Store: p.st, Clients: p.fk, Scaling: p.st, MaxDesiredCount: 50, Now: time.Now},
	})
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)
	return srv
}

func (p *snapPair) tagID(t *testing.T, name string) authz.TagID {
	t.Helper()
	tags, err := p.st.ListTags(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, tag := range tags {
		if tag.Name == name {
			return tag.ID
		}
	}
	t.Fatalf("no tag %q", name)
	return 0
}

func (p *snapPair) setPermission(t *testing.T, username, tag, preset string) {
	t.Helper()
	users, err := p.st.ListUsers(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range users {
		if u.Username == username {
			if err := p.st.SetPermission(t.Context(), u.ID, p.tagID(t, tag), authz.Presets[preset]); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatalf("no user %q", username)
}

// poll runs one cycle for every slice with the cache attached, as the poller does: it leaves a token per cluster.
func (p *snapPair) poll(t *testing.T) {
	t.Helper()
	for _, account := range p.fk.Estate.Accounts {
		for _, region := range account.Regions {
			target := awsx.Target{AccountID: account.ID, Region: region}
			if err := poller.PollOnce(t.Context(), p.st, p.fk, poller.Config{MaxInFlight: 4, Cache: p.cache}, target); err != nil {
				t.Fatalf("poll %v: %v", target, err)
			}
		}
	}
}

func (p *snapPair) count(t *testing.T, model, result string) int {
	t.Helper()
	var buf bytes.Buffer
	if err := p.tel.WritePrometheus(&buf); err != nil {
		t.Fatal(err)
	}
	prefix := `harbormaster_snapshot_cache_requests_total{model="` + model + `",result="` + result + `"} `
	for _, line := range strings.Split(buf.String(), "\n") {
		if rest, ok := strings.CutPrefix(line, prefix); ok {
			n := 0
			for _, ch := range rest {
				n = n*10 + int(ch-'0')
			}
			return n
		}
	}
	return 0
}

// body returns the status and raw body of a GET.
func body(t *testing.T, c *apiClient, path string) (int, string) {
	t.Helper()
	resp := c.get(path)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(raw)
}

type snapIDs struct{ clusters, services []string }

func (p *snapPair) ids(t *testing.T) snapIDs {
	t.Helper()
	admin := login(t, p.off, "sam", snapUserPassword)
	var clusters ListResponse[ClusterCardDTO]
	resp := admin.get("/api/clusters?limit=500")
	decode(t, resp, &clusters)
	var services ServiceListDTO
	resp = admin.get("/api/services")
	decode(t, resp, &services)
	var ids snapIDs
	for _, c := range clusters.Items {
		ids.clusters = append(ids.clusters, c.ID)
	}
	for _, s := range services.Items {
		ids.services = append(ids.services, s.ID)
	}
	if len(ids.clusters) < 4 || len(ids.services) < 8 {
		t.Fatalf("the estate has %d clusters and %d services: too small to prove anything", len(ids.clusters), len(ids.services))
	}
	return ids
}

func TestClusterRead_SameBodyWithCacheOnAndOff(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, c kv.Client) {
		p := newSnapPair(t, c)
		ids := p.ids(t)
		tagProd := p.tagID(t, "Prod")
		suffixes := []string{"", "?tag=" + strconv.Itoa(int(tagProd))}
		for _, user := range []string{"sam", "viewer", "operator", "omer"} {
			on, off := login(t, p.on, user, snapUserPassword), login(t, p.off, user, snapUserPassword)
			for pass := 0; pass < 2; pass++ { // the second pass is served from the cache
				sawVisible := false
				for _, id := range ids.clusters {
					for _, suffix := range suffixes {
						gotStatus, got := body(t, on, "/api/clusters/"+id+suffix)
						wantStatus, want := body(t, off, "/api/clusters/"+id+suffix)
						if gotStatus != wantStatus || got != want {
							t.Fatalf("%s pass %d GET /api/clusters/%s%s\n cache on  (%d): %.400s\n cache off (%d): %.400s", user, pass, id, suffix, gotStatus, got, wantStatus, want)
						}
						sawVisible = sawVisible || wantStatus == http.StatusOK
					}
				}
				if user != "omer" && !sawVisible {
					t.Fatalf("%s saw no cluster at all: the comparison is vacuous", user)
				}
			}
		}
		if p.count(t, "cluster", "hit") == 0 {
			t.Fatal("the cache on server never served a hit")
		}
	})
}

func TestServiceRead_SameBodyWithCacheOnAndOff(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, c kv.Client) {
		p := newSnapPair(t, c)
		ids := p.ids(t)
		tagProd := p.tagID(t, "Prod")
		suffixes := []string{"", "?tag=" + strconv.Itoa(int(tagProd))}
		for _, user := range []string{"sam", "viewer", "operator", "omer"} {
			on, off := login(t, p.on, user, snapUserPassword), login(t, p.off, user, snapUserPassword)
			for pass := 0; pass < 2; pass++ {
				sawVisible := false
				for _, id := range ids.services {
					for _, suffix := range suffixes {
						gotStatus, got := body(t, on, "/api/services/"+id+suffix)
						wantStatus, want := body(t, off, "/api/services/"+id+suffix)
						if gotStatus != wantStatus || got != want {
							t.Fatalf("%s pass %d GET /api/services/%s%s\n cache on  (%d): %.400s\n cache off (%d): %.400s", user, pass, id, suffix, gotStatus, got, wantStatus, want)
						}
						sawVisible = sawVisible || wantStatus == http.StatusOK
					}
				}
				if user != "omer" && !sawVisible {
					t.Fatalf("%s saw no service at all: the comparison is vacuous", user)
				}
			}
		}
		if p.count(t, "service", "hit") == 0 {
			t.Fatal("the cache on server never served a hit")
		}
	})
}

func TestServiceTasks_SameBodyWithCacheOnAndOff(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, c kv.Client) {
		p := newSnapPair(t, c)
		ids := p.ids(t)
		for _, user := range []string{"sam", "viewer", "operator"} {
			on, off := login(t, p.on, user, snapUserPassword), login(t, p.off, user, snapUserPassword)
			for _, id := range ids.services {
				for _, filter := range []string{"", "?filter=all", "?filter=running&limit=2"} {
					gotStatus, got := body(t, on, "/api/services/"+id+"/tasks"+filter)
					wantStatus, want := body(t, off, "/api/services/"+id+"/tasks"+filter)
					if gotStatus != wantStatus || got != want {
						t.Fatalf("%s GET /api/services/%s/tasks%s\n cache on  (%d): %.300s\n cache off (%d): %.300s", user, id, filter, gotStatus, got, wantStatus, want)
					}
				}
			}
		}
	})
}

func paymentsServiceID(t *testing.T, p *snapPair) string {
	t.Helper()
	return model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: homelabAccountID, Region: "us-east-1", Cluster: "core-services"}, Service: "payments-api"}.ID()
}

func TestClusterRead_RevokeTakesEffectOnNextRequestWithWarmCache(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, c kv.Client) {
		p := newSnapPair(t, c)
		cluster := model.ClusterKey{AccountID: homelabAccountID, Region: "us-east-1", Cluster: "core-services"}.ID()
		viewer := login(t, p.on, "viewer", snapUserPassword)
		for i := 0; i < 2; i++ {
			if status, _ := body(t, viewer, "/api/clusters/"+cluster); status != http.StatusOK {
				t.Fatalf("viewer on Prod read %d, want 200", status)
			}
		}
		hits := p.count(t, "cluster", "hit")
		if hits == 0 {
			t.Fatal("the cache was not warm: the test proves nothing")
		}
		// Revoke the permission. Nothing touches the cache.
		users, _ := p.st.ListUsers(t.Context())
		for _, u := range users {
			if u.Username == "viewer" {
				if err := p.st.SetPermission(t.Context(), u.ID, p.tagID(t, "Prod"), authz.ActionSet(0)); err != nil {
					t.Fatal(err)
				}
			}
		}
		status, text := body(t, viewer, "/api/clusters/"+cluster)
		if status != http.StatusNotFound {
			t.Fatalf("after the revoke the viewer got %d %.200s, want the same 404 as an unknown cluster", status, text)
		}
		off := login(t, p.off, "viewer", snapUserPassword)
		if wantStatus, want := body(t, off, "/api/clusters/"+cluster); wantStatus != status || want != text {
			t.Fatalf("the cached 404 differs from today's: %q vs %q", text, want)
		}
	})
}

func TestServiceRead_TagReassignmentTakesEffectOnNextRequestWithWarmCache(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, c kv.Client) {
		p := newSnapPair(t, c)
		id := paymentsServiceID(t, p)
		key := model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: homelabAccountID, Region: "us-east-1", Cluster: "core-services"}, Service: "payments-api"}
		viewer, operator := login(t, p.on, "viewer", snapUserPassword), login(t, p.on, "operator", snapUserPassword)
		for i := 0; i < 2; i++ {
			if status, _ := body(t, viewer, "/api/services/"+id); status != http.StatusOK {
				t.Fatalf("viewer on Prod read %d, want 200", status)
			}
		}
		if status, _ := body(t, operator, "/api/services/"+id); status != http.StatusNotFound {
			t.Fatalf("operator on Staging read %d before the reassignment, want 404", status)
		}
		if p.count(t, "service", "hit") == 0 {
			t.Fatal("the cache was not warm: the test proves nothing")
		}
		staging := p.tagID(t, "Staging")
		if err := p.st.SetServiceTag(t.Context(), key, &staging); err != nil {
			t.Fatal(err)
		}
		if status, _ := body(t, viewer, "/api/services/"+id); status != http.StatusNotFound {
			t.Fatalf("after moving the service to Staging the Prod viewer read %d, want 404", status)
		}
		status, text := body(t, operator, "/api/services/"+id)
		if status != http.StatusOK {
			t.Fatalf("after moving the service to Staging the Staging operator read %d, want 200", status)
		}
		off := login(t, p.off, "operator", snapUserPassword)
		if _, want := body(t, off, "/api/services/"+id); want != text {
			t.Fatalf("the cached body differs from today's after the reassignment\n got: %.300s\nwant: %.300s", text, want)
		}
	})
}

func TestServiceRead_DeploymentOutcomeIsReadFromPostgres(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, c kv.Client) {
		p := newSnapPair(t, c)
		id := paymentsServiceID(t, p)
		key := model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: homelabAccountID, Region: "us-east-1", Cluster: "core-services"}, Service: "payments-api"}
		started := time.Now().Add(-10 * time.Minute)
		if _, err := p.st.InsertDeploymentIfNew(t.Context(), store.DeploymentRecord{Key: key, DeploymentID: "ecs-svc/snapshot-test", Revision: 7, Kind: "deploy", StartedAt: started}); err != nil {
			t.Fatal(err)
		}
		admin := login(t, p.on, "sam", snapUserPassword)
		for i := 0; i < 2; i++ {
			body(t, admin, "/api/services/"+id)
		}
		if p.count(t, "service", "hit") == 0 {
			t.Fatal("the cache was not warm: the test proves nothing")
		}
		if _, err := p.st.FinishDeployment(t.Context(), key, "ecs-svc/snapshot-test", "completed", time.Now(), nil); err != nil {
			t.Fatal(err)
		}
		// No poll, no token change: the outcome is written by the deploy code, not the poller.
		var dto ServiceDTO
		resp := admin.get("/api/services/" + id)
		decode(t, resp, &dto)
		found := false
		for _, rec := range dto.Deployments14d {
			if rec.DeploymentID == "ecs-svc/snapshot-test" {
				found = true
				if rec.Outcome != "completed" {
					t.Fatalf("outcome = %q, want completed straight after the deploy finished", rec.Outcome)
				}
			}
		}
		if !found {
			t.Fatal("the deployment is not on the page")
		}
	})
}

// A target group with an unhealthy target turns a steady service's status into unhealthy_task: the cached model must
// carry that fact, and a new token (as the poller sets after a health change) must replace the old model.
func TestServiceRead_UnhealthyTargetReachesTheStatusThroughTheCache(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, c kv.Client) {
		p := newSnapPair(t, c)
		admin, off := login(t, p.on, "sam", snapUserPassword), login(t, p.off, "sam", snapUserPassword)
		var services ServiceListDTO
		decode(t, off.get("/api/services"), &services)
		var steady *ServiceListItemDTO
		for i, s := range services.Items {
			if s.Status == model.StatusSteady {
				steady = &services.Items[i]
				break
			}
		}
		if steady == nil {
			t.Fatal("the estate has no steady service")
		}
		key, err := model.ParseServiceID(steady.ID)
		if err != nil {
			t.Fatal(err)
		}
		statusOf := func(c *apiClient) model.Status {
			var dto ServiceDTO
			decode(t, c.get("/api/services/"+steady.ID), &dto)
			return dto.Status
		}
		for i := 0; i < 2; i++ { // warm
			if got := statusOf(admin); got != model.StatusSteady {
				t.Fatalf("status = %s before the change, want steady", got)
			}
		}
		if err := p.st.UpsertTargetHealth(t.Context(), []model.TargetHealth{{Key: key, TargetGroupARN: "arn:tg/unhealthy", LoadBalancerARN: "arn:lb",
			Healthy: 1, Unhealthy: 2, Targets: []model.Target{}, PolledAt: time.Now()}}); err != nil {
			t.Fatal(err)
		}
		// What the poller does after it commits a target health change.
		for _, k := range []snapcache.GenKey{
			{AccountID: key.AccountID, Region: key.Region, Cluster: key.Cluster},
			{AccountID: key.AccountID, Region: key.Region},
		} {
			if err := p.cache.Bump(t.Context(), k, "9-1"); err != nil {
				t.Fatal(err)
			}
		}
		if got, want := statusOf(admin), statusOf(off); got != model.StatusUnhealthyTask || got != want {
			t.Fatalf("status with the cache = %s, without = %s, want both unhealthy_task", got, want)
		}
	})
}

// Disconnecting an account deletes its rows from the web task, not the poller: the cluster pages of that account must stop
// being served from the cache at once, not after the TTL.
func TestClusterRead_DisconnectedAccountIsNotServedFromAWarmCache(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, c kv.Client) {
		p := newSnapPair(t, c)
		accounts, err := p.st.ListAccounts(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		var account store.AWSAccount
		for _, a := range accounts {
			if a.AccountID == homelabAccountID {
				account = a
			}
		}
		if account.ID == 0 {
			t.Fatal("the seeded estate has no homelab account row")
		}
		cluster := model.ClusterKey{AccountID: homelabAccountID, Region: "us-east-1", Cluster: "core-services"}.ID()
		admin := login(t, p.on, "sam", snapUserPassword)
		for i := 0; i < 2; i++ {
			if status, _ := body(t, admin, "/api/clusters/"+cluster); status != http.StatusOK {
				t.Fatalf("status %d before the disconnect", status)
			}
		}
		if p.count(t, "cluster", "hit") == 0 {
			t.Fatal("the cache was not warm: the test proves nothing")
		}
		resp := admin.do(http.MethodDelete, "/api/accounts/"+strconv.FormatInt(account.ID, 10), nil, nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("disconnect = %d", resp.StatusCode)
		}
		if status, text := body(t, admin, "/api/clusters/"+cluster); status != http.StatusNotFound {
			t.Fatalf("after the disconnect the cluster page answered %d %.120s, want 404", status, text)
		}
		var services ServiceListDTO
		decode(t, admin.get("/api/services"), &services)
		for _, item := range services.Items {
			if item.AccountID == homelabAccountID {
				t.Fatalf("the service list still shows %s of the disconnected account", item.Name)
			}
		}
	})
}

// failingTokens is a store that cannot be reached.
type failingTokens struct{ snapcache.Cache }

func (failingTokens) Tokens(context.Context, ...snapcache.GenKey) ([]string, error) {
	return nil, errors.New("store down")
}

func TestRead_StoreDownStillAnswersWithTodaysBodies(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, c kv.Client) {
		p := newSnapPair(t, c)
		p.reader.Cache = failingTokens{p.cache}
		id := paymentsServiceID(t, p)
		cluster := model.ClusterKey{AccountID: homelabAccountID, Region: "us-east-1", Cluster: "core-services"}.ID()
		on, off := login(t, p.on, "sam", snapUserPassword), login(t, p.off, "sam", snapUserPassword)
		for _, path := range []string{"/api/clusters/" + cluster, "/api/services/" + id} {
			gotStatus, got := body(t, on, path)
			wantStatus, want := body(t, off, path)
			if gotStatus != http.StatusOK || gotStatus != wantStatus || got != want {
				t.Fatalf("GET %s with the store down = %d, want today's 200 body", path, gotStatus)
			}
		}
		if p.count(t, "cluster", "error") == 0 {
			t.Fatal("the failures were not counted")
		}
	})
}

func (p *snapPair) reads(t *testing.T, model string) int {
	t.Helper()
	return p.count(t, model, "hit") + p.count(t, model, "miss")
}

// A focus heartbeat only needs to know whether the user may see the page. With the cache on that answer comes from the
// cached read model (no Postgres read per heartbeat), and it must be the same answer the store gives for every user.
func TestFocus_VisibilityCheckUsesTheCacheAndMatchesThePostgresAnswer(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, c kv.Client) {
		p := newSnapPair(t, c)
		ids := p.ids(t)
		beforeClusters, beforeServices := p.reads(t, "cluster"), p.reads(t, "service")
		visible, hidden := 0, 0
		for _, user := range []string{"sam", "viewer", "operator", "omer"} {
			on, off := login(t, p.on, user, snapUserPassword), login(t, p.off, user, snapUserPassword)
			for kind, list := range map[string][]string{"cluster": ids.clusters, "service": ids.services} {
				for _, id := range list {
					got, _ := postFocus(t, on, kind, id, false)
					want, _ := postFocus(t, off, kind, id, false)
					if got != want {
						t.Fatalf("%s %s as %s: cache on answered %d, Postgres %d", kind, id, user, got, want)
					}
					if got == http.StatusOK {
						visible++
					} else {
						hidden++
					}
				}
			}
		}
		if visible == 0 || hidden == 0 {
			t.Fatalf("visible=%d hidden=%d: the users must see some objects and not others", visible, hidden)
		}
		if p.reads(t, "cluster") == beforeClusters || p.reads(t, "service") == beforeServices {
			t.Fatal("focus did not read the cached models: it still asks Postgres on every heartbeat")
		}
	})
}

func TestRead_CacheOffTakesTodaysPath(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, c kv.Client) {
		p := newSnapPair(t, c)
		off := login(t, p.off, "sam", snapUserPassword)
		id := paymentsServiceID(t, p)
		for i := 0; i < 3; i++ {
			if status, _ := body(t, off, "/api/services/"+id); status != http.StatusOK {
				t.Fatalf("status %d", status)
			}
		}
		for _, result := range []string{"hit", "miss", "bypass", "error"} {
			if n := p.count(t, "service", result); n != 0 {
				t.Fatalf("the cache-off server counted %d %s reads", n, result)
			}
		}
	})
}
