package api

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// servicesListCap is the most rows GET /api/services returns; a var so a test can lower it.
var servicesListCap = 2000

// listServicesFlat answers GET /api/services?account=&region=&tag=: every visible service in one list, using
// the same scope and tag rules as /api/clusters. Filtering, sorting and paging happen in the SPA.
func (s *Server) listServicesFlat(w http.ResponseWriter, r *http.Request) {
	sc, err := s.scopeFrom(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	ctx := r.Context()
	view, err := s.loadEstate(ctx, sc.Filter, sc.Vis, "", nil)
	if err != nil {
		s.fail(w, err)
		return
	}
	tags, err := s.tagDTOs(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	names, err := s.accountNames(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	polledAt, err := s.Store.PolledAt(ctx, sc.Filter)
	if err != nil {
		s.fail(w, err)
		return
	}
	attention, err := s.newestAlertTextByService(ctx, sc.Filter)
	if err != nil {
		s.fail(w, err)
		return
	}
	lastEvents, err := s.Store.RecentServiceEventsPerService(ctx, sc.Filter, 1)
	if err != nil {
		s.fail(w, err)
		return
	}

	definitions := map[string]model.TaskDefinitionRef{}
	definition := func(key model.ServiceKey, arn string) model.TaskDefinitionRef {
		cacheKey := key.AccountID + "/" + key.Region + "/" + arn
		if td, ok := definitions[cacheKey]; ok {
			return td
		}
		td, err := s.Store.GetTaskDefinition(ctx, key.AccountID, key.Region, arn)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			s.log().Warn("get task definition", "arn", arn, "error", err)
		}
		definitions[cacheKey] = td
		return td
	}

	items := make([]ServiceListItemDTO, 0, len(view.services))
	for _, svc := range view.services {
		td := definition(svc.Key, svc.TaskDefinitionARN)
		item := ServiceListItemDTO{
			ID: svc.Key.ID(), Name: svc.Key.Service,
			Cluster:   ClusterRef{ID: svc.Key.ClusterKey.ID(), Name: svc.Key.Cluster},
			AccountID: svc.Key.AccountID, AccountName: names[svc.Key.AccountID], Region: svc.Key.Region,
			Tag: inheritedTag(tags, svc.EffectiveTagID, false), TagInherited: svc.TagInherited,
			Status: view.statusByService[svc.Key], Attention: attention[svc.Key],
			Family: svc.Family, Revision: svc.Revision, ImageTag: td.ImageTag(svc.Key.Service),
			Running: svc.Running, Desired: svc.Desired, CPU: td.CPU, Memory: td.Memory,
			Rollout: rolloutOf(svc.Service),
			Can: ServiceListCanDTO{
				Operate:  authz.Can(sc.Subject, authz.Operate, svc.EffectiveTagID),
				Logs:     authz.Can(sc.Subject, authz.Logs, svc.EffectiveTagID),
				Terminal: authz.Can(sc.Subject, authz.Terminal, svc.EffectiveTagID),
			},
		}
		if events := lastEvents[svc.Key]; len(events) > 0 {
			at := events[0].CreatedAt
			item.LastChangeAt = &at
		}
		items = append(items, item)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Name != items[j].Name {
			return items[i].Name < items[j].Name
		}
		return items[i].ID < items[j].ID
	})
	total := len(items)
	if total > servicesListCap {
		items = items[:servicesListCap]
	}
	writeJSON(w, http.StatusOK, ServiceListDTO{Items: items, Total: total, Truncated: total > len(items), PolledAt: polledAt})
}

// newestAlertTextByService maps each service to the text of its newest open alert.
func (s *Server) newestAlertTextByService(ctx context.Context, f store.TargetFilter) (map[model.ServiceKey]string, error) {
	alerts, err := s.Store.ListAlerts(ctx, store.AlertFilter{AccountID: f.AccountID, Region: f.Region})
	if err != nil {
		return nil, err
	}
	newest := map[model.ServiceKey]model.Alert{}
	for _, alert := range alerts {
		if alert.Key.Service == "" {
			continue
		}
		if current, ok := newest[alert.Key]; !ok || alert.At.After(current.At) {
			newest[alert.Key] = alert
		}
	}
	text := make(map[model.ServiceKey]string, len(newest))
	for key, alert := range newest {
		text[key] = alert.Text
	}
	return text, nil
}

// rolloutOf reports the newest deployment's rollout state as in_progress, failed or completed; nil when unknown.
func rolloutOf(svc model.Service) *string {
	var newest *model.Deployment
	for i := range svc.Deployments {
		if newest == nil || svc.Deployments[i].CreatedAt.After(newest.CreatedAt) {
			newest = &svc.Deployments[i]
		}
	}
	if newest == nil {
		return nil
	}
	state := strings.ToLower(newest.RolloutState)
	switch state {
	case "in_progress", "failed", "completed":
		return &state
	}
	return nil
}
