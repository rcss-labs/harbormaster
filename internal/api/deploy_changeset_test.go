package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	astypes "github.com/aws/aws-sdk-go-v2/service/applicationautoscaling/types"
	"github.com/aws/smithy-go"
)

var changesetWrites = []string{"RegisterTaskDefinition", "UpdateService", "RegisterScalableTarget", "PutScalingPolicy", "PutScheduledAction",
	"DeleteScalingPolicy", "DeleteScheduledAction", "DeregisterScalableTarget"}

func assertNoChangesetWrites(t *testing.T, env writeEnv) {
	t.Helper()
	calls := env.fk.Calls()
	for _, name := range changesetWrites {
		if calls[name] != 0 {
			t.Errorf("%s was called %d times, want no AWS write", name, calls[name])
		}
	}
}

func liveSettings(t *testing.T, c *apiClient) DeploySettingsResponse {
	t.Helper()
	var out DeploySettingsResponse
	resp := c.get("/api/services/" + paymentsID() + "/deploy-settings")
	wantStatus(t, resp, http.StatusOK)
	decode(t, resp, &out)
	return out
}

func changesetPath() string { return "/api/services/" + paymentsID() + "/changeset" }

func TestChangeSet_403NamesFirstMissingAction(t *testing.T) {
	env := newWriteEnv(t)
	settings := liveSettings(t, env.login("pavel"))
	body := map[string]any{
		"base_revision": 42, "base_version": settings.Version,
		"task_definition": map[string]any{"definition": draftFor(t, env.login("pavel"))},
		"service":         map[string]any{"desired_count": 4},
	}
	resp := env.login("operator").post(changesetPath(), body)
	wantStatus(t, resp, http.StatusForbidden)
	code, msg := errorCode(t, resp)
	if code != "forbidden" || msg != "You need Edit task definition on Prod" {
		t.Errorf("error = %s %q", code, msg)
	}
	assertNoChangesetWrites(t, env)

	// Without the definition only Operate is asked for.
	resp = env.login("viewer").post(changesetPath(), map[string]any{
		"base_revision": 42, "base_version": settings.Version, "service": map[string]any{"desired_count": 4},
	})
	wantStatus(t, resp, http.StatusForbidden)
	if _, msg := errorCode(t, resp); msg != "You need Operate on Prod" {
		t.Errorf("message = %q", msg)
	}
	assertNoChangesetWrites(t, env)

	// A user who cannot see the service gets a 404, not a 403.
	wantStatus(t, env.login("nobody").post(changesetPath(), body), http.StatusNotFound)
}

func TestChangeSet_EditOnlyRegistersOnly(t *testing.T) {
	env := newWriteEnv(t)
	editor := env.login("editor")
	resp := editor.post(changesetPath(), map[string]any{
		"base_revision": 42, "base_version": "", "reason": "bump",
		"task_definition": map[string]any{"definition": draftFor(t, editor)},
	})
	wantStatus(t, resp, http.StatusOK)
	var out ChangeSetResponse
	decode(t, resp, &out)
	if out.Status != "registered_only" || out.RegisteredRevision == nil || *out.RegisteredRevision != 43 ||
		out.DeploymentID != "" || out.FromRevision != nil || out.ToRevision != nil || out.Warning != nil ||
		out.ChangeSetID == "" || len(out.Steps) != 1 || out.Steps[0].Name != "register" || out.Steps[0].Status != "done" {
		t.Fatalf("response = %+v", out)
	}
	if env.fk.Calls()["UpdateService"] != 0 {
		t.Error("UpdateService was called for a register-only change set")
	}
}

func TestChangeSet_InvalidBodyStrictJSON(t *testing.T) {
	env := newWriteEnv(t)
	admin := env.login("pavel")
	settings := liveSettings(t, admin)
	for name, body := range map[string]any{
		"unknown field":         map[string]any{"base_version": settings.Version, "service": map[string]any{"desired_count": 4, "bogus": true}},
		"unknown top level":     map[string]any{"base_version": settings.Version, "bogus": 1},
		"definition+revision":   map[string]any{"base_revision": 42, "task_definition": map[string]any{"definition": map[string]any{"family": "x"}, "revision": 41}},
		"empty task_definition": map[string]any{"base_revision": 42, "task_definition": map[string]any{}},
		"wrong type":            map[string]any{"base_version": settings.Version, "service": map[string]any{"desired_count": "four"}},
		"nothing to change":     map[string]any{"base_revision": 42, "base_version": settings.Version},
	} {
		t.Run(name, func(t *testing.T) {
			resp := admin.post(changesetPath(), body)
			wantStatus(t, resp, http.StatusUnprocessableEntity)
			if code, _ := errorCode(t, resp); code != "invalid" {
				t.Errorf("code = %q, want invalid", code)
			}
		})
	}
	assertNoChangesetWrites(t, env)
}

func TestChangeSet_PartialIs200WithSteps(t *testing.T) {
	env := newWriteEnv(t)
	admin := env.login("pavel")
	settings := liveSettings(t, admin)
	env.fk.FailNext("PutScalingPolicy", &astypes.ValidationException{Message: aws.String("nope")})
	resp := admin.post(changesetPath(), map[string]any{
		"base_revision": 42, "base_version": settings.Version,
		"service": map[string]any{"enable_execute_command": false},
		"scaling": map[string]any{"put_policies": []map[string]any{{
			"name": "cpu-extra", "metric": "cpu", "target_value": 50, "scale_in_cooldown": 300, "scale_out_cooldown": 60,
			"disable_scale_in": false, "target_group_arn": "",
		}}},
	})
	wantStatus(t, resp, http.StatusOK)
	var out ChangeSetResponse
	decode(t, resp, &out)
	if out.Status != "partial" || len(out.Steps) != 2 || out.Steps[0].Status != "done" || out.Steps[1].Name != "scaling" ||
		out.Steps[1].Status != "failed" || out.Steps[1].Message == "" {
		t.Fatalf("response = %+v", out)
	}
	if out.SettingsVersion == "" || out.SettingsVersion == settings.Version {
		t.Errorf("settings_version = %q, want the fresh version (was %q)", out.SettingsVersion, settings.Version)
	}

	// The retry with the returned version and only the scaling section goes through.
	retry := admin.post(changesetPath(), map[string]any{
		"base_revision": 42, "base_version": out.SettingsVersion,
		"scaling": map[string]any{"put_policies": []map[string]any{{
			"name": "cpu-extra", "metric": "cpu", "target_value": 50, "scale_in_cooldown": 300, "scale_out_cooldown": 60,
			"disable_scale_in": false, "target_group_arn": "",
		}}},
	})
	wantStatus(t, retry, http.StatusOK)
}

func TestChangeSet_StaleVersion409CarriesSettings(t *testing.T) {
	env := newWriteEnv(t)
	admin := env.login("pavel")
	settings := liveSettings(t, admin)
	resp := admin.post(changesetPath(), map[string]any{
		"base_revision": 42, "base_version": "not-the-live-version", "service": map[string]any{"desired_count": 4},
	})
	wantStatus(t, resp, http.StatusConflict)
	var out struct {
		Error    struct{ Code, Message string } `json:"error"`
		Settings DeploySettingsResponse         `json:"settings"`
	}
	decode(t, resp, &out)
	if out.Error.Code != "conflict" || out.Error.Message == "" || out.Settings.Version != settings.Version ||
		out.Settings.DesiredCount != settings.DesiredCount || !out.Settings.Can.Operate {
		t.Fatalf("body = %+v", out)
	}
	assertNoChangesetWrites(t, env)
}

func TestDeploySettings_ViewOnly404ForInvisible(t *testing.T) {
	env := newWriteEnv(t)
	wantStatus(t, env.login("nobody").get("/api/services/"+paymentsID()+"/deploy-settings"), http.StatusNotFound)
	wantStatus(t, env.login("viewer").get("/api/services/"+paymentsID()+"/deploy-settings"), http.StatusOK)
}

var errScheduledActionsUnsupported = &smithy.GenericAPIError{Code: "UnsupportedOperation", Message: "DescribeScheduledActions is not supported", Fault: smithy.FaultClient}

func TestDeploySettings_PartialScheduledActions(t *testing.T) {
	env := newWriteEnv(t)
	admin := env.login("pavel")
	if got := liveSettings(t, admin); got.Partial == nil || len(got.Partial) != 0 {
		t.Fatalf("complete settings: partial = %#v, want an empty array", got.Partial)
	}
	env.fk.FailAlways("DescribeScheduledActions", errScheduledActionsUnsupported)
	got := liveSettings(t, admin)
	if len(got.Partial) != 1 || got.Partial[0].Part != "scheduled_actions" || got.Partial[0].Code != "aws_error" ||
		!strings.HasPrefix(got.Partial[0].Message, "Scheduled actions could not be read: ") {
		t.Fatalf("partial = %+v", got.Partial)
	}
	if got.DeployedRevision != 42 || len(got.Scaling.Policies) != 1 || !got.Scaling.HasTarget ||
		got.Scaling.ScheduledActions == nil || len(got.Scaling.ScheduledActions) != 0 {
		t.Fatalf("every other part must still be returned: %+v", got)
	}
}

func TestChangeSet_AppliesWhenScheduledActionsUnreadable(t *testing.T) {
	env := newWriteEnv(t)
	admin := env.login("pavel")
	env.fk.FailAlways("DescribeScheduledActions", errScheduledActionsUnsupported)
	settings := liveSettings(t, admin)
	resp := admin.post(changesetPath(), map[string]any{
		"base_revision": 42, "base_version": settings.Version,
		"service": map[string]any{"enable_execute_command": !settings.EnableExecuteCommand},
	})
	wantStatus(t, resp, http.StatusOK)
	var out ChangeSetResponse
	decode(t, resp, &out)
	if out.Status != "applied" {
		t.Fatalf("response = %+v", out)
	}
}

func TestChangeSet_RefusesScheduledActionEditWhenUnreadable(t *testing.T) {
	env := newWriteEnv(t)
	admin := env.login("pavel")
	env.fk.FailAlways("DescribeScheduledActions", errScheduledActionsUnsupported)
	settings := liveSettings(t, admin)
	resp := admin.post(changesetPath(), map[string]any{
		"base_revision": 42, "base_version": settings.Version,
		"scaling": map[string]any{"put_scheduled_actions": []map[string]any{{
			"name": "night", "schedule": "cron(0 2 * * ? *)", "timezone": "", "min_capacity": 1, "max_capacity": nil,
		}}},
	})
	wantStatus(t, resp, http.StatusUnprocessableEntity)
	if code, msg := errorCode(t, resp); code != "invalid" || msg != "Scheduled actions could not be read; retry before changing them." {
		t.Fatalf("error = %s: %s", code, msg)
	}
	assertNoChangesetWrites(t, env)
}

func TestDeploySettings_CanFlags(t *testing.T) {
	env := newWriteEnv(t)
	for user, want := range map[string]ChangeCanDTO{
		"pavel":    {Edit: true, Deploy: true, Operate: true, Admin: true},
		"deployer": {Edit: true, Deploy: true, Operate: true},
		"operator": {Operate: true},
		"editor":   {Edit: true},
		"viewer":   {},
	} {
		t.Run(user, func(t *testing.T) {
			got := liveSettings(t, env.login(user))
			if got.Can != want {
				t.Errorf("can = %+v, want %+v", got.Can, want)
			}
			if got.Version == "" || got.DeployedRevision != 42 || got.Family != "payments-api" || got.Scaling.Policies == nil ||
				got.TargetGroups == nil || got.Scaling.ScheduledActions == nil {
				t.Errorf("settings = %+v", got)
			}
		})
	}
}
