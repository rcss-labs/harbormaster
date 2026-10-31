package deploy

import (
	"encoding/json"
	"fmt"

	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// ControllerInfo describes which deployment controller owns a service.
// Supported is true only for the plain ECS rolling controller, the one Harbormaster can deploy.
type ControllerInfo struct {
	Type      string
	Supported bool
	Name      string
}

// ecsDefaultMinimumHealthyPercent and ecsDefaultMaximumPercent are the values ECS applies
// when a service's deployment configuration omits them.
const (
	ecsDefaultMinimumHealthyPercent = 100
	ecsDefaultMaximumPercent        = 200
)

// Controller reports the deployment controller of svc. A missing controller means ECS.
func Controller(svc ecstypes.Service) ControllerInfo {
	controllerType := ecstypes.DeploymentControllerTypeEcs
	if svc.DeploymentController != nil && svc.DeploymentController.Type != "" {
		controllerType = svc.DeploymentController.Type
	}
	info := ControllerInfo{Type: string(controllerType)}
	switch controllerType {
	case ecstypes.DeploymentControllerTypeCodeDeploy:
		info.Name = "CodeDeploy"
	case ecstypes.DeploymentControllerTypeExternal:
		info.Name = "an external controller"
	default:
		if isBlueGreen(svc) {
			info.Name = "ECS blue/green"
		} else {
			info.Supported = true
		}
	}
	return info
}

// ControllerBanner is the notice shown for a service whose controller Harbormaster cannot deploy.
func ControllerBanner(name string) string {
	return "This service is deployed by " + name + ". Harbormaster shows it but can't deploy it yet."
}

// StrategyText summarises the ECS rolling update settings and circuit breaker state of svc.
func StrategyText(svc ecstypes.Service) string {
	minimumHealthy := int32(ecsDefaultMinimumHealthyPercent)
	maximum := int32(ecsDefaultMaximumPercent)
	var breaker *ecstypes.DeploymentCircuitBreaker
	if config := svc.DeploymentConfiguration; config != nil {
		if config.MinimumHealthyPercent != nil {
			minimumHealthy = *config.MinimumHealthyPercent
		}
		if config.MaximumPercent != nil {
			maximum = *config.MaximumPercent
		}
		breaker = config.DeploymentCircuitBreaker
	}

	prefix := fmt.Sprintf("Rolling update · min healthy %d%% · max %d%% · ", minimumHealthy, maximum)
	switch {
	case breaker == nil || !breaker.Enable:
		return prefix + "circuit breaker is off"
	case breaker.Rollback:
		return prefix + "circuit breaker with rollback is on"
	default:
		return prefix + "circuit breaker is on, without rollback"
	}
}

// PrimaryDeployment returns the deployment whose status is PRIMARY, the one serving production traffic.
func PrimaryDeployment(svc ecstypes.Service) (ecstypes.Deployment, bool) {
	for _, deployment := range svc.Deployments {
		if deployment.Status != nil && *deployment.Status == "PRIMARY" {
			return deployment, true
		}
	}
	return ecstypes.Deployment{}, false
}

// RolloutRunning reports whether any deployment of svc is still rolling out.
func RolloutRunning(svc ecstypes.Service) bool {
	for _, deployment := range svc.Deployments {
		if deployment.RolloutState == ecstypes.DeploymentRolloutStateInProgress {
			return true
		}
	}
	return false
}

// ParseService decodes the SDK service JSON stored in model.Service.Raw.
func ParseService(raw json.RawMessage) (ecstypes.Service, error) {
	var svc ecstypes.Service
	if err := json.Unmarshal(raw, &svc); err != nil {
		return ecstypes.Service{}, fmt.Errorf("decode service: %w", err)
	}
	return svc, nil
}

func isBlueGreen(svc ecstypes.Service) bool {
	return svc.DeploymentConfiguration != nil &&
		svc.DeploymentConfiguration.Strategy == ecstypes.DeploymentStrategyBlueGreen
}
