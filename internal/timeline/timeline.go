// Package timeline turns a service's recorded history into the events its Timeline tab shows.
package timeline

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

type Lane string

const (
	LaneChange Lane = "change"
	LaneStop   Lane = "stop"
	LaneAlert  Lane = "alert"
	LaneAccess Lane = "access"
)

type Severity string

const (
	SeverityChange Severity = "change"
	SeverityBad    Severity = "bad"
	SeverityWarn   Severity = "warn"
	SeverityInfo   Severity = "info"
)

// DiffRow is one before/after line of an event's "Show the change" view.
type DiffRow struct{ Label, From, To string }

// Event is one marker on the timeline. ID is stable across polls because the client selects by it.
type Event struct {
	ID       string
	Lane     Lane
	At       time.Time
	Severity Severity
	Title    string
	Actor    string
	Detail   string
	Diff     []DiffRow
	// TaskID is the task id, on stop events only.
	TaskID                   string
	FromRevision, ToRevision *int
	// Op is the operation behind the event: "deploy", "rollback", "external_deploy", ...
	Op string
	// EvidenceAt is an alert's true evidence time, set only when InRange clamped At to the start of the range.
	EvidenceAt *time.Time
	// Affecting marks events eligible as the likely trigger of an incident.
	Affecting bool
}

type Input struct {
	Key         model.ServiceKey
	From, To    time.Time
	Operations  []store.OperationRow
	Deployments []store.TimelineDeployment
	Stops       []store.TimelineStop
	Alerts      []model.Alert
}

// affectingOps are the operations that can plausibly change how the service behaves. Registering a revision
// alone changes nothing until something deploys it, and stopping a task is its own lane's concern.
var affectingOps = map[string]bool{
	"deploy": true, "rollback": true, "restart": true, "scale": true,
	"rollout_settings": true, "exec": true, "scaling": true,
}

// rolloutOps are the operations whose details carry the revision change of a deployment.
var rolloutOps = map[string]bool{"deploy": true, "rollback": true, "restart": true}

// BuildEvents returns every event for the input sorted by time ascending. It includes changes older than
// in.From because the trigger of an incident at the start of the range may precede it.
func BuildEvents(in Input) []Event {
	var events []Event
	events = append(events, operationEvents(in.Operations)...)
	events = append(events, deploymentEvents(in.Deployments, in.Operations)...)
	events = append(events, stopEvents(in.Stops)...)
	events = append(events, alertEvents(in.Alerts)...)
	sort.SliceStable(events, func(i, j int) bool {
		if !events[i].At.Equal(events[j].At) {
			return events[i].At.Before(events[j].At)
		}
		return events[i].ID < events[j].ID
	})
	return events
}

// InRange keeps the events at or after from. Alerts are always kept: an alert's time is its latest evidence, not
// when it started, so one older than from is clamped to from and its true time is kept in EvidenceAt.
func InRange(events []Event, from time.Time) []Event {
	kept := make([]Event, 0, len(events))
	for _, event := range events {
		if event.Lane == LaneAlert {
			if event.At.Before(from) {
				evidence := event.At
				event.EvidenceAt = &evidence
				event.At = from
			}
			kept = append(kept, event)
			continue
		}
		if !event.At.Before(from) {
			kept = append(kept, event)
		}
	}
	return kept
}

func operationEvents(rows []store.OperationRow) []Event {
	var events []Event
	changeSets := map[string][]store.OperationRow{}
	var changeSetOrder []string
	terminal := terminalSessions{}
	for _, row := range rows {
		op := detailString(row.Details, "op")
		switch {
		case op == "" || op == "outcome":
			continue
		case op == "terminal" || op == "terminal_close":
			terminal.add(row, op)
		case detailString(row.Details, "changeset_id") != "":
			id := detailString(row.Details, "changeset_id")
			if _, seen := changeSets[id]; !seen {
				changeSetOrder = append(changeSetOrder, id)
			}
			changeSets[id] = append(changeSets[id], row)
		default:
			events = append(events, singleOperationEvent(row, op))
		}
	}
	for _, id := range changeSetOrder {
		events = append(events, changeSetEvent(id, changeSets[id]))
	}
	return append(events, terminal.events()...)
}

func singleOperationEvent(row store.OperationRow, op string) Event {
	event := Event{
		ID: "op:" + strconv.FormatInt(row.ID, 10), Lane: LaneChange, At: row.TS, Severity: SeverityChange,
		Title: row.Text, Actor: row.Actor, Detail: detailString(row.Details, "reason"), Op: op, Affecting: affectingOps[op],
	}
	event.FromRevision, event.ToRevision = revisions(row.Details)
	event.Diff = desiredDiff(row.Details)
	return event
}

// changeSetEvent collapses the rows one change set wrote into a single event, because the user applied one change.
func changeSetEvent(id string, rows []store.OperationRow) Event {
	first := rows[0]
	event := Event{
		ID: "changeset:" + id, Lane: LaneChange, At: first.TS, Severity: SeverityChange,
		Title: "Change set applied", Actor: first.Actor, Detail: detailString(first.Details, "reason"),
	}
	for _, row := range rows {
		if row.TS.Before(event.At) {
			event.At = row.TS
		}
		if event.Actor == "" {
			event.Actor = row.Actor
		}
		op := detailString(row.Details, "op")
		if affectingOps[op] {
			event.Affecting = true
		}
		if event.Op == "" || (rolloutOps[op] && !rolloutOps[event.Op]) ||
			(affectingOps[op] && !affectingOps[event.Op] && !rolloutOps[event.Op]) {
			event.Op = op
		}
		if rolloutOps[op] {
			if from, to := revisions(row.Details); to != nil {
				event.FromRevision, event.ToRevision = from, to
			}
		}
		event.Diff = append(event.Diff, desiredDiff(row.Details)...)
	}
	if event.ToRevision != nil {
		event.Title = fmt.Sprintf("Change set applied: revision %d", *event.ToRevision)
	}
	return event
}

// deploymentEvents adds an event for each deployment Harbormaster did not start (no operation shares its id) and
// one for each failed deployment.
func deploymentEvents(deployments []store.TimelineDeployment, rows []store.OperationRow) []Event {
	attributed := map[string]bool{}
	for _, row := range rows {
		// Outcome rows also carry deployment ids, including for deployments made outside Harbormaster.
		if id := detailString(row.Details, "deployment_id"); id != "" && rolloutOps[detailString(row.Details, "op")] {
			attributed[id] = true
		}
	}
	oldestFirst := append([]store.TimelineDeployment(nil), deployments...)
	sort.SliceStable(oldestFirst, func(i, j int) bool { return oldestFirst[i].StartedAt.Before(oldestFirst[j].StartedAt) })

	var events []Event
	for i, deployment := range oldestFirst {
		revision := deployment.Revision
		if !attributed[deployment.DeploymentID] {
			event := Event{
				ID: "dep:" + deployment.DeploymentID, Lane: LaneChange, At: deployment.StartedAt, Severity: SeverityChange,
				Title: fmt.Sprintf("Deployment to :%d started outside Harbormaster", revision), Actor: "ECS",
				Op: "external_deploy", Affecting: true, ToRevision: &revision,
			}
			if deployment.StartedBy != "" {
				// Harbormaster started it, but its operation row is missing (failed log write or past the limit).
				event.Title, event.Actor, event.Op = fmt.Sprintf("Deployment to :%d", revision), deployment.StartedBy, "deploy"
			}
			// A failed deployment never served traffic, so it is not a revision to roll back to.
			for j := i - 1; j >= 0; j-- {
				if oldestFirst[j].Outcome != "failed" {
					previous := oldestFirst[j].Revision
					event.FromRevision = &previous
					break
				}
			}
			events = append(events, event)
		}
		if deployment.Outcome == "failed" && deployment.FinishedAt != nil {
			events = append(events, Event{
				ID: "depfail:" + deployment.DeploymentID, Lane: LaneChange, At: *deployment.FinishedAt, Severity: SeverityBad,
				Title: fmt.Sprintf("Deployment :%d failed", revision), Actor: "ECS", ToRevision: &revision,
				Detail: joinNonEmpty(": ", deployment.FailureReason, deployment.FailureDetail),
			})
		}
	}
	return events
}

func stopEvents(stops []store.TimelineStop) []Event {
	events := make([]Event, 0, len(stops))
	for _, stop := range stops {
		taskID := lastSegment(stop.TaskARN)
		event := Event{
			ID: "stop:" + taskID, Lane: LaneStop, At: stop.StoppedAt, Severity: SeverityInfo, Actor: "ECS", TaskID: taskID,
			Title: "Task " + taskID[:min(8, len(taskID))] + " stopped",
		}
		if stop.Kind == "error" {
			event.Severity = SeverityBad
			event.Detail = sentence(firstNonEmpty(stop.StoppedReason, stop.ReasonGroup))
			if stop.ExitCode != nil {
				event.Detail = joinNonEmpty(" ", event.Detail, fmt.Sprintf("Exit code %d.", *stop.ExitCode))
			}
		} else {
			event.Detail = firstNonEmpty(stop.Cause, stop.StoppedReason)
		}
		events = append(events, event)
	}
	return events
}

func alertEvents(alerts []model.Alert) []Event {
	events := make([]Event, 0, len(alerts))
	for _, alert := range alerts {
		severity := SeverityInfo
		switch alert.Severity {
		case model.AlertDanger:
			severity = SeverityBad
		case model.AlertWarning:
			severity = SeverityWarn
		}
		events = append(events, Event{
			ID: "alert:" + alert.Kind, Lane: LaneAlert, At: alert.At, Severity: severity,
			Title: "Alert active: " + alert.Title, Actor: "Harbormaster", Detail: alert.Text,
		})
	}
	return events
}

// terminalSessions joins the open and close rows of each terminal session.
type terminalSessions struct {
	order  []string
	opens  map[string]store.OperationRow
	closes map[string]store.OperationRow
}

func (t *terminalSessions) add(row store.OperationRow, op string) {
	session := detailString(row.Details, "session_id")
	if session == "" {
		session = "row-" + strconv.FormatInt(row.ID, 10)
	}
	if t.opens == nil {
		t.opens, t.closes = map[string]store.OperationRow{}, map[string]store.OperationRow{}
	}
	_, hasOpen := t.opens[session]
	_, hasClose := t.closes[session]
	if !hasOpen && !hasClose {
		t.order = append(t.order, session)
	}
	if op == "terminal" {
		t.opens[session] = row
	} else {
		t.closes[session] = row
	}
}

func (t *terminalSessions) events() []Event {
	var events []Event
	for _, session := range t.order {
		opened, hasOpen := t.opens[session]
		closed, hasClose := t.closes[session]
		event := Event{ID: "term:" + session, Lane: LaneAccess, Severity: SeverityInfo}
		lasted := ""
		if hasClose {
			if ms, ok := detailNumber(closed.Details, "duration_ms"); ok {
				lasted = "Lasted " + durationText(time.Duration(ms)*time.Millisecond) + "."
			}
		}
		if !hasOpen {
			event.At, event.Actor, event.Title, event.Detail = closed.TS, closed.Actor, "Terminal session closed", lasted
			events = append(events, event)
			continue
		}
		event.At, event.Actor, event.Title = opened.TS, opened.Actor, "Terminal session opened"
		where := fmt.Sprintf("Task %s, container %s, %s.", lastSegment(detailString(opened.Details, "task_arn")),
			detailString(opened.Details, "container"), detailString(opened.Details, "command"))
		if hasClose {
			event.Detail = joinNonEmpty(" ", where, lasted)
		} else {
			event.Detail = where + " No close recorded."
		}
		events = append(events, event)
	}
	return events
}

func durationText(d time.Duration) string {
	minutes := int(d / time.Minute)
	switch {
	case minutes < 1:
		return "under a minute"
	case minutes == 1:
		return "1 minute"
	case minutes < 60:
		return fmt.Sprintf("%d minutes", minutes)
	}
	hours, rest := minutes/60, minutes%60
	text := fmt.Sprintf("%d hours", hours)
	if hours == 1 {
		text = "1 hour"
	}
	switch {
	case rest == 1:
		text += " 1 minute"
	case rest > 1:
		text += fmt.Sprintf(" %d minutes", rest)
	}
	return text
}

// desiredDiff returns the desired-count change recorded in the details; older rows have none.
func desiredDiff(details map[string]any) []DiffRow {
	from, fromOK := detailNumber(details, "from_desired")
	to, toOK := detailNumber(details, "to_desired")
	if !fromOK || !toOK {
		return nil
	}
	return []DiffRow{{Label: "desired", From: strconv.FormatInt(from, 10), To: strconv.FormatInt(to, 10)}}
}

func revisions(details map[string]any) (from, to *int) {
	if n, ok := detailNumber(details, "from_revision"); ok {
		value := int(n)
		from = &value
	}
	if n, ok := detailNumber(details, "to_revision"); ok {
		value := int(n)
		to = &value
	}
	return from, to
}

// detailString reads a string detail; a missing key or any other type counts as absent.
func detailString(details map[string]any, key string) string {
	value, _ := details[key].(string)
	return value
}

// detailNumber reads a whole-number detail. JSON numbers decode as float64; a missing key, another type or a
// fractional value counts as absent.
func detailNumber(details map[string]any, key string) (int64, bool) {
	switch value := details[key].(type) {
	case float64:
		if value == float64(int64(value)) {
			return int64(value), true
		}
	case int:
		return int64(value), true
	case int64:
		return value, true
	}
	return 0, false
}

func lastSegment(arn string) string { return arn[strings.LastIndex(arn, "/")+1:] }

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func joinNonEmpty(separator string, values ...string) string {
	var kept []string
	for _, value := range values {
		if value != "" {
			kept = append(kept, value)
		}
	}
	return strings.Join(kept, separator)
}

// sentence ends text with a period so a following sentence reads cleanly.
func sentence(text string) string {
	if text == "" || strings.HasSuffix(text, ".") || strings.HasSuffix(text, "!") || strings.HasSuffix(text, "?") {
		return text
	}
	return text + "."
}
