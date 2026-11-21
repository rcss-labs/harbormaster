package notify

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// maxGroupLines is how many failing services an aggregated message lists before "and N more" (D37).
const maxGroupLines = 10

// Notifiable reports whether an alert incident may notify and count toward a threshold now. An incident inside its
// resolve grace is not. A scale-up from zero raises service_failing and a scale-out raises service_degraded until
// the new tasks run, so those two kinds wait for the settle time; every other kind is immediate (D42). A settle of
// zero or less turns the wait off.
func Notifiable(i store.NotificationIncident, now time.Time, settle time.Duration) bool {
	if i.MissingSince != nil {
		return false
	}
	if (i.Kind == "service_failing" || i.Kind == "service_degraded") && settle > 0 {
		return now.Sub(i.OpenedAt) >= settle
	}
	return true
}

// Totals is how many ACTIVE services each cluster, account and account x region has: the "of M" of a message.
type Totals struct {
	Clusters map[model.ClusterKey]int
	Targets  map[awsx.Target]int
	Accounts map[string]int
}

// TotalsFrom counts the services of the picker data.
func TotalsFrom(t store.NotificationTargets) Totals {
	totals := Totals{Clusters: map[model.ClusterKey]int{}, Targets: map[awsx.Target]int{}, Accounts: map[string]int{}}
	for _, s := range t.Services {
		totals.Clusters[s.Key.ClusterKey]++
		totals.Targets[awsx.Target{AccountID: s.Key.AccountID, Region: s.Key.Region}]++
		totals.Accounts[s.Key.AccountID]++
	}
	return totals
}

// GroupPreview is an entry that has failing services but has not reached its threshold.
type GroupPreview struct {
	State       store.GroupState
	Open        bool
	Unreachable bool
}

// Plan is what should be notified right now. It is pure: the dispatcher and the preview both build it, so the
// preview cannot drift from what is sent.
type Plan struct {
	PerAlert map[int64][]int64  // alert incident id -> ids of the routes that take it per alert
	Groups   []store.GroupState // group incidents that should be open, for SyncNotificationGroups
	Below    []GroupPreview     // entries with at least one failing service but under their threshold
	Covered  map[int64]bool     // alert incident id -> a non-catch-all route notifies about it
}

// BuildPlan decides, for every enabled route, which alert incidents it takes per alert (service and tag entries and
// the empty scope) and which group incidents its cluster and account entries should have open. Only incidents that
// are Notifiable count. Non-catch-all routes are planned first; a catch-all route then sees only the incidents no
// other route notified about (D28, D41), so it never suppresses another route.
func BuildPlan(routes []store.NotificationRoute, scopes map[int64]Scope, incidents []store.NotificationIncident,
	totals Totals, accountNames map[string]string, now time.Time, settle time.Duration) Plan {
	plan := Plan{PerAlert: map[int64][]int64{}, Covered: map[int64]bool{}}

	var candidates []candidate
	for _, incident := range incidents {
		if incident.GroupRouteID != 0 || !Notifiable(incident, now, settle) {
			continue
		}
		severity, ok := SeverityName(incident.Severity)
		if !ok {
			continue
		}
		name := accountNames[incident.Key.AccountID]
		if name == "" {
			name = incident.Key.AccountID
		}
		candidates = append(candidates, candidate{incident, Subject{Key: incident.Key, Kind: incident.Kind, Severity: severity,
			Title: incident.Title, Text: incident.Text, At: incident.OpenedAt, AccountName: name}})
	}

	for _, pass := range []bool{false, true} { // false: ordinary routes, true: catch-alls
		var visible []candidate
		for _, c := range candidates {
			if !pass || !plan.Covered[c.incident.ID] {
				visible = append(visible, c)
			}
		}
		for _, r := range routes {
			if !r.Enabled || r.Fallback != pass {
				continue
			}
			scope := scopes[r.ID]
			for _, c := range visible {
				if Matches(r, c.subject, scope) {
					plan.PerAlert[c.incident.ID] = append(plan.PerAlert[c.incident.ID], r.ID)
					if !pass {
						plan.Covered[c.incident.ID] = true
					}
				}
			}
			for _, g := range scope.Groups {
				state, candidate, wanted := planGroup(r, g, visible, totals, accountNames)
				switch {
				case wanted:
					plan.Groups = append(plan.Groups, state)
					if !pass {
						for _, id := range candidate {
							plan.Covered[id] = true
						}
					}
				case state.Failing > 0:
					plan.Below = append(plan.Below, GroupPreview{State: state})
				}
			}
		}
	}
	return plan
}

// candidate is a notifiable alert incident with the subject the route filters see.
type candidate struct {
	incident store.NotificationIncident
	subject  Subject
}

// planGroup computes one entry's group for route r: the failing services it counts, whether the threshold (or an
// unreachable account) wants a notification, and the state to store when it does. state.Failing is set even when
// the group is not wanted, so the preview can say "1 of 3, below 2".
func planGroup(r store.NotificationRoute, g GroupSelector, candidates []candidate, totals Totals, accountNames map[string]string) (state store.GroupState, ids []int64, wanted bool) {
	best := map[[2]string]candidate{} // {cluster, service} -> its most severe candidate alert
	var unreachable []candidate
	for _, c := range candidates {
		if !passesFilters(r, c.subject) || !g.Contains(c.incident.Key) {
			continue
		}
		ids = append(ids, c.incident.ID)
		if c.incident.Key.Service == "" {
			if c.incident.Kind == "account_unreachable" && g.Kind == store.NotificationSelectorAccount {
				unreachable = append(unreachable, c)
			}
			continue
		}
		key := [2]string{c.incident.Key.Cluster, c.incident.Key.Service}
		if current, ok := best[key]; !ok || moreSevere(c, current) {
			best[key] = c
		}
	}

	members := make([]store.IncidentMember, 0, len(best))
	critical := len(unreachable) > 0
	for _, c := range best {
		isCritical := c.incident.Severity == model.AlertDanger
		critical = critical || isCritical
		members = append(members, store.IncidentMember{Cluster: c.incident.Key.Cluster, Service: c.incident.Key.Service,
			Kind: c.incident.Kind, Title: c.incident.Title, Critical: isCritical})
	}
	sort.Slice(members, func(i, j int) bool {
		a, b := members[i], members[j]
		if a.Critical != b.Critical {
			return a.Critical
		}
		if a.Cluster != b.Cluster {
			return a.Cluster < b.Cluster
		}
		return a.Service < b.Service
	})
	sort.Slice(unreachable, func(i, j int) bool { return unreachable[i].incident.Key.Region < unreachable[j].incident.Key.Region })

	severity := model.AlertWarning
	if critical {
		severity = model.AlertDanger
	}
	state = store.GroupState{
		RouteID: r.ID, ScopeKind: g.Kind, Key: g.Key(), Kind: KindClusterFailing, Severity: severity,
		Threshold: g.Threshold, Failing: len(members), Total: groupTotal(g, totals), Members: members,
	}
	if g.Kind == store.NotificationSelectorAccount {
		state.Kind = KindAccountFailing
	}
	wanted = len(members) >= g.Threshold || len(unreachable) > 0
	state.Title = groupTitle(g, state, unreachable, accountNames)
	state.Text = groupText(state, unreachable)
	return state, ids, wanted
}

// moreSevere orders two alerts of one service: critical first, then the newest, then by kind and id so the choice
// never depends on input order.
func moreSevere(a, b candidate) bool {
	ac, bc := a.incident.Severity == model.AlertDanger, b.incident.Severity == model.AlertDanger
	if ac != bc {
		return ac
	}
	if !a.incident.OpenedAt.Equal(b.incident.OpenedAt) {
		return a.incident.OpenedAt.After(b.incident.OpenedAt)
	}
	if a.incident.Kind != b.incident.Kind {
		return a.incident.Kind < b.incident.Kind
	}
	return a.incident.ID < b.incident.ID
}

func groupTotal(g GroupSelector, totals Totals) int {
	switch {
	case g.Kind == store.NotificationSelectorCluster:
		return totals.Clusters[model.ClusterKey{AccountID: g.AccountID, Region: g.Region, Cluster: g.Cluster}]
	case g.Region != "":
		return totals.Targets[awsx.Target{AccountID: g.AccountID, Region: g.Region}]
	}
	return totals.Accounts[g.AccountID]
}

func groupTitle(g GroupSelector, state store.GroupState, unreachable []candidate, accountNames map[string]string) string {
	name := accountNames[g.AccountID]
	if name == "" {
		name = g.AccountID
	}
	if g.Kind == store.NotificationSelectorCluster {
		return fmt.Sprintf("%s: %d of %d services failing", g.Cluster, state.Failing, state.Total)
	}
	place := name
	if len(unreachable) > 0 {
		regions := make([]string, len(unreachable))
		for i, c := range unreachable {
			regions[i] = c.incident.Key.Region
		}
		return fmt.Sprintf("%s / %s: unreachable", name, strings.Join(regions, ", "))
	}
	if g.Region != "" {
		place += " / " + g.Region
	}
	return fmt.Sprintf("%s: %d of %d services failing", place, state.Failing, state.Total)
}

// groupText lists the unreachable alerts first, then up to ten failing services, then how many more, then the rule.
func groupText(state store.GroupState, unreachable []candidate) string {
	var lines []string
	for _, c := range unreachable {
		lines = append(lines, c.incident.Text)
	}
	for i, member := range state.Members {
		if i == maxGroupLines {
			lines = append(lines, fmt.Sprintf("and %d more", len(state.Members)-maxGroupLines))
			break
		}
		lines = append(lines, member.Title)
	}
	if state.Threshold <= 1 {
		lines = append(lines, "Notifies when any service is failing.")
	} else {
		lines = append(lines, fmt.Sprintf("Notifies when %d or more services are failing.", state.Threshold))
	}
	return strings.Join(lines, "\n")
}
