// Package network is the pure logic behind the Networks pages: task
// visibility, subnet IP math, VPC tree assembly and region/VPC summaries.
package network

import (
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
)

// TaskVisibility reports whether the subject may see a task.
type TaskVisibility func(model.Task) bool

// Visibility checks View on the task's effective tag: the service tag for
// service tasks, the cluster tag for standalone tasks (rules 4 and 7).
func Visibility(subj authz.Subject, tags authz.Tags) TaskVisibility {
	return func(task model.Task) bool {
		return authz.Can(subj, authz.View, authz.EffectiveTaskTag(tags, task.Cluster, task.ServiceName))
	}
}

// FilterTasks keeps the RUNNING tasks the subject may see.
func FilterTasks(visible TaskVisibility, tasks []model.Task) []model.Task {
	kept := make([]model.Task, 0, len(tasks))
	for _, task := range tasks {
		if task.LastStatus == "RUNNING" && visible(task) {
			kept = append(kept, task)
		}
	}
	return kept
}
