package deploy

import "github.com/rcss-labs/harbormaster/internal/poller"

// partsEdited lists the settings parts a change set edits or depends on. Putting a policy or a
// scheduled action needs to know whether a scalable target exists, and an ALB policy needs the
// target group labels, so those count as edits of the target and the target groups.
func partsEdited(cs ChangeSet) []string {
	scaling := cs.Scaling
	if scaling == nil {
		return nil
	}
	var parts []string
	puts := len(scaling.PutPolicies) > 0 || len(scaling.PutScheduledActions) > 0
	if scaling.Target != nil || scaling.RemoveTarget || puts {
		parts = append(parts, poller.PartScalingTarget)
	}
	// Deregistering the target deletes its policies and scheduled actions with it, so removing it edits them too.
	if scaling.RemoveTarget || len(scaling.PutPolicies) > 0 || len(scaling.DeletePolicies) > 0 {
		parts = append(parts, poller.PartScalingPolicies)
	}
	if scaling.RemoveTarget || len(scaling.PutScheduledActions) > 0 || len(scaling.DeleteScheduledActions) > 0 {
		parts = append(parts, poller.PartScheduledActions)
	}
	for _, policy := range scaling.PutPolicies {
		if policy.Metric == "alb" {
			parts = append(parts, PartTargetGroups)
			break
		}
	}
	return parts
}

// unreadableMessages is what a change set gets when it edits a part that cannot be read right now.
var unreadableMessages = map[string]string{
	poller.PartScheduledActions: "Scheduled actions could not be read; retry before changing them.",
	poller.PartScalingPolicies:  "Scaling policies could not be read; retry before changing them.",
	poller.PartScalingTarget:    "The scalable target could not be read; retry before changing it.",
	PartTargetGroups:            "Target groups could not be read; retry before changing them.",
}

// refuseUnreadableEdits is the only place an unreadable part blocks a change set: one that leaves
// the part alone applies as usual.
func refuseUnreadableEdits(st DeploySettings, edited []string) error {
	for _, part := range edited {
		if st.unreadable(part) {
			return invalid("%s", unreadableMessages[part])
		}
	}
	return nil
}
