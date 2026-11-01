package store_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func TestAlerts_ReplaceIsPerTarget(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	targetA := awsx.Target{AccountID: "482109375512", Region: "us-east-1"}
	targetB := awsx.Target{AccountID: "123456789012", Region: "us-east-1"}
	at := time.Date(2026, 10, 31, 10, 0, 0, 0, time.UTC)
	keyA := model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: targetA.AccountID, Region: targetA.Region, Cluster: "prod"}, Service: "api"}
	keyB := model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: targetB.AccountID, Region: targetB.Region, Cluster: "prod"}, Service: "api"}

	alertsA := []model.Alert{
		{Key: keyA, Kind: "unhealthy_target", Severity: model.AlertDanger, Title: "2 unhealthy", Text: "api-tg", At: at, Tab: "targets"},
		{Key: keyA, Kind: "scaling_activity", Severity: model.AlertWarning, Title: "scale out", Text: "cpu", At: at, Tab: "scaling"},
	}
	alertsB := []model.Alert{
		{Key: keyB, Kind: "account_unreachable", Severity: model.AlertInfo, Title: "unreachable", Text: "sts", At: at, Tab: "accounts"},
	}
	if err := s.ReplaceAlerts(ctx, targetA, alertsA); err != nil {
		t.Fatalf("ReplaceAlerts A: %v", err)
	}
	if err := s.ReplaceAlerts(ctx, targetB, alertsB); err != nil {
		t.Fatalf("ReplaceAlerts B: %v", err)
	}
	all, err := s.ListAlerts(ctx, store.AlertFilter{})
	if err != nil || len(all) != 3 {
		t.Fatalf("ListAlerts({}) = %d, %v; want 3", len(all), err)
	}

	if err := s.ReplaceAlerts(ctx, targetA, nil); err != nil {
		t.Fatalf("ReplaceAlerts A empty: %v", err)
	}
	forA, err := s.ListAlerts(ctx, store.AlertFilter{AccountID: targetA.AccountID, Region: targetA.Region})
	if err != nil || len(forA) != 0 {
		t.Fatalf("ListAlerts(A) = %d, %v; want 0", len(forA), err)
	}
	all, err = s.ListAlerts(ctx, store.AlertFilter{})
	if err != nil || len(all) != 1 {
		t.Fatalf("ListAlerts({}) after replacing A = %d, %v; want 1", len(all), err)
	}
}

// alertRowVersions returns id and xmin per alert key; a row that is deleted and reinserted changes both,
// one that is updated keeps its id but changes xmin, and an untouched row changes neither.
func alertRowVersions(t *testing.T, s *store.Store) map[string]string {
	t.Helper()
	rows, err := s.Pool.Query(context.Background(),
		`select account_id || '/' || cluster_name || '/' || service_name || '/' || kind, id::text || ':' || xmin::text from alerts`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	versions := map[string]string{}
	for rows.Next() {
		var key, version string
		if err := rows.Scan(&key, &version); err != nil {
			t.Fatal(err)
		}
		versions[key] = version
	}
	return versions
}

func TestAlerts_ReplaceWritesOnlyTheDifference(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	target := awsx.Target{AccountID: "482109375512", Region: "us-east-1"}
	other := awsx.Target{AccountID: "123456789012", Region: "us-east-1"}
	at := time.Date(2026, 10, 31, 10, 0, 0, 0, time.UTC)
	key := func(tg awsx.Target, service string) model.ServiceKey {
		return model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: tg.AccountID, Region: tg.Region, Cluster: "prod"}, Service: service}
	}
	unhealthy := model.Alert{Key: key(target, "api"), Kind: "unhealthy_task", Severity: model.AlertWarning, Title: "api: 1 unhealthy task", Text: "Container health check fails.", At: at, Tab: "tasks"}
	failing := model.Alert{Key: key(target, "worker"), Kind: "service_failing", Severity: model.AlertDanger, Title: "worker: 0 of 2 tasks running", Text: "Tasks fail to start.", At: at, Tab: "tasks"}
	otherAlert := model.Alert{Key: key(other, "api"), Kind: "unhealthy_task", Severity: model.AlertWarning, Title: "x", Text: "y", At: at, Tab: "tasks"}

	if err := s.ReplaceAlerts(ctx, target, []model.Alert{unhealthy, failing}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceAlerts(ctx, other, []model.Alert{otherAlert}); err != nil {
		t.Fatal(err)
	}
	first := alertRowVersions(t, s)

	// Same alerts again: no row may be written.
	if err := s.ReplaceAlerts(ctx, target, []model.Alert{unhealthy, failing}); err != nil {
		t.Fatal(err)
	}
	if again := alertRowVersions(t, s); !reflect.DeepEqual(first, again) {
		t.Fatalf("identical replace rewrote rows: before %v, after %v", first, again)
	}

	// One alert changes text, one is resolved, one is new; the other target is untouched.
	changed := unhealthy
	changed.Text = "Target group health checks fail: api-tg"
	fresh := model.Alert{Key: key(target, "api"), Kind: "cannot_place", Severity: model.AlertDanger, Title: "api: cannot place tasks", Text: "unable to place a task", At: at, Tab: "overview"}
	if err := s.ReplaceAlerts(ctx, target, []model.Alert{changed, fresh}); err != nil {
		t.Fatal(err)
	}
	after := alertRowVersions(t, s)
	if len(after) != 3 {
		t.Fatalf("rows after diff = %v, want 3 (changed, new, other target)", after)
	}
	if _, ok := after["482109375512/prod/worker/service_failing"]; ok {
		t.Errorf("resolved alert still present: %v", after)
	}
	if after["482109375512/prod/api/unhealthy_task"] == first["482109375512/prod/api/unhealthy_task"] {
		t.Errorf("changed alert was not updated")
	}
	if idOf(after["482109375512/prod/api/unhealthy_task"]) != idOf(first["482109375512/prod/api/unhealthy_task"]) {
		t.Errorf("changed alert got a new id: %s -> %s", first["482109375512/prod/api/unhealthy_task"], after["482109375512/prod/api/unhealthy_task"])
	}
	if after["482109375512/prod/api/cannot_place"] == "" {
		t.Errorf("new alert missing: %v", after)
	}
	for k, v := range first {
		if k == "482109375512/prod/api/unhealthy_task" || k == "482109375512/prod/worker/service_failing" {
			continue
		}
		if after[k] != v {
			t.Errorf("untouched row %s was rewritten: %s -> %s", k, v, after[k])
		}
	}
	listed, err := s.ListAlerts(ctx, store.AlertFilter{AccountID: target.AccountID, Region: target.Region})
	if err != nil || len(listed) != 2 {
		t.Fatalf("ListAlerts = %d, %v; want 2", len(listed), err)
	}
	for _, alert := range listed {
		if alert.Kind == "unhealthy_task" && alert.Text != changed.Text {
			t.Errorf("changed text not stored: %q", alert.Text)
		}
	}
}

func idOf(version string) string { return strings.SplitN(version, ":", 2)[0] }
