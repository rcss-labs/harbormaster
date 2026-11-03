package deploy

import "github.com/rcss-labs/harbormaster/internal/authz"

// ChangeSet is everything the Deploy tab's review screen approved, applied in one request:
// register a revision, update the service, then change auto scaling.
type ChangeSet struct {
	BaseRevision int
	BaseVersion  string
	Reason       string

	TaskDefinition *TaskDefinitionChange
	Service        *ServiceChange
	Scaling        *ScalingChange
}

// TaskDefinitionChange carries either a new Definition to register or the Revision a previous run
// already registered (a retry never registers twice). Exactly one is set.
type TaskDefinitionChange struct {
	Definition Definition
	Revision   int
}

// ServiceChange is the UpdateService part. Nil fields are left alone.
type ServiceChange struct {
	DesiredCount                  *int
	EnableExecuteCommand          *bool
	DeploymentConfiguration       *DeploymentConfigChange
	HealthCheckGracePeriodSeconds *int
	ForceNewDeployment            bool
}

// DeploymentConfigChange replaces the service's deployment configuration as a whole.
type DeploymentConfigChange struct {
	CircuitBreakerEnable, CircuitBreakerRollback bool
	MinimumHealthyPercent, MaximumPercent        int
}

// ScalingChange is the Application Auto Scaling part.
type ScalingChange struct {
	Target                 *ScalingTargetChange
	RemoveTarget           bool
	PutPolicies            []PolicyChange
	DeletePolicies         []string
	PutScheduledActions    []ScheduledActionChange
	DeleteScheduledActions []string
}

// ScalingTargetChange registers the scalable target or changes its range.
type ScalingTargetChange struct{ Min, Max int }

// PolicyChange upserts a target tracking policy. Metric is "cpu", "memory" or "alb"; an alb policy
// names one of the service's own target groups.
type PolicyChange struct {
	Name, Metric                      string
	TargetValue                       float64
	ScaleInCooldown, ScaleOutCooldown int
	DisableScaleIn                    bool
	TargetGroupARN                    string
}

// ScheduledActionChange upserts a scheduled action; at least one of the capacities is set.
type ScheduledActionChange struct {
	Name, Schedule, Timezone string
	MinCapacity, MaxCapacity *int
}

// Step is the outcome of one phase of a change set. Status is "done", "failed" or "skipped".
type Step struct{ Name, Status, Message string }

// ChangeSetResult reports what a change set did. Status is "applied", "partial" or "registered_only".
// Steps lists only the phases the change set needed. Rollout is set when a new deployment started
// (Kind is then its kind: "deploy", "rollback" or "restart"). Warning is set when AWS accepted a
// change but Harbormaster could not record it. SettingsVersion is the settings version after the run
// (best effort, "" when it could not be read); a retry sends it as BaseVersion.
type ChangeSetResult struct {
	Status             string
	Steps              []Step
	ChangeSetID        string
	SettingsVersion    string
	RegisteredRevision int
	Rollout            *Rollout
	Kind               string
	Warning            string
}

// StaleVersionError means the service or scaling settings changed since the client read them.
type StaleVersionError struct{ Current DeploySettings }

func (e *StaleVersionError) Error() string {
	return "The service or scaling settings changed while you were editing"
}

// ForbiddenError means the subject lacks Action on the service. The API checks first; this is the
// service's own defense in depth.
type ForbiddenError struct{ Action authz.Action }

func (e *ForbiddenError) Error() string { return "You do not have permission for this change" }
