package poller

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// alertsLoop returns the harness's one AlertsLoop, so its in-memory poll counts persist across runs like the real tier's.
func (h *observeHarness) alertsLoop() *AlertsLoop {
	if h.alerts == nil {
		h.alerts = &AlertsLoop{Store: h.store, Every: 5 * time.Second, Now: func() time.Time { return loopNow }}
	}
	return h.alerts
}

func (h *observeHarness) runAlerts(t *testing.T) {
	t.Helper()
	if err := h.alertsLoop().Run(context.Background(), h.target); err != nil {
		t.Fatalf("AlertsLoop.Run: %v", err)
	}
}

// resetAlertState forgets what PollOnce's own alerts pass recorded, so a test counts runs from zero.
func (h *observeHarness) resetAlertState(t *testing.T) {
	t.Helper()
	h.alerts = nil
	for _, table := range []string{"service_status_runs", "alerts"} {
		if _, err := h.store.Pool.Exec(context.Background(), `delete from `+table); err != nil {
			t.Fatal(err)
		}
	}
}

func (h *observeHarness) alertsFor(t *testing.T, key model.ServiceKey) []model.Alert {
	t.Helper()
	all, err := h.store.ListAlerts(context.Background(), store.AlertFilter{AccountID: h.target.AccountID, Region: h.target.Region})
	if err != nil {
		t.Fatal(err)
	}
	var mine []model.Alert
	for _, alert := range all {
		if alert.Key == key {
			mine = append(mine, alert)
		}
	}
	return mine
}

func alertKinds(alerts []model.Alert) map[string]model.Alert {
	kinds := map[string]model.Alert{}
	for _, alert := range alerts {
		kinds[alert.Kind] = alert
	}
	return kinds
}

// statusRunVersions returns each status run's row version (xmin), which changes whenever the row is written.
func (h *observeHarness) statusRunVersions(t *testing.T) map[string]string {
	t.Helper()
	rows, err := h.store.Pool.Query(context.Background(),
		`select cluster_name || '/' || service_name, xmin::text from service_status_runs`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	versions := map[string]string{}
	for rows.Next() {
		var name, xmin string
		if err := rows.Scan(&name, &xmin); err != nil {
			t.Fatal(err)
		}
		versions[name] = xmin
	}
	return versions
}

// A static estate must not rewrite one status-run row per service every cycle (plan 06 soak: 60 000 writes/min).
func TestAlertsLoop_UnchangedStatusWritesNoStatusRuns(t *testing.T) {
	h := newObserveHarness(t, loopTarget)
	h.runAlerts(t)
	before := h.statusRunVersions(t)
	if len(before) == 0 {
		t.Fatal("first alerts run stored no status runs")
	}
	h.runAlerts(t)
	h.runAlerts(t)
	after := h.statusRunVersions(t)
	for name, version := range before {
		if after[name] != version {
			t.Errorf("status run %s was rewritten although its status did not change", name)
		}
	}
}

func TestAlertsLoop_WritesFailingAndRestartLoop(t *testing.T) {
	h := newObserveHarness(t, loopTarget)
	key := serviceKey(loopTarget, "background-jobs", "image-resizer")

	h.runAlerts(t)

	mine := h.alertsFor(t, key)
	kinds := alertKinds(mine)
	if len(mine) != 2 || kinds["service_failing"].Kind == "" || kinds["restart_loop"].Kind == "" {
		t.Fatalf("image-resizer alerts = %+v, want service_failing and restart_loop", mine)
	}
	if text := kinds["service_failing"].Text; !strings.HasSuffix(text, "(exit 137)") {
		t.Errorf("service_failing text = %q, want suffix (exit 137)", text)
	}
}

func TestAlertsLoop_DegradedAppearsOnThirdRun(t *testing.T) {
	h := newObserveHarness(t, loopTarget)
	key := serviceKey(loopTarget, "core-services", "payments-api")
	var runningARN string
	for _, task := range h.estate.Tasks {
		if aws.ToString(task.Task.Group) == "service:payments-api" && aws.ToString(task.Task.LastStatus) == "RUNNING" {
			runningARN = aws.ToString(task.Task.TaskArn)
			break
		}
	}
	if runningARN == "" {
		t.Fatal("payments-api has no running task in the estate")
	}
	h.estate.StopTask(runningARN, "Task failed ELB health checks", 1)
	h.poll(t)
	h.resetAlertState(t)

	for run := 1; run <= 3; run++ {
		h.runAlerts(t)
		_, degraded := alertKinds(h.alertsFor(t, key))["service_degraded"]
		if want := run == 3; degraded != want {
			t.Fatalf("after run %d service_degraded present = %v, want %v", run, degraded, want)
		}
	}
}

func TestAlertsLoop_ReplacesOldAlerts(t *testing.T) {
	h := newObserveHarness(t, loopTarget)
	key := serviceKey(loopTarget, "background-jobs", "image-resizer")
	h.runAlerts(t)
	if len(h.alertsFor(t, key)) == 0 {
		t.Fatal("image-resizer has no alerts before the fix")
	}

	// Fix the service: two healthy tasks, and drop the OOM stop history that feeds restart_loop.
	h.estate.AddRunningTasks("background-jobs", "image-resizer", 2)
	h.poll(t)
	if _, err := h.store.Pool.Exec(context.Background(), `delete from tasks where service_name = 'image-resizer' and last_status = 'STOPPED'`); err != nil {
		t.Fatal(err)
	}
	h.runAlerts(t)

	if mine := h.alertsFor(t, key); len(mine) != 0 {
		t.Fatalf("image-resizer alerts after fix = %+v, want none", mine)
	}
}

func TestAlertsLoop_AccountUnreachable(t *testing.T) {
	h := newObserveHarness(t, loopTarget)
	lastOK := loopNow.Add(-2 * time.Minute)
	if err := h.store.UpsertPollLoop(context.Background(), store.PollLoop{
		Target: loopTarget, Tier: "services", LastOKAt: &lastOK, LastError: "AccessDenied",
		Interval: 10 * time.Second, BaseInterval: 10 * time.Second, Failures: 3, UpdatedAt: loopNow,
	}); err != nil {
		t.Fatal(err)
	}

	h.runAlerts(t)

	var unreachable []model.Alert
	for _, alert := range h.alertsFor(t, model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: loopTarget.AccountID, Region: loopTarget.Region}}) {
		if alert.Kind == "account_unreachable" {
			unreachable = append(unreachable, alert)
		}
	}
	if len(unreachable) != 1 {
		t.Fatalf("account_unreachable alerts = %+v, want exactly one with an empty service", unreachable)
	}
}

// An UNHEALTHY task that ECS is draining (DEPROVISIONING) counts as running for the API's
// RunningTasks, so the alerts tier must flag it too; otherwise the service pill and the
// Needs-attention list disagree for the whole deregistration delay.
func TestAlertsLoop_UnhealthyDeprovisioningTaskMatchesAPIStatus(t *testing.T) {
	h := newObserveHarness(t, loopTarget)
	key := serviceKey(loopTarget, "core-services", "payments-api")
	ctx := context.Background()
	tag, err := h.store.Pool.Exec(ctx,
		`update tasks set last_status = 'DEPROVISIONING', health_status = 'UNHEALTHY'
		 where ctid = (select ctid from tasks where account_id = $1 and region = $2 and cluster_name = $3
		   and service_name = $4 and last_status = 'RUNNING' limit 1)`,
		key.AccountID, key.Region, key.Cluster, key.Service)
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("seed deprovisioning task: %v, rows %d", err, tag.RowsAffected())
	}
	h.resetAlertState(t)

	h.runAlerts(t)

	apiTasks, err := h.store.RunningTasks(ctx, store.TargetFilter{AccountID: key.AccountID, Region: key.Region}, store.Visibility{Admin: true})
	if err != nil {
		t.Fatal(err)
	}
	apiSeesUnhealthy := false
	for _, row := range apiTasks {
		if row.ServiceName == key.Service && row.Cluster == key.ClusterKey && row.HealthStatus == "UNHEALTHY" {
			apiSeesUnhealthy = true
		}
	}
	_, alerted := alertKinds(h.alertsFor(t, key))["unhealthy_task"]
	if !apiSeesUnhealthy || !alerted {
		t.Fatalf("API sees unhealthy running task = %v, alerts tier flags it = %v; both must be true", apiSeesUnhealthy, alerted)
	}
}
