package api

import (
	"net/http"
	"testing"
)

const logDestinationMessage = "Changing where a container's logs go needs a global admin"

// registerForeignGroupRevision has the admin register revision 43 with logs in another service's group.
func registerForeignGroupRevision(t *testing.T, env writeEnv) {
	t.Helper()
	admin := env.login("sam")
	draft := draftFor(t, admin)
	for _, entry := range draft["containerDefinitions"].([]any) {
		logConfig, _ := entry.(map[string]any)["logConfiguration"].(map[string]any)
		if options, ok := logConfig["options"].(map[string]any); ok {
			options["awslogs-group"] = "/ecs/prod/another-service"
		}
	}
	wantStatus(t, admin.post("/api/services/"+paymentsID()+"/revisions", map[string]any{"base_revision": 42, "definition": draft}), http.StatusCreated)
}

func TestDeploy_NonAdminForeignLogGroupRevisionIs422AndNothingChanges(t *testing.T) {
	env := newWriteEnv(t)
	registerForeignGroupRevision(t, env)
	before := env.fk.Calls()["UpdateService"]
	resp := env.login("deployer").post("/api/services/"+paymentsID()+"/deploy", map[string]any{"revision": 43})
	wantStatus(t, resp, http.StatusUnprocessableEntity)
	if code, msg := errorCode(t, resp); code != "invalid" || msg != logDestinationMessage {
		t.Errorf("error = %s %q", code, msg)
	}
	if after := env.fk.Calls()["UpdateService"]; after != before {
		t.Errorf("UpdateService called %d times for a rejected revision", after-before)
	}
	wantStatus(t, env.login("sam").post("/api/services/"+paymentsID()+"/deploy", map[string]any{"revision": 43}), http.StatusAccepted)
}

func TestChangeSet_NonAdminForeignLogDestinationIs422AndNothingRegistered(t *testing.T) {
	for name, mutate := range map[string]func(options map[string]any){
		"foreign group":  func(options map[string]any) { options["awslogs-group"] = "/ecs/prod/another-service" },
		"foreign prefix": func(options map[string]any) { options["awslogs-stream-prefix"] = "billing" },
	} {
		t.Run(name, func(t *testing.T) {
			env := newWriteEnv(t)
			draft := draftFor(t, env.login("sam"))
			first := draft["containerDefinitions"].([]any)[0].(map[string]any)
			mutate(first["logConfiguration"].(map[string]any)["options"].(map[string]any))
			resp := env.login("deployer").post(changesetPath(), map[string]any{
				"base_revision": 42, "base_version": "", "task_definition": map[string]any{"definition": draft},
			})
			wantStatus(t, resp, http.StatusUnprocessableEntity)
			if code, msg := errorCode(t, resp); code != "invalid" || msg != logDestinationMessage {
				t.Errorf("error = %s %q", code, msg)
			}
			assertNoChangesetWrites(t, env)
		})
	}
}
