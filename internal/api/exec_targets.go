package api

import (
	"errors"
	"net/http"

	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// ExecCommands are the shells the terminal may start; the browser cannot send anything else.
var ExecCommands = []string{"/bin/sh", "/bin/bash"}

// ExecAgentStatus is the ECS Exec agent state of one container: RUNNING, PENDING, STOPPED,
// or MISSING when the container is unknown or reports no ExecuteCommandAgent.
func ExecAgentStatus(t model.Task, container string) string {
	for _, c := range t.Containers {
		if c.Name == container && c.ExecAgentStatus != "" {
			return c.ExecAgentStatus
		}
	}
	return "MISSING"
}

// ExecEnabled reports whether the service allows ECS Exec. Standalone tasks (zero service) are
// never exec-enabled in v1 because the Terminal tab lives on the service page.
func ExecEnabled(svc model.Service) bool { return svc.EnableExecuteCommand }

// TaskTag is the tag that governs t: its service's tag, or the cluster's for standalone tasks.
func TaskTag(tags authz.Tags, t model.Task) authz.TagID {
	return authz.EffectiveTaskTag(tags, t.Cluster, t.ServiceName)
}

type execContainerDTO struct {
	Name      string `json:"name"`
	ExecAgent string `json:"exec_agent"`
}

type execTargetsDTO struct {
	TaskID      string             `json:"task_id"`
	ExecEnabled bool               `json:"exec_enabled"`
	Containers  []execContainerDTO `json:"containers"`
	Commands    []string           `json:"commands"`
	CanTerminal bool               `json:"can_terminal"`
}

// execTargets answers GET /api/tasks/{taskId}/exec-targets. Unknown, non-RUNNING and invisible
// tasks are 404; a missing Terminal action is reported as can_terminal=false.
func (s *Server) execTargets(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("taskId")
	account, region, arn, err := model.ParseTaskID(taskID)
	if err != nil {
		s.fail(w, errNotFound)
		return
	}
	ctx := r.Context()
	task, err := s.Store.GetTask(ctx, account, region, arn)
	if err != nil {
		s.fail(w, err)
		return
	}
	tags, err := s.tags(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	subject := auth.SubjectFrom(ctx)
	tag := TaskTag(tags, task)
	if task.LastStatus != "RUNNING" || !authz.Can(subject, authz.View, tag) {
		s.fail(w, errNotFound)
		return
	}

	enabled := false
	if task.ServiceName != "" {
		svc, err := s.Store.GetService(ctx, model.ServiceKey{ClusterKey: task.Cluster, Service: task.ServiceName})
		switch {
		case err == nil:
			enabled = ExecEnabled(svc)
		case !errors.Is(err, store.ErrNotFound):
			s.fail(w, err)
			return
		}
	}

	containers := make([]execContainerDTO, 0, len(task.Containers))
	for _, c := range task.Containers {
		containers = append(containers, execContainerDTO{Name: c.Name, ExecAgent: ExecAgentStatus(task, c.Name)})
	}
	writeJSON(w, http.StatusOK, execTargetsDTO{
		TaskID: taskID, ExecEnabled: enabled, Containers: containers,
		Commands: ExecCommands, CanTerminal: authz.Can(subject, authz.Terminal, tag),
	})
}
