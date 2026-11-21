package notify

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

type fakeTagMatcher struct {
	calls   [][]store.OffHoursSelector
	matches []store.OffHoursMatch
	err     error
}

func (f *fakeTagMatcher) OffHoursMatches(_ context.Context, selectors []store.OffHoursSelector) ([]store.OffHoursMatch, error) {
	f.calls = append(f.calls, selectors)
	return f.matches, f.err
}

func TestResolveScope_EmptyIsAll(t *testing.T) {
	tags := &fakeTagMatcher{}
	scope, err := ResolveScope(context.Background(), tags, nil)
	if err != nil || !scope.All {
		t.Fatalf("scope = %+v err=%v, want All", scope, err)
	}
	if len(tags.calls) != 0 {
		t.Error("an empty scope must not query")
	}
}

func TestResolveScope_KeysAreExact(t *testing.T) {
	a, b := model.ClusterKey{AccountID: "1", Region: "us-east-1", Cluster: "a"}, model.ClusterKey{AccountID: "1", Region: "us-east-1", Cluster: "b"}
	scope, err := ResolveScope(context.Background(), &fakeTagMatcher{}, []store.NotificationSelector{
		{Kind: "service", AccountID: "1", Region: "us-east-1", Cluster: "a", Service: "api"},
		{Kind: "cluster", AccountID: "1", Region: "us-east-1", Cluster: "b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	covers := func(k model.ServiceKey) bool { return scope.Covers(k) }
	for name, tc := range map[string]struct {
		key  model.ServiceKey
		want bool
	}{
		"the picked service":               {model.ServiceKey{ClusterKey: a, Service: "api"}, true},
		"another service in that cluster":  {model.ServiceKey{ClusterKey: a, Service: "web"}, false},
		"every service of the cluster":     {model.ServiceKey{ClusterKey: b, Service: "anything"}, true},
		"same service name, other cluster": {model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: "1", Region: "us-east-1", Cluster: "c"}, Service: "api"}, false},
		"cluster b in another region":      {model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: "1", Region: "eu-west-1", Cluster: "b"}, Service: "x"}, false},
		"cluster b in another account":     {model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: "2", Region: "us-east-1", Cluster: "b"}, Service: "x"}, false},
		"the account-level alert":          {model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: "1", Region: "us-east-1"}}, false},
	} {
		if got := covers(tc.key); got != tc.want {
			t.Errorf("%s: covers = %v, want %v", name, got, tc.want)
		}
	}
}

func TestResolveScope_AccountWithAndWithoutRegion(t *testing.T) {
	scope, err := ResolveScope(context.Background(), &fakeTagMatcher{}, []store.NotificationSelector{
		{Kind: "account", AccountID: "1"},
		{Kind: "account", AccountID: "2", Region: "eu-west-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !scope.Accounts["1"] || scope.Accounts["2"] || !scope.Targets[awsx.Target{AccountID: "2", Region: "eu-west-1"}] {
		t.Fatalf("scope = %+v", scope)
	}
	acct := func(a, r string) model.ServiceKey {
		return model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: a, Region: r}}
	}
	if !scope.Covers(acct("1", "us-east-1")) || !scope.Covers(acct("1", "eu-west-1")) || !scope.Covers(acct("2", "eu-west-1")) || scope.Covers(acct("2", "us-east-1")) {
		t.Error("account and account x region selectors cover the wrong account-level alerts")
	}
}

func TestResolveScope_TagSelectorsUseOneQuery(t *testing.T) {
	svcA := model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: "1", Region: "r", Cluster: "c"}, Service: "tagged"}
	tags := &fakeTagMatcher{matches: []store.OffHoursMatch{{Key: svcA}}}
	scope, err := ResolveScope(context.Background(), tags, []store.NotificationSelector{
		{Kind: "aws", Key: "env", Value: "prod"}, {Kind: "aws", Key: "team", Value: "web"}, {Kind: "harbormaster", TagID: authz.TagID(7)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(tags.calls) != 1 || len(tags.calls[0]) != 3 {
		t.Fatalf("OffHoursMatches calls = %v, want one call carrying all three", tags.calls)
	}
	if !scope.Covers(svcA) {
		t.Error("a service the tags select must be covered")
	}

	tags2 := &fakeTagMatcher{}
	if _, err := ResolveScope(context.Background(), tags2, []store.NotificationSelector{{Kind: "account", AccountID: "1"}, {Kind: "cluster", AccountID: "1", Region: "r", Cluster: "c"}}); err != nil {
		t.Fatal(err)
	}
	if len(tags2.calls) != 0 {
		t.Errorf("a route without tag selectors queried %d times", len(tags2.calls))
	}
}

func TestResolveScope_TagAndKeySelectorsUnion(t *testing.T) {
	tagged := model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: "1", Region: "r", Cluster: "c"}, Service: "tagged"}
	picked := model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: "1", Region: "r", Cluster: "c"}, Service: "picked"}
	other := model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: "1", Region: "r", Cluster: "c"}, Service: "other"}
	scope, err := ResolveScope(context.Background(), &fakeTagMatcher{matches: []store.OffHoursMatch{{Key: tagged}}}, []store.NotificationSelector{
		{Kind: "aws", Key: "env", Value: "prod"},
		{Kind: "service", AccountID: "1", Region: "r", Cluster: "c", Service: "picked"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !scope.Covers(tagged) || !scope.Covers(picked) || scope.Covers(other) {
		t.Errorf("union wrong: tagged=%v picked=%v other=%v", scope.Covers(tagged), scope.Covers(picked), scope.Covers(other))
	}
}

func TestResolveScope_TagQueryErrorIsReturned(t *testing.T) {
	boom := errors.New("database is down")
	scope, err := ResolveScope(context.Background(), &fakeTagMatcher{err: boom}, []store.NotificationSelector{
		{Kind: "aws", Key: "env", Value: "prod"}, {Kind: "account", AccountID: "1"},
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the lookup error", err)
	}
	if scope.All || len(scope.Accounts) != 0 || len(scope.Services) != 0 {
		t.Errorf("a failed resolve must not return a partial scope: %+v", scope)
	}
}

func TestDescribeScope_NamesOrderAndMissing(t *testing.T) {
	targets := store.NotificationTargets{
		Accounts: []store.AWSAccount{
			{AccountID: "111111111111", Name: "prod", Regions: []string{"us-east-1"}, Enabled: true},
			{AccountID: "222222222222", Name: "staging", Regions: []string{"eu-west-1"}, Enabled: false},
		},
		Clusters: []store.NotificationTargetCluster{{Key: model.ClusterKey{AccountID: "111111111111", Region: "us-east-1", Cluster: "core"}, AccountName: "prod"}},
		Services: []store.NotificationTargetService{{Key: model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: "111111111111", Region: "us-east-1", Cluster: "core"}, Service: "api"}, AccountName: "prod"}},
	}
	selectors := []store.NotificationSelector{
		{Kind: "aws", Key: "env", Value: "prod"},
		{Kind: "harbormaster", TagID: authz.TagID(7)},
		{Kind: "service", AccountID: "111111111111", Region: "us-east-1", Cluster: "core", Service: "gone"},
		{Kind: "service", AccountID: "111111111111", Region: "us-east-1", Cluster: "core", Service: "api"},
		{Kind: "cluster", AccountID: "111111111111", Region: "us-east-1", Cluster: "renamed"},
		{Kind: "cluster", AccountID: "111111111111", Region: "us-east-1", Cluster: "core"},
		{Kind: "account", AccountID: "999999999999"},
		{Kind: "account", AccountID: "111111111111", Region: "ap-south-1"},
		{Kind: "account", AccountID: "222222222222"},
	}
	got := DescribeScope(selectors, targets, []store.Tag{{ID: 7, Name: "Payments"}})
	if len(got) != len(selectors) {
		t.Fatalf("described %d selectors, want %d", len(got), len(selectors))
	}
	kinds := make([]string, len(got))
	for i, info := range got {
		kinds[i] = info.Selector.Kind
	}
	keyOf := func(s store.NotificationSelector) string {
		return strings.Join([]string{s.Kind, s.AccountID, s.Region, s.Cluster, s.Service, s.Key}, "|")
	}
	byKey := map[string]SelectorInfo{}
	for _, info := range got {
		byKey[keyOf(info.Selector)] = info
	}
	check := func(sel store.NotificationSelector, missing bool, name string) {
		t.Helper()
		key := keyOf(sel)
		info, ok := byKey[key]
		if !ok {
			t.Fatalf("selector %q not described", key)
		}
		if info.Missing != missing {
			t.Errorf("%s: Missing = %v, want %v", key, info.Missing, missing)
		}
		if name != "" && info.AccountName != name && info.TagName != name {
			t.Errorf("%s: name = %q/%q, want %q", key, info.AccountName, info.TagName, name)
		}
	}
	check(selectors[2], true, "prod")
	check(selectors[3], false, "prod")
	check(selectors[4], true, "prod")
	check(selectors[5], false, "prod")
	check(selectors[6], true, "")
	check(selectors[7], true, "prod")
	check(selectors[8], false, "staging") // disabled is present, not missing
	check(selectors[1], false, "Payments")
	check(selectors[0], false, "")

	// Order: accounts, clusters, services, Harbormaster tags, AWS tags.
	rank := map[string]int{"account": 0, "cluster": 1, "service": 2, "harbormaster": 3, "aws": 4}
	for i := 1; i < len(got); i++ {
		if rank[got[i].Selector.Kind] < rank[got[i-1].Selector.Kind] {
			t.Fatalf("kinds out of order at %d: %v", i, kinds)
		}
	}
}
