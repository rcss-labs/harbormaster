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

func (h *observeHarness) alertsLoop() *AlertsLoop {
	return &AlertsLoop{Store: h.store, Every: 5 * time.Second, Now: func() time.Time { return loopNow }}
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
