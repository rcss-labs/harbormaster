// Package status derives service and cluster health from polled ECS data.
package status

import "github.com/rcss-labs/harbormaster/internal/model"

// Inputs carries everything Service needs to derive a service status.
type Inputs struct {
	Service         model.Service
	RunningTasks    []model.Task
	TargetUnhealthy bool // set by the load balancer poll
}

// Service applies the §6.12 precedence: first match wins.
func Service(in Inputs) model.Status {
	svc := in.Service
	if hasRolloutInProgress(svc.Deployments) {
		return model.StatusDeploying
	}
	if svc.Desired == 0 {
		return model.StatusScaledToZero
	}
	if svc.Running == 0 {
		return model.StatusFailing
	}
	if svc.Running < svc.Desired {
		return model.StatusDegraded
	}
	if in.TargetUnhealthy || anyUnhealthyTask(in.RunningTasks) {
		return model.StatusUnhealthyTask
	}
	return model.StatusSteady
}

// Cluster rolls service statuses up to one cluster status.
func Cluster(statuses []model.Status) model.ClusterStatus {
	var anyDegraded, anyDeploying bool
	for _, status := range statuses {
		switch status {
		case model.StatusFailing:
			return model.ClusterNeedsAttention
		case model.StatusDegraded, model.StatusUnhealthyTask:
			anyDegraded = true
		case model.StatusDeploying:
			anyDeploying = true
		}
	}
	if anyDegraded {
		return model.ClusterDegraded
	}
	if anyDeploying {
		return model.ClusterDeploying
	}
	return model.ClusterHealthy
}

func hasRolloutInProgress(deployments []model.Deployment) bool {
	for _, deployment := range deployments {
		if deployment.RolloutState == "IN_PROGRESS" {
			return true
		}
	}
	return false
}

func anyUnhealthyTask(tasks []model.Task) bool {
	for _, task := range tasks {
		if task.HealthStatus == "UNHEALTHY" {
			return true
		}
	}
	return false
}
