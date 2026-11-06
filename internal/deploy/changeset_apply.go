package deploy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// RequiredActions lists the actions the change set needs, in the order Edit, Deploy, Operate.
// A definition alone needs only Edit: ApplyChangeSet then deploys the new revision when the caller
// also holds Deploy, and otherwise stops at "registered_only". A definition submitted together with
// any other section, or an existing revision, is a deploy and needs Deploy.
func RequiredActions(cs ChangeSet) []authz.Action {
	need := map[authz.Action]bool{}
	if td := cs.TaskDefinition; td != nil {
		need[authz.Edit] = td.Definition != nil
		if td.Revision != 0 || (td.Definition != nil && (cs.Service != nil || cs.Scaling != nil)) {
			need[authz.Deploy] = true
		}
	}
	if svc := cs.Service; svc != nil {
		if svc.DesiredCount != nil || svc.ForceNewDeployment {
			need[authz.Operate] = true
		}
		if svc.EnableExecuteCommand != nil || svc.DeploymentConfiguration != nil || svc.HealthCheckGracePeriodSeconds != nil {
			need[authz.Deploy] = true
		}
	}
	if cs.Scaling != nil {
		need[authz.Operate] = true
	}
	var actions []authz.Action
	for _, action := range []authz.Action{authz.Edit, authz.Deploy, authz.Operate} {
		if need[action] {
			actions = append(actions, action)
		}
	}
	return actions
}

// changePlan is what a change set will do, after dropping settings that already have the requested value.
type changePlan struct {
	register       bool
	deployRevision bool // point the service at a new task definition revision
	desired        *int
	exec           *bool
	config         *DeploymentConfigChange
	grace          *int
	forceNew       bool
	scaling        *ScalingChange
}

func buildPlan(cs ChangeSet, st DeploySettings, canDeploy bool) changePlan {
	var plan changePlan
	if td := cs.TaskDefinition; td != nil {
		plan.register = td.Definition != nil
		plan.deployRevision = td.Revision != 0 || canDeploy
	}
	if svc := cs.Service; svc != nil {
		if svc.DesiredCount != nil && *svc.DesiredCount != st.DesiredCount {
			plan.desired = svc.DesiredCount
		}
		if svc.EnableExecuteCommand != nil && *svc.EnableExecuteCommand != st.EnableExecuteCommand {
			plan.exec = svc.EnableExecuteCommand
		}
		current := DeploymentConfigChange{
			CircuitBreakerEnable: st.DeploymentConfiguration.CircuitBreakerEnable, CircuitBreakerRollback: st.DeploymentConfiguration.CircuitBreakerRollback,
			MinimumHealthyPercent: st.DeploymentConfiguration.MinimumHealthyPercent, MaximumPercent: st.DeploymentConfiguration.MaximumPercent,
		}
		if config := svc.DeploymentConfiguration; config != nil && *config != current {
			plan.config = config
		}
		if grace := svc.HealthCheckGracePeriodSeconds; grace != nil && (st.HealthCheckGracePeriodSeconds == nil || *grace != *st.HealthCheckGracePeriodSeconds) {
			plan.grace = grace
		}
		// A new revision replaces the tasks anyway; the exec flag only reaches existing tasks through a new deployment.
		plan.forceNew = !plan.deployRevision && (svc.ForceNewDeployment || plan.exec != nil)
	}
	plan.scaling = cs.Scaling
	return plan
}

// updatesService reports whether the plan needs the single UpdateService call.
func (p changePlan) updatesService() bool {
	return p.deployRevision || p.desired != nil || p.exec != nil || p.config != nil || p.grace != nil || p.forceNew
}

// replacesTasks reports whether the plan changes how tasks roll out, which the controller and rollout gates guard.
func (p changePlan) replacesTasks() bool {
	return p.deployRevision || p.exec != nil || p.config != nil || p.grace != nil || p.forceNew
}

func (p changePlan) empty() bool { return !p.register && !p.updatesService() && p.scaling == nil }

// serviceTag resolves the tag that decides the caller's permissions on the service.
func (s *Service) serviceTag(ctx context.Context, key model.ServiceKey) (authz.TagID, error) {
	row, err := s.Store.GetServiceView(ctx, key, store.Visibility{Admin: true})
	if err != nil {
		return 0, err
	}
	return row.EffectiveTagID, nil
}

// ApplyChangeSet checks every precondition, then applies the change set in a fixed order: register a
// revision, one UpdateService call, then Application Auto Scaling. Preconditions are permissions,
// shape, stale base revision and settings version, the controller and rollout gates, and semantic
// validation; all run before the first AWS write.
//
// A failure before any write is returned as an error. After the first write a failure is not an
// error: the result has Status "partial" with the failed step and the later steps skipped, so the
// caller can retry the rest (with TaskDefinition.Revision instead of Definition, so nothing is
// registered twice). Failures to record a completed change become Warning.
func (s *Service) ApplyChangeSet(ctx context.Context, subj authz.Subject, key model.ServiceKey, cs ChangeSet) (ChangeSetResult, error) {
	tag, err := s.serviceTag(ctx, key)
	if err != nil {
		return ChangeSetResult{}, err
	}
	for _, action := range RequiredActions(cs) {
		if !authz.Can(subj, action, tag) {
			return ChangeSetResult{}, &ForbiddenError{Action: action}
		}
	}
	if err := validateShape(cs); err != nil {
		return ChangeSetResult{}, err
	}
	ecsAPI, live, err := s.liveService(ctx, key)
	if err != nil {
		return ChangeSetResult{}, err
	}
	st, err := s.settingsFrom(ctx, key, live)
	if err != nil {
		return ChangeSetResult{}, err
	}
	// base_revision guards the task definition; scaling and service writes are guarded by base_version.
	if cs.TaskDefinition != nil && cs.BaseRevision != 0 && cs.BaseRevision != st.DeployedRevision {
		return ChangeSetResult{}, &StaleBaseError{BaseRevision: cs.BaseRevision, DeployedRevision: st.DeployedRevision}
	}
	if cs.Service != nil || cs.Scaling != nil {
		if cs.BaseVersion == "" {
			return ChangeSetResult{}, invalid("base_version is required with a service or scaling change")
		}
		edited := partsEdited(cs)
		if err := refuseUnreadableEdits(st, edited); err != nil {
			return ChangeSetResult{}, err
		}
		if versionConflict(cs.BaseVersion, st.Version, edited) {
			return ChangeSetResult{}, &StaleVersionError{Current: st}
		}
	}
	if err := validateAgainst(cs, st); err != nil {
		return ChangeSetResult{}, err
	}
	plan := buildPlan(cs, st, authz.Can(subj, authz.Deploy, tag))
	if plan.empty() {
		return ChangeSetResult{}, invalid("Nothing to change")
	}
	if plan.replacesTasks() {
		if !st.Controller.Supported {
			return ChangeSetResult{}, &UnsupportedControllerError{Name: st.Controller.Name}
		}
		if RolloutRunning(live) {
			return ChangeSetResult{}, ErrRolloutRunning
		}
	}
	var registerInput *ecs.RegisterTaskDefinitionInput
	if td := cs.TaskDefinition; td != nil && td.Definition != nil {
		deployed, err := s.deployedFrom(ctx, key, live)
		if err != nil {
			return ChangeSetResult{}, err
		}
		if registerInput, err = Prepare(deployed.Definition, td.Definition, subj.Admin, key.AccountID, deployed.Tags); err != nil {
			return ChangeSetResult{}, err
		}
	} else if td != nil {
		if err := validateExistingRevision(ctx, ecsAPI, st, td.Revision); err != nil {
			return ChangeSetResult{}, err
		}
	}
	run := &changeSetRun{
		s: s, subj: subj, key: key, cs: cs, plan: plan, st: st, ecsAPI: ecsAPI, registerInput: registerInput,
		id: uuid.NewString(),
	}
	return run.execute(ctx)
}

// validateExistingRevision requires revision to be an ACTIVE revision of the deployed family other than the deployed one.
func validateExistingRevision(ctx context.Context, ecsAPI ECSClient, st DeploySettings, revision int) error {
	if revision == st.DeployedRevision {
		return invalid("Revision :%d is already deployed. Use a restart to replace its tasks.", revision)
	}
	out, err := ecsAPI.DescribeTaskDefinition(ctx, &ecs.DescribeTaskDefinitionInput{
		TaskDefinition: aws.String(fmt.Sprintf("%s:%d", st.Family, revision))})
	if err != nil {
		var awsErr *AWSError
		if errors.As(wrapAWS(err), &awsErr) && awsErr.Validation {
			return invalid("%s:%d is not a revision of this service's task definition", st.Family, revision)
		}
		return wrapAWS(err)
	}
	if out.TaskDefinition == nil || out.TaskDefinition.Status != ecstypes.TaskDefinitionStatusActive {
		return invalid("Revision :%d is not active and cannot be deployed", revision)
	}
	return nil
}

// changeSetRun is one execution of a validated change set.
type changeSetRun struct {
	s             *Service
	subj          authz.Subject
	key           model.ServiceKey
	cs            ChangeSet
	plan          changePlan
	st            DeploySettings
	ecsAPI        ECSClient
	registerInput *ecs.RegisterTaskDefinitionInput
	id            string

	result  ChangeSetResult
	wrote   bool // any AWS write succeeded, so failures are partial results instead of errors
	targetN int  // revision the service is pointed at
	warns   []string
}

func (r *changeSetRun) details(extra map[string]any) map[string]any {
	details := map[string]any{"changeset_id": r.id, "reason": r.cs.Reason}
	for name, value := range extra {
		details[name] = value
	}
	return details
}

func (r *changeSetRun) warn(message string) { r.warns = append(r.warns, message) }

func (r *changeSetRun) execute(ctx context.Context) (ChangeSetResult, error) {
	r.result.ChangeSetID = r.id
	var phases []func(context.Context) error
	var names []string
	add := func(name string, phase func(context.Context) error) {
		names = append(names, name)
		phases = append(phases, phase)
	}
	if r.plan.register {
		add("register", r.register)
	}
	if r.plan.updatesService() {
		add("service", r.updateService)
	}
	if r.plan.scaling != nil {
		add("scaling", r.scale)
	}
	if td := r.cs.TaskDefinition; td != nil && td.Revision != 0 {
		r.targetN = td.Revision
	}

	for i, phase := range phases {
		err := phase(ctx)
		if err == nil {
			r.result.Steps = append(r.result.Steps, Step{Name: names[i], Status: "done"})
			continue
		}
		if !r.wrote {
			return ChangeSetResult{}, err
		}
		r.result.Steps = append(r.result.Steps, Step{Name: names[i], Status: "failed", Message: err.Error()})
		for _, name := range names[i+1:] {
			r.result.Steps = append(r.result.Steps, Step{Name: name, Status: "skipped", Message: "Not run because an earlier step failed"})
		}
		r.result.Status = "partial"
		break
	}
	if r.result.Status == "" {
		r.result.Status = "applied"
		if len(names) == 1 && names[0] == "register" {
			r.result.Status = "registered_only"
		}
	}
	if r.result.Rollout != nil {
		r.result.Kind = r.result.Rollout.Kind
	}
	r.result.Warning = strings.Join(r.warns, " ")
	// Applied and partial runs may have changed the settings version; hand back the fresh one so a
	// retry does not 409 on the old base_version. Best effort: a failed read leaves it empty.
	if r.wrote {
		if fresh, err := r.s.DeploySettings(context.WithoutCancel(ctx), r.key); err == nil {
			r.result.SettingsVersion = fresh.Version
		} else {
			slog.WarnContext(ctx, "could not read settings after change set", "service", r.key.Service, "error", err)
		}
	}
	return r.result, nil
}

func (r *changeSetRun) register(ctx context.Context) error {
	registered, err := r.s.registerPrepared(ctx, r.subj, r.key, r.ecsAPI, r.registerInput, r.details(nil))
	if err != nil {
		return err
	}
	r.wrote = true
	r.result.RegisteredRevision = registered.Revision
	r.targetN = registered.Revision
	if registered.Warning != "" {
		r.warn(registered.Warning)
	}
	return nil
}

func (r *changeSetRun) updateService(ctx context.Context) error {
	input := &ecs.UpdateServiceInput{Cluster: aws.String(r.key.Cluster), Service: aws.String(r.key.Service)}
	reference := fmt.Sprintf("%s:%d", r.st.Family, r.targetN)
	if r.plan.deployRevision {
		input.TaskDefinition = aws.String(reference)
	}
	if r.plan.desired != nil {
		input.DesiredCount = aws.Int32(int32(*r.plan.desired))
	}
	if r.plan.exec != nil {
		input.EnableExecuteCommand = r.plan.exec
	}
	if config := r.plan.config; config != nil {
		input.DeploymentConfiguration = &ecstypes.DeploymentConfiguration{
			MinimumHealthyPercent: aws.Int32(int32(config.MinimumHealthyPercent)), MaximumPercent: aws.Int32(int32(config.MaximumPercent)),
			DeploymentCircuitBreaker: &ecstypes.DeploymentCircuitBreaker{Enable: config.CircuitBreakerEnable, Rollback: config.CircuitBreakerRollback},
		}
	}
	if r.plan.grace != nil {
		input.HealthCheckGracePeriodSeconds = aws.Int32(int32(*r.plan.grace))
	}
	input.ForceNewDeployment = r.plan.forceNew
	out, err := r.ecsAPI.UpdateService(ctx, input)
	if err != nil {
		return wrapAWS(err)
	}
	r.wrote = true

	rollout := Rollout{FromRevision: r.st.DeployedRevision, ToRevision: r.st.DeployedRevision}
	storedKind := ""
	switch {
	case r.plan.deployRevision:
		rollout.ToRevision, rollout.Kind, storedKind = r.targetN, "deploy", "deploy"
		if r.targetN < r.st.DeployedRevision {
			rollout.Kind = "rollback"
		}
	case r.plan.forceNew:
		rollout.Kind, storedKind = "restart", "restart"
	}
	r.recordServiceStep(ctx, out.Service, &rollout, storedKind, reference)
	if rollout.Kind != "" {
		r.result.Rollout = &rollout
	}
	return nil
}

// recordServiceStep stores the new deployment's attribution and one operations line per change class,
// in one transaction. AWS has already applied the change, so a failure is only a warning.
func (r *changeSetRun) recordServiceStep(ctx context.Context, svc *ecstypes.Service, rollout *Rollout, storedKind, reference string) {
	var primary ecstypes.Deployment
	var recordErr error
	if rollout.Kind != "" {
		found, ok := ecstypes.Deployment{}, false
		if svc != nil {
			found, ok = PrimaryDeployment(*svc)
		}
		if ok {
			primary = found
			rollout.DeploymentID = aws.ToString(primary.Id)
		} else {
			recordErr = errors.New("UpdateService returned no primary deployment")
		}
	}
	recordCtx := context.WithoutCancel(ctx)
	now := r.s.now()
	var lines []store.Operation
	line := func(op, text string, extra map[string]any) {
		details := r.details(map[string]any{"op": op})
		for name, value := range extra {
			details[name] = value
		}
		lines = append(lines, store.Operation{
			AccountID: r.key.AccountID, Region: r.key.Region, Cluster: r.key.Cluster, Service: &r.key.Service,
			ActorUserID: &r.subj.UserID, Source: "harbormaster", Text: text, Details: details,
		})
	}
	deployment := map[string]any{"deployment_id": rollout.DeploymentID, "from_revision": rollout.FromRevision, "to_revision": rollout.ToRevision}
	switch rollout.Kind {
	case "deploy":
		line("deploy", fmt.Sprintf("Deployed %s.", reference), deployment)
	case "rollback":
		line("rollback", fmt.Sprintf("Rolled back %s.", reference), deployment)
	case "restart":
		line("restart", fmt.Sprintf("Restarted service (force new deployment) on revision %d.", rollout.ToRevision), deployment)
	}
	if r.plan.desired != nil {
		line("scale", fmt.Sprintf("Changed desired count from %d to %d.", r.st.DesiredCount, *r.plan.desired), nil)
	}
	if r.plan.exec != nil {
		verb := "Disabled"
		if *r.plan.exec {
			verb = "Enabled"
		}
		line("exec", verb+" ECS Exec.", nil)
	}
	if text := rolloutSettingsText(r.plan); text != "" {
		line("rollout_settings", text, nil)
	}
	recordErr = errors.Join(recordErr, r.s.inTx(recordCtx, func(tx store.Tx) error {
		if rollout.Kind != "" && rollout.DeploymentID != "" {
			if err := tx.AttributeDeployment(recordCtx, store.AttributedDeployment{
				Key: r.key, DeploymentID: rollout.DeploymentID, TaskDefinitionARN: aws.ToString(primary.TaskDefinition),
				Revision: rollout.ToRevision, Kind: storedKind, StartedByUserID: r.subj.UserID, StartedAt: now,
				Reason: r.cs.Reason, ChangeSetID: r.id,
			}); err != nil {
				return err
			}
		}
		for _, op := range lines {
			if err := tx.AppendOperation(recordCtx, op); err != nil {
				return err
			}
		}
		return nil
	}))
	if recordErr != nil {
		slog.ErrorContext(ctx, "applied service changes but recording them failed", "service", r.key.Service, "changeset_id", r.id, "error", recordErr)
		message := "The change was applied, but Harbormaster could not record it in the operations log."
		if rollout.Kind != "" {
			message = "The rollout started, but Harbormaster could not record who started it; it may show as started outside Harbormaster."
		}
		r.warn(message)
	}
}

// rolloutSettingsText describes the deployment configuration and grace period changes, or "" when there are none.
func rolloutSettingsText(plan changePlan) string {
	var parts []string
	if config := plan.config; config != nil {
		breaker := "off"
		if config.CircuitBreakerEnable {
			breaker = "on"
			if config.CircuitBreakerRollback {
				breaker = "on with rollback"
			}
		}
		parts = append(parts, fmt.Sprintf("circuit breaker %s, minimum healthy %d%%, maximum %d%%", breaker, config.MinimumHealthyPercent, config.MaximumPercent))
	}
	if plan.grace != nil {
		parts = append(parts, fmt.Sprintf("health check grace period %d s", *plan.grace))
	}
	if len(parts) == 0 {
		return ""
	}
	return "Changed rollout settings: " + strings.Join(parts, "; ") + "."
}

func (r *changeSetRun) scale(ctx context.Context) error {
	api, err := r.s.Clients.AutoScaling(ctx, targetOf(r.key))
	if err != nil {
		return err
	}
	done, scaleErr := applyScaling(ctx, api, r.key, *r.plan.scaling, r.st)
	if len(done) == 0 {
		return scaleErr
	}
	r.wrote = true
	recordCtx := context.WithoutCancel(ctx)
	if err := r.s.refreshStoredScaling(recordCtx, api, r.key); err != nil {
		slog.ErrorContext(ctx, "applied scaling changes but refreshing the stored state failed", "service", r.key.Service, "error", err)
		r.warn("Auto scaling was changed, but Harbormaster could not refresh its stored state; the Scaling tab updates within a minute.")
	}
	logged := store.Operation{
		AccountID: r.key.AccountID, Region: r.key.Region, Cluster: r.key.Cluster, Service: &r.key.Service,
		ActorUserID: &r.subj.UserID, Source: "harbormaster",
		Text:    "Changed auto scaling: " + strings.Join(done, "; ") + ".",
		Details: r.details(map[string]any{"op": "scaling"}),
	}
	if warning := r.s.logAfterAWS(ctx, logged, "auto scaling change"); warning != "" {
		r.warn(warning)
	}
	return scaleErr
}
