package deploy_test

import (
	"errors"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/deploy"
)

const logDestinationMessage = "Changing where a container's logs go needs a global admin"

// registerForeignLogRevision has the admin register revision 43 of payments-api with logs
// pointed at another service's group, the way a Terraform apply could.
func registerForeignLogRevision(t *testing.T, h *harness) {
	t.Helper()
	draft := draftFromDeployed(t, h, "payments-api:1.18.3")
	for _, entry := range draft["containerDefinitions"].([]any) {
		logConfig, _ := entry.(map[string]any)["logConfiguration"].(map[string]any)
		existing, ok := logConfig["options"].(map[string]any)
		if !ok {
			continue
		}
		options := map[string]any{}
		for key, value := range existing {
			options[key] = value
		}
		options["awslogs-group"] = "/ecs/prod/another-service"
		logConfig["options"] = options
	}
	if _, err := h.svc.Register(t.Context(), h.sam, paymentsKey, deploy.RegisterInput{BaseRevision: 42, Definition: draft}); err != nil {
		t.Fatal(err)
	}
}

func requireLogDestinationError(t *testing.T, err error) {
	t.Helper()
	var validation *deploy.ValidationError
	if !errors.As(err, &validation) || validation.Message != logDestinationMessage {
		t.Fatalf("err = %v, want ValidationError %q", err, logDestinationMessage)
	}
}

func TestDeploy_NonAdminCannotDeployRevisionWithOtherLogDestination(t *testing.T) {
	h := newHarness(t)
	registerForeignLogRevision(t, h)
	before := h.fake.Calls()["UpdateService"]
	deployer := subjectWith(t, h, paymentsKey, authz.Deploy)
	_, err := h.svc.Deploy(t.Context(), deployer, paymentsKey, 43, false)
	requireLogDestinationError(t, err)
	if h.fake.Calls()["UpdateService"] != before {
		t.Fatal("UpdateService was called for a rejected revision")
	}
	if _, err := h.svc.Deploy(t.Context(), h.sam, paymentsKey, 43, false); err != nil {
		t.Fatalf("admin Deploy = %v, want nil", err)
	}
}

func TestChangeSet_NonAdminCannotDeployRevisionWithOtherLogDestination(t *testing.T) {
	h := newHarness(t)
	registerForeignLogRevision(t, h)
	cs := baseChangeSet(t, h, paymentsKey)
	cs.TaskDefinition = &deploy.TaskDefinitionChange{Revision: 43}
	before := h.fake.Calls()["UpdateService"]
	_, err := h.svc.ApplyChangeSet(t.Context(), subjectWith(t, h, paymentsKey, authz.Deploy), paymentsKey, cs)
	requireLogDestinationError(t, err)
	if h.fake.Calls()["UpdateService"] != before {
		t.Fatal("UpdateService was called for a rejected revision")
	}
}
