package api

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

func paymentsKey() model.ServiceKey {
	return model.ServiceKey{
		ClusterKey: model.ClusterKey{AccountID: homelabAccountID, Region: "us-east-1", Cluster: "core-services"},
		Service:    "payments-api",
	}
}

// seedTimelineOperation records an operation and moves it to ago before now, since the log stamps rows with the
// database clock.
func seedTimelineOperation(t *testing.T, st *store.Store, key model.ServiceKey, ago time.Duration, text string, details map[string]any) {
	t.Helper()
	service := key.Service
	err := st.AppendOperation(t.Context(), store.Operation{
		AccountID: key.AccountID, Region: key.Region, Cluster: key.Cluster, Service: &service,
		Source: "harbormaster", Text: text, Details: details,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(t.Context(),
		`update operation_log set ts = $1 where id = (select max(id) from operation_log)`, time.Now().Add(-ago)); err != nil {
		t.Fatal(err)
	}
}

func seedTimelineStop(t *testing.T, st *store.Store, key model.ServiceKey, id, stopCode, reason string, exitCode int, ago time.Duration) {
	t.Helper()
	stoppedAt := time.Now().Add(-ago)
	if _, err := st.UpsertTasks(t.Context(), []model.Task{{
		Cluster: key.ClusterKey, ARN: "arn:aws:ecs:" + key.Region + ":" + key.AccountID + ":task/" + key.Cluster + "/" + id,
		ServiceName: key.Service, LastStatus: "STOPPED", StopCode: stopCode, StoppedReason: reason,
		StoppedAt: &stoppedAt, Revision: 7, Containers: []model.Container{{Name: "app", ExitCode: &exitCode}},
	}}); err != nil {
		t.Fatal(err)
	}
}

// seedDeployAndOOMStops records a deploy of the service's current revision at deployAgo and three OOM stops after it.
func seedDeployAndOOMStops(t *testing.T, st *store.Store, key model.ServiceKey, revision int, deployAgo time.Duration, stopsAgo ...time.Duration) {
	t.Helper()
	seedTimelineOperation(t, st, key, deployAgo, "Deployed revision", map[string]any{
		"op": "deploy", "from_revision": revision - 1, "to_revision": revision,
	})
	for i, ago := range stopsAgo {
		seedTimelineStop(t, st, key, "oom"+string(rune('a'+i)), "EssentialContainerExited", "OutOfMemoryError: Container killed", 137, ago)
	}
}

func getTimeline(t *testing.T, c *apiClient, id, query string) (int, TimelineDTO) {
	t.Helper()
	resp := c.get("/api/services/" + id + "/timeline" + query)
	var body TimelineDTO
	if resp.StatusCode == http.StatusOK {
		decode(t, resp, &body)
	} else {
		resp.Body.Close()
	}
	return resp.StatusCode, body
}

func timelineFixture(t *testing.T) (*apiClient, *store.Store, int) {
	t.Helper()
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	for _, u := range []string{"oncall", "omer"} {
		setPassword(t, st, u)
	}
	pollAll(t, st, fk)
	c := login(t, srv, "sam", testPassword)
	return c, st, getService(t, c, paymentsID()).Revision
}

func timelineEventsOn(body TimelineDTO, lane string) []TimelineEventDTO {
	var events []TimelineEventDTO
	for _, event := range body.Events {
		if event.Lane == lane {
			events = append(events, event)
		}
	}
	return events
}

func timelineLane(body TimelineDTO, lane string) (TimelineLaneDTO, bool) {
	for _, l := range body.Lanes {
		if l.Lane == lane {
			return l, true
		}
	}
	return TimelineLaneDTO{}, false
}

func TestTimeline_ReturnsLanesAndIncident(t *testing.T) {
	c, st, revision := timelineFixture(t)
	seedDeployAndOOMStops(t, st, paymentsKey(), revision, 50*time.Minute, 30*time.Minute, 25*time.Minute, 20*time.Minute)

	status, body := getTimeline(t, c, paymentsID(), "?range=1h")
	if status != http.StatusOK {
		t.Fatalf("status %d, want 200", status)
	}
	changes, stops := timelineEventsOn(body, "change"), timelineEventsOn(body, "stop")
	if len(changes) == 0 || len(stops) < 3 {
		t.Fatalf("change events %d, stop events %d; want changes and at least 3 stops", len(changes), len(stops))
	}
	if body.Incident == nil || body.Incident.Trigger == nil {
		t.Fatalf("incident = %+v, want one with a trigger", body.Incident)
	}
	if !slices.ContainsFunc(changes, func(e TimelineEventDTO) bool { return e.ID == body.Incident.Trigger.ID }) {
		t.Errorf("trigger %q is not among the change events", body.Incident.Trigger.ID)
	}
	if body.Incident.Trigger.Op != "deploy" {
		t.Errorf("trigger op = %q, want deploy", body.Incident.Trigger.Op)
	}
	if body.Incident.RollbackRevision == nil || *body.Incident.RollbackRevision != revision-1 {
		t.Errorf("rollback revision = %v, want %d", body.Incident.RollbackRevision, revision-1)
	}
	if body.Incident.FailureCount != 3 {
		t.Errorf("failure count = %d, want 3", body.Incident.FailureCount)
	}
	if body.DeployedRevision != revision || body.PolledAt.IsZero() {
		t.Errorf("deployed revision %d (want %d), polled_at %v", body.DeployedRevision, revision, body.PolledAt)
	}
}

func TestTimeline_ExpectedStopsDoNotRaiseAnIncident(t *testing.T) {
	c, st, revision := timelineFixture(t)
	key := paymentsKey()
	seedTimelineOperation(t, st, key, 50*time.Minute, "Deployed revision", map[string]any{
		"op": "deploy", "from_revision": revision - 1, "to_revision": revision,
	})
	seedTimelineStop(t, st, key, "spot-a", "SpotInterruption", "Your Spot Task was interrupted.", 1, 30*time.Minute)
	seedTimelineStop(t, st, key, "spot-b", "SpotInterruption", "Your Spot Task was interrupted.", 1, 25*time.Minute)
	seedTimelineStop(t, st, key, "replaced", "ServiceSchedulerInitiated", "Task replaced by a new deployment", 0, 20*time.Minute)

	_, body := getTimeline(t, c, paymentsID(), "?range=1h")
	if got := len(timelineEventsOn(body, "stop")); got < 3 {
		t.Fatalf("stop events = %d, want the 3 expected stops on the lane", got)
	}
	if body.Incident != nil {
		t.Errorf("incident = %+v, want null for expected stops", body.Incident)
	}
}

func TestTimeline_RangeFiltersEventsButNotIncident(t *testing.T) {
	c, st, revision := timelineFixture(t)
	seedDeployAndOOMStops(t, st, paymentsKey(), revision, 5*time.Hour, 40*time.Minute, 35*time.Minute, 30*time.Minute)

	_, body := getTimeline(t, c, paymentsID(), "?range=1h")
	if body.Incident == nil || body.Incident.Trigger == nil {
		t.Fatalf("incident = %+v, want one whose trigger is the change before the range", body.Incident)
	}
	for _, event := range body.Events {
		if event.ID == body.Incident.Trigger.ID {
			t.Errorf("trigger %q listed in events although it is older than the range", event.ID)
		}
	}
	if body.Incident.Trigger.Op != "deploy" {
		t.Errorf("trigger op = %q, want deploy", body.Incident.Trigger.Op)
	}
}

func TestTimeline_IncidentOutsideRangeIsHidden(t *testing.T) {
	c, st, revision := timelineFixture(t)
	seedDeployAndOOMStops(t, st, paymentsKey(), revision, 4*time.Hour, 190*time.Minute, 185*time.Minute, 180*time.Minute)

	if _, body := getTimeline(t, c, paymentsID(), "?range=1h"); body.Incident != nil {
		t.Errorf("range=1h incident = %+v, want null", body.Incident)
	}
	if _, body := getTimeline(t, c, paymentsID(), "?range=6h"); body.Incident == nil {
		t.Error("range=6h incident is null, want present")
	}
}

func TestTimeline_InvalidRangeAndVisibility(t *testing.T) {
	c, _, _ := timelineFixture(t)
	if status, _ := getTimeline(t, c, paymentsID(), "?range=2h"); status != http.StatusUnprocessableEntity {
		t.Errorf("range=2h status %d, want 422 (what invalid() returns)", status)
	}
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	setPassword(t, st, "omer")
	pollAll(t, st, fk)
	if status, _ := getTimeline(t, login(t, srv, "omer", testPassword), paymentsID(), ""); status != http.StatusNotFound {
		t.Errorf("user without View: status %d, want 404", status)
	}
}

func TestTimeline_MakesNoAWSCalls(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	for _, method := range []string{"FilterLogEvents", "DescribeServices"} {
		fk.FailAlways(method, errors.New("AWS must not be called"))
	}
	seedTimelineStop(t, st, paymentsKey(), "0123456789abcdef", "EssentialContainerExited", "OutOfMemoryError", 137, time.Hour)
	c := login(t, srv, "sam", testPassword)
	if status, _ := getTimeline(t, c, paymentsID(), "?range=7d"); status != http.StatusOK {
		t.Errorf("status %d, want 200 with AWS failing", status)
	}
	resp := c.get("/api/services/" + paymentsID() + "/timeline/tasks/0123456789abcdef")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("task route status %d, want 200 with AWS failing", resp.StatusCode)
	}
}

func TestTimeline_RetentionNote(t *testing.T) {
	note := func(retention time.Duration, query string) string {
		srv, fk, st, _ := newServerCustom(t, true, func(s *Server) { s.Cfg.Retention.StoppedTasks = retention })
		seedMockupTags(t, st)
		pollAll(t, st, fk)
		_, body := getTimeline(t, login(t, srv, "sam", testPassword), paymentsID(), query)
		lane, ok := timelineLane(body, "stop")
		if !ok {
			t.Fatalf("no stop lane in %+v", body.Lanes)
		}
		return lane.Note
	}
	if got, want := note(24*time.Hour, "?range=7d"), "Stops older than 1 day are not kept."; got != want {
		t.Errorf("24h retention, range=7d: note %q, want %q", got, want)
	}
	if got := note(24*time.Hour, "?range=1h"); got != "" {
		t.Errorf("24h retention, range=1h: note %q, want none", got)
	}
	if got := note(0, "?range=7d"); got != "" {
		t.Errorf("zero retention: note %q, want none", got)
	}
}

func TestTimeline_AlertLaneAlwaysExplainsItself(t *testing.T) {
	c, _, _ := timelineFixture(t)
	_, body := getTimeline(t, c, paymentsID(), "")
	lane, ok := timelineLane(body, "alert")
	want := "Shows alerts active now. Harbormaster does not keep cleared alerts."
	if !ok || lane.Note != want {
		t.Errorf("alert lane = %+v, want note %q", lane, want)
	}
}

func TestTimeline_ContainsNoLogData(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	setPassword(t, st, "omer")
	pollAll(t, st, fk)
	// omer holds View without Logs on Staging.
	status, body := getTimeline(t, login(t, srv, "omer", testPassword), stagingWebID(), "?range=7d")
	if status != http.StatusOK {
		t.Fatalf("status %d, want 200", status)
	}
	var lanes []string
	for _, lane := range body.Lanes {
		lanes = append(lanes, lane.Lane)
	}
	if want := []string{"change", "stop", "alert", "access"}; !slices.Equal(lanes, want) {
		t.Errorf("lanes = %v, want %v", lanes, want)
	}
	for _, event := range body.Events {
		if event.Lane == "log" || event.Lane == "logs" {
			t.Errorf("event %q is on the log lane", event.ID)
		}
	}
}

func TestTimeline_LateFailedDeploymentStillBuildsTheIncident(t *testing.T) {
	c, st, revision := timelineFixture(t)
	key := paymentsKey()
	seedTimelineOperation(t, st, key, 3*time.Hour, "Deployed revision", map[string]any{
		"op": "deploy", "from_revision": revision - 1, "to_revision": revision, "deployment_id": "ecs-svc/late",
	})
	// The deployment started before the 1h range and failed inside it; the timeline lists deployments by start.
	if _, err := st.InsertDeploymentIfNew(t.Context(), store.DeploymentRecord{
		Key: key, DeploymentID: "ecs-svc/late", Kind: "deploy", StartedAt: time.Now().Add(-3 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.FinishDeployment(t.Context(), key, "ecs-svc/late", "failed", time.Now().Add(-30*time.Minute),
		&store.FailureInfo{Group: "circuit_breaker", Detail: "tasks failed to start"}); err != nil {
		t.Fatal(err)
	}
	_, body := getTimeline(t, c, paymentsID(), "?range=1h")
	if body.Incident == nil || body.Incident.Trigger == nil || body.Incident.FailureCount != 1 {
		t.Fatalf("incident = %+v, want one failure named after the deploy", body.Incident)
	}
}

func TestTimelineTask_FindsOldStoppedTask(t *testing.T) {
	c, st, _ := timelineFixture(t)
	seedTimelineStop(t, st, paymentsKey(), "0123456789abcdef", "EssentialContainerExited", "OutOfMemoryError", 137, 3*24*time.Hour)

	resp := c.get("/api/services/" + paymentsID() + "/timeline/tasks/0123456789abcdef")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200 for a stop older than the Tasks window", resp.StatusCode)
	}
	var task TaskDTO
	decode(t, resp, &task)
	if task.ShortID != "0123456789abcdef" || !strings.HasSuffix(task.ARN, "/0123456789abcdef") {
		t.Errorf("task = %q %q", task.ShortID, task.ARN)
	}

	other := c.get("/api/services/" + stagingWebID() + "/timeline/tasks/0123456789abcdef")
	if other.StatusCode != http.StatusNotFound {
		t.Errorf("another service's task: status %d, want 404", other.StatusCode)
	}
	other.Body.Close()
}

// withTimelineLimits shrinks the read limits for one test.
func withTimelineLimits(t *testing.T, lane, failures int) {
	t.Helper()
	oldLane, oldFailures := timelineLaneLimit, timelineFailureLimit
	timelineLaneLimit, timelineFailureLimit = lane, failures
	t.Cleanup(func() { timelineLaneLimit, timelineFailureLimit = oldLane, oldFailures })
}

func TestTimeline_CutOffBurstHasNoTrigger(t *testing.T) {
	withTimelineLimits(t, 500, 3)
	c, st, revision := timelineFixture(t)
	key := paymentsKey()
	seedTimelineOperation(t, st, key, 50*time.Minute, "Deployed revision", map[string]any{
		"op": "deploy", "from_revision": revision - 1, "to_revision": revision,
	})
	// The crash loop began at -40m, but only the newest 3 failures are read. The restart during the loop must not
	// be named as the trigger.
	seedTimelineStop(t, st, key, "oom-a", "EssentialContainerExited", "OutOfMemoryError", 137, 40*time.Minute)
	seedTimelineOperation(t, st, key, 35*time.Minute, "Restarted", map[string]any{"op": "restart"})
	seedTimelineStop(t, st, key, "oom-b", "EssentialContainerExited", "OutOfMemoryError", 137, 30*time.Minute)
	seedTimelineStop(t, st, key, "oom-c", "EssentialContainerExited", "OutOfMemoryError", 137, 25*time.Minute)
	seedTimelineStop(t, st, key, "oom-d", "EssentialContainerExited", "OutOfMemoryError", 137, 20*time.Minute)

	_, body := getTimeline(t, c, paymentsID(), "?range=1h")
	if body.Incident == nil {
		t.Fatal("incident is null, want one")
	}
	if !body.Incident.TriggerUnknown {
		t.Error("trigger_unknown = false, want true for a burst that reaches the failure limit")
	}
	if body.Incident.Trigger != nil || body.Incident.RollbackRevision != nil || body.Incident.RolledBack != nil {
		t.Errorf("incident = %+v, want no trigger, rollback or rolled back", body.Incident)
	}
}

func TestTimeline_TruncatedChangesNewerThanTheBurstHaveNoTrigger(t *testing.T) {
	withTimelineLimits(t, 2, 2000)
	c, st, revision := timelineFixture(t)
	key := paymentsKey()
	seedTimelineOperation(t, st, key, 50*time.Minute, "Deployed revision", map[string]any{
		"op": "deploy", "from_revision": revision - 1, "to_revision": revision,
	})
	seedTimelineStop(t, st, key, "oom-a", "EssentialContainerExited", "OutOfMemoryError", 137, 30*time.Minute)
	seedTimelineStop(t, st, key, "oom-b", "EssentialContainerExited", "OutOfMemoryError", 137, 25*time.Minute)
	// Two newer changes fill the limit, so the real deploy was dropped from the read.
	seedTimelineOperation(t, st, key, 10*time.Minute, "Restarted", map[string]any{"op": "restart"})
	seedTimelineOperation(t, st, key, 5*time.Minute, "Restarted", map[string]any{"op": "restart"})

	_, body := getTimeline(t, c, paymentsID(), "?range=1h")
	if body.Incident == nil || !body.Incident.TriggerUnknown {
		t.Fatalf("incident = %+v, want trigger_unknown true", body.Incident)
	}
	if body.Incident.Trigger != nil || body.Incident.RollbackRevision != nil {
		t.Errorf("incident = %+v, want no trigger or rollback", body.Incident)
	}
}

func TestTimeline_ChangeLaneIsNotTruncatedWhenTheRangeIsComplete(t *testing.T) {
	withTimelineLimits(t, 2, 2000)
	c, st, _ := timelineFixture(t)
	key := paymentsKey()
	// The failures widen the read to 24h before the burst; both rows fill the limit but one is older than the range.
	seedTimelineStop(t, st, key, "oom-a", "EssentialContainerExited", "OutOfMemoryError", 137, 30*time.Minute)
	seedTimelineStop(t, st, key, "oom-b", "EssentialContainerExited", "OutOfMemoryError", 137, 25*time.Minute)
	seedTimelineStop(t, st, key, "oom-c", "EssentialContainerExited", "OutOfMemoryError", 137, 20*time.Minute)
	seedTimelineOperation(t, st, key, 3*time.Hour, "Restarted", map[string]any{"op": "restart"})
	seedTimelineOperation(t, st, key, 40*time.Minute, "Restarted", map[string]any{"op": "restart"})

	_, body := getTimeline(t, c, paymentsID(), "?range=1h")
	for _, lane := range []string{"change", "access"} {
		if l, _ := timelineLane(body, lane); l.Truncated {
			t.Errorf("%s lane truncated, want false: every row in the range was read", lane)
		}
	}
	if l, _ := timelineLane(body, "stop"); !l.Truncated {
		t.Error("stop lane not truncated, want true: 3 stops in range, limit 2")
	}
}

func TestTimeline_ClampedAlertCarriesEvidenceAt(t *testing.T) {
	c, st, _ := timelineFixture(t)
	srvFake := serviceAlert(homelabAccountID, "us-east-1", "core-services", "payments-api", model.AlertWarning, "Old evidence")
	srvFake.alert.At = time.Now().Add(-3 * time.Hour)
	for _, target := range []awsx.Target{srvFake.target} {
		if err := st.ReplaceAlerts(t.Context(), target, []model.Alert{srvFake.alert}); err != nil {
			t.Fatal(err)
		}
	}
	_, body := getTimeline(t, c, paymentsID(), "?range=1h")
	alerts := timelineEventsOn(body, "alert")
	if len(alerts) != 1 || alerts[0].EvidenceAt == nil {
		t.Fatalf("alert events = %+v, want one with evidence_at", alerts)
	}
	if alerts[0].At.Before(body.From) || alerts[0].EvidenceAt.After(body.From) {
		t.Errorf("at %v evidence_at %v from %v; want at clamped to from and evidence_at before it", alerts[0].At, alerts[0].EvidenceAt, body.From)
	}
}

func TestTimelineTask_BoundedLookupAndExactIDOnly(t *testing.T) {
	c, st, _ := timelineFixture(t)
	key := paymentsKey()
	for i := 0; i < 25; i++ {
		seedTimelineStop(t, st, key, fmt.Sprintf("bulk%028d", i), "EssentialContainerExited", "", 0, time.Hour)
	}
	seedTimelineStop(t, st, key, "0123456789abcdef0123456789abcdef", "EssentialContainerExited", "", 0, time.Hour)

	for _, id := range []string{"bulk", "payments-api"} {
		resp := c.get("/api/services/" + paymentsID() + "/timeline/tasks/" + id)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("task id %q: status %d, want 404 (substring or service name, not an id)", id, resp.StatusCode)
		}
	}
	resp := c.get("/api/services/" + paymentsID() + "/timeline/tasks/0123456789abcdef0123456789abcdef")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("real id: status %d, want 200", resp.StatusCode)
	}
}

func TestTimelineTask_InvisibleServiceIs404(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	setPassword(t, st, "omer")
	pollAll(t, st, fk)
	seedTimelineStop(t, st, paymentsKey(), "0123456789abcdef", "EssentialContainerExited", "", 0, time.Hour)
	resp := login(t, srv, "omer", testPassword).get("/api/services/" + paymentsID() + "/timeline/tasks/0123456789abcdef")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status %d, want 404", resp.StatusCode)
	}
}
