package notify

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// TagMatcher finds the services carrying tags. *store.Store satisfies it.
type TagMatcher interface {
	OffHoursMatches(ctx context.Context, selectors []store.OffHoursSelector) ([]store.OffHoursMatch, error)
}

// ResolveScope turns a route's selectors into sets. No selectors means everything. Account, cluster and service
// selectors are natural keys and need no database read: a key that does not exist simply never equals an alert's
// key. The two tag kinds go together to one OffHoursMatches call and add the services they select. A failed lookup
// returns an error and an empty scope: callers must not treat it as "matches nothing".
func ResolveScope(ctx context.Context, tags TagMatcher, selectors []store.NotificationSelector) (Scope, error) {
	if len(selectors) == 0 {
		return Scope{All: true}, nil
	}
	scope := Scope{
		Accounts: map[string]bool{}, Targets: map[awsx.Target]bool{},
		Clusters: map[model.ClusterKey]bool{}, Services: map[model.ServiceKey]bool{},
	}
	var tagSelectors []store.OffHoursSelector
	for _, sel := range selectors {
		switch sel.Kind {
		case store.NotificationSelectorAccount:
			if sel.Region == "" {
				scope.Accounts[sel.AccountID] = true
			} else {
				scope.Targets[awsx.Target{AccountID: sel.AccountID, Region: sel.Region}] = true
			}
		case store.NotificationSelectorCluster:
			scope.Clusters[model.ClusterKey{AccountID: sel.AccountID, Region: sel.Region, Cluster: sel.Cluster}] = true
		case store.NotificationSelectorService:
			scope.Services[model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: sel.AccountID, Region: sel.Region, Cluster: sel.Cluster}, Service: sel.Service}] = true
		case store.NotificationSelectorAWS, store.NotificationSelectorHarbormaster:
			tagSelectors = append(tagSelectors, store.OffHoursSelector{Kind: sel.Kind, Key: sel.Key, Value: sel.Value, TagID: sel.TagID})
		}
	}
	if len(tagSelectors) > 0 {
		matches, err := tags.OffHoursMatches(ctx, tagSelectors)
		if err != nil {
			return Scope{}, fmt.Errorf("resolve notification scope: %w", err)
		}
		for _, m := range matches {
			scope.Services[m.Key] = true
		}
	}
	return scope, nil
}

// SelectorInfo is a selector with the names the UI shows and whether its target is gone.
type SelectorInfo struct {
	Selector             store.NotificationSelector
	AccountName, TagName string
	Missing              bool
}

// DescribeScope names each selector and flags the ones whose target no longer exists. The order is stable:
// accounts, clusters, services, Harbormaster tags, AWS tags, each by display name. Tag selectors are never missing
// (deleting a Harbormaster tag removes its selector). A disabled account is present, not missing.
func DescribeScope(selectors []store.NotificationSelector, targets store.NotificationTargets, tags []store.Tag) []SelectorInfo {
	accounts := map[string]store.AWSAccount{}
	for _, a := range targets.Accounts {
		accounts[a.AccountID] = a
	}
	clusters := map[model.ClusterKey]bool{}
	for _, c := range targets.Clusters {
		clusters[c.Key] = true
	}
	services := map[model.ServiceKey]bool{}
	for _, s := range targets.Services {
		services[s.Key] = true
	}
	tagNames := map[int64]string{}
	for _, t := range tags {
		tagNames[int64(t.ID)] = t.Name
	}

	out := make([]SelectorInfo, 0, len(selectors))
	for _, sel := range selectors {
		info := SelectorInfo{Selector: sel}
		switch sel.Kind {
		case store.NotificationSelectorAccount:
			account, ok := accounts[sel.AccountID]
			info.AccountName = account.Name
			info.Missing = !ok || (sel.Region != "" && !slices.Contains(account.Regions, sel.Region))
		case store.NotificationSelectorCluster:
			info.AccountName = accounts[sel.AccountID].Name
			info.Missing = !clusters[model.ClusterKey{AccountID: sel.AccountID, Region: sel.Region, Cluster: sel.Cluster}]
		case store.NotificationSelectorService:
			info.AccountName = accounts[sel.AccountID].Name
			info.Missing = !services[model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: sel.AccountID, Region: sel.Region, Cluster: sel.Cluster}, Service: sel.Service}]
		case store.NotificationSelectorHarbormaster:
			info.TagName = tagNames[int64(sel.TagID)]
		}
		out = append(out, info)
	}
	rank := map[string]int{
		store.NotificationSelectorAccount: 0, store.NotificationSelectorCluster: 1, store.NotificationSelectorService: 2,
		store.NotificationSelectorHarbormaster: 3, store.NotificationSelectorAWS: 4,
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if rank[a.Selector.Kind] != rank[b.Selector.Kind] {
			return rank[a.Selector.Kind] < rank[b.Selector.Kind]
		}
		return selectorSortKey(a) < selectorSortKey(b)
	})
	return out
}

func selectorSortKey(info SelectorInfo) string {
	s := info.Selector
	name := info.AccountName
	if name == "" {
		name = s.AccountID
	}
	return strings.ToLower(strings.Join([]string{name, s.Region, s.Cluster, s.Service, info.TagName, s.Key, s.Value}, "\x00"))
}
