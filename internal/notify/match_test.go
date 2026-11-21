package notify

import (
	"testing"

	"github.com/rcss-labs/harbormaster/internal/awsx"
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
	otherService := svc("111111111111", "us-east-1", "core", "y")
	otherCluster := svc("111111111111", "us-east-1", "batch", "x")
	otherRegionCluster := svc("111111111111", "eu-west-1", "core", "x")
	sameNameOtherAccount := svc("222222222222", "us-east-1", "core", "x")
	accountLevel := model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: "111111111111", Region: "us-east-1"}}

	scopes := map[string]Scope{
		"all":            {All: true},
		"service x":      {Services: map[model.ServiceKey]bool{x: true}},
		"cluster core":   {Clusters: map[model.ClusterKey]bool{x.ClusterKey: true}},
		"target":         {Targets: map[awsx.Target]bool{{AccountID: "111111111111", Region: "us-east-1"}: true}},
		"account":        {Accounts: map[string]bool{"111111111111": true}},
		"empty":          {},
		"another target": {Targets: map[awsx.Target]bool{{AccountID: "111111111111", Region: "eu-west-1"}: true}},
	}
	tests := []struct {
		scope string
		key   model.ServiceKey
		want  bool
	}{
		{"all", x, true}, {"all", accountLevel, true},
		{"service x", x, true}, {"service x", otherService, false}, {"service x", otherCluster, false},
		{"service x", otherRegionCluster, false}, {"service x", sameNameOtherAccount, false}, {"service x", accountLevel, false},
		{"cluster core", x, true}, {"cluster core", otherService, true}, {"cluster core", otherCluster, false},
		{"cluster core", otherRegionCluster, false}, {"cluster core", sameNameOtherAccount, false}, {"cluster core", accountLevel, false},
		{"target", x, true}, {"target", otherCluster, true}, {"target", otherRegionCluster, false}, {"target", sameNameOtherAccount, false}, {"target", accountLevel, true},
		{"account", x, true}, {"account", otherRegionCluster, true}, {"account", sameNameOtherAccount, false}, {"account", accountLevel, true},
		{"another target", accountLevel, false}, {"another target", otherRegionCluster, true}, {"another target", x, false},
		{"empty", x, false}, {"empty", accountLevel, false},
	}
	for _, tc := range tests {
		if got := scopes[tc.scope].Covers(tc.key); got != tc.want {
			t.Errorf("scope %q covers %+v = %v, want %v", tc.scope, tc.key, got, tc.want)
		}
	}
}
