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
	// Cause repeats Text. JustBefore is nil unless a Harbormaster operation or deployment precedes the alert.
	Cause      string          `json:"cause"`
	NextStep   AlertStepDTO    `json:"next_step"`
	JustBefore *AlertChangeDTO `json:"just_before"`
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
	// Admins see every target, including one with no cluster (an unreachable account has none visible).
	clusterTargets := map[pollTarget]bool{}
	if !sc.Vis.Admin {
		clusters, err := s.Store.ListClusters(ctx, sc.Filter, sc.Vis)
		if err != nil {
			s.fail(w, err)
			return
		}
		for _, cluster := range clusters {
			clusterTargets[pollTarget{cluster.Key.AccountID, cluster.Key.Region}] = true
		}
	}
	targetVisible := func(target pollTarget) bool { return sc.Vis.Admin || clusterTargets[target] }
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

	// Only alerts that passed the visibility filter above get context, so a change text is never
	// looked up for a service the caller cannot see. One batched query serves the whole response.
	changes, err := s.Store.LastChangesBefore(ctx, changeQueries(visible), justBeforeWindow)
	if err != nil {
		s.fail(w, err)
		return
	}

	out := AlertsDTO{Items: make([]AlertDTO, 0, len(visible)), PolledAt: newestAlertsPoll(loops, targetVisible)}
	for _, alert := range visible {
		item := alertDTO(alert)
		if change := changes[store.ChangeQuery{Key: alert.Key, Before: alert.At}]; change != nil {
			item.JustBefore = &AlertChangeDTO{At: change.At, Text: change.Text, Source: change.Source}
		}
		out.Items = append(out.Items, item)
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
	tag := authz.EffectiveServiceTag(sc.Tags, alert.Key)
	return authz.Can(sc.Subject, authz.View, tag) && sc.tagMatches(tag)
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

// justBeforeWindow is how far before an alert a change may be and still count as "just before".
const justBeforeWindow = 24 * time.Hour

// changeQueries asks for the change preceding each service alert; account-level alerts have no service
// and so no change.
func changeQueries(alerts []model.Alert) []store.ChangeQuery {
	queries := make([]store.ChangeQuery, 0, len(alerts))
	for _, alert := range alerts {
		if alert.Key.Service != "" {
			queries = append(queries, store.ChangeQuery{Key: alert.Key, Before: alert.At})
		}
	}
	return queries
}

// alertNextStep builds the link that most directly helps with this kind of alert.
func alertNextStep(alert model.Alert, serviceID string) AlertStepDTO {
	if serviceID == "" {
		return AlertStepDTO{Label: "Open AWS accounts", Href: "/settings/accounts"}
	}
	tab, label := "overview", "Open service"
	switch alert.Kind {
	case "service_failing":
		tab, label = "logs", "Open logs"
	case "restart_loop", "unhealthy_task":
		tab, label = "tasks", "Open tasks"
	case "deployment_failed":
		tab, label = "deploy", "Open deploy"
	}
	return AlertStepDTO{Label: label, Href: "/services/" + serviceID + "/" + tab}
}

func alertDTO(alert model.Alert) AlertDTO {
	out := AlertDTO{
		Kind: alert.Kind, Severity: string(alert.Severity), Title: alert.Title, Text: alert.Text, Cause: alert.Text,
		At: alert.At, Service: alert.Key.Service, Cluster: alert.Key.Cluster, Tab: alert.Tab,
	}
	if alert.Key.Service != "" {
		out.ServiceID = alert.Key.ID()
	}
	out.NextStep = alertNextStep(alert, out.ServiceID)
	if alert.Key.Cluster != "" {
		out.ClusterID = alert.Key.ClusterKey.ID()
	}
	return out
}
