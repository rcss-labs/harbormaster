package snapcache

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

const (
	acctA = "111111111111"
	acctB = "222222222222"
)

// estateFixture is a small estate on a real Postgres: 2 accounts, 3 slices, 4 clusters (one empty of services), 20
// services with running, stopped and standalone tasks, target health and service events (one with no time).
type estateFixture struct {
	st       *store.Store
	now      time.Time
	clusters []model.ClusterKey
	services []model.ServiceKey
	tagIDs   []authz.TagID // the Dev tag first, then three more
}

func ck(account, region, cluster string) model.ClusterKey {
	return model.ClusterKey{AccountID: account, Region: region, Cluster: cluster}
}

func newEstateFixture(t *testing.T) *estateFixture {
	t.Helper()
	st := storetest.Open(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	f := &estateFixture{st: st, now: time.Now().UTC().Truncate(time.Second)}
	dev, err := st.DevTag(ctx)
	must(err)
	f.tagIDs = []authz.TagID{dev.ID}
	for _, name := range []string{"zz-prod-7f3a", "zz-staging-7f3a", "zz-data-7f3a"} {
		tag, err := st.CreateTag(ctx, name, "red", false)
		must(err)
		f.tagIDs = append(f.tagIDs, tag.ID)
	}
	f.clusters = []model.ClusterKey{
		ck(acctA, "us-east-1", "core"), ck(acctA, "us-east-1", "empty"),
		ck(acctA, "eu-west-1", "batch"), ck(acctB, "us-east-1", "edge"),
	}
	var clusters []model.Cluster
	for _, k := range f.clusters {
		clusters = append(clusters, model.Cluster{Key: k, ARN: "arn:" + k.Cluster, Status: "ACTIVE", RunningTasks: 2,
			CapacityProviders: []string{"FARGATE"}, Raw: json.RawMessage(`{"big":"document"}`), PolledAt: f.now})
	}
	_, err = st.UpsertClusters(ctx, clusters)
	must(err)

	counts := map[string]int{"core": 5, "batch": 8, "edge": 7}
	var services []model.Service
	var tasks []model.Task
	var events []model.ServiceEvent
	var health []model.TargetHealth
	started := f.now.Add(-2 * time.Hour)
	for _, k := range f.clusters {
		for i := 0; i < counts[k.Cluster]; i++ {
			sk := model.ServiceKey{ClusterKey: k, Service: fmt.Sprintf("svc-%02d", i)}
			f.services = append(f.services, sk)
			services = append(services, model.Service{Key: sk, ARN: "arn:" + sk.Service, Status: "ACTIVE", Desired: 2, Running: 2,
				Family: sk.Service, Revision: 3 + i, Subnets: []string{"subnet-1"}, SecurityGroups: []string{"sg-1"},
				Deployments: []model.Deployment{{ID: "d1", Status: "PRIMARY", Revision: 3 + i, RolloutState: "COMPLETED", CreatedAt: started}},
				Raw:         json.RawMessage(`{"serviceName":"x"}`), CreatedAt: started, PolledAt: f.now})
			for j := 0; j < 2; j++ {
				tasks = append(tasks, model.Task{Cluster: k, ARN: fmt.Sprintf("arn:task/%s/%s-run%d", k.Cluster, sk.Service, j),
					ServiceName: sk.Service, LastStatus: "RUNNING", HealthStatus: "HEALTHY", CPU: "256", Memory: "512",
					CapacityProvider: "FARGATE", Revision: 3 + i, Containers: []model.Container{{Name: "app", ExecAgentStatus: "RUNNING"}},
					Raw: json.RawMessage(`{"t":1}`), CreatedAt: started, StartedAt: &started, PolledAt: f.now})
			}
			stopped := f.now.Add(-time.Hour)
			tasks = append(tasks, model.Task{Cluster: k, ARN: fmt.Sprintf("arn:task/%s/%s-stop", k.Cluster, sk.Service),
				ServiceName: sk.Service, LastStatus: "STOPPED", StoppedAt: &stopped, CreatedAt: started, PolledAt: f.now})
			if i%2 == 0 {
				created := f.now.Add(-time.Duration(i+1) * time.Minute)
				if i == 4 {
					created = time.Time{} // an event with no time
				}
				events = append(events, model.ServiceEvent{Key: sk, ID: "e1", CreatedAt: created, Message: "m"},
					model.ServiceEvent{Key: sk, ID: "e2", CreatedAt: f.now.Add(-time.Duration(i+10) * time.Minute), Message: "older"})
			}
			if i%3 == 0 {
				health = append(health, model.TargetHealth{Key: sk, TargetGroupARN: "arn:tg/" + sk.Service, LoadBalancerARN: "arn:lb",
					Healthy: 1, Unhealthy: i % 2, Targets: []model.Target{}, Raw: json.RawMessage(`{}`), PolledAt: f.now})
			}
		}
		// A standalone task in every cluster, running and stopped.
		stopped := f.now.Add(-time.Hour)
		tasks = append(tasks,
			model.Task{Cluster: k, ARN: "arn:task/" + k.Cluster + "/solo-run", LastStatus: "RUNNING", Group: "family:solo", CPU: "512", Memory: "1024",
				CreatedAt: started, StartedAt: &started, PolledAt: f.now},
			model.Task{Cluster: k, ARN: "arn:task/" + k.Cluster + "/solo-stop", LastStatus: "STOPPED", StoppedAt: &stopped, CreatedAt: started, PolledAt: f.now})
	}
	_, err = st.UpsertServices(ctx, services)
	must(err)
	_, err = st.UpsertTasks(ctx, tasks)
	must(err)
	_, err = st.InsertServiceEvents(ctx, events)
	must(err)
	must(st.UpsertTargetHealth(ctx, health))
	return f
}

func (f *estateFixture) slices() []store.TargetFilter {
	return []store.TargetFilter{{AccountID: acctA, Region: "us-east-1"}, {AccountID: acctA, Region: "eu-west-1"}, {AccountID: acctB, Region: "us-east-1"}}
}

// randomizeTags assigns each cluster no tag or a random one, and each service no tag (inherit) or a random one.
func (f *estateFixture) randomizeTags(t *testing.T, rng *rand.Rand) {
	t.Helper()
	ctx := context.Background()
	pick := func() authz.TagID { return f.tagIDs[rng.Intn(len(f.tagIDs))] }
	for _, k := range f.clusters {
		var err error
		if rng.Intn(3) == 0 {
			err = f.st.ClearClusterTag(ctx, k)
		} else {
			err = f.st.SetClusterTag(ctx, k, pick())
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, k := range f.services {
		var tag *authz.TagID
		if rng.Intn(3) == 0 {
			id := pick()
			tag = &id
		}
		if err := f.st.SetServiceTag(ctx, k, tag); err != nil {
			t.Fatal(err)
		}
	}
}

// randomSubject is an admin now and then, otherwise a user with a random preset on a random subset of the tags.
func (f *estateFixture) randomSubject(rng *rand.Rand) authz.Subject {
	if rng.Intn(8) == 0 {
		return authz.Subject{UserID: 1, Admin: true}
	}
	presets := []authz.ActionSet{authz.Presets["Viewer"], authz.Presets["Operator"], authz.Presets["Full"]}
	perms := map[authz.TagID]authz.ActionSet{}
	for _, id := range f.tagIDs {
		if rng.Intn(2) == 0 {
			perms[id] = presets[rng.Intn(len(presets))]
		}
	}
	return authz.Subject{UserID: 2, Perms: perms}
}

func (f *estateFixture) randomTagFilter(rng *rand.Rand) []authz.TagID {
	if rng.Intn(3) != 0 {
		return nil
	}
	var out []authz.TagID
	for _, id := range f.tagIDs {
		if rng.Intn(2) == 0 {
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		out = []authz.TagID{f.tagIDs[rng.Intn(len(f.tagIDs))]}
	}
	return out
}

// sqlVisibility is what the API handlers pass to the store for a subject and a ?tag= filter (scope.go).
func sqlVisibility(s authz.Subject, tagFilter []authz.TagID) store.Visibility {
	ids := make([]int64, 0, len(s.Perms))
	for _, id := range authz.VisibleTags(s) {
		ids = append(ids, int64(id))
	}
	vis := store.Visibility{Admin: s.Admin, TagIDs: ids}
	if len(tagFilter) == 0 {
		return vis
	}
	narrowed := make([]int64, 0, len(tagFilter))
	for _, tag := range tagFilter {
		if vis.Admin || slices.Contains(vis.TagIDs, int64(tag)) {
			narrowed = append(narrowed, int64(tag))
		}
	}
	return store.Visibility{TagIDs: narrowed}
}

func (f *estateFixture) vis(s authz.Subject, tagFilter []authz.TagID) store.Visibility {
	return sqlVisibility(s, tagFilter)
}

// ---- comparing rows ----------------------------------------------------------------------------------------------

func clearRawServices(rows []store.ServiceRow) []store.ServiceRow {
	out := make([]store.ServiceRow, len(rows))
	for i, r := range rows {
		r.Raw = nil
		out[i] = r
	}
	return out
}

func clearRawTasks(rows []store.TaskRow) []store.TaskRow {
	out := make([]store.TaskRow, len(rows))
	for i, r := range rows {
		r.Raw = nil
		out[i] = r
	}
	return out
}

func clearRawClusters(rows []store.ClusterRow) []store.ClusterRow {
	out := make([]store.ClusterRow, len(rows))
	for i, r := range rows {
		r.Raw = nil
		out[i] = r
	}
	return out
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func serviceTagSig(rows []store.ServiceRow) []string {
	out := []string{}
	for _, r := range rows {
		out = append(out, fmt.Sprintf("%s tag=%d inherited=%v", r.Key.ID(), r.EffectiveTagID, r.TagInherited))
	}
	return out
}

func taskTagSig(rows []store.TaskRow) []string {
	out := []string{}
	for _, r := range rows {
		out = append(out, fmt.Sprintf("%s tag=%d", r.ARN, r.EffectiveTagID))
	}
	return out
}

func clusterTagSig(rows []store.ClusterRow) []string {
	out := []string{}
	for _, r := range rows {
		out = append(out, fmt.Sprintf("%s tag=%d set=%v", r.Key.ID(), r.TagID, r.TagSet))
	}
	return out
}

// sameRows fails unless got and want are the same rows: the same JSON (with raw documents left out) and the same tags.
func sameRows(t *testing.T, what, ctx string, got, want any, gotTags, wantTags []string) {
	t.Helper()
	if g, w := jsonOf(t, got), jsonOf(t, want); g != w {
		t.Fatalf("%s: %s differ\n got: %.600s\nwant: %.600s", ctx, what, g, w)
	}
	if !slices.Equal(gotTags, wantTags) {
		t.Fatalf("%s: %s tags differ\n got: %v\nwant: %v", ctx, what, gotTags, wantTags)
	}
}

func sortedKeys(keys []model.ServiceKey) []string {
	out := []string{}
	for _, k := range keys {
		out = append(out, k.ID())
	}
	sort.Strings(out)
	return out
}

func targetOf(f store.TargetFilter) awsx.Target {
	return awsx.Target{AccountID: f.AccountID, Region: f.Region}
}
