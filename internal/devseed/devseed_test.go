package devseed_test

import (
	"errors"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/devseed"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func TestDevseed_IdempotentAndMatchesMockup(t *testing.T) {
	ctx := t.Context()
	st := storetest.Open(t)

	for i := 0; i < 2; i++ {
		if err := devseed.Apply(ctx, st); err != nil {
			t.Fatalf("apply #%d: %v", i+1, err)
		}
	}

	if n, err := st.CountUsers(ctx); err != nil || n != 4 {
		t.Fatalf("users = %d, %v; want 4", n, err)
	}
	tags, err := st.ListTags(ctx)
	if err != nil || len(tags) != 3 {
		t.Fatalf("tags = %v, %v; want 3", tags, err)
	}
	wantTags := map[string]int64{"Dev": 1, "Staging": 2, "Prod": 3}
	for _, tag := range tags {
		if wantTags[tag.Name] != int64(tag.ID) {
			t.Errorf("tag %s has id %d, want %d", tag.Name, tag.ID, wantTags[tag.Name])
		}
	}
	var prodID authz.TagID
	for _, tag := range tags {
		if tag.Name == "Prod" {
			prodID = tag.ID
		}
	}
	ciTools := model.ClusterKey{AccountID: "482109375512", Region: "us-east-1", Cluster: "ci-tools"}
	if got, ok, err := st.ServiceTag(ctx, model.ServiceKey{ClusterKey: ciTools, Service: "artifact-proxy"}); err != nil || !ok || got != prodID {
		t.Errorf("artifact-proxy tag = %v, %v, %v; want explicit Prod %v", got, ok, err, prodID)
	}
	if _, ok, err := st.ClusterTag(ctx, ciTools); err != nil || ok {
		t.Errorf("ci-tools cluster tag ok = %v, %v; want untagged (Dev)", ok, err)
	}
	accounts, err := st.ListAccounts(ctx)
	if err != nil || len(accounts) != 2 {
		t.Fatalf("accounts = %v, %v; want 2", accounts, err)
	}
	entries, err := st.ListAudit(ctx, 100, 0)
	if err != nil || len(entries) != 5 {
		t.Fatalf("audit rows = %d, %v; want 5", len(entries), err)
	}

	omer, err := st.GetUserByUsername(ctx, "omer")
	if err != nil {
		t.Fatal(err)
	}
	if omer.MustChangePassword || omer.Admin {
		t.Errorf("omer = %+v; want non-admin without forced password change", omer)
	}
	if ok, err := auth.VerifyPassword(omer.PasswordHash, "harbormaster-e2e-1"); err != nil || !ok {
		t.Errorf("omer password does not verify: %v %v", ok, err)
	}
	subject, err := st.LoadSubject(ctx, omer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !authz.Can(subject, authz.Operate, 1) {
		t.Error("omer should be able to Operate on Dev")
	}
	if authz.Can(subject, authz.Operate, 2) {
		t.Error("omer should not be able to Operate on Staging")
	}
	oncall, err := st.GetUserByUsername(ctx, "oncall")
	if err != nil {
		t.Fatal(err)
	}
	perms, err := st.PermissionsOf(ctx, oncall.ID)
	if err != nil {
		t.Fatal(err)
	}
	if perms[2] != 39 || perms[3] != 39 {
		t.Errorf("oncall perms = %v; want Staging and Prod 39", perms)
	}
}

func TestDevseed_RefusesPopulatedDatabase(t *testing.T) {
	ctx := t.Context()
	st := storetest.Open(t)
	if _, err := st.CreateUser(ctx, "alice", "hash", true, false); err != nil {
		t.Fatal(err)
	}

	err := devseed.Apply(ctx, st)
	if !errors.Is(err, devseed.ErrDatabaseNotEmpty) {
		t.Fatalf("Apply error = %v, want ErrDatabaseNotEmpty", err)
	}
	if _, err := st.GetUserByUsername(ctx, "sam"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("sam must not be created in a populated database, got err %v", err)
	}
	accounts, err := st.ListAccounts(ctx)
	if err != nil || len(accounts) != 0 {
		t.Fatalf("accounts = %v, %v; want none seeded", accounts, err)
	}
}

func TestApply_SeedsAnalyticsHistoryOnceAndForceApplyDoesNot(t *testing.T) {
	ctx := t.Context()
	count := func(st *store.Store, query string) int {
		t.Helper()
		var n int
		if err := st.Pool.QueryRow(ctx, query).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	forced := storetest.Open(t)
	if err := devseed.ForceApply(ctx, forced); err != nil {
		t.Fatal(err)
	}
	if n := count(forced, `select (select count(*) from service_availability_daily) + (select count(*) from cluster_capacity_daily) + (select count(*) from service_util_daily)`); n != 0 {
		t.Fatalf("ForceApply wrote %d daily rows, want none (tests rely on empty daily tables)", n)
	}

	st := storetest.Open(t)
	var first [5]int
	for run := 0; run < 2; run++ {
		if err := devseed.Apply(ctx, st); err != nil {
			t.Fatal(err)
		}
		got := [5]int{
			count(st, `select count(*) from service_availability_daily`),
			count(st, `select count(*) from cluster_capacity_daily`),
			count(st, `select count(*) from service_util_daily`),
			count(st, `select count(*) from deployment_history where outcome = 'failed' and failure_reason is not null`),
			count(st, `select count(*) from deployment_history`),
		}
		if run == 0 {
			first = got
		} else if got != first {
			t.Fatalf("second Apply changed the counts from %v to %v", first, got)
		}
	}
	if first[0] == 0 || first[1] == 0 || first[2] == 0 || first[3] < 3 {
		t.Errorf("availability/capacity/util/failed = %v, want history in every table and at least 3 failures with a reason", first)
	}
	// Every table covers at least the 30 complete UTC days ending yesterday. Days are UTC like the rollups, so
	// the check must not use current_date, which follows the database's time zone.
	todayUTC := time.Now().UTC().Truncate(24 * time.Hour)
	for _, table := range []string{"service_availability_daily", "cluster_capacity_daily", "service_util_daily"} {
		var days int
		if err := st.Pool.QueryRow(ctx, `select count(distinct day) from `+table+` where day >= $1::date and day < $2::date`,
			todayUTC.AddDate(0, 0, -30), todayUTC).Scan(&days); err != nil {
			t.Fatal(err)
		}
		if days < 30 {
			t.Errorf("%s has %d of the last 30 days, want all 30", table, days)
		}
	}
}

func TestDevseed_TurnsOnPreDeployScanForEdgeNginxOnly(t *testing.T) {
	ctx := t.Context()
	st := storetest.Open(t)
	for i := 0; i < 2; i++ {
		if err := devseed.Apply(ctx, st); err != nil {
			t.Fatalf("apply #%d: %v", i+1, err)
		}
	}
	homelab := model.ClusterKey{AccountID: "482109375512", Region: "eu-central-1", Cluster: "edge-proxy"}
	if on, err := st.PreDeployScan(ctx, model.ServiceKey{ClusterKey: homelab, Service: "edge-nginx"}); err != nil || !on {
		t.Fatalf("edge-nginx pre-deploy scan = %v, %v; want on", on, err)
	}
	// payments-api stays off: the e2e flows change its image and would stop at the scan gate.
	core := model.ClusterKey{AccountID: "482109375512", Region: "us-east-1", Cluster: "core-services"}
	if on, err := st.PreDeployScan(ctx, model.ServiceKey{ClusterKey: core, Service: "payments-api"}); err != nil || on {
		t.Fatalf("payments-api pre-deploy scan = %v, %v; want off", on, err)
	}
	var rows int
	if err := st.Pool.QueryRow(ctx, `select count(*) from service_scan_settings`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("setting rows = %d, %v; want only edge-nginx", rows, err)
	}
}
