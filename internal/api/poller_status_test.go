package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/telemetry"
)

type ownedPollerBody struct {
	Items []struct {
		AccountID     string  `json:"account_id"`
		Region        string  `json:"region"`
		OwnerInstance *string `json:"owner_instance"`
		OwnerEpoch    *int64  `json:"owner_epoch"`
	} `json:"items"`
	Store string `json:"store"`
}

// claimSlice takes the slice's lease for owner, as a poller would.
func claimSlice(t *testing.T, st *store.Store, target awsx.Target, owner uuid.UUID) store.Lease {
	t.Helper()
	ctx := context.Background()
	if err := st.EnsureSliceLease(ctx, target); err != nil {
		t.Fatal(err)
	}
	// The target need not be a connected account here, so claim the way the one-shot poll helpers do.
	l, ok, err := st.ClaimLeaseOnce(ctx, store.LeaseKey{Kind: store.LeaseSlice, AccountID: target.AccountID, Region: target.Region}, owner, time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim %+v: %v %v", target, ok, err)
	}
	return l
}

func TestPollerStatus_AdminSeesOwnerAndEpoch(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedAccounts(t, st)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	target := awsx.Target{AccountID: sandboxAccountID, Region: "eu-west-1"}
	owner := uuid.New()
	l := claimSlice(t, st, target, owner)

	var got ownedPollerBody
	decode(t, login(t, srv, "sam", testPassword).get("/api/poller"), &got)
	var seen, free int
	for _, item := range got.Items {
		if item.AccountID == target.AccountID && item.Region == target.Region {
			seen++
			if item.OwnerInstance == nil || *item.OwnerInstance != owner.String() || item.OwnerEpoch == nil || *item.OwnerEpoch != l.Epoch {
				t.Errorf("owner of the claimed slice = %v epoch %v, want %s epoch %d", item.OwnerInstance, item.OwnerEpoch, owner, l.Epoch)
			}
			continue
		}
		if item.OwnerInstance == nil && item.OwnerEpoch == nil {
			free++
		}
	}
	if seen != 1 || free == 0 {
		t.Fatalf("claimed slice seen %d times, %d other slices without an owner; want 1 and at least 1", seen, free)
	}
}

func TestPollerStatus_NonAdminSeesNoOwner(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedAccounts(t, st)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	claimSlice(t, st, awsx.Target{AccountID: sandboxAccountID, Region: "eu-west-1"}, uuid.New())

	var got ownedPollerBody
	decode(t, login(t, srv, "omer", testPassword).get("/api/poller"), &got)
	if len(got.Items) == 0 {
		t.Fatal("omer sees no slices; the test would prove nothing")
	}
	for _, item := range got.Items {
		if item.OwnerInstance != nil || item.OwnerEpoch != nil {
			t.Errorf("non-admin sees the owner of %s/%s: %v epoch %v", item.AccountID, item.Region, item.OwnerInstance, item.OwnerEpoch)
		}
	}
}

type upStub struct{ up bool }

func (u upStub) Up() bool { return u.up }

func TestPollerStatus_ReportsStoreHealth(t *testing.T) {
	for _, tc := range []struct {
		name string
		kv   interface{ Up() bool }
		want string
	}{{"none", nil, "none"}, {"up", upStub{true}, "up"}, {"down", upStub{false}, "down"}} {
		t.Run(tc.name, func(t *testing.T) {
			srv, _, _, _ := newServerCustom(t, true, func(s *Server) { s.KVHealth = tc.kv })
			var got ownedPollerBody
			decode(t, login(t, srv, "sam", testPassword).get("/api/poller"), &got)
			if got.Store != tc.want {
				t.Fatalf("store = %q, want %q", got.Store, tc.want)
			}
		})
	}
}

// Whether the shared store exists, and whether it is up, is operations detail: only admins get it.
func TestPollerStatus_StoreStateIsAdminOnly(t *testing.T) {
	srv, _, _, _ := newServerCustom(t, true, func(s *Server) { s.KVHealth = upStub{true} })
	var got ownedPollerBody
	decode(t, login(t, srv, "omer", testPassword).get("/api/poller"), &got)
	if got.Store != "" {
		t.Fatalf("non-admin sees store = %q, want it left out", got.Store)
	}
}

func metricsBody(t *testing.T, base string) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, base+"/metrics", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/metrics = %d %s", resp.StatusCode, body)
	}
	return string(body)
}

func withMetrics(s *Server) {
	s.MetricsToken = "tok"
	s.Telemetry = telemetry.New()
}

func TestMetrics_ExposesMembersAndLeases(t *testing.T) {
	srv, _, st, _ := newServerCustom(t, true, withMetrics)
	ctx := context.Background()
	instance := uuid.New()
	if err := st.HeartbeatMember(ctx, store.Member{InstanceID: instance, Roles: []string{"web", "poller"}, Version: "v1", Slices: 3, Weight: 7, FocusObjects: 2}); err != nil {
		t.Fatal(err)
	}
	owned := awsx.Target{AccountID: "111111111111", Region: "eu-west-1"}
	free := awsx.Target{AccountID: "222222222222", Region: "us-east-1"}
	claimSlice(t, st, owned, instance)
	if err := st.EnsureSliceLease(ctx, free); err != nil {
		t.Fatal(err)
	}

	body := metricsBody(t, srv.URL)
	id := instance.String()
	for _, want := range []string{
		`# TYPE harbormaster_poll_member_slices gauge`,
		fmt.Sprintf(`harbormaster_poll_member_slices{instance=%q,roles="web,poller"} 3`, id),
		fmt.Sprintf(`harbormaster_poll_member_weight{instance=%q,roles="web,poller"} 7`, id),
		fmt.Sprintf(`harbormaster_poll_member_focus_objects{instance=%q,roles="web,poller"} 2`, id),
		fmt.Sprintf(`harbormaster_poll_member_heartbeat_age_seconds{instance=%q,roles="web,poller"} `, id),
		fmt.Sprintf(`harbormaster_lease_epoch{kind="slice",account="111111111111",region="eu-west-1",instance=%q} 1`, id),
		`harbormaster_lease_epoch{kind="slice",account="222222222222",region="us-east-1",instance=""} 0`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics lack %q\n%s", want, grepMetrics(body, "member", "lease_epoch"))
		}
	}
}

func grepMetrics(body string, needles ...string) string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		for _, n := range needles {
			if strings.Contains(line, n) {
				out = append(out, line)
				break
			}
		}
	}
	return strings.Join(out, "\n")
}

// A scrape must never fail or hang because the database does: the member and lease series are left out.
func TestMetrics_DatabaseErrorOmitsMemberSeries(t *testing.T) {
	srv, _, st, _ := newServerCustom(t, true, withMetrics)
	if err := st.HeartbeatMember(context.Background(), store.Member{InstanceID: uuid.New(), Roles: []string{"poller"}, Version: "v1"}); err != nil {
		t.Fatal(err)
	}
	st.Pool.Close()
	body := metricsBody(t, srv.URL)
	if strings.Contains(body, "harbormaster_poll_member_") || strings.Contains(body, "harbormaster_lease_epoch") {
		t.Errorf("member series present although the database is unreachable:\n%s", grepMetrics(body, "member", "lease_epoch"))
	}
	if !strings.Contains(body, "harbormaster_build_info") {
		t.Error("the registry's own series are missing")
	}
}
