package deploy

import (
	"encoding/json"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecsdocument "github.com/aws/aws-sdk-go-v2/service/ecs/document"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

func serviceWith(controller ecstypes.DeploymentControllerType, strategy ecstypes.DeploymentStrategy) ecstypes.Service {
	return ecstypes.Service{
		DeploymentController: &ecstypes.DeploymentController{Type: controller},
		DeploymentConfiguration: &ecstypes.DeploymentConfiguration{
			Strategy: strategy,
		},
	}
}

func TestController_Table(t *testing.T) {
	cases := []struct {
		name          string
		service       ecstypes.Service
		wantType      string
		wantSupported bool
		wantName      string
	}{
		{"ecs rolling", serviceWith(ecstypes.DeploymentControllerTypeEcs, ecstypes.DeploymentStrategyRolling), "ECS", true, ""},
		{"nil controller means ecs", ecstypes.Service{}, "ECS", true, ""},
		{"code deploy", serviceWith(ecstypes.DeploymentControllerTypeCodeDeploy, ""), "CODE_DEPLOY", false, "CodeDeploy"},
		{"external", serviceWith(ecstypes.DeploymentControllerTypeExternal, ""), "EXTERNAL", false, "an external controller"},
		{"ecs blue green", serviceWith(ecstypes.DeploymentControllerTypeEcs, ecstypes.DeploymentStrategyBlueGreen), "ECS", false, "ECS blue/green"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Controller(tc.service)
			if got.Type != tc.wantType || got.Supported != tc.wantSupported || got.Name != tc.wantName {
				t.Fatalf("Controller() = %+v, want Type=%q Supported=%v Name=%q", got, tc.wantType, tc.wantSupported, tc.wantName)
			}
		})
	}
}

func TestControllerBanner_Text(t *testing.T) {
	want := "This service is deployed by CodeDeploy. Harbormaster shows it but can't deploy it yet."
	if got := ControllerBanner("CodeDeploy"); got != want {
		t.Fatalf("ControllerBanner() = %q, want %q", got, want)
	}
}

func TestStrategyText_Table(t *testing.T) {
	withBreaker := func(minimumHealthy, maximum int32, enable, rollback bool) ecstypes.Service {
		svc := serviceWith(ecstypes.DeploymentControllerTypeEcs, ecstypes.DeploymentStrategyRolling)
		svc.DeploymentConfiguration.MinimumHealthyPercent = aws.Int32(minimumHealthy)
		svc.DeploymentConfiguration.MaximumPercent = aws.Int32(maximum)
		svc.DeploymentConfiguration.DeploymentCircuitBreaker = &ecstypes.DeploymentCircuitBreaker{Enable: enable, Rollback: rollback}
		return svc
	}
	const defaultPrefix = "Rolling update · min healthy 100% · max 200% · "
	cases := []struct {
		name    string
		service ecstypes.Service
		want    string
	}{
		{"breaker with rollback", withBreaker(100, 200, true, true), defaultPrefix + "circuit breaker with rollback is on"},
		{"breaker without rollback", withBreaker(100, 200, true, false), defaultPrefix + "circuit breaker is on, without rollback"},
		{"breaker off", withBreaker(100, 200, false, false), defaultPrefix + "circuit breaker is off"},
		{"non-default percentages", withBreaker(50, 150, false, false), "Rolling update · min healthy 50% · max 150% · circuit breaker is off"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := StrategyText(tc.service); got != tc.want {
				t.Fatalf("StrategyText() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPrimaryDeployment(t *testing.T) {
	svc := ecstypes.Service{Deployments: []ecstypes.Deployment{
		{Id: aws.String("old"), Status: aws.String("ACTIVE")},
		{Id: aws.String("new"), Status: aws.String("PRIMARY")},
	}}
	primary, ok := PrimaryDeployment(svc)
	if !ok || aws.ToString(primary.Id) != "new" {
		t.Fatalf("PrimaryDeployment() = (%q, %v), want (\"new\", true)", aws.ToString(primary.Id), ok)
	}
}

func TestRolloutRunning(t *testing.T) {
	inProgressOnActive := ecstypes.Service{Deployments: []ecstypes.Deployment{
		{Status: aws.String("PRIMARY"), RolloutState: ecstypes.DeploymentRolloutStateCompleted},
		{Status: aws.String("ACTIVE"), RolloutState: ecstypes.DeploymentRolloutStateInProgress},
	}}
	if !RolloutRunning(inProgressOnActive) {
		t.Fatal("RolloutRunning() = false, want true when any deployment is IN_PROGRESS")
	}
	settled := ecstypes.Service{Deployments: []ecstypes.Deployment{
		{Status: aws.String("PRIMARY"), RolloutState: ecstypes.DeploymentRolloutStateCompleted},
	}}
	if RolloutRunning(settled) {
		t.Fatal("RolloutRunning() = true, want false when all deployments are COMPLETED")
	}
}

func TestParseService_BlueGreenWithLifecycleHookDetails(t *testing.T) {
	svc := serviceWith(ecstypes.DeploymentControllerTypeEcs, ecstypes.DeploymentStrategyBlueGreen)
	svc.DeploymentConfiguration.LifecycleHooks = []ecstypes.DeploymentLifecycleHook{{
		HookDetails: ecsdocument.NewLazyDocument(map[string]any{"a": 1}),
	}}
	raw, err := json.Marshal(svc)
	if err != nil {
		t.Fatal(err)
	}

	parsed, err := ParseService(raw)
	if err != nil {
		t.Fatalf("ParseService() error = %v", err)
	}
	got := Controller(parsed)
	if got.Name != "ECS blue/green" || got.Supported {
		t.Fatalf("Controller() = %+v, want Name \"ECS blue/green\" and Supported false", got)
	}
}
