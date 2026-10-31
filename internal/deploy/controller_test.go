package deploy

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
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
		wantSupported bool
		wantName      string
	}{
		{"ecs rolling", serviceWith(ecstypes.DeploymentControllerTypeEcs, ecstypes.DeploymentStrategyRolling), true, ""},
		{"code deploy", serviceWith(ecstypes.DeploymentControllerTypeCodeDeploy, ""), false, "CodeDeploy"},
		{"external", serviceWith(ecstypes.DeploymentControllerTypeExternal, ""), false, "an external controller"},
		{"ecs blue green", serviceWith(ecstypes.DeploymentControllerTypeEcs, ecstypes.DeploymentStrategyBlueGreen), false, "ECS blue/green"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Controller(tc.service)
			if got.Supported != tc.wantSupported || got.Name != tc.wantName {
				t.Fatalf("Controller() = %+v, want Supported=%v Name=%q", got, tc.wantSupported, tc.wantName)
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
	withBreaker := func(enable, rollback bool) ecstypes.Service {
		svc := serviceWith(ecstypes.DeploymentControllerTypeEcs, ecstypes.DeploymentStrategyRolling)
		svc.DeploymentConfiguration.MinimumHealthyPercent = aws.Int32(100)
		svc.DeploymentConfiguration.MaximumPercent = aws.Int32(200)
		svc.DeploymentConfiguration.DeploymentCircuitBreaker = &ecstypes.DeploymentCircuitBreaker{Enable: enable, Rollback: rollback}
		return svc
	}
	const prefix = "Rolling update · min healthy 100% · max 200% · "
	cases := []struct {
		name    string
		service ecstypes.Service
		want    string
	}{
		{"breaker with rollback", withBreaker(true, true), prefix + "circuit breaker with rollback is on"},
		{"breaker without rollback", withBreaker(true, false), prefix + "circuit breaker is on, without rollback"},
		{"breaker off", withBreaker(false, false), prefix + "circuit breaker is off"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := StrategyText(tc.service); got != tc.want {
				t.Fatalf("StrategyText() = %q, want %q", got, tc.want)
			}
		})
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
