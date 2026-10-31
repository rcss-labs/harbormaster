package deploy_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/rcss-labs/harbormaster/internal/deploy"
)

// draftFromDeployed returns the deployed definition of key with the main container image replaced.
func draftFromDeployed(t *testing.T, h *harness, image string) deploy.Definition {
	t.Helper()
	deployed, err := h.svc.DeployedDefinition(t.Context(), paymentsKey)
	if err != nil {
		t.Fatal(err)
	}
	draft := deploy.Definition{}
	for k, v := range deployed.Definition {
		draft[k] = v
	}
	containers := deployed.Definition["containerDefinitions"].([]any)
	copied := make([]any, len(containers))
	for i, c := range containers {
		m := map[string]any{}
		for k, v := range c.(map[string]any) {
			m[k] = v
		}
		if m["name"] == "payments-api" {
			m["image"] = image
		}
		copied[i] = m
	}
	draft["containerDefinitions"] = copied
	return draft
}

func countRevisions(h *harness, family string) int {
	h.fake.Estate.Mu.Lock()
	defer h.fake.Estate.Mu.Unlock()
	n := 0
	for _, td := range h.fake.Estate.TaskDefinitions {
		if aws.ToString(td.Definition.Family) == family {
			n++
		}
	}
	return n
}

func TestRegister_HappyPath_RecordsEverything(t *testing.T) {
	h := newHarness(t)
	draft := draftFromDeployed(t, h, "482109375512.dkr.ecr.us-east-1.amazonaws.com/payments-api:1.18.3")

	got, err := h.svc.Register(t.Context(), h.pavel, paymentsKey, deploy.RegisterInput{BaseRevision: 42, Definition: draft})
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != 43 || got.ImageTag != "1.18.3" || got.CPU != "512" || got.Memory != "1024" {
		t.Fatalf("Registered = %+v", got)
	}

	var tags []ecstypes.Tag
	h.fake.Estate.Mu.Lock()
	for _, td := range h.fake.Estate.TaskDefinitions {
		if aws.ToString(td.Definition.TaskDefinitionArn) == got.ARN {
			tags = td.Tags
		}
	}
	h.fake.Estate.Mu.Unlock()
	if len(tags) != 1 || aws.ToString(tags[0].Key) != "managed-by" || aws.ToString(tags[0].Value) != "terraform" {
		t.Fatalf("tags on revision 43 = %+v, want managed-by=terraform", tags)
	}

	registrars, err := h.store.RevisionRegistrars(t.Context(), []string{got.ARN})
	if err != nil || registrars[got.ARN] != "pavel" {
		t.Fatalf("registrars = %v, err %v", registrars, err)
	}
	ops, err := h.store.ListOperations(t.Context(), paymentsKey, false, nil, 1)
	if err != nil || len(ops) != 1 {
		t.Fatalf("operations = %v, err %v", ops, err)
	}
	want := "Registered task definition payments-api:43 (image 1.18.3, 512 CPU / 1024 MiB)."
	if ops[0].Text != want || ops[0].Actor != "pavel" || ops[0].Source != "harbormaster" {
		t.Fatalf("operation = %+v, want text %q by pavel", ops[0], want)
	}
	if ops[0].Details["task_definition_arn"] != got.ARN {
		t.Fatalf("operation details = %v", ops[0].Details)
	}
	if _, err := h.store.GetTaskDefinition(t.Context(), homelabAccountID, homelabRegion, got.ARN); err != nil {
		t.Fatalf("registered definition not stored: %v", err)
	}
}

func TestRegister_StaleBase(t *testing.T) {
	h := newHarness(t)
	draft := draftFromDeployed(t, h, "payments-api:1.18.3")
	// Someone else deploys revision 43 behind our back.
	if _, err := h.svc.Register(t.Context(), h.pavel, paymentsKey, deploy.RegisterInput{BaseRevision: 42, Definition: draft}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Deploy(t.Context(), h.pavel, paymentsKey, 43); err != nil {
		t.Fatal(err)
	}
	before := countRevisions(h, "payments-api")

	_, err := h.svc.Register(t.Context(), h.pavel, paymentsKey, deploy.RegisterInput{BaseRevision: 42, Definition: draft})
	var stale *deploy.StaleBaseError
	if !errors.As(err, &stale) || stale.BaseRevision != 42 || stale.DeployedRevision != 43 {
		t.Fatalf("err = %v, want StaleBaseError{42, 43}", err)
	}
	if err.Error() != "The service moved from :42 to :43 while you were editing" {
		t.Fatalf("message = %q", err.Error())
	}
	if countRevisions(h, "payments-api") != before {
		t.Fatal("a revision was registered despite the stale base")
	}
}

func TestRegister_ValidationErrorIsNotRegistered(t *testing.T) {
	h := newHarness(t)
	draft := draftFromDeployed(t, h, "payments-api:1.18.3")
	draft["family"] = "payments-api-v2"
	before := countRevisions(h, "payments-api")

	_, err := h.svc.Register(t.Context(), h.pavel, paymentsKey, deploy.RegisterInput{BaseRevision: 42, Definition: draft})
	var validation *deploy.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("err = %v, want ValidationError", err)
	}
	if countRevisions(h, "payments-api") != before {
		t.Fatal("a revision was registered")
	}
	ops, _ := h.store.ListOperations(t.Context(), paymentsKey, false, nil, 5)
	for _, op := range ops {
		if strings.HasPrefix(op.Text, "Registered") {
			t.Fatalf("unexpected operation %q", op.Text)
		}
	}
}

func TestRegister_AWSValidationShownVerbatim(t *testing.T) {
	h := newHarness(t)
	draft := draftFromDeployed(t, h, "payments-api:1.18.3")
	message := "Container.image repository 'x' is invalid"
	h.fake.FailNext("RegisterTaskDefinition", &ecstypes.InvalidParameterException{Message: aws.String(message)})

	_, err := h.svc.Register(t.Context(), h.pavel, paymentsKey, deploy.RegisterInput{BaseRevision: 42, Definition: draft})
	var awsErr *deploy.AWSError
	if !errors.As(err, &awsErr) || !awsErr.Validation || awsErr.Message != message {
		t.Fatalf("err = %#v, want AWSError{Validation: true, Message: %q}", err, message)
	}
}
