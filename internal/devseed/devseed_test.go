package devseed_test

import (
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/cost"
	"github.com/rcss-labs/harbormaster/internal/devseed"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/poller"
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

// TestDevseed_CostDemoHasEveryCase checks that `make run` has a service for each Cost tab case that needs a
// particular estate or history: tasks without a Fargate price, Spot tasks, short and long history, and a task
// definition whose sidecar keeps a service from shrinking.
func TestDevseed_CostDemoHasEveryCase(t *testing.T) {
	ctx := t.Context()
	st := storetest.Open(t)
	if err := devseed.Apply(ctx, st); err != nil {
		t.Fatal(err)
	}

	estate := fake.MockupEstate(time.Now)
	var ec2Tasks int
	notificationsTasks, notificationsSpot := 0, 0
	for _, task := range estate.Tasks {
		if aws.ToString(task.Task.LastStatus) != "RUNNING" {
			continue
		}
		if task.Task.LaunchType == ecstypes.LaunchTypeEc2 {
			ec2Tasks++
		}
		if aws.ToString(task.Task.Group) == "service:notifications" {
			notificationsTasks++
			if aws.ToString(task.Task.CapacityProviderName) == "FARGATE_SPOT" {
				notificationsSpot++
			}
		}
		if task.Task.LaunchType == ecstypes.LaunchTypeEc2 && task.Task.PlatformVersion != nil {
			t.Errorf("EC2 task has platform version %q, which AWS returns for Fargate only", aws.ToString(task.Task.PlatformVersion))
		}
	}
	if ec2Tasks == 0 {
		t.Error("no running EC2 task, so the Cost tab cannot show a service that is not priced")
	}
	if notificationsTasks == 0 || notificationsSpot != notificationsTasks {
		t.Errorf("notifications runs %d of %d tasks on FARGATE_SPOT, want all", notificationsSpot, notificationsTasks)
	}
	for _, service := range estate.Services {
		if service.Service.LaunchType == ecstypes.LaunchTypeEc2 && service.Service.PlatformVersion != nil {
			t.Errorf("EC2 service has platform version %q, which AWS returns for Fargate only", aws.ToString(service.Service.PlatformVersion))
		}
	}

	utilRows := func(service string) int {
		t.Helper()
		var n int
		if err := st.Pool.QueryRow(ctx, `select count(*) from service_util_daily where service_name = $1`, service).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if got := utilRows("artifact-proxy"); got != 5 {
		t.Errorf("artifact-proxy util rows = %d, want 5 (short history)", got)
	}
	if got := utilRows("jenkins-controller"); got != 60 {
		t.Errorf("jenkins-controller util rows = %d, want 60", got)
	}
	if got := utilRows("edge-nginx"); got != 0 {
		t.Errorf("edge-nginx util rows = %d, want none (no data)", got)
	}

	// Fill the service table from the same estate so the cost facts come from real task definitions.
	clients := fake.New(estate)
	for _, target := range []awsx.Target{
		{AccountID: "482109375512", Region: "us-east-1"},
		{AccountID: "482109375512", Region: "eu-central-1"},
		{AccountID: "731022849906", Region: "us-east-1"},
	} {
		if err := poller.PollOnce(ctx, st, clients, poller.Config{MaxInFlight: 4}, target); err != nil {
			t.Fatalf("poll %v: %v", target, err)
		}
	}
	facts, err := st.AnalyticsCostServices(ctx, store.TargetFilter{}, store.Visibility{Admin: true}, time.Now().UTC().AddDate(0, 0, -90))
	if err != nil {
		t.Fatal(err)
	}
	evaluate := func(service string) (cost.Status, []cost.Step) {
		t.Helper()
		for _, fact := range facts {
			if fact.Key.Service != service {
				continue
			}
			if fact.ContainerDefinitions == nil {
				t.Fatalf("%s has no container definitions", service)
			}
			floors, err := cost.ContainerFloors(fact.ContainerDefinitions)
			if err != nil {
				t.Fatal(err)
			}
			return cost.Evaluate(cost.Input{
				Region: fact.Key.Region, Architecture: fact.Architecture, OSFamily: fact.OSFamily,
				RequiresFargate: fact.RequiresFargate, CPUUnits: *fact.CPUUnits, MemoryMiB: *fact.MemoryMiB,
				Floors: floors, OnDemandTasks: 1,
				UsedCPUUnits: fact.UsedCPUUnits, UsedMemMiB: fact.UsedMemMiB,
				HistoryDays: fact.HistoryDays, LatestDay: fact.LatestDay,
				RollupEnabled: true, Today: time.Now().UTC().Truncate(24 * time.Hour),
			})
		}
		t.Fatalf("service %s missing from cost facts", service)
		return "", nil
	}

	status, steps := evaluate("jenkins-controller")
	if status != cost.StatusOK {
		t.Fatalf("jenkins-controller status = %q, want ok", status)
	}
	// A step applies from its headroom up to the next step, so the one in force at the default is the last at or below it.
	var inForce *cost.Step
	for i := range steps {
		if steps[i].FromHeadroomPct <= cost.DefaultHeadroomPct {
			inForce = &steps[i]
		}
	}
	if inForce == nil || inForce.Suggestion == nil {
		t.Errorf("jenkins-controller has no suggestion at %d%% headroom: %+v", cost.DefaultHeadroomPct, steps)
	}

	// The migrator sidecar of staging api reserves memory, so no suggestion may go below 1024 MiB.
	status, steps = evaluate("api")
	if status != cost.StatusOK || len(steps) == 0 {
		t.Fatalf("api status = %q with %d steps, want ok with a ladder", status, len(steps))
	}
	limited := false
	for _, step := range steps {
		if step.Suggestion != nil && step.Suggestion.LimitedByContainers {
			limited = true
		}
		if step.Suggestion != nil && step.Suggestion.MemoryMiB < 1024 {
			t.Errorf("api suggestion at %d%% = %d MiB, want at least 1024", step.FromHeadroomPct, step.Suggestion.MemoryMiB)
		}
	}
	if !limited {
		t.Errorf("no api suggestion is limited by its containers: %+v", steps)
	}
}
