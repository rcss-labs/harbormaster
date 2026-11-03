package deploy_test

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	astypes "github.com/aws/aws-sdk-go-v2/service/applicationautoscaling/types"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/deploy"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

var reportWorkerKey = model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: homelabAccountID, Region: homelabRegion, Cluster: "background-jobs"}, Service: "report-worker"}

func ptr[T any](v T) *T { return &v }

// awsWrites are the operations a change set may use to change AWS.
var awsWrites = []string{"RegisterTaskDefinition", "UpdateService", "RegisterScalableTarget", "DeregisterScalableTarget",
	"PutScalingPolicy", "DeleteScalingPolicy", "PutScheduledAction", "DeleteScheduledAction"}

func writeCalls(h *harness) map[string]int {
	calls := h.fake.Calls()
	out := map[string]int{}
	for _, name := range awsWrites {
		out[name] = calls[name]
	}
	return out
}

func assertNoNewWrites(t *testing.T, h *harness, before map[string]int) {
	t.Helper()
	after := writeCalls(h)
	for name, n := range after {
		if n != before[name] {
			t.Errorf("%s was called %d times, want no AWS write", name, n-before[name])
		}
	}
}

// baseChangeSet returns an empty change set carrying the live base revision and settings version.
func baseChangeSet(t *testing.T, h *harness, key model.ServiceKey) deploy.ChangeSet {
	t.Helper()
	settings, err := h.svc.DeploySettings(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	return deploy.ChangeSet{BaseRevision: settings.DeployedRevision, BaseVersion: settings.Version}
}

// subjectWith is a non-admin who holds exactly actions (plus View) on the service's tag.
func subjectWith(t *testing.T, h *harness, key model.ServiceKey, actions ...authz.Action) authz.Subject {
	t.Helper()
	row, err := h.store.GetServiceView(t.Context(), key, store.Visibility{Admin: true})
	if err != nil {
		t.Fatal(err)
	}
	set := authz.ActionSet(authz.View)
	for _, a := range actions {
		set |= authz.ActionSet(a)
	}
	return authz.Subject{UserID: h.pavel.UserID, Username: "limited", Perms: map[authz.TagID]authz.ActionSet{row.EffectiveTagID: set}}
}

func requireInvalid(t *testing.T, h *harness, key model.ServiceKey, cs deploy.ChangeSet, want string) {
	t.Helper()
	before := writeCalls(h)
	_, err := h.svc.ApplyChangeSet(t.Context(), h.pavel, key, cs)
	var validation *deploy.ValidationError
	if !errors.As(err, &validation) || !strings.Contains(validation.Message, want) {
		t.Fatalf("err = %v, want a ValidationError containing %q", err, want)
	}
	assertNoNewWrites(t, h, before)
}

func cpuPolicy(name string) deploy.PolicyChange {
	return deploy.PolicyChange{Name: name, Metric: "cpu", TargetValue: 50, ScaleInCooldown: 300, ScaleOutCooldown: 60}
}

func TestChangeSet_RejectsEmpty(t *testing.T) {
	h := newHarness(t)
	requireInvalid(t, h, paymentsKey, baseChangeSet(t, h, paymentsKey), "Nothing to change")
	cs := baseChangeSet(t, h, paymentsKey)
	cs.Service = &deploy.ServiceChange{}
	requireInvalid(t, h, paymentsKey, cs, "service")
}

func TestChangeSet_RejectsRollbackWithoutBreaker(t *testing.T) {
	h := newHarness(t)
	cs := baseChangeSet(t, h, paymentsKey)
	cs.Service = &deploy.ServiceChange{DeploymentConfiguration: &deploy.DeploymentConfigChange{
		CircuitBreakerEnable: false, CircuitBreakerRollback: true, MinimumHealthyPercent: 100, MaximumPercent: 200}}
	requireInvalid(t, h, paymentsKey, cs, "rollback")
}

func TestChangeSet_RejectsOutOfRangeNumbers(t *testing.T) {
	h := newHarness(t)
	for name, change := range map[string]deploy.ServiceChange{
		"desired above max": {DesiredCount: ptr(51)},
		"desired negative":  {DesiredCount: ptr(-1)},
		"min healthy 101":   {DeploymentConfiguration: &deploy.DeploymentConfigChange{MinimumHealthyPercent: 101, MaximumPercent: 200}},
		"max percent 99":    {DeploymentConfiguration: &deploy.DeploymentConfigChange{MinimumHealthyPercent: 100, MaximumPercent: 99}},
		"max percent 201":   {DeploymentConfiguration: &deploy.DeploymentConfigChange{MinimumHealthyPercent: 100, MaximumPercent: 201}},
		"grace negative":    {HealthCheckGracePeriodSeconds: ptr(-5)},
	} {
		t.Run(name, func(t *testing.T) {
			cs := baseChangeSet(t, h, paymentsKey)
			change := change
			cs.Service = &change
			requireInvalid(t, h, paymentsKey, cs, "")
		})
	}
	longReason := baseChangeSet(t, h, paymentsKey)
	longReason.Service = &deploy.ServiceChange{DesiredCount: ptr(4)}
	longReason.Reason = strings.Repeat("x", 501)
	requireInvalid(t, h, paymentsKey, longReason, "500")
}

func TestChangeSet_GraceNeedsLoadBalancer(t *testing.T) {
	h := newHarness(t)
	cs := baseChangeSet(t, h, reportWorkerKey)
	cs.Service = &deploy.ServiceChange{HealthCheckGracePeriodSeconds: ptr(30)}
	requireInvalid(t, h, reportWorkerKey, cs, "load balancer")
}

func TestChangeSet_DesiredOutsideNewScalingRange(t *testing.T) {
	h := newHarness(t)
	cs := baseChangeSet(t, h, paymentsKey)
	cs.Service = &deploy.ServiceChange{DesiredCount: ptr(5)}
	cs.Scaling = &deploy.ScalingChange{Target: &deploy.ScalingTargetChange{Min: 1, Max: 4}}
	requireInvalid(t, h, paymentsKey, cs, "Auto scaling would move 5 back into 1–4")

	// Without a scalable target there is no range to violate.
	plain := baseChangeSet(t, h, reportWorkerKey)
	plain.Service = &deploy.ServiceChange{DesiredCount: ptr(5)}
	if _, err := h.svc.ApplyChangeSet(t.Context(), h.pavel, reportWorkerKey, plain); err != nil {
		t.Fatal(err)
	}
}

func TestChangeSet_ScheduledActionBadScheduleOrTimezone(t *testing.T) {
	h := newHarness(t)
	for name, action := range map[string]deploy.ScheduledActionChange{
		"unknown form":      {Name: "a", Schedule: "daily", MinCapacity: ptr(2)},
		"cron five fields":  {Name: "a", Schedule: "cron(0 7 ? * MON-FRI)", MinCapacity: ptr(2)},
		"at without time":   {Name: "a", Schedule: "at(2030-01-01)", MinCapacity: ptr(2)},
		"rate zero":         {Name: "a", Schedule: "rate(0 minutes)", MinCapacity: ptr(2)},
		"unknown timezone":  {Name: "a", Schedule: "rate(5 minutes)", Timezone: "Mars/Phobos", MinCapacity: ptr(2)},
		"no capacity":       {Name: "a", Schedule: "rate(5 minutes)"},
		"above max desired": {Name: "a", Schedule: "rate(5 minutes)", MaxCapacity: ptr(51)},
		"negative capacity": {Name: "a", Schedule: "rate(5 minutes)", MinCapacity: ptr(-1)},
		"min above max":     {Name: "a", Schedule: "rate(5 minutes)", MinCapacity: ptr(5), MaxCapacity: ptr(3)},
		"bad name":          {Name: "bad name!", Schedule: "rate(5 minutes)", MinCapacity: ptr(2)},
	} {
		t.Run(name, func(t *testing.T) {
			cs := baseChangeSet(t, h, paymentsKey)
			cs.Scaling = &deploy.ScalingChange{PutScheduledActions: []deploy.ScheduledActionChange{action}}
			requireInvalid(t, h, paymentsKey, cs, "")
		})
	}
}

func TestChangeSet_RateAcceptsSingularUnit(t *testing.T) {
	h := newHarness(t)
	valid := []string{"rate(1 hour)", "rate(1 minute)", "rate(1 day)", "rate(5 minutes)", "rate(2 hours)", "rate(3 days)",
		"cron(0 7 ? * MON-FRI *)", "at(2030-01-01T10:00:00)"}
	cs := baseChangeSet(t, h, paymentsKey)
	cs.Scaling = &deploy.ScalingChange{}
	for i, schedule := range valid {
		cs.Scaling.PutScheduledActions = append(cs.Scaling.PutScheduledActions, deploy.ScheduledActionChange{
			Name: "s" + string(rune('a'+i)), Schedule: schedule, Timezone: "Asia/Jerusalem", MinCapacity: ptr(2)})
	}
	if _, err := h.svc.ApplyChangeSet(t.Context(), h.pavel, paymentsKey, cs); err != nil {
		t.Fatalf("valid schedules refused: %v", err)
	}
	for _, schedule := range []string{"rate(1 hours)", "rate(5 minute)", "rate(1 week)", "rate(-1 day)"} {
		next := baseChangeSet(t, h, paymentsKey)
		next.Scaling = &deploy.ScalingChange{PutScheduledActions: []deploy.ScheduledActionChange{{Name: "x", Schedule: schedule, MinCapacity: ptr(2)}}}
		requireInvalid(t, h, paymentsKey, next, "schedule")
	}
}

func TestChangeSet_ALBPolicyNeedsOwnTargetGroup(t *testing.T) {
	h := newHarness(t)
	cs := baseChangeSet(t, h, paymentsKey)
	cs.Scaling = &deploy.ScalingChange{PutPolicies: []deploy.PolicyChange{{Name: "alb", Metric: "alb", TargetValue: 100,
		TargetGroupARN: "arn:aws:elasticloadbalancing:us-east-1:482109375512:targetgroup/someone-elses/abc"}}}
	requireInvalid(t, h, paymentsKey, cs, "target group")

	cs.Scaling.PutPolicies[0].TargetGroupARN = ""
	requireInvalid(t, h, paymentsKey, cs, "target group")
}

func TestChangeSet_ALBPolicyWithOwnTargetGroupGetsResourceLabel(t *testing.T) {
	h := newHarness(t)
	settings, err := h.svc.DeploySettings(t.Context(), paymentsKey)
	if err != nil || len(settings.TargetGroups) != 1 {
		t.Fatalf("settings = %+v, err %v", settings.TargetGroups, err)
	}
	cs := baseChangeSet(t, h, paymentsKey)
	cs.Scaling = &deploy.ScalingChange{PutPolicies: []deploy.PolicyChange{{Name: "alb", Metric: "alb", TargetValue: 100,
		TargetGroupARN: settings.TargetGroups[0].ARN}}}
	if _, err := h.svc.ApplyChangeSet(t.Context(), h.pavel, paymentsKey, cs); err != nil {
		t.Fatal(err)
	}
	after, err := h.svc.DeploySettings(t.Context(), paymentsKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, policy := range after.Scaling.Policies {
		if policy.Name == "alb" && policy.Metric == "alb" && policy.ResourceLabel == settings.TargetGroups[0].ResourceLabel {
			return
		}
	}
	t.Fatalf("policies = %+v, want an alb policy labelled %s", after.Scaling.Policies, settings.TargetGroups[0].ResourceLabel)
}

func TestChangeSet_PolicyWithoutTarget(t *testing.T) {
	h := newHarness(t)
	cs := baseChangeSet(t, h, reportWorkerKey)
	cs.Scaling = &deploy.ScalingChange{PutPolicies: []deploy.PolicyChange{cpuPolicy("cpu")}}
	requireInvalid(t, h, reportWorkerKey, cs, "scalable target")

	// Adding the target in the same change set makes the policy valid.
	cs.Scaling.Target = &deploy.ScalingTargetChange{Min: 1, Max: 3}
	if _, err := h.svc.ApplyChangeSet(t.Context(), h.pavel, reportWorkerKey, cs); err != nil {
		t.Fatal(err)
	}
}

func TestChangeSet_DeletesMustNameExistingItems(t *testing.T) {
	h := newHarness(t)
	cs := baseChangeSet(t, h, paymentsKey)
	cs.Scaling = &deploy.ScalingChange{DeletePolicies: []string{"no-such-policy"}}
	requireInvalid(t, h, paymentsKey, cs, "no-such-policy")
	cs.Scaling = &deploy.ScalingChange{DeleteScheduledActions: []string{"no-such-action"}}
	requireInvalid(t, h, paymentsKey, cs, "no-such-action")
}

func TestChangeSet_RevisionNotInFamily(t *testing.T) {
	h := newHarness(t)
	cs := baseChangeSet(t, h, paymentsKey)
	cs.TaskDefinition = &deploy.TaskDefinitionChange{Revision: 999}
	requireInvalid(t, h, paymentsKey, cs, "999")
	cs.TaskDefinition = &deploy.TaskDefinitionChange{Revision: 42}
	requireInvalid(t, h, paymentsKey, cs, "already deployed")
}

func setController(h *harness, key model.ServiceKey, controller ecstypes.DeploymentControllerType) {
	h.fake.Estate.Mu.Lock()
	defer h.fake.Estate.Mu.Unlock()
	for i := range h.fake.Estate.Services {
		s := &h.fake.Estate.Services[i]
		if aws.ToString(s.Service.ServiceName) == key.Service {
			s.Service.DeploymentController = &ecstypes.DeploymentController{Type: controller}
		}
	}
}

func TestChangeSet_UnsupportedControllerRefusesDeploySections(t *testing.T) {
	h := newHarness(t)
	cs := baseChangeSet(t, h, paymentsKey)
	setController(h, paymentsKey, ecstypes.DeploymentControllerTypeCodeDeploy)

	before := writeCalls(h)
	cs.Service = &deploy.ServiceChange{EnableExecuteCommand: ptr(false)}
	var unsupported *deploy.UnsupportedControllerError
	if _, err := h.svc.ApplyChangeSet(t.Context(), h.pavel, paymentsKey, cs); !errors.As(err, &unsupported) {
		t.Fatalf("err = %v, want UnsupportedControllerError", err)
	}
	assertNoNewWrites(t, h, before)

	// Desired count and scaling do not depend on the controller.
	cs.Service = &deploy.ServiceChange{DesiredCount: ptr(4)}
	if _, err := h.svc.ApplyChangeSet(t.Context(), h.pavel, paymentsKey, cs); err != nil {
		t.Fatalf("desired count on an unsupported controller: %v", err)
	}
}

func TestChangeSet_RolloutRunningAllowsScalingOnly(t *testing.T) {
	h := newHarness(t)
	cs := baseChangeSet(t, h, notificationsKey)

	before := writeCalls(h)
	exec := cs
	exec.Service = &deploy.ServiceChange{EnableExecuteCommand: ptr(false)}
	if _, err := h.svc.ApplyChangeSet(t.Context(), h.pavel, notificationsKey, exec); !errors.Is(err, deploy.ErrRolloutRunning) {
		t.Fatalf("exec toggle err = %v, want ErrRolloutRunning", err)
	}
	assertNoNewWrites(t, h, before)

	cs.Service = &deploy.ServiceChange{DesiredCount: ptr(3)}
	cs.Scaling = &deploy.ScalingChange{Target: &deploy.ScalingTargetChange{Min: 1, Max: 4}}
	result, err := h.svc.ApplyChangeSet(t.Context(), h.pavel, notificationsKey, cs)
	if err != nil || result.Status != "applied" {
		t.Fatalf("result = %+v, err %v, want applied", result, err)
	}
}

func TestChangeSet_StaleBaseRevision409(t *testing.T) {
	h := newHarness(t)
	cs := baseChangeSet(t, h, paymentsKey)
	cs.BaseRevision = 41
	cs.TaskDefinition = &deploy.TaskDefinitionChange{Definition: draftFromDeployed(t, h, "payments-api:1.18.3")}
	cs.Service = &deploy.ServiceChange{DesiredCount: ptr(4)}
	before := writeCalls(h)
	_, err := h.svc.ApplyChangeSet(t.Context(), h.pavel, paymentsKey, cs)
	var stale *deploy.StaleBaseError
	if !errors.As(err, &stale) || stale.BaseRevision != 41 || stale.DeployedRevision != 42 {
		t.Fatalf("err = %v, want StaleBaseError{41, 42}", err)
	}
	assertNoNewWrites(t, h, before)
}

func TestChangeSet_StaleVersionReturnsFreshSettings(t *testing.T) {
	h := newHarness(t)
	cs := baseChangeSet(t, h, paymentsKey)
	cs.Service = &deploy.ServiceChange{EnableExecuteCommand: ptr(false)}
	// Someone else changes the desired count after the client read the settings.
	if _, err := h.svc.SetDesiredCount(t.Context(), h.pavel, paymentsKey, 5); err != nil {
		t.Fatal(err)
	}
	before := writeCalls(h)
	_, err := h.svc.ApplyChangeSet(t.Context(), h.pavel, paymentsKey, cs)
	var stale *deploy.StaleVersionError
	if !errors.As(err, &stale) {
		t.Fatalf("err = %v, want StaleVersionError", err)
	}
	if stale.Current.DesiredCount != 5 || stale.Current.Version == cs.BaseVersion || stale.Current.Version == "" {
		t.Fatalf("Current = %+v, want fresh settings with desired 5", stale.Current)
	}
	assertNoNewWrites(t, h, before)
}

func TestChangeSet_RefusesSubjectWithoutRequiredAction(t *testing.T) {
	h := newHarness(t)
	cs := baseChangeSet(t, h, paymentsKey)
	cs.Service = &deploy.ServiceChange{DesiredCount: ptr(4)}
	before := writeCalls(h)
	_, err := h.svc.ApplyChangeSet(t.Context(), subjectWith(t, h, paymentsKey, authz.Edit), paymentsKey, cs)
	var forbidden *deploy.ForbiddenError
	if !errors.As(err, &forbidden) || forbidden.Action != authz.Operate {
		t.Fatalf("err = %v, want ForbiddenError{Operate}", err)
	}
	cs.Service = &deploy.ServiceChange{EnableExecuteCommand: ptr(false)}
	_, err = h.svc.ApplyChangeSet(t.Context(), subjectWith(t, h, paymentsKey, authz.Operate), paymentsKey, cs)
	if !errors.As(err, &forbidden) || forbidden.Action != authz.Deploy {
		t.Fatalf("err = %v, want ForbiddenError{Deploy}", err)
	}
	assertNoNewWrites(t, h, before)
}

func TestRequiredActions_Matrix(t *testing.T) {
	definition := &deploy.TaskDefinitionChange{Definition: deploy.Definition{"family": "x"}}
	cases := []struct {
		name string
		cs   deploy.ChangeSet
		want []authz.Action
	}{
		{"empty", deploy.ChangeSet{}, nil},
		{"definition only", deploy.ChangeSet{TaskDefinition: definition}, []authz.Action{authz.Edit}},
		{"existing revision", deploy.ChangeSet{TaskDefinition: &deploy.TaskDefinitionChange{Revision: 3}}, []authz.Action{authz.Deploy}},
		{"definition with desired", deploy.ChangeSet{TaskDefinition: definition, Service: &deploy.ServiceChange{DesiredCount: ptr(2)}},
			[]authz.Action{authz.Edit, authz.Deploy, authz.Operate}},
		{"desired count", deploy.ChangeSet{Service: &deploy.ServiceChange{DesiredCount: ptr(2)}}, []authz.Action{authz.Operate}},
		{"force new deployment", deploy.ChangeSet{Service: &deploy.ServiceChange{ForceNewDeployment: true}}, []authz.Action{authz.Operate}},
		{"exec", deploy.ChangeSet{Service: &deploy.ServiceChange{EnableExecuteCommand: ptr(true)}}, []authz.Action{authz.Deploy}},
		{"deployment configuration", deploy.ChangeSet{Service: &deploy.ServiceChange{DeploymentConfiguration: &deploy.DeploymentConfigChange{}}}, []authz.Action{authz.Deploy}},
		{"grace period", deploy.ChangeSet{Service: &deploy.ServiceChange{HealthCheckGracePeriodSeconds: ptr(5)}}, []authz.Action{authz.Deploy}},
		{"desired and exec", deploy.ChangeSet{Service: &deploy.ServiceChange{DesiredCount: ptr(2), EnableExecuteCommand: ptr(true)}}, []authz.Action{authz.Deploy, authz.Operate}},
		{"scaling", deploy.ChangeSet{Scaling: &deploy.ScalingChange{RemoveTarget: true}}, []authz.Action{authz.Operate}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := deploy.RequiredActions(tc.cs)
			if len(got) != len(tc.want) {
				t.Fatalf("RequiredActions = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("RequiredActions = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestChangeSet_RegisterDeployAndScaleHappyPath(t *testing.T) {
	h := newHarness(t)
	cs := baseChangeSet(t, h, paymentsKey)
	cs.Reason = "raise capacity for launch"
	cs.TaskDefinition = &deploy.TaskDefinitionChange{Definition: draftFromDeployed(t, h, "payments-api:1.18.3")}
	cs.Service = &deploy.ServiceChange{DesiredCount: ptr(4)}
	cs.Scaling = &deploy.ScalingChange{
		Target:         &deploy.ScalingTargetChange{Min: 3, Max: 6},
		PutPolicies:    []deploy.PolicyChange{{Name: "memory", Metric: "memory", TargetValue: 70, ScaleInCooldown: 300, ScaleOutCooldown: 60}},
		DeletePolicies: []string{"cpu-target"},
	}

	got, err := h.svc.ApplyChangeSet(t.Context(), h.pavel, paymentsKey, cs)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "applied" || got.RegisteredRevision != 43 || got.ChangeSetID == "" || got.Warning != "" {
		t.Fatalf("result = %+v", got)
	}
	if len(got.Steps) != 3 {
		t.Fatalf("steps = %+v, want register, service, scaling", got.Steps)
	}
	for i, name := range []string{"register", "service", "scaling"} {
		if got.Steps[i].Name != name || got.Steps[i].Status != "done" {
			t.Fatalf("steps = %+v", got.Steps)
		}
	}
	if got.Rollout == nil || got.Kind != "deploy" || got.Rollout.FromRevision != 42 || got.Rollout.ToRevision != 43 || got.Rollout.DeploymentID == "" {
		t.Fatalf("rollout = %+v kind %q", got.Rollout, got.Kind)
	}
	if desired := h.primaryDesired(t, paymentsKey); desired != 4 {
		t.Fatalf("desired = %d, want 4", desired)
	}
	if calls := h.fake.Calls()["UpdateService"]; calls != 1 {
		t.Fatalf("UpdateService calls = %d, want exactly one", calls)
	}

	// Every operations line of the run shares the change set id and reason; history has both too.
	ops, err := h.store.ListOperations(t.Context(), paymentsKey, false, nil, 20)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, op := range ops {
		if op.Details["changeset_id"] != got.ChangeSetID {
			continue
		}
		if op.Details["reason"] != "raise capacity for launch" {
			t.Errorf("operation %q reason = %v", op.Text, op.Details["reason"])
		}
		seen[op.Details["op"].(string)] = true
	}
	for _, op := range []string{"task_definition", "deploy", "scale", "scaling"} {
		if !seen[op] {
			t.Errorf("no operation line with op %q for the change set; saw %v", op, seen)
		}
	}
	var reason, changeSet string
	if err := h.store.Pool.QueryRow(t.Context(),
		`select coalesce(reason, ''), coalesce(changeset_id, '') from deployment_history where deployment_id = $1`,
		got.Rollout.DeploymentID).Scan(&reason, &changeSet); err != nil {
		t.Fatal(err)
	}
	if reason != "raise capacity for launch" || changeSet != got.ChangeSetID {
		t.Fatalf("deployment_history reason/changeset = %q / %q", reason, changeSet)
	}
}

func TestChangeSet_ExecToggleWithoutRevisionForcesNewDeployment(t *testing.T) {
	h := newHarness(t)
	old := aws.ToString(h.primary(t, paymentsKey).Id)
	cs := baseChangeSet(t, h, paymentsKey)
	cs.Service = &deploy.ServiceChange{EnableExecuteCommand: ptr(false)}

	got, err := h.svc.ApplyChangeSet(t.Context(), h.pavel, paymentsKey, cs)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "applied" || got.Kind != "restart" || got.Rollout == nil || got.Rollout.DeploymentID == old || got.Rollout.ToRevision != 42 {
		t.Fatalf("result = %+v rollout %+v", got, got.Rollout)
	}
	settings, err := h.svc.DeploySettings(t.Context(), paymentsKey)
	if err != nil || settings.EnableExecuteCommand {
		t.Fatalf("exec still enabled: %+v err %v", settings.EnableExecuteCommand, err)
	}
	if kind, _ := deploymentKind(t, h, paymentsKey, got.Rollout.DeploymentID); kind != "restart" {
		t.Fatalf("deployment kind = %q", kind)
	}
}

func TestChangeSet_RegisterOnlyWithoutDeploy(t *testing.T) {
	h := newHarness(t)
	cs := baseChangeSet(t, h, paymentsKey)
	cs.TaskDefinition = &deploy.TaskDefinitionChange{Definition: draftFromDeployed(t, h, "payments-api:1.18.3")}

	got, err := h.svc.ApplyChangeSet(t.Context(), subjectWith(t, h, paymentsKey, authz.Edit), paymentsKey, cs)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "registered_only" || got.RegisteredRevision != 43 || got.Rollout != nil || len(got.Steps) != 1 {
		t.Fatalf("result = %+v", got)
	}
	if calls := h.fake.Calls()["UpdateService"]; calls != 0 {
		t.Fatalf("UpdateService calls = %d, want 0", calls)
	}
}

func TestChangeSet_UpdateServiceFailureAfterRegisterIsPartial(t *testing.T) {
	h := newHarness(t)
	cs := baseChangeSet(t, h, paymentsKey)
	cs.TaskDefinition = &deploy.TaskDefinitionChange{Definition: draftFromDeployed(t, h, "payments-api:1.18.3")}
	cs.Scaling = &deploy.ScalingChange{Target: &deploy.ScalingTargetChange{Min: 2, Max: 8}}
	h.fake.FailNext("UpdateService", &ecstypes.ServerException{Message: aws.String("boom")})

	got, err := h.svc.ApplyChangeSet(t.Context(), h.pavel, paymentsKey, cs)
	if err != nil {
		t.Fatalf("err = %v, want a partial result after the register succeeded", err)
	}
	if got.Status != "partial" || got.RegisteredRevision != 43 || got.Rollout != nil || len(got.Steps) != 3 {
		t.Fatalf("result = %+v", got)
	}
	if got.Steps[0].Status != "done" || got.Steps[1].Status != "failed" || got.Steps[1].Message == "" || got.Steps[2].Status != "skipped" {
		t.Fatalf("steps = %+v", got.Steps)
	}
	if h.fake.Calls()["RegisterScalableTarget"] != 0 {
		t.Fatal("scaling ran after the service step failed")
	}
}

func TestChangeSet_RetryWithRevisionDoesNotRegisterAgain(t *testing.T) {
	h := newHarness(t)
	cs := baseChangeSet(t, h, paymentsKey)
	cs.TaskDefinition = &deploy.TaskDefinitionChange{Definition: draftFromDeployed(t, h, "payments-api:1.18.3")}
	h.fake.FailNext("UpdateService", &ecstypes.ServerException{Message: aws.String("boom")})
	first, err := h.svc.ApplyChangeSet(t.Context(), h.pavel, paymentsKey, cs)
	if err != nil || first.Status != "partial" {
		t.Fatalf("first = %+v, err %v", first, err)
	}

	retry := baseChangeSet(t, h, paymentsKey)
	retry.TaskDefinition = &deploy.TaskDefinitionChange{Revision: first.RegisteredRevision}
	got, err := h.svc.ApplyChangeSet(t.Context(), h.pavel, paymentsKey, retry)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "applied" || got.Rollout == nil || got.Rollout.ToRevision != 43 || got.RegisteredRevision != 0 {
		t.Fatalf("retry = %+v", got)
	}
	if calls := h.fake.Calls()["RegisterTaskDefinition"]; calls != 1 {
		t.Fatalf("RegisterTaskDefinition calls = %d, want 1", calls)
	}
}

func TestChangeSet_ScalingFailureAfterServiceIsPartial(t *testing.T) {
	h := newHarness(t)
	cs := baseChangeSet(t, h, paymentsKey)
	cs.Service = &deploy.ServiceChange{DesiredCount: ptr(4)}
	cs.Scaling = &deploy.ScalingChange{PutPolicies: []deploy.PolicyChange{cpuPolicy("cpu-extra")}}
	h.fake.FailNext("PutScalingPolicy", &astypes.ValidationException{Message: aws.String("nope")})

	got, err := h.svc.ApplyChangeSet(t.Context(), h.pavel, paymentsKey, cs)
	if err != nil {
		t.Fatalf("err = %v, want partial", err)
	}
	if got.Status != "partial" || len(got.Steps) != 2 || got.Steps[0].Status != "done" || got.Steps[1].Status != "failed" ||
		!strings.Contains(got.Steps[1].Message, "nope") {
		t.Fatalf("result = %+v", got)
	}
}

func TestChangeSet_FailureBeforeFirstWriteIsError(t *testing.T) {
	h := newHarness(t)
	cs := baseChangeSet(t, h, reportWorkerKey)
	cs.Scaling = &deploy.ScalingChange{Target: &deploy.ScalingTargetChange{Min: 1, Max: 3}}
	h.fake.FailNext("RegisterScalableTarget", &astypes.ValidationException{Message: aws.String("rejected")})
	if _, err := h.svc.ApplyChangeSet(t.Context(), h.pavel, reportWorkerKey, cs); err == nil {
		t.Fatal("err = nil, want the AWS error when nothing was written")
	}
}

func TestChangeSet_ScalingWriteThroughUpdatesStore(t *testing.T) {
	h := newHarness(t)
	cs := baseChangeSet(t, h, reportWorkerKey)
	cs.Scaling = &deploy.ScalingChange{
		Target:              &deploy.ScalingTargetChange{Min: 1, Max: 3},
		PutPolicies:         []deploy.PolicyChange{cpuPolicy("cpu")},
		PutScheduledActions: []deploy.ScheduledActionChange{{Name: "night", Schedule: "cron(0 20 ? * * *)", Timezone: "UTC", MinCapacity: ptr(1)}},
	}
	if _, err := h.svc.ApplyChangeSet(t.Context(), h.pavel, reportWorkerKey, cs); err != nil {
		t.Fatal(err)
	}
	stored, err := h.store.ScalingForService(t.Context(), reportWorkerKey)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.HasTarget || stored.Min != 1 || stored.Max != 3 || len(stored.Policies) != 1 || len(stored.Scheduled) != 1 {
		t.Fatalf("stored scaling = %+v, want the new target, policy and action without waiting for a poll", stored)
	}

	remove := baseChangeSet(t, h, reportWorkerKey)
	remove.Scaling = &deploy.ScalingChange{RemoveTarget: true}
	if _, err := h.svc.ApplyChangeSet(t.Context(), h.pavel, reportWorkerKey, remove); err != nil {
		t.Fatal(err)
	}
	stored, err = h.store.ScalingForService(t.Context(), reportWorkerKey)
	if err != nil || stored.HasTarget || len(stored.Policies) != 0 {
		t.Fatalf("stored after removal = %+v, err %v", stored, err)
	}
}

func TestChangeSet_RecordingFailureIsWarning(t *testing.T) {
	h := newHarness(t)
	h.svc.InTx = failAtCommit(h.store)
	cs := baseChangeSet(t, h, paymentsKey)
	cs.Service = &deploy.ServiceChange{EnableExecuteCommand: ptr(false)}

	got, err := h.svc.ApplyChangeSet(t.Context(), h.pavel, paymentsKey, cs)
	if err != nil {
		t.Fatalf("err = %v, want success with a warning (a retry would hit the rollout gate)", err)
	}
	if got.Status != "applied" || !strings.Contains(got.Warning, "could not record") || got.Rollout == nil || got.Rollout.DeploymentID == "" {
		t.Fatalf("result = %+v", got)
	}
}

func TestDeploySettings_VersionChangesWhenDesiredChanges(t *testing.T) {
	h := newHarness(t)
	first, err := h.svc.DeploySettings(t.Context(), paymentsKey)
	if err != nil {
		t.Fatal(err)
	}
	if first.Version == "" || first.DeployedRevision != 42 || first.Family != "payments-api" || first.DesiredCount != 3 || first.MaxDesired != 50 {
		t.Fatalf("settings = %+v", first)
	}

	// The running count is observation, not configuration: it must not change the version.
	h.fake.Estate.Mu.Lock()
	for i := range h.fake.Estate.Services {
		if aws.ToString(h.fake.Estate.Services[i].Service.ServiceName) == "payments-api" {
			h.fake.Estate.Services[i].Service.RunningCount = 1
		}
	}
	h.fake.Estate.Mu.Unlock()
	running, err := h.svc.DeploySettings(t.Context(), paymentsKey)
	if err != nil {
		t.Fatal(err)
	}
	if running.RunningCount != 1 || running.Version != first.Version {
		t.Fatalf("running count change: running %d version %s vs %s", running.RunningCount, running.Version, first.Version)
	}

	if _, err := h.svc.SetDesiredCount(t.Context(), h.pavel, paymentsKey, 4); err != nil {
		t.Fatal(err)
	}
	second, err := h.svc.DeploySettings(t.Context(), paymentsKey)
	if err != nil {
		t.Fatal(err)
	}
	if second.Version == first.Version || second.DesiredCount != 4 {
		t.Fatalf("version did not change with desired count: %s -> %s", first.Version, second.Version)
	}
}

func TestDeploySettings_TargetGroupsResourceLabel(t *testing.T) {
	h := newHarness(t)
	settings, err := h.svc.DeploySettings(t.Context(), paymentsKey)
	if err != nil {
		t.Fatal(err)
	}
	if !settings.HasLoadBalancer || len(settings.TargetGroups) != 1 {
		t.Fatalf("target groups = %+v", settings.TargetGroups)
	}
	label := regexp.MustCompile(`^app/core-services-alb/[0-9a-f]+/targetgroup/payments-api-tg/[0-9a-f]+$`)
	tg := settings.TargetGroups[0]
	if !label.MatchString(tg.ResourceLabel) || tg.Name != "payments-api-tg" || !strings.Contains(tg.ARN, ":targetgroup/payments-api-tg/") {
		t.Fatalf("target group = %+v", tg)
	}
	if !settings.Scaling.HasTarget || settings.Scaling.Min != 2 || settings.Scaling.Max != 6 || len(settings.Scaling.Policies) != 1 ||
		settings.Scaling.Policies[0].Metric != "cpu" || !settings.Scaling.Policies[0].Editable || len(settings.Scaling.ScheduledActions) != 2 {
		t.Fatalf("scaling = %+v", settings.Scaling)
	}
	if !settings.EnableExecuteCommand || settings.DeploymentConfiguration.MaximumPercent != 200 || !settings.DeploymentConfiguration.CircuitBreakerRollback {
		t.Fatalf("service settings = %+v", settings)
	}
}

// primaryDesired is the fake service's desired count.
func (h *harness) primaryDesired(t *testing.T, key model.ServiceKey) int {
	t.Helper()
	h.fake.Estate.Mu.Lock()
	defer h.fake.Estate.Mu.Unlock()
	for _, s := range h.fake.Estate.Services {
		if s.ClusterName == key.Cluster && aws.ToString(s.Service.ServiceName) == key.Service {
			return int(s.Service.DesiredCount)
		}
	}
	t.Fatalf("no service %v", key)
	return 0
}

func TestChangeSet_PartialReturnsFreshSettingsVersionForRetry(t *testing.T) {
	h := newHarness(t)
	cs := baseChangeSet(t, h, paymentsKey)
	cs.Service = &deploy.ServiceChange{DesiredCount: ptr(4)}
	cs.Scaling = &deploy.ScalingChange{PutPolicies: []deploy.PolicyChange{cpuPolicy("cpu-extra")}}
	h.fake.FailNext("PutScalingPolicy", &astypes.ValidationException{Message: aws.String("nope")})

	first, err := h.svc.ApplyChangeSet(t.Context(), h.pavel, paymentsKey, cs)
	if err != nil || first.Status != "partial" {
		t.Fatalf("first = %+v, err %v", first, err)
	}
	if first.SettingsVersion == "" || first.SettingsVersion == cs.BaseVersion {
		t.Fatalf("SettingsVersion = %q, want the fresh version (old %q)", first.SettingsVersion, cs.BaseVersion)
	}

	// Retry only the scaling section with the version the partial result handed back.
	retry := deploy.ChangeSet{BaseRevision: cs.BaseRevision, BaseVersion: first.SettingsVersion, Scaling: cs.Scaling}
	got, err := h.svc.ApplyChangeSet(t.Context(), h.pavel, paymentsKey, retry)
	if err != nil {
		t.Fatalf("retry err = %v", err)
	}
	if got.Status != "applied" || got.SettingsVersion == "" {
		t.Fatalf("retry = %+v", got)
	}
}

func TestChangeSet_DeploymentConfigurationAndGraceApplied(t *testing.T) {
	h := newHarness(t)
	before := h.primary(t, paymentsKey)
	cs := baseChangeSet(t, h, paymentsKey)
	cs.Service = &deploy.ServiceChange{
		DeploymentConfiguration:       &deploy.DeploymentConfigChange{CircuitBreakerEnable: true, CircuitBreakerRollback: true, MinimumHealthyPercent: 50, MaximumPercent: 150},
		HealthCheckGracePeriodSeconds: ptr(90),
	}
	got, err := h.svc.ApplyChangeSet(t.Context(), h.pavel, paymentsKey, cs)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "applied" || got.Rollout != nil || got.Kind != "" {
		t.Fatalf("result = %+v rollout %+v", got, got.Rollout)
	}
	settings, err := h.svc.DeploySettings(t.Context(), paymentsKey)
	if err != nil {
		t.Fatal(err)
	}
	config := settings.DeploymentConfiguration
	if !config.CircuitBreakerEnable || !config.CircuitBreakerRollback || config.MinimumHealthyPercent != 50 || config.MaximumPercent != 150 ||
		settings.HealthCheckGracePeriodSeconds == nil || *settings.HealthCheckGracePeriodSeconds != 90 {
		t.Fatalf("settings = %+v grace %v", config, settings.HealthCheckGracePeriodSeconds)
	}
	if after := h.primary(t, paymentsKey); aws.ToString(after.Id) != aws.ToString(before.Id) {
		t.Fatalf("a new deployment %s started", aws.ToString(after.Id))
	}
	ops, err := h.store.ListOperations(t.Context(), paymentsKey, false, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, op := range ops {
		found = found || op.Details["op"] == "rollout_settings"
	}
	if !found {
		t.Fatalf("no rollout_settings operations line in %+v", ops)
	}
}

// AWS allows a scheduled action to move the target outside its current range (max 20 at peak on a 1-4 target).
func TestChangeSet_ScheduledActionMayLeaveCurrentScalingRange(t *testing.T) {
	h := newHarness(t)
	settings, err := h.svc.DeploySettings(t.Context(), paymentsKey)
	if err != nil || !settings.Scaling.HasTarget {
		t.Fatalf("settings = %+v, err %v", settings.Scaling, err)
	}
	cs := baseChangeSet(t, h, paymentsKey)
	cs.Scaling = &deploy.ScalingChange{PutScheduledActions: []deploy.ScheduledActionChange{
		{Name: "peak", Schedule: "rate(5 minutes)", Timezone: "UTC", MinCapacity: ptr(settings.Scaling.Max + 5), MaxCapacity: ptr(settings.Scaling.Max + 10)},
		{Name: "quiet", Schedule: "rate(1 hour)", Timezone: "UTC", MinCapacity: ptr(0)},
	}}
	if _, err := h.svc.ApplyChangeSet(t.Context(), h.pavel, paymentsKey, cs); err != nil {
		t.Fatalf("scheduled action outside the current range refused: %v", err)
	}
}

func TestChangeSet_ScalingOnlyIgnoresStaleBaseRevision(t *testing.T) {
	h := newHarness(t)
	cs := baseChangeSet(t, h, paymentsKey)
	cs.BaseRevision = 41
	cs.Scaling = &deploy.ScalingChange{PutPolicies: []deploy.PolicyChange{cpuPolicy("cpu-extra")}}
	if _, err := h.svc.ApplyChangeSet(t.Context(), h.pavel, paymentsKey, cs); err != nil {
		t.Fatalf("scaling-only change set refused for a stale base revision: %v", err)
	}
}

func TestChangeSet_RetryAfterDeployDoneScalingFailedAppliesScalingOnly(t *testing.T) {
	h := newHarness(t)
	cs := baseChangeSet(t, h, paymentsKey)
	cs.TaskDefinition = &deploy.TaskDefinitionChange{Definition: draftFromDeployed(t, h, "payments-api:1.18.3")}
	cs.Scaling = &deploy.ScalingChange{PutPolicies: []deploy.PolicyChange{cpuPolicy("cpu-extra")}}
	h.fake.FailNext("PutScalingPolicy", &astypes.ValidationException{Message: aws.String("nope")})
	first, err := h.svc.ApplyChangeSet(t.Context(), h.pavel, paymentsKey, cs)
	if err != nil || first.Status != "partial" || first.RegisteredRevision != 43 {
		t.Fatalf("first = %+v, err %v", first, err)
	}

	// What the frontend sends: no task definition (the service step is done), the scaling section, a fresh version.
	retry := deploy.ChangeSet{BaseRevision: cs.BaseRevision, BaseVersion: first.SettingsVersion, Scaling: cs.Scaling}
	got, err := h.svc.ApplyChangeSet(t.Context(), h.pavel, paymentsKey, retry)
	if err != nil || got.Status != "applied" {
		t.Fatalf("retry = %+v, err %v", got, err)
	}
	calls := h.fake.Calls()
	if calls["RegisterTaskDefinition"] != 1 || calls["UpdateService"] != 1 {
		t.Fatalf("calls = register %d, update %d; want 1 each", calls["RegisterTaskDefinition"], calls["UpdateService"])
	}
}
