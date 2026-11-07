package authz

import (
	"testing"

	"github.com/rcss-labs/harbormaster/internal/model"
)

// mapTags is a minimal in-test implementation of Tags.
type mapTags struct {
	dev      TagID
	clusters map[model.ClusterKey]TagID
	services map[model.ServiceKey]TagID
}

func (m mapTags) DevTagID() TagID { return m.dev }
func (m mapTags) ClusterTag(k model.ClusterKey) (TagID, bool) {
	id, ok := m.clusters[k]
	return id, ok
}
func (m mapTags) ServiceTag(k model.ServiceKey) (TagID, bool) {
	id, ok := m.services[k]
	return id, ok
}

const (
	tagDev     TagID = 1
	tagStaging TagID = 2
	tagProd    TagID = 3
)

func TestNormalize(t *testing.T) {
	tests := []struct {
		name string
		in   ActionSet
		want ActionSet
	}{
		{"logs implies view", ActionSet(Logs), ActionSet(View | Logs)},
		{"empty stays empty", 0, 0},
		{"deploy+edit implies view", ActionSet(Deploy | Edit), ActionSet(View | Deploy | Edit)},
		{"already normalized", ActionSet(View | Terminal), ActionSet(View | Terminal)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Normalize(tc.in); got != tc.want {
				t.Fatalf("Normalize(%b) = %b, want %b", tc.in, got, tc.want)
			}
		})
	}
}

func TestPresetName(t *testing.T) {
	for name, set := range Presets {
		if got := PresetName(set); got != name {
			t.Errorf("PresetName(%q set) = %q", name, got)
		}
	}
	if got := PresetName(ActionSet(View | Terminal)); got != "Custom" {
		t.Errorf("View|Terminal = %q, want Custom", got)
	}
	if got := PresetName(ActionSet(Terminal)); got != "Custom" {
		t.Errorf("Terminal alone normalizes to View|Terminal, want Custom, got %q", got)
	}
}

func TestCan_RequiresViewAndAction(t *testing.T) {
	noView := Subject{Perms: map[TagID]ActionSet{tagProd: ActionSet(Logs | Operate)}}
	for _, a := range []Action{View, Logs, Operate, Deploy, Edit, Terminal} {
		if Can(noView, a, tagProd) {
			t.Errorf("Can without View granted action %d", a)
		}
	}
	withView := Subject{Perms: map[TagID]ActionSet{tagProd: ActionSet(View | Logs)}}
	if !Can(withView, View, tagProd) || !Can(withView, Logs, tagProd) {
		t.Error("View|Logs should allow View and Logs")
	}
	if Can(withView, Operate, tagProd) || Can(withView, Logs, tagDev) {
		t.Error("unexpected grant")
	}
	admin := Subject{Admin: true}
	for _, a := range []Action{View, Logs, Operate, Deploy, Edit, Terminal} {
		if !Can(admin, a, tagProd) {
			t.Errorf("admin denied action %d", a)
		}
	}
}

var (
	clusterA = model.ClusterKey{AccountID: "1", Region: "us-east-1", Cluster: "a"}
	svcA     = model.ServiceKey{ClusterKey: clusterA, Service: "web"}
)

func TestEffectiveServiceTag_OwnThenClusterThenDev(t *testing.T) {
	tags := mapTags{dev: tagDev, clusters: map[model.ClusterKey]TagID{}, services: map[model.ServiceKey]TagID{}}
	if got := EffectiveServiceTag(tags, svcA); got != tagDev {
		t.Fatalf("untagged = %d, want Dev", got)
	}
	tags.clusters[clusterA] = tagStaging
	if got := EffectiveServiceTag(tags, svcA); got != tagStaging {
		t.Fatalf("cluster-tagged = %d, want Staging", got)
	}
	tags.services[svcA] = tagProd
	if got := EffectiveServiceTag(tags, svcA); got != tagProd {
		t.Fatalf("service-tagged = %d, want Prod", got)
	}
	if got := EffectiveClusterTag(tags, clusterA); got != tagStaging {
		t.Fatalf("cluster tag = %d, want Staging", got)
	}
}

func TestEffectiveTaskTag_StandaloneUsesClusterTag(t *testing.T) {
	tags := mapTags{dev: tagDev, clusters: map[model.ClusterKey]TagID{clusterA: tagStaging},
		services: map[model.ServiceKey]TagID{svcA: tagProd}}
	if got := EffectiveTaskTag(tags, clusterA, ""); got != tagStaging {
		t.Fatalf("standalone = %d, want cluster tag Staging", got)
	}
	if got := EffectiveTaskTag(tags, clusterA, "web"); got != tagProd {
		t.Fatalf("service task = %d, want Prod", got)
	}
}

func TestCanSeeCluster_AnyVisibleService(t *testing.T) {
	other := model.ServiceKey{ClusterKey: clusterA, Service: "api"}
	tags := mapTags{dev: tagDev, clusters: map[model.ClusterKey]TagID{clusterA: tagDev},
		services: map[model.ServiceKey]TagID{other: tagProd}}
	prodOnly := Subject{Perms: map[TagID]ActionSet{tagProd: ActionSet(View)}}
	if !CanSeeCluster(prodOnly, tags, clusterA, []string{"web", "api"}) {
		t.Error("one visible service should make the cluster visible")
	}
	if CanSeeCluster(prodOnly, tags, clusterA, []string{"web"}) {
		t.Error("no visible service should hide the cluster")
	}
	if CanSeeCluster(prodOnly, tags, clusterA, nil) {
		t.Error("cluster without services is invisible to non-admins")
	}
	if !CanSeeCluster(Subject{Admin: true}, tags, clusterA, nil) {
		t.Error("admin sees everything")
	}
}

func TestVisibleTagsAndPermissionsOf(t *testing.T) {
	s := Subject{Perms: map[TagID]ActionSet{tagProd: ActionSet(View | Logs), tagDev: ActionSet(View), tagStaging: ActionSet(Logs)}}
	got := VisibleTags(s)
	if len(got) != 2 || got[0] != tagDev || got[1] != tagProd {
		t.Fatalf("VisibleTags = %v, want [1 3]", got)
	}
	p := PermissionsOf(s, tagProd)
	want := map[string]bool{"view": true, "logs": true, "operate": false, "deploy": false, "edit": false, "terminal": false}
	if len(p) != 6 {
		t.Fatalf("PermissionsOf keys = %v", p)
	}
	for k, v := range want {
		if p[k] != v {
			t.Errorf("%s = %v, want %v", k, p[k], v)
		}
	}
	for k, v := range PermissionsOf(Subject{Admin: true}, tagProd) {
		if !v {
			t.Errorf("admin %s false", k)
		}
	}
}

func TestActionNames_IncludesAcceptRiskLast(t *testing.T) {
	got := ActionNames()
	want := []struct {
		a    Action
		n, l string
	}{{View, "View", "V"}, {Logs, "Logs", "L"}, {Operate, "Operate", "O"}, {Deploy, "Deploy", "D"}, {Edit, "Edit task definition", "E"}, {Terminal, "Terminal", "T"}, {AcceptRisk, "Accept risk", "R"}}
	if len(got) != len(want) {
		t.Fatalf("len = %d", len(got))
	}
	for i, w := range want {
		if got[i].Action != w.a || got[i].Name != w.n || got[i].Letter != w.l {
			t.Errorf("[%d] = %+v", i, got[i])
		}
	}
	if AcceptRisk != 64 {
		t.Errorf("AcceptRisk = %d, want 64", AcceptRisk)
	}
}

// Accepting a vulnerability is a separate responsibility: no preset grants it, and Normalize
// never adds it, so a value stored before the permission existed (0..63) cannot gain it.
func TestPresets_NoneGrantAcceptRisk(t *testing.T) {
	for name, set := range Presets {
		if set.Has(AcceptRisk) {
			t.Errorf("preset %s grants Accept risk", name)
		}
		if Normalize(set).Has(AcceptRisk) {
			t.Errorf("Normalize(%s) grants Accept risk", name)
		}
	}
	if Normalize(ActionSet(View)).Has(AcceptRisk) {
		t.Error("Normalize(View) grants Accept risk")
	}
	if Presets["Full"] != 63 {
		t.Errorf("Full = %d, want 63", Presets["Full"])
	}
	for stored := ActionSet(0); stored <= 63; stored++ {
		if Normalize(stored).Has(AcceptRisk) {
			t.Fatalf("stored value %d gains Accept risk", stored)
		}
	}
	if got := PresetName(ActionSet(View | AcceptRisk)); got != "Custom" {
		t.Errorf("View|AcceptRisk = %q, want Custom", got)
	}
	if got := PresetName(Presets["Full"] | ActionSet(AcceptRisk)); got != "Custom" {
		t.Errorf("Full|AcceptRisk = %q, want Custom", got)
	}
	if got := Normalize(ActionSet(AcceptRisk)); got != ActionSet(View|AcceptRisk) {
		t.Errorf("Normalize(AcceptRisk) = %d, want 65", got)
	}
}

func TestMockupUsers(t *testing.T) {
	// c1 Prod, c2-c5 Dev, c6 Staging; one service per cluster inheriting the cluster tag.
	tags := mapTags{dev: tagDev, clusters: map[model.ClusterKey]TagID{}, services: map[model.ServiceKey]TagID{}}
	clusterTags := []TagID{tagProd, tagDev, tagDev, tagDev, tagDev, tagStaging}
	var clusters []model.ClusterKey
	for i, tag := range clusterTags {
		k := model.ClusterKey{AccountID: "1", Region: "us-east-1", Cluster: string(rune('a' + i))}
		clusters = append(clusters, k)
		tags.clusters[k] = tag
	}
	P := func(a ...Action) ActionSet {
		var s ActionSet
		for _, x := range a {
			s |= ActionSet(x)
		}
		return s
	}
	dana := Subject{Username: "dana", Perms: map[TagID]ActionSet{
		tagDev: Presets["Full"], tagStaging: Presets["Deployer"], tagProd: P(View, Logs)}}
	omer := Subject{Username: "omer", Perms: map[TagID]ActionSet{
		tagDev: Presets["Operator"], tagStaging: P(View)}}
	oncall := Subject{Username: "oncall", Perms: map[TagID]ActionSet{
		tagStaging: P(View, Logs, Operate, Terminal), tagProd: P(View, Logs, Operate, Terminal)}}

	count := func(s Subject) int {
		n := 0
		for _, k := range clusters {
			if CanSeeCluster(s, tags, k, []string{"svc"}) {
				n++
			}
		}
		return n
	}
	if n := count(omer); n != 5 {
		t.Errorf("omer sees %d clusters, want 5", n)
	}
	if n := count(oncall); n != 2 {
		t.Errorf("oncall sees %d clusters, want 2", n)
	}
	if n := count(dana); n != 6 {
		t.Errorf("dana sees %d clusters, want 6", n)
	}
	if !Can(oncall, Terminal, tagProd) || Can(oncall, Deploy, tagProd) {
		t.Error("oncall Prod: want Terminal yes, Deploy no")
	}
	if Can(dana, Operate, tagProd) || !Can(dana, Edit, tagStaging) || Can(dana, Terminal, tagStaging) {
		t.Error("dana permission mismatch")
	}
}
