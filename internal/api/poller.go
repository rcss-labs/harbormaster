package api

import (
	"net/http"
	"sort"

	"github.com/rcss-labs/harbormaster/internal/store"
)

// nonCoreTiers are the poll tiers that do not feed clusters, services or tasks. Their failure
// is reported only in PollerItemDTO.Tiers, so it never marks core data stale. Unknown tiers count as core.
var nonCoreTiers = map[string]bool{"target_health": true, "scaling": true, "alerts": true, "network": true, "storage": true}

// tierLabels are the user-facing names; tiers sharing a label are merged into one entry.
var tierLabels = map[string]string{
	"clusters": "Clusters and services", "serviceList": "Clusters and services", "services": "Clusters and services",
	"runningTasks": "Tasks", "stoppedTasks": "Tasks", "fullTasks": "Tasks",
	"target_health": "Target health", "scaling": "Auto scaling", "alerts": "Alerts",
	"network": "Networks", "storage": "Storage",
}

func tierIsCore(tier string) bool { return !nonCoreTiers[tier] }

func tierLabel(tier string) string {
	if label, ok := tierLabels[tier]; ok {
		return label
	}
	return tier
}

// addTier folds one loop into the item's tier list: loops with the same label merge, the worst
// state wins and last_ok_at is the oldest one (a label is only as fresh as its stalest loop).
func addTier(item *PollerItemDTO, loop store.PollLoop) {
	label := tierLabel(loop.Tier)
	entry := PollerTierDTO{
		Tier: loop.Tier, Label: label, Core: tierIsCore(loop.Tier),
		Failing: loop.Failing(), Throttled: loop.Throttled(), LastError: loop.LastError,
		IntervalMs: loop.Interval.Milliseconds(),
	}
	if loop.LastOKAt != nil {
		at := *loop.LastOKAt
		entry.LastOKAt = &at
	}
	for i := range item.Tiers {
		have := &item.Tiers[i]
		if have.Label != label {
			continue
		}
		if entry.Failing && !have.Failing {
			have.Failing, have.LastError = true, entry.LastError
		} else if have.LastError == "" && entry.Failing == have.Failing {
			have.LastError = entry.LastError
		}
		have.Throttled = have.Throttled || entry.Throttled
		if entry.LastOKAt == nil || (have.LastOKAt != nil && entry.LastOKAt.Before(*have.LastOKAt)) {
			have.LastOKAt = entry.LastOKAt
		}
		if entry.IntervalMs > have.IntervalMs {
			have.IntervalMs = entry.IntervalMs
		}
		return
	}
	item.Tiers = append(item.Tiers, entry)
}

type pollTarget struct{ accountID, region string }

// pollerStatus answers GET /api/poller: one aggregated item per account x region the caller can see.
func (s *Server) pollerStatus(w http.ResponseWriter, r *http.Request) {
	sc, err := s.scopeFrom(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	ctx := r.Context()
	loops, err := s.Store.ListPollLoops(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	visible := map[pollTarget]bool{}
	if !sc.Vis.Admin {
		clusters, err := s.Store.ListClusters(ctx, store.TargetFilter{}, sc.Vis)
		if err != nil {
			s.fail(w, err)
			return
		}
		for _, c := range clusters {
			visible[pollTarget{c.Key.AccountID, c.Key.Region}] = true
		}
	}
	names, err := s.accountNames(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}

	// The services tier decides last_error first, so order it ahead of the others (stable).
	sort.SliceStable(loops, func(i, j int) bool { return loops[i].Tier == "services" && loops[j].Tier != "services" })
	byTarget := map[pollTarget]*PollerItemDTO{}
	var order []pollTarget
	for _, loop := range loops {
		target := pollTarget{loop.Target.AccountID, loop.Target.Region}
		if !sc.Vis.Admin && !visible[target] {
			continue
		}
		item, ok := byTarget[target]
		if !ok {
			item = &PollerItemDTO{AccountID: target.accountID, AccountName: names[target.accountID], Region: target.region, Tiers: []PollerTierDTO{}}
			byTarget[target] = item
			order = append(order, target)
		}
		addTier(item, loop)
		if tierIsCore(loop.Tier) {
			item.Throttled = item.Throttled || loop.Throttled()
			item.Failing = item.Failing || loop.Failing()
			if item.LastError == "" {
				item.LastError = loop.LastError
			}
			if loop.LastOKAt != nil && (item.LastOKAt == nil || loop.LastOKAt.Before(*item.LastOKAt)) {
				at := *loop.LastOKAt
				item.LastOKAt = &at
			}
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		if order[i].accountID != order[j].accountID {
			return order[i].accountID < order[j].accountID
		}
		return order[i].region < order[j].region
	})
	items := make([]PollerItemDTO, 0, len(order))
	for _, target := range order {
		item := *byTarget[target]
		// AWS error text can name resources the caller cannot see; only admins, or anyone once the target fails, get it.
		if !sc.Vis.Admin && !item.Failing {
			item.LastError = ""
		}
		if !sc.Vis.Admin {
			for i := range item.Tiers {
				if !item.Tiers[i].Failing {
					item.Tiers[i].LastError = ""
				}
			}
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, ItemsResponse[PollerItemDTO]{Items: items})
}
