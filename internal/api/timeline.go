package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/timeline"
)

// The read limits are variables so tests can reach them with a few rows.
var (
	timelineLaneLimit    = 500
	timelineFailureLimit = 2000
)

const (
	timelineIncidentSpan  = 7 * 24 * time.Hour
	timelineDefaultRange  = "1h"
	timelineAlertLaneNote = "Shows alerts active now. Harbormaster does not keep cleared alerts."
)

var timelineRanges = map[string]time.Duration{
	"1h": time.Hour, "6h": 6 * time.Hour, "24h": 24 * time.Hour, "7d": timelineIncidentSpan,
}

// TimelineDTO is GET /api/services/{id}/timeline. It is built from recorded history only and holds no log data.
type TimelineDTO struct {
	Range            string               `json:"range"`
	From             time.Time            `json:"from"`
	To               time.Time            `json:"to"`
	Events           []TimelineEventDTO   `json:"events"`
	Lanes            []TimelineLaneDTO    `json:"lanes"`
	Incident         *TimelineIncidentDTO `json:"incident"`
	DeployedRevision int                  `json:"deployed_revision"`
	PolledAt         time.Time            `json:"polled_at"`
}

// TimelineEventDTO is one marker. EvidenceAt is set only on an alert whose time was clamped to the range start.
type TimelineEventDTO struct {
	ID           string               `json:"id"`
	Lane         string               `json:"lane"`
	Severity     string               `json:"severity"`
	Title        string               `json:"title"`
	Actor        string               `json:"actor"`
	Detail       string               `json:"detail"`
	Op           string               `json:"op"`
	TaskID       string               `json:"task_id"`
	At           time.Time            `json:"at"`
	EvidenceAt   *time.Time           `json:"evidence_at"`
	Diff         []TimelineDiffRowDTO `json:"diff"`
	FromRevision *int                 `json:"from_revision"`
	ToRevision   *int                 `json:"to_revision"`
}

type TimelineDiffRowDTO struct {
	Label string `json:"label"`
	From  string `json:"from"`
	To    string `json:"to"`
}

// TimelineLaneDTO says whether a lane was cut at its limit and carries a note the lane should show.
type TimelineLaneDTO struct {
	Lane      string `json:"lane"`
	Truncated bool   `json:"truncated"`
	Note      string `json:"note"`
}

// TimelineIncidentDTO is the newest failure burst and the change most likely behind it.
type TimelineIncidentDTO struct {
	FirstFailureAt time.Time `json:"first_failure_at"`
	LastFailureAt  time.Time `json:"last_failure_at"`
	FailureCount   int       `json:"failure_count"`
	TopReason      string    `json:"top_reason"`
	TopReasonCount int       `json:"top_reason_count"`
	// TriggerUnknown is true when reads were cut off before the burst's real start or before its trigger, so
	// FailureCount is a lower bound and no trigger or rollback is offered.
	TriggerUnknown   bool              `json:"trigger_unknown"`
	Trigger          *TimelineEventDTO `json:"trigger"`
	RollbackRevision *int              `json:"rollback_revision"`
	RolledBack       *TimelineEventDTO `json:"rolled_back"`
}

func timelineEventDTO(event timeline.Event) TimelineEventDTO {
	diff := make([]TimelineDiffRowDTO, 0, len(event.Diff))
	for _, row := range event.Diff {
		diff = append(diff, TimelineDiffRowDTO{Label: row.Label, From: row.From, To: row.To})
	}
	return TimelineEventDTO{
		ID: event.ID, Lane: string(event.Lane), Severity: string(event.Severity), Title: event.Title,
		Actor: event.Actor, Detail: event.Detail, Op: event.Op, TaskID: event.TaskID, At: event.At,
		EvidenceAt: event.EvidenceAt, Diff: diff, FromRevision: event.FromRevision, ToRevision: event.ToRevision,
	}
}

func timelineEventPointer(event *timeline.Event) *TimelineEventDTO {
	if event == nil {
		return nil
	}
	dto := timelineEventDTO(*event)
	return &dto
}

// retentionNote explains a lane that cannot reach back over the whole range because its data is pruned sooner.
// A zero retention means nothing is pruned.
func retentionNote(subject string, retention, window time.Duration) string {
	if retention <= 0 || retention >= window {
		return ""
	}
	return fmt.Sprintf("%s older than %s are not kept.", subject, retentionText(retention))
}

func retentionText(d time.Duration) string {
	if d%(24*time.Hour) == 0 {
		days := int(d / (24 * time.Hour))
		if days == 1 {
			return "1 day"
		}
		return fmt.Sprintf("%d days", days)
	}
	hours := int(d.Round(time.Hour) / time.Hour)
	if hours == 1 {
		return "1 hour"
	}
	return fmt.Sprintf("%d hours", max(hours, 1))
}

// getTimeline answers GET /api/services/{serviceId}/timeline?range=. It reads only recorded history, so it
// makes no AWS calls.
func (s *Server) getTimeline(w http.ResponseWriter, r *http.Request) {
	key, _, service, ok := s.visibleService(w, r)
	if !ok {
		return
	}
	rangeName := r.URL.Query().Get("range")
	if rangeName == "" {
		rangeName = timelineDefaultRange
	}
	window, known := timelineRanges[rangeName]
	if !known {
		s.fail(w, invalid("range must be one of 1h, 6h, 24h or 7d."))
		return
	}
	ctx := r.Context()
	now := s.now()
	from := now.Add(-window)

	// The incident ignores the selected range (D6), so it is found from the last 7 days of failures.
	failures, err := s.Store.TimelineFailures(ctx, key, now.Add(-timelineIncidentSpan), timelineFailureLimit)
	if err != nil {
		s.fail(w, err)
		return
	}
	burst := timeline.DetectBurst(failures)
	changesSince := from
	if burst != nil {
		// The trigger is looked for before the burst, which may start before the range. A deployment that started
		// before this point and failed inside it only adds a failure mark, which needs no deployment row.
		if lookback := burst.First.Add(-timeline.TriggerLookback); lookback.Before(changesSince) {
			changesSince = lookback
		}
	}
	operations, err := s.Store.TimelineOperations(ctx, key, changesSince, timelineLaneLimit)
	if err != nil {
		s.fail(w, err)
		return
	}
	deployments, err := s.Store.TimelineDeployments(ctx, key, changesSince, timelineLaneLimit)
	if err != nil {
		s.fail(w, err)
		return
	}
	stops, err := s.Store.TimelineStops(ctx, key, from, timelineLaneLimit)
	if err != nil {
		s.fail(w, err)
		return
	}
	alerts, err := s.serviceAlerts(r, key)
	if err != nil {
		s.fail(w, err)
		return
	}
	polledAt, err := s.Store.PolledAt(ctx, store.TargetFilter{AccountID: key.AccountID, Region: key.Region})
	if err != nil {
		s.fail(w, err)
		return
	}

	events := timeline.BuildEvents(timeline.Input{
		Key: key, From: from, To: now, Operations: operations, Deployments: deployments, Stops: stops, Alerts: alerts,
	})
	// service.Revision is the PRIMARY deployment's revision.
	incident := timeline.BuildIncident(burst, events, service.Revision)
	// A burst that fills the failure read may have started earlier, and truncated change reads may have dropped
	// the real trigger, so naming one could blame an operator's action during the incident.
	if incident != nil {
		failuresCut := len(failures) >= timelineFailureLimit && burst.Count >= timelineFailureLimit
		changesCut := (len(operations) >= timelineLaneLimit && oldestOperation(operations).After(burst.First)) ||
			(len(deployments) >= timelineLaneLimit && oldestDeployment(deployments).After(burst.First))
		if failuresCut || changesCut {
			incident.MarkTriggerUnknown()
		}
	}
	operationsCut := len(operations) >= timelineLaneLimit && oldestOperation(operations).After(from)
	deploymentsCut := len(deployments) >= timelineLaneLimit && oldestDeployment(deployments).After(from)

	out := TimelineDTO{
		Range: rangeName, From: from, To: now, Events: []TimelineEventDTO{}, DeployedRevision: service.Revision,
		PolledAt: polledAt,
		Lanes: []TimelineLaneDTO{
			{Lane: string(timeline.LaneChange), Truncated: operationsCut || deploymentsCut,
				Note: retentionNote("Changes", shortestNonZeroRetention(s.Cfg.Retention.OperationLog, s.Cfg.Retention.DeploymentHistory), window)},
			{Lane: string(timeline.LaneStop), Truncated: len(stops) >= timelineLaneLimit && stops[len(stops)-1].StoppedAt.After(from),
				Note: retentionNote("Stops", s.Cfg.Retention.StoppedTasks, window)},
			{Lane: string(timeline.LaneAlert), Note: timelineAlertLaneNote},
			{Lane: string(timeline.LaneAccess), Truncated: operationsCut},
		},
	}
	for _, event := range timeline.InRange(events, from) {
		out.Events = append(out.Events, timelineEventDTO(event))
	}
	if incident != nil && !incident.Last.Before(from) {
		out.Incident = &TimelineIncidentDTO{
			FirstFailureAt: incident.First, LastFailureAt: incident.Last, FailureCount: incident.Count,
			TopReason: incident.TopReason, TopReasonCount: incident.TopReasonCount, TriggerUnknown: incident.TriggerUnknown,
			Trigger: timelineEventPointer(incident.Trigger), RollbackRevision: incident.RollbackRevision,
			RolledBack: timelineEventPointer(incident.RolledBack),
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func oldestOperation(rows []store.OperationRow) time.Time {
	oldest := rows[0].TS
	for _, row := range rows {
		if row.TS.Before(oldest) {
			oldest = row.TS
		}
	}
	return oldest
}

func oldestDeployment(rows []store.TimelineDeployment) time.Time {
	oldest := rows[0].StartedAt
	for _, row := range rows {
		if row.StartedAt.Before(oldest) {
			oldest = row.StartedAt
		}
	}
	return oldest
}

// shortestNonZeroRetention is the smaller of the non-zero retentions; zero means nothing is pruned.
func shortestNonZeroRetention(retentions ...time.Duration) time.Duration {
	var shortest time.Duration
	for _, retention := range retentions {
		if retention > 0 && (shortest == 0 || retention < shortest) {
			shortest = retention
		}
	}
	return shortest
}

func (s *Server) serviceAlerts(r *http.Request, key model.ServiceKey) ([]model.Alert, error) {
	all, err := s.Store.ListAlerts(r.Context(), store.AlertFilter{AccountID: key.AccountID, Region: key.Region})
	if err != nil {
		return nil, err
	}
	var own []model.Alert
	for _, alert := range all {
		if alert.Key == key {
			own = append(own, alert)
		}
	}
	return own, nil
}

// getTimelineTask answers GET /api/services/{serviceId}/timeline/tasks/{taskId}. The Tasks list hides stopped tasks
// after a window, so the Timeline cannot reach an old stop through it.
func (s *Server) getTimelineTask(w http.ResponseWriter, r *http.Request) {
	key, sc, _, ok := s.visibleService(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	taskID := r.PathValue("taskId")
	if taskID == "" || strings.Contains(taskID, "/") {
		s.fail(w, errNotFound)
		return
	}
	filter := store.TargetFilter{AccountID: key.AccountID, Region: key.Region}
	// Q also matches family and service names, so the exact id is checked on the ARN below.
	rows, _, err := s.Store.ListTasks(ctx, store.TaskQuery{
		TargetFilter: filter, Vis: sc.Vis, Cluster: key.Cluster, Service: key.Service, Q: taskID, Filter: "all", Limit: 20,
	})
	if err != nil {
		s.fail(w, err)
		return
	}
	for _, row := range rows {
		if !strings.HasSuffix(row.ARN, "/"+taskID) {
			continue
		}
		view, err := s.loadEstate(ctx, filter, sc.Vis, key.Cluster, nil)
		if err != nil {
			s.fail(w, err)
			return
		}
		names, err := s.accountNames(ctx)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, taskDTO(sc, view, names, row))
		return
	}
	s.fail(w, errNotFound)
}
