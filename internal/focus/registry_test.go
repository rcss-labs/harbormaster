package focus

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/kv"
	"github.com/rcss-labs/harbormaster/internal/kv/kvtest"
	"github.com/rcss-labs/harbormaster/internal/model"
)

const testTTL = 30 * time.Second

func serviceObject(name string) Object {
	return Object{Kind: KindService, Cluster: model.ClusterKey{AccountID: "111111111111", Region: "us-east-1", Cluster: "prod"}, Service: name}
}

func clusterObject(name string) Object {
	return Object{Kind: KindCluster, Cluster: model.ClusterKey{AccountID: "111111111111", Region: "us-east-1", Cluster: name}}
}

func limits() Limits { return Limits{TTL: testTTL, MaxObjects: 50, MaxPerUser: 5} }

// eachRegistry runs fn against the memory registry and against each configured Valkey/Redis server.
func eachRegistry(t *testing.T, fn func(t *testing.T, r Registry)) {
	t.Helper()
	t.Run("memory", func(t *testing.T) { fn(t, NewMemoryRegistry(time.Now)) })
	kvtest.Each(t, func(t *testing.T, c kv.Client) { fn(t, NewKVRegistry(c)) })
}

func members(t *testing.T, r Registry) []string {
	t.Helper()
	objs, err := r.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	out := make([]string, 0, len(objs))
	for _, o := range objs {
		out = append(out, o.Member())
	}
	slices.Sort(out)
	return out
}

func heartbeat(t *testing.T, r Registry, o Object, user int64, lim Limits) Admission {
	t.Helper()
	got, err := r.Heartbeat(context.Background(), o, user, lim)
	if err != nil {
		t.Fatalf("Heartbeat(%s, user %d): %v", o.Member(), user, err)
	}
	return got
}

func TestRegistry_AdmitsThenRefreshes(t *testing.T) {
	eachRegistry(t, func(t *testing.T, r Registry) {
		o := serviceObject("web")
		if got := heartbeat(t, r, o, 1, limits()); got != Admitted {
			t.Fatalf("first heartbeat = %q, want %q", got, Admitted)
		}
		if got := heartbeat(t, r, o, 1, limits()); got != Refreshed {
			t.Fatalf("second heartbeat = %q, want %q", got, Refreshed)
		}
		if got := members(t, r); !slices.Equal(got, []string{o.Member()}) {
			t.Fatalf("members = %v", got)
		}
	})
}

func TestRegistry_OneMemberForManyViewers(t *testing.T) {
	eachRegistry(t, func(t *testing.T, r Registry) {
		o := serviceObject("web")
		for user := int64(1); user <= 3; user++ {
			heartbeat(t, r, o, user, limits())
		}
		if got := members(t, r); len(got) != 1 {
			t.Fatalf("members = %v, want one for three viewers", got)
		}
	})
}

func TestRegistry_EstateCap(t *testing.T) {
	eachRegistry(t, func(t *testing.T, r Registry) {
		lim := Limits{TTL: testTTL, MaxObjects: 2, MaxPerUser: 5}
		heartbeat(t, r, serviceObject("a"), 1, lim)
		heartbeat(t, r, serviceObject("b"), 2, lim)
		if got := heartbeat(t, r, serviceObject("c"), 3, lim); got != Capped {
			t.Fatalf("third object = %q, want %q", got, Capped)
		}
		if got := heartbeat(t, r, serviceObject("a"), 3, lim); got != Refreshed {
			t.Fatalf("an already focused object = %q, want %q", got, Refreshed)
		}
		if got := members(t, r); len(got) != 2 {
			t.Fatalf("members = %v, want 2", got)
		}
	})
}

func TestRegistry_PerUserCap(t *testing.T) {
	eachRegistry(t, func(t *testing.T, r Registry) {
		lim := Limits{TTL: testTTL, MaxObjects: 50, MaxPerUser: 2}
		heartbeat(t, r, serviceObject("a"), 1, lim)
		heartbeat(t, r, serviceObject("b"), 1, lim)
		if got := heartbeat(t, r, serviceObject("c"), 1, lim); got != UserCapped {
			t.Fatalf("user's third object = %q, want %q", got, UserCapped)
		}
		if got := heartbeat(t, r, serviceObject("c"), 2, lim); got != Admitted {
			t.Fatalf("another user = %q, want %q", got, Admitted)
		}
		if got := heartbeat(t, r, serviceObject("a"), 1, lim); got != Refreshed {
			t.Fatalf("the user's own object = %q, want %q", got, Refreshed)
		}
	})
}

func TestRegistry_ExpiresWithoutHeartbeat(t *testing.T) {
	eachRegistry(t, func(t *testing.T, r Registry) {
		o := serviceObject("web")
		heartbeat(t, r, o, 1, Limits{TTL: 300 * time.Millisecond, MaxObjects: 50, MaxPerUser: 5})
		if got := members(t, r); len(got) != 1 {
			t.Fatalf("members right after the heartbeat = %v", got)
		}
		time.Sleep(450 * time.Millisecond)
		if got := members(t, r); len(got) != 0 {
			t.Fatalf("members after the TTL = %v, want none", got)
		}
	})
}

// TestRegistry_ReleaseShortensExpiryButAnotherViewerRestoresIt checks, on every backend, that a release leaves the
// object listed (the member is lowered to the grace, not removed) and that a second viewer's heartbeat is a refresh.
func TestRegistry_ReleaseShortensExpiryButAnotherViewerRestoresIt(t *testing.T) {
	eachRegistry(t, func(t *testing.T, r Registry) {
		o := serviceObject("web")
		heartbeat(t, r, o, 1, limits())
		heartbeat(t, r, o, 2, limits())
		if err := r.Release(context.Background(), o, 1); err != nil {
			t.Fatal(err)
		}
		if got := members(t, r); len(got) != 1 {
			t.Fatalf("members after one viewer left = %v, want the object still listed", got)
		}
		if got := heartbeat(t, r, o, 2, limits()); got != Refreshed {
			t.Fatalf("heartbeat after another viewer's release = %q, want %q", got, Refreshed)
		}
	})
}

func TestRegistry_ReleaseLowersTheMemberToTheGrace(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)}
	r := NewMemoryRegistry(clock.now)
	o := serviceObject("web")
	heartbeat(t, r, o, 1, limits())
	if err := r.Release(context.Background(), o, 1); err != nil {
		t.Fatal(err)
	}
	clock.advance(ReleaseGrace - time.Second)
	if got := members(t, r); len(got) != 1 {
		t.Fatalf("members inside the grace = %v, want one", got)
	}
	clock.advance(2 * time.Second)
	if got := members(t, r); len(got) != 0 {
		t.Fatalf("members after the grace = %v, want none (the TTL is %v)", got, testTTL)
	}
	// A heartbeat from another viewer restores the whole TTL, so the member outlives the grace.
	heartbeat(t, r, o, 2, limits())
	if err := r.Release(context.Background(), o, 1); err != nil {
		t.Fatal(err)
	}
	heartbeat(t, r, o, 2, limits())
	clock.advance(ReleaseGrace + 5*time.Second)
	if got := members(t, r); len(got) != 1 {
		t.Fatalf("members after a viewer restored the expiry = %v, want one", got)
	}
}

func TestRegistry_ReleaseNeverRaisesAnExpiry(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)}
	r := NewMemoryRegistry(clock.now)
	o := serviceObject("web")
	heartbeat(t, r, o, 1, Limits{TTL: 5 * time.Second, MaxObjects: 50, MaxPerUser: 5})
	if err := r.Release(context.Background(), o, 1); err != nil {
		t.Fatal(err)
	}
	clock.advance(6 * time.Second)
	if got := members(t, r); len(got) != 0 {
		t.Fatalf("members = %v: a release raised a 5 s expiry to the %v grace", got, ReleaseGrace)
	}
}

func TestRegistry_ReleaseFreesTheUsersSlot(t *testing.T) {
	eachRegistry(t, func(t *testing.T, r Registry) {
		lim := Limits{TTL: testTTL, MaxObjects: 50, MaxPerUser: 1}
		heartbeat(t, r, serviceObject("a"), 1, lim)
		if got := heartbeat(t, r, serviceObject("b"), 1, lim); got != UserCapped {
			t.Fatalf("second object = %q, want %q", got, UserCapped)
		}
		if err := r.Release(context.Background(), serviceObject("a"), 1); err != nil {
			t.Fatal(err)
		}
		if got := heartbeat(t, r, serviceObject("b"), 1, lim); got != Admitted {
			t.Fatalf("after release = %q, want %q", got, Admitted)
		}
	})
}

func TestRegistry_FreshRoundTrip(t *testing.T) {
	eachRegistry(t, func(t *testing.T, r Registry) {
		ctx := context.Background()
		o := clusterObject("prod")
		if _, ok, err := r.Fresh(ctx, o); err != nil || ok {
			t.Fatalf("Fresh before any write = ok %v err %v", ok, err)
		}
		want := Fresh{At: time.Now().UTC().Truncate(time.Millisecond), Interval: 5 * time.Second, State: "live"}
		if err := r.SetFresh(ctx, o, want); err != nil {
			t.Fatal(err)
		}
		got, ok, err := r.Fresh(ctx, o)
		if err != nil || !ok {
			t.Fatalf("Fresh = ok %v err %v", ok, err)
		}
		if !got.At.Equal(want.At) || got.Interval != want.Interval || got.State != want.State {
			t.Fatalf("Fresh = %+v, want %+v", got, want)
		}
	})
}

func TestRegistry_FreshExpiresAfterSixtySeconds(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)}
	r := NewMemoryRegistry(clock.now)
	o := clusterObject("prod")
	if err := r.SetFresh(context.Background(), o, Fresh{At: clock.now(), Interval: 5 * time.Second, State: "live"}); err != nil {
		t.Fatal(err)
	}
	clock.advance(59 * time.Second)
	if _, ok, _ := r.Fresh(context.Background(), o); !ok {
		t.Fatal("fresh entry gone after 59 s")
	}
	clock.advance(2 * time.Second)
	if _, ok, _ := r.Fresh(context.Background(), o); ok {
		t.Fatal("fresh entry still there after 61 s")
	}
}

func TestRegistry_UsesServerTime(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, c kv.Client) {
		ctx := context.Background()
		r := NewKVRegistry(c)
		o := serviceObject("web")
		heartbeat(t, r, o, 1, limits())
		server, err := c.ServerTime(ctx)
		if err != nil {
			t.Fatal(err)
		}
		// The member's score is the expiry in ms on the server's clock: within two seconds of server time + TTL.
		lo := float64(server.Add(testTTL - 2*time.Second).UnixMilli())
		hi := float64(server.Add(testTTL + 2*time.Second).UnixMilli())
		got, err := c.ZRangeByScore(ctx, c.Key("focus"), lo, hi)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, []string{o.Member()}) {
			t.Fatalf("members scored around server time + TTL = %v, want %q", got, o.Member())
		}
	})
}

func TestObject_MemberRoundTrip(t *testing.T) {
	for _, o := range []Object{serviceObject("web"), clusterObject("prod")} {
		got, err := ParseMember(o.Member())
		if err != nil {
			t.Fatalf("ParseMember(%q): %v", o.Member(), err)
		}
		if got != o {
			t.Fatalf("round trip = %+v, want %+v", got, o)
		}
	}
	if got, want := serviceObject("web").Member(), "service|111111111111|us-east-1|prod|web"; got != want {
		t.Fatalf("service member = %q, want %q", got, want)
	}
	if got, want := clusterObject("prod").Member(), "cluster|111111111111|us-east-1|prod"; got != want {
		t.Fatalf("cluster member = %q, want %q", got, want)
	}
}

func TestParseMember_RejectsMalformed(t *testing.T) {
	for _, s := range []string{
		"", "cluster", "cluster|a|b", "cluster|a|b|c|d", "service|a|b|c", "service|a|b|c|d|e",
		"pod|a|b|c", "cluster||b|c", "service|a|b|c|", "cluster|a|b|",
	} {
		if _, err := ParseMember(s); err == nil {
			t.Errorf("ParseMember(%q) = nil error, want a rejection", s)
		}
	}
}

func TestObject_ValidRejectsSeparatorInANameAndWrongShape(t *testing.T) {
	if serviceObject("we|b").Valid() {
		t.Error("a service name with | reported valid")
	}
	if !serviceObject("web").Valid() || !clusterObject("prod").Valid() {
		t.Error("plain objects reported invalid")
	}
	cluster := clusterObject("prod")
	cluster.Service = "web"
	if cluster.Valid() {
		t.Error("a cluster object with a service reported valid")
	}
}

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }
