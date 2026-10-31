package deploy_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/rcss-labs/harbormaster/internal/deploy"
	"github.com/rcss-labs/harbormaster/internal/model"
)

// fixedScaling is a ScalingRangeSource with one registered range.
type fixedScaling struct{ min, max int }

func (f fixedScaling) Range(context.Context, model.ServiceKey) (int, int, bool, error) {
	return f.min, f.max, true, nil
}

// runningTaskARN returns a RUNNING task of the store's snapshot in cluster.
// A nil service selects standalone tasks.
func runningTaskARN(t *testing.T, h *harness, cluster string, service *string) string {
	t.Helper()
	var arn string
	err := h.store.Pool.QueryRow(t.Context(),
		`select task_arn from tasks
		 where account_id = $1 and region = $2 and cluster_name = $3
		   and service_name is not distinct from $4 and last_status = 'RUNNING'
		 order by task_arn limit 1`,
		homelabAccountID, homelabRegion, cluster, service).Scan(&arn)
	if err != nil {
		t.Fatalf("running task in %s: %v", cluster, err)
	}
	return arn
}

// fakeTaskStatus returns the last status and stopped reason of a fake task, read under the estate lock.
func fakeTaskStatus(t *testing.T, h *harness, arn string) (lastStatus, stoppedReason string) {
	t.Helper()
	h.fake.Estate.Mu.Lock()
	defer h.fake.Estate.Mu.Unlock()
	for _, task := range h.fake.Estate.Tasks {
		if aws.ToString(task.Task.TaskArn) == arn {
			return aws.ToString(task.Task.LastStatus), aws.ToString(task.Task.StoppedReason)
		}
	}
	t.Fatalf("task %s not in fake estate", arn)
	return "", ""
}

func TestSetDesiredCount_ChangesAndLogs(t *testing.T) {
	h := newHarness(t)
	got, err := h.svc.SetDesiredCount(t.Context(), h.pavel, paymentsKey, 5)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Changed || got.Previous != 3 || got.Desired != 5 || got.Warning != "" {
		t.Fatalf("DesiredCountResult = %+v", got)
	}
	info, err := h.svc.DesiredCount(t.Context(), paymentsKey)
	if err != nil {
		t.Fatal(err)
	}
	if info.Desired != 5 {
		t.Fatalf("fake desired count = %d, want 5", info.Desired)
	}
	if text := lastOperationText(t, h, paymentsKey); text != "Changed desired count from 3 to 5." {
		t.Fatalf("operation = %q", text)
	}
}

func TestSetDesiredCount_SameValueIsNoop(t *testing.T) {
	h := newHarness(t)
	before := h.fake.Calls()["UpdateService"]
	got, err := h.svc.SetDesiredCount(t.Context(), h.pavel, paymentsKey, 3)
	if err != nil {
		t.Fatal(err)
	}
	if got.Changed {
		t.Fatalf("Changed = true for an unchanged count: %+v", got)
	}
	if h.fake.Calls()["UpdateService"] != before {
		t.Fatal("UpdateService was called for an unchanged count")
	}
	ops, err := h.store.ListOperations(t.Context(), paymentsKey, false, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Fatalf("operations = %+v, want none", ops)
	}
}

func TestSetDesiredCount_OutOfRange(t *testing.T) {
	h := newHarness(t)
	before := h.fake.Calls()["UpdateService"]
	for _, n := range []int{-1, 51} {
		_, err := h.svc.SetDesiredCount(t.Context(), h.pavel, paymentsKey, n)
		var validation *deploy.ValidationError
		if !errors.As(err, &validation) || validation.Message != "Desired count must be between 0 and 50" {
			t.Fatalf("n=%d err = %v", n, err)
		}
	}
	if h.fake.Calls()["UpdateService"] != before {
		t.Fatal("UpdateService was called for an out-of-range count")
	}
}

func TestSetDesiredCount_WarningOutsideAutoScaling(t *testing.T) {
	h := newHarness(t)
	h.svc.Scaling = fixedScaling{min: 2, max: 6}

	got, err := h.svc.SetDesiredCount(t.Context(), h.pavel, paymentsKey, 7)
	if err != nil {
		t.Fatal(err)
	}
	if want := "Auto scaling keeps this service between 2 and 6. It will move 7 back into that range."; got.Warning != want {
		t.Fatalf("warning = %q, want %q", got.Warning, want)
	}

	got, err = h.svc.SetDesiredCount(t.Context(), h.pavel, paymentsKey, 4)
	if err != nil {
		t.Fatal(err)
	}
	if got.Warning != "" {
		t.Fatalf("warning inside the range = %q", got.Warning)
	}
}

func TestStopTask_ReasonAndLog(t *testing.T) {
	h := newHarness(t)
	payments := "payments-api"
	arn := runningTaskARN(t, h, "core-services", &payments)

	stopped, err := h.svc.StopTask(t.Context(), h.pavel, homelabAccountID, homelabRegion, arn)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.TaskARN != arn || stopped.ServiceName != payments || stopped.Reason != "Stopped by pavel via Harbormaster" {
		t.Fatalf("Stopped = %+v", stopped)
	}
	lastStatus, reason := fakeTaskStatus(t, h, arn)
	if lastStatus != "STOPPED" || reason != "Stopped by pavel via Harbormaster" {
		t.Fatalf("fake task status = %s, reason %q", lastStatus, reason)
	}
	taskID := arn[strings.LastIndex(arn, "/")+1:]
	if text := lastOperationText(t, h, paymentsKey); text != "Stopped task "+taskID[:12]+"." {
		t.Fatalf("operation = %q", text)
	}
}

func TestStopTask_StandaloneTaskLogsWithoutService(t *testing.T) {
	h := newHarness(t)
	arn := runningTaskARN(t, h, "ci-tools", nil)

	if _, err := h.svc.StopTask(t.Context(), h.pavel, homelabAccountID, homelabRegion, arn); err != nil {
		t.Fatal(err)
	}
	var serviceIsNull bool
	var cluster, loggedARN, text string
	err := h.store.Pool.QueryRow(t.Context(),
		`select service_name is null, details->>'cluster', details->>'task_arn', text
		 from operation_log where account_id = $1 and region = $2 and cluster_name = 'ci-tools'
		 order by id desc limit 1`,
		homelabAccountID, homelabRegion).Scan(&serviceIsNull, &cluster, &loggedARN, &text)
	if err != nil {
		t.Fatal(err)
	}
	taskID := arn[strings.LastIndex(arn, "/")+1:]
	if !serviceIsNull || cluster != "ci-tools" || loggedARN != arn || text != "Stopped task "+taskID[:12]+"." {
		t.Fatalf("operation row: service null %v, cluster %q, task_arn %q, text %q", serviceIsNull, cluster, loggedARN, text)
	}
}
