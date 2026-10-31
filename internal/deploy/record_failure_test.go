package deploy_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/rcss-labs/harbormaster/internal/deploy"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// failAtCommit runs the writes for real inside the transaction and then fails it, like a
// connection lost before COMMIT: everything the action wrote must roll back together.
func failAtCommit(st *store.Store) func(context.Context, func(store.Tx) error) error {
	return func(ctx context.Context, fn func(store.Tx) error) error {
		return st.InTx(ctx, func(tx store.Tx) error {
			if err := fn(tx); err != nil {
				return err
			}
			return errors.New("injected: connection lost before commit")
		})
	}
}

func operationCount(t *testing.T, h *harness) int {
	t.Helper()
	var n int
	if err := h.store.Pool.QueryRow(t.Context(), `select count(*) from operation_log`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRegister_RecordFailureAfterAWSAcceptedIsSuccessWithWarning(t *testing.T) {
	h := newHarness(t)
	// Prime the revisions cache so the test can see it evicted.
	if _, err := h.svc.Revisions(t.Context(), paymentsKey); err != nil {
		t.Fatal(err)
	}
	h.svc.InTx = failAtCommit(h.store)
	opsBefore := operationCount(t, h)
	draft := draftFromDeployed(t, h, "payments-api:1.18.3")

	got, err := h.svc.Register(t.Context(), h.pavel, paymentsKey, deploy.RegisterInput{BaseRevision: 42, Definition: draft})
	if err != nil {
		t.Fatalf("Register() error = %v, want success with a warning (a 5xx invites a duplicate-registering retry)", err)
	}
	if got.Revision != 43 || got.ARN == "" || !strings.Contains(got.Warning, "could not record") {
		t.Errorf("Registered = %+v, want revision 43 and a recording warning", got)
	}
	if calls := h.fake.Calls()["RegisterTaskDefinition"]; calls != 1 {
		t.Errorf("RegisterTaskDefinition calls = %d, want 1", calls)
	}
	if n := operationCount(t, h); n != opsBefore {
		t.Errorf("operation_log grew by %d, want the transaction rolled back as a whole", n-opsBefore)
	}
	if registrars, _ := h.store.RevisionRegistrars(t.Context(), []string{got.ARN}); len(registrars) != 0 {
		t.Errorf("revision record survived the rollback: %v", registrars)
	}
	list, err := h.svc.Revisions(t.Context(), paymentsKey)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, revision := range list.Items {
		found = found || revision.Revision == 43
	}
	if !found {
		t.Error("revisions cache was not evicted: :43 is missing from the list")
	}
}

func TestDeploy_RecordFailureAfterAWSAcceptedIsSuccessWithWarning(t *testing.T) {
	h := newHarness(t)
	h.svc.InTx = failAtCommit(h.store)
	opsBefore := operationCount(t, h)

	got, err := h.svc.Deploy(t.Context(), h.pavel, paymentsKey, 41)
	if err != nil {
		t.Fatalf("Deploy() error = %v, want success with a warning", err)
	}
	if got.DeploymentID == "" || got.Kind != "rollback" || !strings.Contains(got.Warning, "could not record") {
		t.Errorf("Rollout = %+v", got)
	}
	if calls := h.fake.Calls()["UpdateService"]; calls != 1 {
		t.Errorf("UpdateService calls = %d, want 1", calls)
	}
	if n := operationCount(t, h); n != opsBefore {
		t.Errorf("operation_log grew by %d, want the transaction rolled back as a whole", n-opsBefore)
	}
}

func TestRestart_RecordFailureAfterAWSAcceptedIsSuccessWithWarning(t *testing.T) {
	h := newHarness(t)
	h.svc.InTx = failAtCommit(h.store)
	got, err := h.svc.Restart(t.Context(), h.pavel, paymentsKey)
	if err != nil || got.DeploymentID == "" || !strings.Contains(got.Warning, "could not record") {
		t.Fatalf("Restart() = %+v, %v; want success with a warning", got, err)
	}
}

func TestSetDesiredCount_RecordFailureAfterAWSAcceptedIsSuccessWithWarning(t *testing.T) {
	h := newHarness(t)
	h.svc.InTx = failAtCommit(h.store)
	got, err := h.svc.SetDesiredCount(t.Context(), h.pavel, paymentsKey, 4)
	if err != nil || !got.Changed || got.Desired != 4 || !strings.Contains(got.Warning, "could not record") {
		t.Fatalf("SetDesiredCount() = %+v, %v; want success with a warning", got, err)
	}
	if calls := h.fake.Calls()["UpdateService"]; calls != 1 {
		t.Errorf("UpdateService calls = %d, want 1", calls)
	}
}

func TestStopTask_RecordFailureAfterAWSAcceptedIsSuccessWithWarning(t *testing.T) {
	h := newHarness(t)
	h.svc.InTx = failAtCommit(h.store)
	service := paymentsKey.Service
	arn := runningTaskARN(t, h, paymentsKey.Cluster, &service)
	got, err := h.svc.StopTask(t.Context(), h.pavel, homelabAccountID, homelabRegion, arn)
	if err != nil || !strings.Contains(got.Warning, "could not record") {
		t.Fatalf("StopTask() = %+v, %v; want success with a warning", got, err)
	}
}

// A failure between claiming the outcome and logging it must not lose the line:
// the claim rolls back with the failed log write, so the next pass tries again.
func TestOutcomeLogger_FailedLogWriteRollsBackClaimAndNextPassRetries(t *testing.T) {
	f := newOutcomeFixture(t, ecstypes.DeploymentRolloutStateCompleted, "", time.Minute)
	ctx := context.Background()

	failing := f.logger()
	failing.InTx = func(ctx context.Context, fn func(store.Tx) error) error {
		return f.st.InTx(ctx, func(tx store.Tx) error {
			if err := fn(tx); err != nil {
				return err
			}
			return errors.New("injected: log write failed")
		})
	}
	if err := failing.Once(ctx); err == nil {
		t.Fatal("Once() = nil, want the injected failure reported")
	}
	if got := f.outcome(t); got != "" {
		t.Fatalf("outcome after failed pass = %q, want the claim rolled back (still open)", got)
	}

	if err := f.logger().Once(ctx); err != nil {
		t.Fatal(err)
	}
	assertOnlyText(t, f.operationTexts(t), "Deployment of image-resizer:7 completed.")
}
