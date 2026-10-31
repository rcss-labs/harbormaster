package api

import (
	"net/http"
	"sort"

	"github.com/rcss-labs/harbormaster/internal/store"
)

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
			item = &PollerItemDTO{AccountID: target.accountID, AccountName: names[target.accountID], Region: target.region}
			byTarget[target] = item
			order = append(order, target)
		}
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
	sort.SliceStable(order, func(i, j int) bool {
		if order[i].accountID != order[j].accountID {
			return order[i].accountID < order[j].accountID
		}
		return order[i].region < order[j].region
	})
	items := make([]PollerItemDTO, 0, len(order))
	for _, target := range order {
		items = append(items, *byTarget[target])
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
