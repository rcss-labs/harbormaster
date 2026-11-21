package notify

import (
	"testing"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

func svc(account, region, cluster, service string) model.ServiceKey {
	return model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: account, Region: region, Cluster: cluster}, Service: service}
}

func TestSeverityName(t *testing.T) {
	for in, want := range map[model.AlertSeverity]string{model.AlertDanger: "critical", model.AlertWarning: "warning"} {
		if got, ok := SeverityName(in); !ok || got != want {
			t.Errorf("SeverityName(%s) = %q, %v", in, got, ok)
		}
	}
	if _, ok := SeverityName(model.AlertInfo); ok {
		t.Error("info alerts must never be notified")
	}
}

func TestMatches(t *testing.T) {
	key := svc("111111111111", "us-east-1", "core", "api")
	all := Scope{All: true}
	route := func(min string, kinds ...string) store.NotificationRoute {
		return store.NotificationRoute{MinSeverity: min, Kinds: kinds}
	}
	tests := []struct {
		name  string
		route store.NotificationRoute
		sub   Subject
		scope Scope
		want  bool
	}{
		{"warning route takes a critical", route("warning", "service_failing"), Subject{Key: key, Kind: "service_failing", Severity: "critical"}, all, true},
		{"warning route takes a warning", route("warning", "restart_loop"), Subject{Key: key, Kind: "restart_loop", Severity: "warning"}, all, true},
		{"critical-only route skips a warning", route("critical", "restart_loop"), Subject{Key: key, Kind: "restart_loop", Severity: "warning"}, all, false},
		{"critical-only route takes a critical", route("critical", "service_failing"), Subject{Key: key, Kind: "service_failing", Severity: "critical"}, all, true},
		{"unchecked kind", route("warning", "restart_loop"), Subject{Key: key, Kind: "service_failing", Severity: "critical"}, all, false},
		{"scope does not cover the key", route("warning", "service_failing"), Subject{Key: key, Kind: "service_failing", Severity: "critical"},
			Scope{Services: map[model.ServiceKey]bool{svc("111111111111", "us-east-1", "core", "other"): true}}, false},
		{"scope covers the key", route("warning", "service_failing"), Subject{Key: key, Kind: "service_failing", Severity: "critical"},
			Scope{Services: map[model.ServiceKey]bool{key: true}}, true},
	}
	for _, tc := range tests {
		if got := Matches(tc.route, tc.sub, tc.scope); got != tc.want {
			t.Errorf("%s: Matches = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestScope_Covers(t *testing.T) {
	x := svc("111111111111", "us-east-1", "core", "x")
	y := svc("111111111111", "us-east-1", "core", "y")
	accountLevel := model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: "111111111111", Region: "us-east-1"}}
	clusterEntry := GroupSelector{Kind: "cluster", AccountID: "111111111111", Region: "us-east-1", Cluster: "core", Threshold: 1}
	accountEntry := GroupSelector{Kind: "account", AccountID: "111111111111", Threshold: 1}

	scopes := map[string]Scope{
		"all":             {All: true},
		"service x":       {Services: map[model.ServiceKey]bool{x: true}},
		"cluster entry":   {Groups: []GroupSelector{clusterEntry}},
		"account entry":   {Groups: []GroupSelector{accountEntry}},
		"service+cluster": {Services: map[model.ServiceKey]bool{x: true}, Groups: []GroupSelector{clusterEntry}},
		"empty":           {},
	}
	tests := []struct {
		scope string
		key   model.ServiceKey
		want  bool
	}{
		{"all", x, true}, {"all", accountLevel, true},
		{"service x", x, true}, {"service x", y, false}, {"service x", accountLevel, false},
		// Cluster and account entries are threshold entries (D35): they are never per-alert coverage.
		{"cluster entry", x, false}, {"cluster entry", y, false}, {"cluster entry", accountLevel, false},
		{"account entry", x, false}, {"account entry", accountLevel, false},
		{"service+cluster", x, true}, {"service+cluster", y, false},
		{"empty", x, false}, {"empty", accountLevel, false},
	}
	for _, tc := range tests {
		if got := scopes[tc.scope].Covers(tc.key); got != tc.want {
			t.Errorf("scope %q covers %+v = %v, want %v", tc.scope, tc.key, got, tc.want)
		}
	}
}

func TestGroupSelector_ContainsIsExact(t *testing.T) {
	cluster := GroupSelector{Kind: "cluster", AccountID: "1", Region: "us-east-1", Cluster: "core", Threshold: 2}
	account := GroupSelector{Kind: "account", AccountID: "1", Threshold: 1}
	accountRegion := GroupSelector{Kind: "account", AccountID: "1", Region: "eu-west-1", Threshold: 1}
	inCluster := svc("1", "us-east-1", "core", "api")
	for name, tc := range map[string]struct {
		g    GroupSelector
		key  model.ServiceKey
		want bool
	}{
		"cluster holds its service":               {cluster, inCluster, true},
		"cluster, same name other cluster":        {cluster, svc("1", "us-east-1", "batch", "api"), false},
		"cluster, same name other region":         {cluster, svc("1", "eu-west-1", "core", "api"), false},
		"cluster, same name other account":        {cluster, svc("2", "us-east-1", "core", "api"), false},
		"cluster does not hold the account key":   {cluster, model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: "1", Region: "us-east-1"}}, false},
		"account holds every region":              {account, svc("1", "eu-west-1", "x", "y"), true},
		"account holds its account-level key":     {account, model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: "1", Region: "us-east-1"}}, true},
		"account, other account":                  {account, svc("2", "us-east-1", "x", "y"), false},
		"account x region holds that region":      {accountRegion, svc("1", "eu-west-1", "x", "y"), true},
		"account x region, another region":        {accountRegion, svc("1", "us-east-1", "x", "y"), false},
		"account x region, its account-level key": {accountRegion, model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: "1", Region: "eu-west-1"}}, true},
	} {
		if got := tc.g.Contains(tc.key); got != tc.want {
			t.Errorf("%s: Contains = %v, want %v", name, got, tc.want)
		}
	}
	if k := cluster.Key(); k != svc("1", "us-east-1", "core", "") {
		t.Errorf("cluster group key = %+v", k)
	}
	if k := accountRegion.Key(); k != (model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: "1", Region: "eu-west-1"}}) {
		t.Errorf("account group key = %+v", k)
	}
	if k := account.Key(); k.Region != "" || k.Cluster != "" || k.Service != "" {
		t.Errorf("all-regions account group key = %+v, want only the account", k)
	}
}
