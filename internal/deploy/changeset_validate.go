package deploy

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // timezone validation must not depend on the host (distroless images have no zoneinfo)
	"unicode/utf8"

	astypes "github.com/aws/aws-sdk-go-v2/service/applicationautoscaling/types"
)

const maxReasonLength = 500

var (
	// policyNamePattern is the name rule for policies and scheduled actions.
	policyNamePattern = regexp.MustCompile(`^[\w./-]{1,256}$`)
	ratePattern       = regexp.MustCompile(`^rate\((\d+) (minute|minutes|hour|hours|day|days)\)$`)
	atPattern         = regexp.MustCompile(`^at\((\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})\)$`)
	cronPattern       = regexp.MustCompile(`^cron\((.+)\)$`)
)

// validateShape checks everything that needs no AWS state, so malformed requests fail before any read.
func validateShape(cs ChangeSet) error {
	if cs.TaskDefinition == nil && cs.Service == nil && cs.Scaling == nil {
		return invalid("Nothing to change")
	}
	if utf8.RuneCountInString(cs.Reason) > maxReasonLength {
		return invalid("Reason must be at most %d characters", maxReasonLength)
	}
	if td := cs.TaskDefinition; td != nil {
		if (td.Definition == nil) == (td.Revision == 0) {
			return invalid("task_definition needs either a definition or a revision, not both")
		}
		if td.Revision < 0 {
			return invalid("Revision must be a positive number")
		}
		if cs.BaseRevision <= 0 {
			return invalid("base_revision is required with a task definition")
		}
	}
	if svc := cs.Service; svc != nil {
		if err := validateServiceShape(*svc); err != nil {
			return err
		}
	}
	if scaling := cs.Scaling; scaling != nil {
		return validateScalingShape(*scaling)
	}
	return nil
}

func validateServiceShape(svc ServiceChange) error {
	if svc.DesiredCount == nil && svc.EnableExecuteCommand == nil && svc.DeploymentConfiguration == nil &&
		svc.HealthCheckGracePeriodSeconds == nil && !svc.ForceNewDeployment {
		return invalid("The service section changes nothing")
	}
	if svc.DesiredCount != nil && *svc.DesiredCount < 0 {
		return invalid("Desired count must be 0 or more")
	}
	if svc.HealthCheckGracePeriodSeconds != nil && *svc.HealthCheckGracePeriodSeconds < 0 {
		return invalid("Health check grace period must be 0 or more seconds")
	}
	if config := svc.DeploymentConfiguration; config != nil {
		switch {
		case config.MinimumHealthyPercent < 0 || config.MinimumHealthyPercent > 100:
			return invalid("Minimum healthy percent must be between 0 and 100")
		case config.MaximumPercent < 100 || config.MaximumPercent > 200:
			return invalid("Maximum percent must be between 100 and 200")
		case config.CircuitBreakerRollback && !config.CircuitBreakerEnable:
			return invalid("Circuit breaker rollback needs the circuit breaker enabled")
		}
	}
	return nil
}

func validateScalingShape(scaling ScalingChange) error {
	if scaling.Target == nil && !scaling.RemoveTarget && len(scaling.PutPolicies) == 0 && len(scaling.DeletePolicies) == 0 &&
		len(scaling.PutScheduledActions) == 0 && len(scaling.DeleteScheduledActions) == 0 {
		return invalid("The scaling section changes nothing")
	}
	if scaling.Target != nil && scaling.RemoveTarget {
		return invalid("Scaling cannot both set and remove the scalable target")
	}
	if target := scaling.Target; target != nil && (target.Min < 0 || target.Min > target.Max) {
		return invalid("Scaling minimum must be 0 or more and not above the maximum")
	}
	putPolicies := map[string]bool{}
	for _, policy := range scaling.PutPolicies {
		if err := validatePolicyShape(policy); err != nil {
			return err
		}
		if putPolicies[policy.Name] {
			return invalid("Policy %q is set twice", policy.Name)
		}
		putPolicies[policy.Name] = true
	}
	for _, name := range scaling.DeletePolicies {
		if putPolicies[name] {
			return invalid("Policy %q is both set and deleted", name)
		}
	}
	putActions := map[string]bool{}
	for _, action := range scaling.PutScheduledActions {
		if err := validateScheduledActionShape(action); err != nil {
			return err
		}
		if putActions[action.Name] {
			return invalid("Scheduled action %q is set twice", action.Name)
		}
		putActions[action.Name] = true
	}
	for _, name := range scaling.DeleteScheduledActions {
		if putActions[name] {
			return invalid("Scheduled action %q is both set and deleted", name)
		}
	}
	return nil
}

func validatePolicyShape(policy PolicyChange) error {
	if !policyNamePattern.MatchString(policy.Name) {
		return invalid("Policy name %q must be 1-256 letters, digits, '.', '/', '_' or '-'", policy.Name)
	}
	switch policy.Metric {
	case "cpu", "memory":
		if policy.TargetValue <= 0 || policy.TargetValue > 100 {
			return invalid("Policy %q: the %s target must be above 0 and at most 100", policy.Name, policy.Metric)
		}
	case "alb":
		if policy.TargetValue <= 0 {
			return invalid("Policy %q: the request count target must be above 0", policy.Name)
		}
	default:
		return invalid("Policy %q: metric must be cpu, memory or alb", policy.Name)
	}
	if policy.ScaleInCooldown < 0 || policy.ScaleOutCooldown < 0 {
		return invalid("Policy %q: cooldowns must be 0 or more seconds", policy.Name)
	}
	return nil
}

func validateScheduledActionShape(action ScheduledActionChange) error {
	if !policyNamePattern.MatchString(action.Name) {
		return invalid("Scheduled action name %q must be 1-256 letters, digits, '.', '/', '_' or '-'", action.Name)
	}
	if err := validateSchedule(action.Schedule); err != nil {
		return invalid("Scheduled action %q: %s", action.Name, err.Error())
	}
	if action.Timezone != "" {
		if _, err := time.LoadLocation(action.Timezone); err != nil || action.Timezone == "Local" {
			return invalid("Scheduled action %q: unknown timezone %q", action.Name, action.Timezone)
		}
	}
	if action.MinCapacity == nil && action.MaxCapacity == nil {
		return invalid("Scheduled action %q needs a minimum or a maximum capacity", action.Name)
	}
	if action.MinCapacity != nil && *action.MinCapacity < 0 || action.MaxCapacity != nil && *action.MaxCapacity < 0 {
		return invalid("Scheduled action %q: capacities must be 0 or more", action.Name)
	}
	if action.MinCapacity != nil && action.MaxCapacity != nil && *action.MinCapacity > *action.MaxCapacity {
		return invalid("Scheduled action %q: minimum is above maximum", action.Name)
	}
	return nil
}

// validateSchedule accepts the three Application Auto Scaling forms. rate() takes the singular
// unit for 1 and the plural for more ("rate(1 hour)", "rate(5 minutes)").
func validateSchedule(schedule string) error {
	const hint = "schedule must be cron(6 fields), at(YYYY-MM-DDThh:mm:ss) or rate(N minutes|hours|days)"
	if match := ratePattern.FindStringSubmatch(schedule); match != nil {
		n, err := strconv.Atoi(match[1])
		if err != nil || n < 1 {
			return fmt.Errorf("the rate value must be 1 or more; %s", hint)
		}
		if plural := strings.HasSuffix(match[2], "s"); plural != (n > 1) {
			return fmt.Errorf("use the singular unit for 1 and the plural for more (rate(1 hour), rate(5 hours)); %s", hint)
		}
		return nil
	}
	if match := atPattern.FindStringSubmatch(schedule); match != nil {
		if _, err := time.Parse("2006-01-02T15:04:05", match[1]); err != nil {
			return fmt.Errorf("%q is not a valid date and time; %s", match[1], hint)
		}
		return nil
	}
	if match := cronPattern.FindStringSubmatch(schedule); match != nil {
		if len(strings.Fields(match[1])) != 6 {
			return fmt.Errorf("cron() needs exactly 6 fields; %s", hint)
		}
		return nil
	}
	return fmt.Errorf("%s", hint)
}

// validateAgainst checks the change set against the live settings it was read from.
func validateAgainst(cs ChangeSet, st DeploySettings) error {
	if svc := cs.Service; svc != nil {
		if svc.DesiredCount != nil && *svc.DesiredCount > st.MaxDesired {
			return invalid("Desired count must be between 0 and %d", st.MaxDesired)
		}
		if svc.HealthCheckGracePeriodSeconds != nil && !st.HasLoadBalancer {
			return invalid("A health check grace period needs a load balancer, and this service has none")
		}
	}
	scaling := cs.Scaling
	rangeMin, rangeMax, hasTarget := st.Scaling.Min, st.Scaling.Max, st.Scaling.HasTarget
	if scaling != nil {
		if target := scaling.Target; target != nil {
			if target.Max > st.MaxDesired {
				return invalid("Scaling maximum must be at most %d", st.MaxDesired)
			}
			rangeMin, rangeMax, hasTarget = target.Min, target.Max, true
		}
		if scaling.RemoveTarget {
			hasTarget = false
		}
		if err := validateScalingAgainst(*scaling, st, rangeMin, rangeMax, hasTarget); err != nil {
			return err
		}
	}
	if svc := cs.Service; svc != nil && svc.DesiredCount != nil && hasTarget &&
		(*svc.DesiredCount < rangeMin || *svc.DesiredCount > rangeMax) {
		return invalid("Auto scaling would move %d back into %d–%d", *svc.DesiredCount, rangeMin, rangeMax)
	}
	return nil
}

func validateScalingAgainst(scaling ScalingChange, st DeploySettings, rangeMin, rangeMax int, hasTarget bool) error {
	if !hasTarget && (len(scaling.PutPolicies) > 0 || len(scaling.PutScheduledActions) > 0) {
		return invalid("Scaling policies and scheduled actions need a scalable target; set a minimum and maximum first")
	}
	existingPolicies := map[string]PolicySetting{}
	for _, policy := range st.Scaling.Policies {
		existingPolicies[policy.Name] = policy
	}
	for _, name := range scaling.DeletePolicies {
		if _, ok := existingPolicies[name]; !ok {
			return invalid("There is no scaling policy named %q to delete", name)
		}
	}
	existingActions := map[string]bool{}
	for _, action := range st.Scaling.ScheduledActions {
		existingActions[action.Name] = true
	}
	for _, name := range scaling.DeleteScheduledActions {
		if !existingActions[name] {
			return invalid("There is no scheduled action named %q to delete", name)
		}
	}
	for _, policy := range scaling.PutPolicies {
		if existing, ok := existingPolicies[policy.Name]; ok && !existing.Editable {
			return invalid("Policy %q is a policy Harbormaster cannot edit; delete it or pick another name", policy.Name)
		}
		if policy.Metric == "alb" {
			if _, ok := albLabelFor(st, policy.TargetGroupARN); !ok {
				return invalid("Policy %q needs one of this service's application load balancer target groups", policy.Name)
			}
		}
	}
	for _, action := range scaling.PutScheduledActions {
		for _, capacity := range []*int{action.MinCapacity, action.MaxCapacity} {
			if capacity != nil && (*capacity < rangeMin || *capacity > rangeMax) {
				return invalid("Scheduled action %q: capacity %d is outside the scaling range %d–%d", action.Name, *capacity, rangeMin, rangeMax)
			}
		}
	}
	return nil
}

// albLabelFor is the resource label of one of the service's own target groups.
func albLabelFor(st DeploySettings, targetGroupARN string) (string, bool) {
	for _, group := range st.TargetGroups {
		if group.ARN == targetGroupARN && group.ResourceLabel != "" {
			return group.ResourceLabel, true
		}
	}
	return "", false
}

// predefinedMetric maps a policy metric name to the AWS predefined metric.
func predefinedMetric(metric string) astypes.MetricType {
	switch metric {
	case "memory":
		return astypes.MetricTypeECSServiceAverageMemoryUtilization
	case "alb":
		return astypes.MetricTypeALBRequestCountPerTarget
	default:
		return astypes.MetricTypeECSServiceAverageCPUUtilization
	}
}
