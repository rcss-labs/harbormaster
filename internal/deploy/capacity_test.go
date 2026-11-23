package deploy_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/deploy"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/poller"
)

const clusterCopy = "Cluster core-services has no FARGATE_SPOT capacity provider. Associate it with the cluster (in Terraform: aws_ecs_cluster_capacity_providers), then this service qualifies."

// editService applies change to a service of the fake estate under its lock.
func editService(h *harness, key model.ServiceKey, change func(*ecstypes.Service)) {
	h.fake.Estate.Mu.Lock()
	defer h.fake.Estate.Mu.Unlock()
	for i := range h.fake.Estate.Services {
		s := &h.fake.Estate.Services[i]
		if s.ClusterName == key.Cluster && aws.ToString(s.Service.ServiceName) == key.Service {
			change(&s.Service)
		}
	}
}

// useStrategy moves a fake service from its launch type to a capacity provider strategy.
func useStrategy(h *harness, key model.ServiceKey, items ...ecstypes.CapacityProviderStrategyItem) {
	editService(h, key, func(s *ecstypes.Service) {
		s.LaunchType = ""
		s.CapacityProviderStrategy = items
	})
}

func repoll(t *testing.T, h *harness) {
	t.Helper()
	target := awsx.Target{AccountID: sandboxAccountID, Region: sandboxRegion}
	if err := poller.PollOnce(t.Context(), h.store, h.fake, poller.Config{MaxInFlight: 4}, target); err != nil {
		t.Fatal(err)
	}
}

// removeSpotFromCluster makes the stored cluster list only FARGATE, as a cluster never associated with Spot does.
func removeSpotFromCluster(t *testing.T, h *harness, cluster string) {
	t.Helper()
	h.fake.Estate.Mu.Lock()
	for i := range h.fake.Estate.Clusters {
		if aws.ToString(h.fake.Estate.Clusters[i].Cluster.ClusterName) == cluster {
			h.fake.Estate.Clusters[i].Cluster.CapacityProviders = []string{"FARGATE"}
		}
	}
	h.fake.Estate.Mu.Unlock()
	repoll(t, h)
}

func spotStrategy(base int) []deploy.CapacityItem {
	return []deploy.CapacityItem{{Provider: "FARGATE", Base: base, Weight: 0}, {Provider: "FARGATE_SPOT", Weight: 1}}
}

func TestDeploySettings_CapacityFromLaunchTypeAndStrategy(t *testing.T) {
	h := newHarness(t)

	got, err := h.svc.DeploySettings(t.Context(), paymentsKey)
	if err != nil {
		t.Fatal(err)
	}
	capacity := got.Capacity
	if capacity.LaunchType != "FARGATE" || len(capacity.Strategy) != 0 || !capacity.Editable || capacity.LockReason != "" {
		t.Fatalf("launch type service capacity = %+v, want FARGATE, no strategy, editable", capacity)
	}
	if len(capacity.ClusterProviders) != 2 || capacity.ClusterProviders[0] != "FARGATE" || capacity.ClusterProviders[1] != "FARGATE_SPOT" {
		t.Fatalf("cluster providers = %v, want the stored cluster's two providers", capacity.ClusterProviders)
	}

	useStrategy(h, paymentsKey,
		ecstypes.CapacityProviderStrategyItem{CapacityProvider: aws.String("FARGATE_SPOT"), Weight: 1},
		ecstypes.CapacityProviderStrategyItem{CapacityProvider: aws.String("FARGATE"), Base: 1})
	got, err = h.svc.DeploySettings(t.Context(), paymentsKey)
	if err != nil {
		t.Fatal(err)
	}
	capacity = got.Capacity
	want := []deploy.CapacityItem{{Provider: "FARGATE", Base: 1}, {Provider: "FARGATE_SPOT", Weight: 1}}
	if capacity.LaunchType != "" || len(capacity.Strategy) != 2 || capacity.Strategy[0] != want[0] || capacity.Strategy[1] != want[1] || !capacity.Editable {
		t.Fatalf("strategy service capacity = %+v, want a sorted editable strategy %v", capacity, want)
	}

	editService(h, paymentsKey, func(s *ecstypes.Service) { s.LaunchType, s.CapacityProviderStrategy = ecstypes.LaunchTypeEc2, nil })
	got, _ = h.svc.DeploySettings(t.Context(), paymentsKey)
	if got.Capacity.Editable || got.Capacity.LockReason != "This service does not run on Fargate." {
		t.Fatalf("EC2 service capacity = %+v, want locked as not Fargate", got.Capacity)
	}

	useStrategy(h, paymentsKey, ecstypes.CapacityProviderStrategyItem{CapacityProvider: aws.String("my-asg"), Weight: 1})
	got, _ = h.svc.DeploySettings(t.Context(), paymentsKey)
	if got.Capacity.Editable || got.Capacity.LockReason != "This service uses a capacity provider Harbormaster does not edit." {
		t.Fatalf("custom provider capacity = %+v, want locked", got.Capacity)
	}
}

func TestDeploySettings_CapacityLockedForWindows(t *testing.T) {
	h := newHarness(t)
	// The stored copy is what the settings read; give the deployed revision a Windows runtime platform.
	windows := `{"OperatingSystemFamily":"WINDOWS_SERVER_2019_CORE"}`
	if _, err := h.store.Pool.Exec(t.Context(),
		`update task_definitions set raw = jsonb_set(raw, '{RuntimePlatform}', $1::jsonb) where family = 'payments-api'`, windows); err != nil {
		t.Fatal(err)
	}
	got, err := h.svc.DeploySettings(t.Context(), paymentsKey)
	if err != nil {
		t.Fatal(err)
	}
	if got.Capacity.Editable || got.Capacity.LockReason != "Windows tasks cannot run on Fargate Spot." {
		t.Fatalf("Windows capacity = %+v, want locked", got.Capacity)
	}
}

func TestDeploySettings_CapacityLockedForForeignController(t *testing.T) {
	h := newHarness(t)
	editService(h, paymentsKey, func(s *ecstypes.Service) {
		s.DeploymentController = &ecstypes.DeploymentController{Type: ecstypes.DeploymentControllerTypeCodeDeploy}
	})
	got, err := h.svc.DeploySettings(t.Context(), paymentsKey)
	if err != nil {
		t.Fatal(err)
	}
	if got.Capacity.Editable || !strings.Contains(got.Capacity.LockReason, "CodeDeploy") {
		t.Fatalf("CodeDeploy capacity = %+v, want locked with the controller message", got.Capacity)
	}
}

func TestSettingsVersion_ChangesWithCapacityStrategy(t *testing.T) {
	h := newHarness(t)
	before, err := h.svc.DeploySettings(t.Context(), paymentsKey)
	if err != nil {
		t.Fatal(err)
	}
	useStrategy(h, paymentsKey,
		ecstypes.CapacityProviderStrategyItem{CapacityProvider: aws.String("FARGATE"), Base: 1},
		ecstypes.CapacityProviderStrategyItem{CapacityProvider: aws.String("FARGATE_SPOT"), Weight: 1})
	after, err := h.svc.DeploySettings(t.Context(), paymentsKey)
	if err != nil {
		t.Fatal(err)
	}
	if before.Version == after.Version {
		t.Fatalf("version %q did not change with the capacity strategy", after.Version)
	}
}

func TestValidateShape_CapacityStrategy(t *testing.T) {
	h := newHarness(t)
	cases := []struct {
		name     string
		strategy []deploy.CapacityItem
		want     string
	}{
		{"empty list", []deploy.CapacityItem{}, "1 or 2"},
		{"unknown provider", []deploy.CapacityItem{{Provider: "MY_ASG", Weight: 1}}, "FARGATE or FARGATE_SPOT"},
		{"duplicate", []deploy.CapacityItem{{Provider: "FARGATE", Weight: 1}, {Provider: "FARGATE", Weight: 1}}, "once"},
		{"base on spot", []deploy.CapacityItem{{Provider: "FARGATE_SPOT", Base: 1, Weight: 1}}, "base"},
		{"all weights zero", []deploy.CapacityItem{{Provider: "FARGATE", Base: 1}}, "weight above 0"},
		{"spot without weight", []deploy.CapacityItem{{Provider: "FARGATE", Weight: 1}, {Provider: "FARGATE_SPOT"}}, "FARGATE_SPOT"},
		{"three items", []deploy.CapacityItem{{Provider: "FARGATE", Weight: 1}, {Provider: "FARGATE_SPOT", Weight: 1}, {Provider: "FARGATE", Weight: 1}}, "1 or 2"},
		{"negative base", []deploy.CapacityItem{{Provider: "FARGATE", Base: -1, Weight: 1}}, "base"},
		{"negative weight", []deploy.CapacityItem{{Provider: "FARGATE", Weight: -1}}, "weight"},
		{"base too large", []deploy.CapacityItem{{Provider: "FARGATE", Base: 100001, Weight: 1}}, "base"},
		{"weight too large", []deploy.CapacityItem{{Provider: "FARGATE", Weight: 1001}}, "weight"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cs := baseChangeSet(t, h, paymentsKey)
			cs.Service = &deploy.ServiceChange{CapacityProviderStrategy: tc.strategy}
			requireInvalid(t, h, paymentsKey, cs, tc.want)
		})
	}
}

func TestRequiredActions_CapacityNeedsDeploy(t *testing.T) {
	cs := deploy.ChangeSet{Service: &deploy.ServiceChange{CapacityProviderStrategy: spotStrategy(1)}}
	got := deploy.RequiredActions(cs)
	if len(got) != 1 || got[0] != authz.Deploy {
		t.Fatalf("required actions = %v, want only Deploy", got)
	}
}

// primaryID is the id of the service's PRIMARY deployment.
func primaryID(t *testing.T, h *harness) string {
	t.Helper()
	return aws.ToString(h.primary(t, paymentsKey).Id)
}

func currentStrategy(h *harness, key model.ServiceKey) (launchType ecstypes.LaunchType, items []ecstypes.CapacityProviderStrategyItem) {
	editService(h, key, func(s *ecstypes.Service) {
		launchType, items = s.LaunchType, append(items, s.CapacityProviderStrategy...)
	})
	return launchType, items
}

func TestApplyChangeSet_CapacityForcesNewDeployment(t *testing.T) {
	h := newHarness(t)
	oldDeployment := primaryID(t, h)
	cs := baseChangeSet(t, h, paymentsKey)
	cs.Reason = "save money"
	cs.Service = &deploy.ServiceChange{CapacityProviderStrategy: spotStrategy(1)}

	got, err := h.svc.ApplyChangeSet(t.Context(), h.sam, paymentsKey, cs)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "applied" || got.Rollout == nil || got.Kind != "restart" {
		t.Fatalf("result = %+v, want applied with a restart rollout", got)
	}
	if primaryID(t, h) == oldDeployment {
		t.Fatal("no new deployment started: a strategy change alone does not replace tasks, so ForceNewDeployment must be set")
	}
	launchType, items := currentStrategy(h, paymentsKey)
	if launchType != "" || len(items) != 2 {
		t.Fatalf("service now has launch type %q and strategy %v, want only the strategy", launchType, items)
	}
	if calls := h.fake.Calls()["UpdateService"]; calls != 1 {
		t.Fatalf("UpdateService calls = %d, want one", calls)
	}
	ops, err := h.store.ListOperations(t.Context(), paymentsKey, false, nil, 20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, op := range ops {
		if op.Details["op"] == "capacity" {
			found = op.Text == "Changed capacity providers from FARGATE (launch type) to FARGATE base 1, FARGATE_SPOT weight 1." &&
				op.Details["changeset_id"] == got.ChangeSetID
		}
	}
	if !found {
		t.Fatalf("operations = %+v, want one capacity line with the before and after text", ops)
	}
}

func TestApplyChangeSet_CapacityWithNewRevisionStillForces(t *testing.T) {
	h := newHarness(t)
	cs := baseChangeSet(t, h, paymentsKey)
	cs.TaskDefinition = &deploy.TaskDefinitionChange{Revision: 41}
	cs.Service = &deploy.ServiceChange{CapacityProviderStrategy: spotStrategy(1)}
	got, err := h.svc.ApplyChangeSet(t.Context(), h.sam, paymentsKey, cs)
	if err != nil {
		t.Fatal(err)
	}
	if got.Rollout == nil || got.Rollout.ToRevision != 41 {
		t.Fatalf("result = %+v, want a rollout to revision 41", got)
	}
	if _, items := currentStrategy(h, paymentsKey); len(items) != 2 {
		t.Fatalf("strategy = %v, want it applied together with the revision", items)
	}
}

func TestApplyChangeSet_CapacityNeedsClusterProvider(t *testing.T) {
	h := newHarness(t)
	removeSpotFromCluster(t, h, "core-services")
	cs := baseChangeSet(t, h, paymentsKey)
	cs.Service = &deploy.ServiceChange{CapacityProviderStrategy: spotStrategy(1)}
	requireInvalid(t, h, paymentsKey, cs, clusterCopy)
	if calls := h.fake.Calls()["UpdateService"]; calls != 0 {
		t.Fatalf("UpdateService calls = %d, want none before the check", calls)
	}
	// Going back to on-demand only is allowed on such a cluster.
	onDemand := baseChangeSet(t, h, paymentsKey)
	onDemand.Service = &deploy.ServiceChange{CapacityProviderStrategy: []deploy.CapacityItem{{Provider: "FARGATE", Weight: 1}}}
	if _, err := h.svc.ApplyChangeSet(t.Context(), h.sam, paymentsKey, onDemand); err != nil {
		t.Fatalf("on-demand strategy refused: %v", err)
	}
}

func TestApplyChangeSet_SpotRefusedWhenClusterProvidersAreUnknown(t *testing.T) {
	h := newHarness(t)
	cs := baseChangeSet(t, h, paymentsKey)
	cs.Service = &deploy.ServiceChange{CapacityProviderStrategy: spotStrategy(1)}
	// The stored cluster cannot be read, so nothing says FARGATE_SPOT is associated: refuse, do not guess.
	if _, err := h.store.Pool.Exec(t.Context(), `delete from clusters where cluster_name = 'core-services'`); err != nil {
		t.Fatal(err)
	}
	requireInvalid(t, h, paymentsKey, cs, "capacity providers")
	if calls := h.fake.Calls()["UpdateService"]; calls != 0 {
		t.Fatalf("UpdateService calls = %d, want none", calls)
	}
	// On-demand only never needs Spot, so it is still allowed.
	onDemand := baseChangeSet(t, h, paymentsKey)
	onDemand.Service = &deploy.ServiceChange{CapacityProviderStrategy: []deploy.CapacityItem{{Provider: "FARGATE", Weight: 1}}}
	if _, err := h.svc.ApplyChangeSet(t.Context(), h.sam, paymentsKey, onDemand); err != nil {
		t.Fatalf("on-demand strategy refused: %v", err)
	}
}

func TestApplyChangeSet_OnlyTheNotAssociatedErrorGetsTheClusterCopy(t *testing.T) {
	cases := map[string]struct {
		message string
		cluster bool
	}{
		"association":   {"The specified capacity provider FARGATE_SPOT is not associated with the cluster.", true},
		"another cause": {"The capacity provider weight must be between 0 and 1000.", false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.fake.FailNext("UpdateService", &ecstypes.InvalidParameterException{Message: aws.String(tc.message)})
			cs := baseChangeSet(t, h, paymentsKey)
			cs.Service = &deploy.ServiceChange{CapacityProviderStrategy: spotStrategy(1)}
			_, err := h.svc.ApplyChangeSet(t.Context(), h.sam, paymentsKey, cs)
			if err == nil {
				t.Fatal("no error")
			}
			if got := strings.Contains(err.Error(), "aws_ecs_cluster_capacity_providers"); got != tc.cluster {
				t.Fatalf("err = %q, cluster copy = %v, want %v", err, got, tc.cluster)
			}
			if !tc.cluster && !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("err = %q, want the AWS text kept", err)
			}
		})
	}
}

func TestApplyChangeSet_CapacityRefusedWhenNotEditable(t *testing.T) {
	h := newHarness(t)
	editService(h, paymentsKey, func(s *ecstypes.Service) { s.LaunchType = ecstypes.LaunchTypeEc2 })
	cs := baseChangeSet(t, h, paymentsKey)
	cs.Service = &deploy.ServiceChange{CapacityProviderStrategy: spotStrategy(1)}
	requireInvalid(t, h, paymentsKey, cs, "This service does not run on Fargate.")
}

func TestApplyChangeSet_CapacityRefusedDuringRollout(t *testing.T) {
	h := newHarness(t)
	cs := baseChangeSet(t, h, notificationsKey)
	cs.Service = &deploy.ServiceChange{CapacityProviderStrategy: spotStrategy(1)}
	if _, err := h.svc.ApplyChangeSet(t.Context(), h.sam, notificationsKey, cs); !errors.Is(err, deploy.ErrRolloutRunning) {
		t.Fatalf("err = %v, want ErrRolloutRunning", err)
	}
}

func TestApplyChangeSet_UnchangedStrategyIsNothingToChange(t *testing.T) {
	h := newHarness(t)
	useStrategy(h, paymentsKey,
		ecstypes.CapacityProviderStrategyItem{CapacityProvider: aws.String("FARGATE_SPOT"), Weight: 1},
		ecstypes.CapacityProviderStrategyItem{CapacityProvider: aws.String("FARGATE"), Base: 1})
	cs := baseChangeSet(t, h, paymentsKey)
	cs.Service = &deploy.ServiceChange{CapacityProviderStrategy: []deploy.CapacityItem{{Provider: "FARGATE_SPOT", Weight: 1}, {Provider: "FARGATE", Base: 1}}}
	requireInvalid(t, h, paymentsKey, cs, "Nothing to change")
}

func TestApplyChangeSet_CapacityNeedsDeployPermission(t *testing.T) {
	h := newHarness(t)
	cs := baseChangeSet(t, h, paymentsKey)
	cs.Service = &deploy.ServiceChange{CapacityProviderStrategy: spotStrategy(1)}
	before := writeCalls(h)
	operator := subjectWith(t, h, paymentsKey, authz.Operate, authz.Logs)
	_, err := h.svc.ApplyChangeSet(t.Context(), operator, paymentsKey, cs)
	var forbidden *deploy.ForbiddenError
	if !errors.As(err, &forbidden) || forbidden.Action != authz.Deploy {
		t.Fatalf("err = %v, want a Deploy ForbiddenError", err)
	}
	assertNoNewWrites(t, h, before)
}
