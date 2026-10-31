package api

import (
	"net/http"
	"time"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/status"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// AlertsDTO is the GET /api/alerts body. Counts are computed after visibility filtering.
type AlertsDTO struct {
	Items    []AlertDTO    `json:"items"`
	Counts   AlertCountDTO `json:"counts"`
	PolledAt *time.Time    `json:"polled_at"`
}

// AlertDTO is one visible alert. Service fields are empty for account-level alerts.
type AlertDTO struct {
	Kind      string    `json:"kind"`
	Severity  string    `json:"severity"`
	Title     string    `json:"title"`
	Text      string    `json:"text"`
	At        time.Time `json:"at"`
	ServiceID string    `json:"service_id"`
	Service   string    `json:"service"`
	Cluster   string    `json:"cluster"`
	ClusterID string    `json:"cluster_id"`
	Tab       string    `json:"tab"`
}

type AlertCountDTO struct {
	Danger  int `json:"danger"`
	Warning int `json:"warning"`
	Info    int `json:"info"`
}

// getAlerts answers GET /api/alerts?account=&region=. A service alert is visible when the caller
// can View the service's effective tag; an account-level alert is visible when the caller can see
// at least one cluster of that account x region. polled_at is the newest successful alerts-tier
// poll among the visible targets, null when none has succeeded.
func (s *Server) getAlerts(w http.ResponseWriter, r *http.Request) {
	sc, err := s.scopeFrom(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	ctx := r.Context()
	alerts, err := s.Store.ListAlerts(ctx, store.AlertFilter{AccountID: sc.Filter.AccountID, Region: sc.Filter.Region})
	if err != nil {
		s.fail(w, err)
		return
	}
	clusters, err := s.Store.ListClusters(ctx, sc.Filter, sc.Vis)
	if err != nil {
		s.fail(w, err)
		return
	}
	clusterTargets := map[pollTarget]bool{}
	for _, cluster := range clusters {
		clusterTargets[pollTarget{cluster.Key.AccountID, cluster.Key.Region}] = true
	}
	// Admins see every target, including one with no cluster (an unreachable account has none visible).
	targetVisible := func(target pollTarget) bool { return sc.Subject.Admin || clusterTargets[target] }
	loops, err := s.Store.ListPollLoops(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}

	var visible []model.Alert
	for _, alert := range alerts {
		if alertVisible(sc, targetVisible, alert) {
			visible = append(visible, alert)
		}
	}
	status.SortAlerts(visible)

	out := AlertsDTO{Items: make([]AlertDTO, 0, len(visible)), PolledAt: newestAlertsPoll(loops, targetVisible)}
	for _, alert := range visible {
		out.Items = append(out.Items, alertDTO(alert))
		switch alert.Severity {
		case model.AlertDanger:
			out.Counts.Danger++
		case model.AlertWarning:
			out.Counts.Warning++
		case model.AlertInfo:
			out.Counts.Info++
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func alertVisible(sc scope, targetVisible func(pollTarget) bool, alert model.Alert) bool {
	if alert.Key.Service == "" {
		return targetVisible(pollTarget{alert.Key.AccountID, alert.Key.Region})
	}
	return authz.Can(sc.Subject, authz.View, authz.EffectiveServiceTag(sc.Tags, alert.Key))
}

// newestAlertsPoll returns the latest successful alerts-tier poll among the visible targets.
func newestAlertsPoll(loops []store.PollLoop, targetVisible func(pollTarget) bool) *time.Time {
	var newest *time.Time
	for _, loop := range loops {
		target := pollTarget{loop.Target.AccountID, loop.Target.Region}
		if loop.Tier != "alerts" || loop.LastOKAt == nil || !targetVisible(target) {
			continue
		}
		if newest == nil || loop.LastOKAt.After(*newest) {
			polled := *loop.LastOKAt
			newest = &polled
		}
	}
	return newest
}

func alertDTO(alert model.Alert) AlertDTO {
	out := AlertDTO{
		Kind: alert.Kind, Severity: string(alert.Severity), Title: alert.Title, Text: alert.Text,
		At: alert.At, Service: alert.Key.Service, Cluster: alert.Key.Cluster, Tab: alert.Tab,
	}
	if alert.Key.Service != "" {
		out.ServiceID = alert.Key.ID()
	}
	if alert.Key.Cluster != "" {
		out.ClusterID = alert.Key.ClusterKey.ID()
	}
	return out
}
