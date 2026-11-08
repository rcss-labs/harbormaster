package deploy_test

import (
	"errors"
	"testing"

	"github.com/aws/smithy-go"
)

var errScheduledUnsupported = &smithy.GenericAPIError{Code: "UnsupportedOperation", Message: "DescribeScheduledActions is not supported", Fault: smithy.FaultClient}

func TestSettingsFrom_PartialScheduledActions(t *testing.T) {
	h := newHarness(t)
	h.fake.FailAlways("DescribeScheduledActions", errScheduledUnsupported)

	first, err := h.svc.DeploySettings(t.Context(), paymentsKey)
	if err != nil {
		t.Fatalf("a failing scheduled actions read must not fail the settings: %v", err)
	}
	if len(first.Partial) != 1 || first.Partial[0].Part != "scheduled_actions" || !errors.Is(first.Partial[0].Err, errScheduledUnsupported) {
		t.Fatalf("partial = %+v, want one scheduled_actions entry carrying the AWS error", first.Partial)
	}
	if !first.Scaling.HasTarget || first.Scaling.Min != 2 || first.Scaling.Max != 6 || len(first.Scaling.Policies) != 1 {
		t.Fatalf("readable scaling parts missing: %+v", first.Scaling)
	}
	if len(first.Scaling.ScheduledActions) != 0 {
		t.Fatalf("unreadable scheduled actions must stay at the zero value: %+v", first.Scaling.ScheduledActions)
	}
	second, err := h.svc.DeploySettings(t.Context(), paymentsKey)
	if err != nil {
		t.Fatal(err)
	}
	if first.Version == "" || second.Version != first.Version {
		t.Fatalf("version not stable across calls: %q vs %q", first.Version, second.Version)
	}
}

func TestSettingsFrom_FatalErrorsStayFatal(t *testing.T) {
	h := newHarness(t)
	h.fake.FailAlways("DescribeServices", errScheduledUnsupported)
	if _, err := h.svc.DeploySettings(t.Context(), paymentsKey); err == nil {
		t.Fatal("DeploySettings succeeded although the live service could not be read")
	}
}
