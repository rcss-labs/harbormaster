package api

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/model"
)

func TestExecAgentStatus_FromContainer(t *testing.T) {
	task := model.Task{Containers: []model.Container{
		{Name: "running", ExecAgentStatus: "RUNNING"},
		{Name: "pending", ExecAgentStatus: "PENDING"},
		{Name: "stopped", ExecAgentStatus: "STOPPED"},
		{Name: "noagent"},
		{Name: "stopping", ExecAgentStatus: "STOPPING"},
		{Name: "deactivating", ExecAgentStatus: "DEACTIVATING"},
	}}
	for _, tc := range []struct{ container, want string }{
		{"running", "RUNNING"}, {"pending", "PENDING"}, {"stopped", "STOPPED"},
		{"noagent", "MISSING"}, {"unknown", "MISSING"},
		{"stopping", "STOPPED"}, {"deactivating", "STOPPED"},
	} {
		if got := ExecAgentStatus(task, tc.container); got != tc.want {
			t.Errorf("ExecAgentStatus(%q) = %q, want %q", tc.container, got, tc.want)
		}
	}
}

// execTargetsResponse mirrors the endpoint body.
type execTargetsResponse struct {
	TaskID      string `json:"task_id"`
	ExecEnabled bool   `json:"exec_enabled"`
	Containers  []struct {
		Name      string `json:"name"`
		ExecAgent string `json:"exec_agent"`
	} `json:"containers"`
	Commands    []string `json:"commands"`
	CanTerminal bool     `json:"can_terminal"`
}

// taskIDWhere looks a task up through the admin task list; service "" means standalone.
func taskIDWhere(t *testing.T, srv *httptest.Server, service, family, status string) string {
	t.Helper()
	list := getTasks(t, login(t, srv, "sam", testPassword), "/api/tasks?filter=all&limit=500")
	for _, task := range list.Items {
		if (service == "") != (task.Service == nil) || (service != "" && task.Service.Name != service) {
			continue
		}
		if family != "" && task.Family != family {
			continue
		}
		if task.Status == status {
			return task.ID
		}
	}
	t.Fatalf("no %s task for service %q family %q", status, service, family)
	return ""
}

func getExecTargets(t *testing.T, c *apiClient, taskID string, wantStatus int) execTargetsResponse {
	t.Helper()
	resp := c.get("/api/tasks/" + taskID + "/exec-targets")
	if resp.StatusCode != wantStatus {
		t.Fatalf("exec-targets status = %d, want %d", resp.StatusCode, wantStatus)
	}
	var out execTargetsResponse
	if wantStatus == http.StatusOK {
		decode(t, resp, &out)
	} else if code, _ := errorCode(t, resp); code != "not_found" {
		t.Fatalf("error code = %q, want not_found", code)
	}
	return out
}

func seededExecServer(t *testing.T) *httptest.Server {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	setPassword(t, st, "dana")
	pollAll(t, st, fk)
	return srv
}

func TestExecTargets_ReturnsContainersAndFlags(t *testing.T) {
	srv := seededExecServer(t)
	id := taskIDWhere(t, srv, "payments-api", "", "RUNNING")
	got := getExecTargets(t, login(t, srv, "sam", testPassword), id, http.StatusOK)
	if got.TaskID != id || !got.ExecEnabled || !got.CanTerminal {
		t.Errorf("got %+v", got)
	}
	if !slices.Equal(got.Commands, []string{"/bin/sh", "/bin/bash"}) {
		t.Errorf("commands = %v", got.Commands)
	}
	found := false
	for _, c := range got.Containers {
		if c.Name == "payments-api" {
			found = true
			if c.ExecAgent != "RUNNING" {
				t.Errorf("payments-api exec_agent = %q", c.ExecAgent)
			}
		}
	}
	if !found {
		t.Errorf("payments-api container missing: %+v", got.Containers)
	}
}

func TestExecTargets_ExecOffService(t *testing.T) {
	srv := seededExecServer(t)
	id := taskIDWhere(t, srv, "cert-renewer", "", "RUNNING")
	got := getExecTargets(t, login(t, srv, "sam", testPassword), id, http.StatusOK)
	if got.ExecEnabled || len(got.Containers) == 0 {
		t.Errorf("got %+v, want exec_enabled false with containers listed", got)
	}
}

func TestExecTargets_UserWithoutTerminal_CanTerminalFalse(t *testing.T) {
	srv := seededExecServer(t)
	id := taskIDWhere(t, srv, "payments-api", "", "RUNNING")
	got := getExecTargets(t, login(t, srv, "dana", testPassword), id, http.StatusOK)
	if got.CanTerminal {
		t.Errorf("dana (Prod View|Logs) can_terminal = true")
	}
}

func TestExecTargets_InvisibleTask_404(t *testing.T) {
	srv := seededExecServer(t)
	id := taskIDWhere(t, srv, "payments-api", "", "RUNNING")
	getExecTargets(t, login(t, srv, "omer", testPassword), id, http.StatusNotFound)
}

func TestExecTargets_StoppedTask_404(t *testing.T) {
	srv := seededExecServer(t)
	id := taskIDWhere(t, srv, "image-resizer", "", "STOPPED")
	getExecTargets(t, login(t, srv, "sam", testPassword), id, http.StatusNotFound)
}

func TestExecTargets_StandaloneTask_UsesClusterTagAndOwnFlag(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	setPassword(t, st, "dana")
	pollAll(t, st, fk)
	id := taskIDWhere(t, srv, "", "release-build", "RUNNING")
	got := getExecTargets(t, login(t, srv, "dana", testPassword), id, http.StatusOK)
	if !got.ExecEnabled || !got.CanTerminal {
		t.Errorf("exec on: got %+v, want exec_enabled and can_terminal true", got)
	}

	setStandaloneExecFlag(t, st, fk, false)
	got = getExecTargets(t, login(t, srv, "dana", testPassword), id, http.StatusOK)
	if got.ExecEnabled || !got.CanTerminal {
		t.Errorf("exec off: got %+v, want exec_enabled false, can_terminal true", got)
	}
}

func TestTasksList_StandaloneExecEnabledFollowsTaskFlag(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	admin := login(t, srv, "sam", testPassword)
	standaloneExec := func() bool {
		for _, task := range getTasks(t, admin, "/api/tasks?filter=running&limit=500").Items {
			if task.Service == nil && task.Family == "release-build" {
				return task.ExecEnabled
			}
		}
		t.Fatal("standalone release-build not listed")
		return false
	}
	if !standaloneExec() {
		t.Error("exec on: exec_enabled = false")
	}
	setStandaloneExecFlag(t, st, fk, false)
	if standaloneExec() {
		t.Error("exec off: exec_enabled = true")
	}
}
