package deploy_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/rcss-labs/harbormaster/internal/deploy"
	"github.com/rcss-labs/harbormaster/internal/model"
)

func lastOperationText(t *testing.T, h *harness, key model.ServiceKey) string {
	t.Helper()
	ops, err := h.store.ListOperations(t.Context(), key, false, nil, 1)
	if err != nil || len(ops) != 1 {
		t.Fatalf("operations = %v, err %v", ops, err)
	}
	return ops[0].Text
}

// lastOperationOp is the details.op kind of the service's newest operation row.
func lastOperationOp(t *testing.T, h *harness, key model.ServiceKey) any {
	t.Helper()
	ops, err := h.store.ListOperations(t.Context(), key, false, nil, 1)
	if err != nil || len(ops) != 1 {
		t.Fatalf("operations = %v, err %v", ops, err)
	}
	return ops[0].Details["op"]
}

func deploymentKind(t *testing.T, h *harness, key model.ServiceKey, id string) (kind string, userID int64) {
	t.Helper()
	err := h.store.Pool.QueryRow(t.Context(),
		`select kind, started_by_user_id from deployment_history
		 where account_id=$1 and region=$2 and cluster_name=$3 and service_name=$4 and deployment_id=$5`,
		key.AccountID, key.Region, key.Cluster, key.Service, id).Scan(&kind, &userID)
	if err != nil {
		t.Fatalf("deployment_history row for %s: %v", id, err)
	}
	return kind, userID
}

func TestDeploy_StartsRolloutAndAttributes(t *testing.T) {
	h := newHarness(t)
	draft := draftFromDeployed(t, h, "payments-api:1.18.3")
	if _, err := h.svc.Register(t.Context(), h.sam, paymentsKey, deploy.RegisterInput{BaseRevision: 42, Definition: draft}); err != nil {
		t.Fatal(err)
	}

	got, err := h.svc.Deploy(t.Context(), h.sam, paymentsKey, 43, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != "deploy" || got.FromRevision != 42 || got.ToRevision != 43 || got.DeploymentID == "" {
		t.Fatalf("Rollout = %+v", got)
	}
	primary := h.primary(t, paymentsKey)
	if aws.ToString(primary.Id) != got.DeploymentID || primary.RolloutState != ecstypes.DeploymentRolloutStateInProgress ||
		!strings.HasSuffix(aws.ToString(primary.TaskDefinition), ":task-definition/payments-api:43") {
		t.Fatalf("fake primary = %+v", primary)
	}
	if kind, user := deploymentKind(t, h, paymentsKey, got.DeploymentID); kind != "deploy" || user != h.sam.UserID {
		t.Fatalf("deployment_history kind=%q user=%d", kind, user)
	}
	if text := lastOperationText(t, h, paymentsKey); text != "Deployed payments-api:43." {
		t.Fatalf("operation = %q", text)
	}
	if op := lastOperationOp(t, h, paymentsKey); op != "deploy" {
		t.Fatalf("details.op = %v, want deploy", op)
	}
}

func TestDeploy_OlderRevisionIsRollback(t *testing.T) {
	h := newHarness(t)
	got, err := h.svc.Deploy(t.Context(), h.sam, paymentsKey, 41, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != "rollback" || got.FromRevision != 42 || got.ToRevision != 41 {
		t.Fatalf("Rollout = %+v", got)
	}
	if kind, _ := deploymentKind(t, h, paymentsKey, got.DeploymentID); kind != "deploy" {
		t.Fatalf("deployment_history kind = %q, want deploy", kind)
	}
	if text := lastOperationText(t, h, paymentsKey); text != "Rolled back payments-api:41." {
		t.Fatalf("operation = %q", text)
	}
	if op := lastOperationOp(t, h, paymentsKey); op != "rollback" {
		t.Fatalf("details.op = %v, want rollback", op)
	}
}

func TestDeploy_RefusedWhileRolloutRunning(t *testing.T) {
	h := newHarness(t)
	before := h.fake.Calls()["UpdateService"]
	_, err := h.svc.Deploy(t.Context(), h.sam, notificationsKey, 9, false)
	if !errors.Is(err, deploy.ErrRolloutRunning) {
		t.Fatalf("err = %v, want ErrRolloutRunning", err)
	}
	if _, err := h.svc.Restart(t.Context(), h.sam, notificationsKey); !errors.Is(err, deploy.ErrRolloutRunning) {
		t.Fatalf("restart err = %v, want ErrRolloutRunning", err)
	}
	if h.fake.Calls()["UpdateService"] != before {
		t.Fatal("UpdateService was called")
	}
}

func TestDeploy_AlreadyDeployedRevision(t *testing.T) {
	h := newHarness(t)
	_, err := h.svc.Deploy(t.Context(), h.sam, paymentsKey, 42, false)
	var validation *deploy.ValidationError
	if !errors.As(err, &validation) || validation.Message != "Revision :42 is already deployed. Use Restart to replace its tasks." {
		t.Fatalf("err = %v", err)
	}
}

func TestDeploy_UnsupportedController(t *testing.T) {
	h := newHarness(t)
	h.fake.Estate.Mu.Lock()
	for i := range h.fake.Estate.Services {
		s := &h.fake.Estate.Services[i]
		if aws.ToString(s.Service.ServiceName) == "payments-api" {
			s.Service.DeploymentController = &ecstypes.DeploymentController{Type: ecstypes.DeploymentControllerTypeCodeDeploy}
		}
	}
	h.fake.Estate.Mu.Unlock()

	_, err := h.svc.Deploy(t.Context(), h.sam, paymentsKey, 41, false)
	var unsupported *deploy.UnsupportedControllerError
	if !errors.As(err, &unsupported) || unsupported.Name != "CodeDeploy" {
		t.Fatalf("err = %v, want UnsupportedControllerError{CodeDeploy}", err)
	}
	if _, err := h.svc.Restart(t.Context(), h.sam, paymentsKey); !errors.As(err, &unsupported) {
		t.Fatalf("restart err = %v", err)
	}
}

func TestRestart_ForceNewDeployment(t *testing.T) {
	h := newHarness(t)
	old := aws.ToString(h.primary(t, paymentsKey).Id)
	got, err := h.svc.Restart(t.Context(), h.sam, paymentsKey)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != "restart" || got.DeploymentID == "" || got.DeploymentID == old || got.FromRevision != 42 || got.ToRevision != 42 {
		t.Fatalf("Rollout = %+v (old deployment %s)", got, old)
	}
	if kind, _ := deploymentKind(t, h, paymentsKey, got.DeploymentID); kind != "restart" {
		t.Fatalf("kind = %q", kind)
	}
	if text := lastOperationText(t, h, paymentsKey); text != "Restarted service (force new deployment) on revision 42." {
		t.Fatalf("operation = %q", text)
	}
	if op := lastOperationOp(t, h, paymentsKey); op != "restart" {
		t.Fatalf("details.op = %v, want restart", op)
	}
}

func TestDeploy_RejectsNonPositiveRevision(t *testing.T) {
	h := newHarness(t)
	_, err := h.svc.Deploy(t.Context(), h.sam, paymentsKey, 0, false)
	var validation *deploy.ValidationError
	if !errors.As(err, &validation) || validation.Message != "Revision must be a positive number" {
		t.Fatalf("err = %v", err)
	}
}

func TestDeploy_InterruptAllowsRollbackDuringRollout(t *testing.T) {
	h := newHarness(t)
	got, err := h.svc.Deploy(t.Context(), h.sam, notificationsKey, 9, true)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != "rollback" || got.FromRevision != 10 || got.ToRevision != 9 || got.DeploymentID == "" {
		t.Fatalf("Rollout = %+v", got)
	}
}

func TestDeploy_InterruptDoesNotAllowForwardDeployDuringRollout(t *testing.T) {
	h := newHarness(t)
	before := h.fake.Calls()["UpdateService"]
	_, err := h.svc.Deploy(t.Context(), h.sam, notificationsKey, 11, true)
	if !errors.Is(err, deploy.ErrRolloutRunning) {
		t.Fatalf("err = %v, want ErrRolloutRunning", err)
	}
	if h.fake.Calls()["UpdateService"] != before {
		t.Fatal("UpdateService was called")
	}
}
