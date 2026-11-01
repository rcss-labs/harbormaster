package api

import (
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/smithy-go"

	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// writeEnv is a polled mockup estate with one user per permission shape on the Prod tag.
type writeEnv struct {
	fk    *fake.Fake
	st    *store.Store
	login func(user string) *apiClient
}

func newWriteEnv(t *testing.T) writeEnv {
	t.Helper()
	srv, fk, st := newTestServer(t)
	seedAccounts(t, st)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	tags, err := st.ListTags(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var prod authz.TagID
	for _, tag := range tags {
		if tag.Name == "Prod" {
			prod = tag.ID
		}
	}
	for name, actions := range map[string]authz.ActionSet{
		"deployer": authz.Presets["Deployer"],
		"operator": authz.Presets["Operator"],
		"editor":   authz.ActionSet(authz.View | authz.Edit),
		"viewer":   authz.Presets["Viewer"],
		"nobody":   0,
	} {
		user, err := st.CreateUser(t.Context(), name, hash, false, false)
		if err != nil {
			t.Fatal(err)
		}
		if actions != 0 {
			if err := st.SetPermission(t.Context(), user.ID, prod, actions); err != nil {
				t.Fatal(err)
			}
		}
	}
	return writeEnv{fk: fk, st: st, login: func(user string) *apiClient { return login(t, srv, user, testPassword) }}
}

// draftFor returns the deployed definition of payments-api with its image tag bumped.
func draftFor(t *testing.T, c *apiClient) map[string]any {
	t.Helper()
	var td struct {
		Definition map[string]any `json:"definition"`
	}
	decode(t, c.get("/api/services/"+paymentsID()+"/task-definition"), &td)
	containers := td.Definition["containerDefinitions"].([]any)
	first := containers[0].(map[string]any)
	first["image"] = first["image"].(string) + "-hotfix"
	return td.Definition
}

func wantStatus(t *testing.T, resp *http.Response, want int) {
	t.Helper()
	if resp.StatusCode != want {
		code, msg := "", ""
		if resp.StatusCode >= 400 {
			code, msg = errorCode(t, resp)
		}
		t.Fatalf("status %d (%s: %s), want %d", resp.StatusCode, code, msg, want)
	}
}

func TestRegister_Permissions(t *testing.T) {
	for _, tc := range []struct {
		user    string
		status  int
		message string
	}{
		{"editor", http.StatusCreated, ""},
		{"deployer", http.StatusCreated, ""},
		{"operator", http.StatusForbidden, "You need Edit task definition on Prod"},
		{"viewer", http.StatusForbidden, "You need Edit task definition on Prod"},
		{"nobody", http.StatusNotFound, ""},
	} {
		t.Run(tc.user, func(t *testing.T) {
			env := newWriteEnv(t)
			draft := draftFor(t, env.login("pavel"))
			resp := env.login(tc.user).post("/api/services/"+paymentsID()+"/revisions", map[string]any{"base_revision": 42, "definition": draft})
			if resp.StatusCode != tc.status {
				t.Fatalf("status %d, want %d", resp.StatusCode, tc.status)
			}
			if tc.status == http.StatusCreated {
				var out map[string]any
				decode(t, resp, &out)
				tag, _ := out["tag"].(map[string]any)
				if out["revision"] != float64(43) || out["can_deploy"] != (tc.user == "deployer") || tag["name"] != "Prod" || tag["inherited"] != true || out["strategy"] == "" {
					t.Errorf("response = %v", out)
				}
				for _, key := range []string{"arn", "image_tag", "cpu", "memory"} {
					if s, _ := out[key].(string); s == "" {
						t.Errorf("%s missing in %v", key, out)
					}
				}
				return
			}
			code, msg := errorCode(t, resp)
			wantCode := map[int]string{http.StatusForbidden: "forbidden", http.StatusNotFound: "not_found"}[tc.status]
			if code != wantCode || (tc.message != "" && msg != tc.message) {
				t.Errorf("error = %s %q", code, msg)
			}
		})
	}
}

func TestRegister_StaleBaseIs409WithDeployedRevision(t *testing.T) {
	env := newWriteEnv(t)
	admin := env.login("pavel")
	resp := admin.post("/api/services/"+paymentsID()+"/revisions", map[string]any{"base_revision": 41, "definition": draftFor(t, admin)})
	wantStatus(t, resp, http.StatusConflict)
	var out struct {
		Error            struct{ Code, Message string } `json:"error"`
		DeployedRevision int                            `json:"deployed_revision"`
	}
	decode(t, resp, &out)
	if out.Error.Code != "conflict" || out.DeployedRevision != 42 || out.Error.Message != "The service moved from :41 to :42 while you were editing" {
		t.Errorf("body = %+v", out)
	}
}

func TestRegister_ValidationIs422(t *testing.T) {
	env := newWriteEnv(t)
	admin := env.login("pavel")
	draft := draftFor(t, admin)
	draft["family"] = "other-family"
	resp := admin.post("/api/services/"+paymentsID()+"/revisions", map[string]any{"base_revision": 42, "definition": draft})
	wantStatus(t, resp, http.StatusUnprocessableEntity)
	code, msg := errorCode(t, resp)
	if code != "invalid" || msg != "Use Terraform to create a new family" {
		t.Errorf("error = %s %q", code, msg)
	}
}

func TestRegister_NonAdminRoleChangeIs422(t *testing.T) {
	env := newWriteEnv(t)
	draft := draftFor(t, env.login("pavel"))
	draft["taskRoleArn"] = "arn:aws:iam::" + homelabAccountID + ":role/escalated"
	body := map[string]any{"base_revision": 42, "definition": draft}
	resp := env.login("deployer").post("/api/services/"+paymentsID()+"/revisions", body)
	wantStatus(t, resp, http.StatusUnprocessableEntity)
	if code, msg := errorCode(t, resp); code != "invalid" || msg != "Changing roles needs a global admin" {
		t.Errorf("error = %s %q", code, msg)
	}
	wantStatus(t, env.login("pavel").post("/api/services/"+paymentsID()+"/revisions", body), http.StatusCreated)
}

func TestRegister_NonAdminEnvironmentFileIs422AndNothingRegistered(t *testing.T) {
	env := newWriteEnv(t)
	draft := draftFor(t, env.login("pavel"))
	first := draft["containerDefinitions"].([]any)[0].(map[string]any)
	first["environmentFiles"] = []any{map[string]any{"type": "s3", "value": "arn:aws:s3:::attacker-bucket/stolen.env"}}
	body := map[string]any{"base_revision": 42, "definition": draft}
	before := env.fk.Calls()["RegisterTaskDefinition"]
	resp := env.login("deployer").post("/api/services/"+paymentsID()+"/revisions", body)
	wantStatus(t, resp, http.StatusUnprocessableEntity)
	if code, msg := errorCode(t, resp); code != "invalid" || msg != "Changing environment files needs a global admin" {
		t.Errorf("error = %s %q", code, msg)
	}
	if after := env.fk.Calls()["RegisterTaskDefinition"]; after != before {
		t.Errorf("RegisterTaskDefinition called %d times for a rejected draft", after-before)
	}
	wantStatus(t, env.login("pavel").post("/api/services/"+paymentsID()+"/revisions", body), http.StatusCreated)
}

func TestDeploy_Permissions(t *testing.T) {
	for _, tc := range []struct {
		user    string
		status  int
		message string
	}{
		{"deployer", http.StatusAccepted, ""},
		{"editor", http.StatusForbidden, "You need Deploy on Prod"},
		{"operator", http.StatusForbidden, "You need Deploy on Prod"},
		{"viewer", http.StatusForbidden, "You need Deploy on Prod"},
		{"nobody", http.StatusNotFound, ""},
	} {
		t.Run(tc.user, func(t *testing.T) {
			env := newWriteEnv(t)
			resp := env.login(tc.user).post("/api/services/"+paymentsID()+"/deploy", map[string]any{"revision": 41})
			if resp.StatusCode != tc.status {
				t.Fatalf("status %d, want %d", resp.StatusCode, tc.status)
			}
			if tc.status == http.StatusAccepted {
				var out map[string]any
				decode(t, resp, &out)
				if out["from_revision"] != float64(42) || out["to_revision"] != float64(41) || out["kind"] != "rollback" || out["deployment_id"] == "" {
					t.Errorf("response = %v", out)
				}
				return
			}
			code, msg := errorCode(t, resp)
			wantCode := map[int]string{http.StatusForbidden: "forbidden", http.StatusNotFound: "not_found"}[tc.status]
			if code != wantCode || (tc.message != "" && msg != tc.message) {
				t.Errorf("error = %s %q", code, msg)
			}
		})
	}
}

func TestDeploy_AlreadyDeployedIs422(t *testing.T) {
	env := newWriteEnv(t)
	resp := env.login("pavel").post("/api/services/"+paymentsID()+"/deploy", map[string]any{"revision": 42})
	wantStatus(t, resp, http.StatusUnprocessableEntity)
}

func TestDeploy_RolloutRunningIs409(t *testing.T) {
	env := newWriteEnv(t)
	id := serviceID(homelabAccountID, "us-east-1", "core-services", "notifications")
	resp := env.login("pavel").post("/api/services/"+id+"/deploy", map[string]any{"revision": 8})
	wantStatus(t, resp, http.StatusConflict)
	if code, msg := errorCode(t, resp); code != "conflict" || msg != "A rollout is already running" {
		t.Errorf("error = %s %q", code, msg)
	}
}

func TestDeploy_CodeDeployServiceIs422WithBanner(t *testing.T) {
	env := newWriteEnv(t)
	env.fk.Estate.Mu.Lock()
	for i := range env.fk.Estate.Services {
		svc := &env.fk.Estate.Services[i].Service
		if aws.ToString(svc.ServiceName) == "image-resizer" {
			svc.DeploymentController = &ecstypes.DeploymentController{Type: ecstypes.DeploymentControllerTypeCodeDeploy}
		}
	}
	env.fk.Estate.Mu.Unlock()
	id := serviceID(homelabAccountID, "us-east-1", "background-jobs", "image-resizer")
	resp := env.login("pavel").post("/api/services/"+id+"/deploy", map[string]any{"revision": 5})
	wantStatus(t, resp, http.StatusUnprocessableEntity)
	want := "This service is deployed by CodeDeploy. Harbormaster shows it but can't deploy it yet."
	if code, msg := errorCode(t, resp); code != "invalid" || msg != want {
		t.Errorf("error = %s %q", code, msg)
	}
}

func TestRestart_OperatorAllowed(t *testing.T) {
	env := newWriteEnv(t)
	resp := env.login("operator").post("/api/services/"+paymentsID()+"/restart", nil)
	wantStatus(t, resp, http.StatusAccepted)
	var out map[string]any
	decode(t, resp, &out)
	if out["revision"] != float64(42) || out["kind"] != "restart" || out["deployment_id"] == "" {
		t.Errorf("response = %v", out)
	}
	resp = env.login("viewer").post("/api/services/"+paymentsID()+"/restart", nil)
	wantStatus(t, resp, http.StatusForbidden)
	if _, msg := errorCode(t, resp); msg != "You need Operate on Prod" {
		t.Errorf("message = %q", msg)
	}
}

func TestDesiredCount_RangeAndNoop(t *testing.T) {
	env := newWriteEnv(t)
	op := env.login("operator")
	path := "/api/services/" + paymentsID() + "/desired-count"
	wantStatus(t, op.post(path, map[string]any{"desired": 51}), http.StatusUnprocessableEntity)

	resp := op.post(path, map[string]any{"desired": 3})
	wantStatus(t, resp, http.StatusOK)
	var noop map[string]any
	decode(t, resp, &noop)
	if noop["changed"] != false || noop["previous"] != float64(3) || noop["desired"] != float64(3) || noop["warning"] != nil {
		t.Errorf("noop = %v", noop)
	}

	resp = op.post(path, map[string]any{"desired": 5})
	wantStatus(t, resp, http.StatusOK)
	var changed map[string]any
	decode(t, resp, &changed)
	if changed["changed"] != true || changed["previous"] != float64(3) || changed["desired"] != float64(5) {
		t.Errorf("changed = %v", changed)
	}
}

func TestStopTask_ServiceTagAndStandaloneClusterTag(t *testing.T) {
	env := newWriteEnv(t)
	env.fk.Estate.Mu.Lock()
	var standalone, inProd string
	for _, task := range env.fk.Estate.Tasks {
		arn := aws.ToString(task.Task.TaskArn)
		switch {
		case task.ClusterName == "ci-tools" && aws.ToString(task.Task.Group) == "family:release-build" && task.Task.LastStatus != nil && *task.Task.LastStatus == "RUNNING":
			standalone = arn
		case task.ClusterName == "core-services" && inProd == "":
			inProd = arn
		}
	}
	env.fk.Estate.Mu.Unlock()
	if standalone == "" || inProd == "" {
		t.Fatalf("seed tasks not found: standalone %q prod %q", standalone, inProd)
	}
	standaloneID := model.TaskID(homelabAccountID, "us-east-1", standalone)
	prodID := model.TaskID(homelabAccountID, "us-east-1", inProd)

	// omer is Operator on Dev: may stop the Dev standalone task, but cannot see Prod tasks.
	omer := env.login("omer")
	resp := omer.post("/api/tasks/"+standaloneID+"/stop", nil)
	wantStatus(t, resp, http.StatusOK)
	var out map[string]any
	decode(t, resp, &out)
	if out["task_arn"] != standalone || out["reason"] != "Stopped by omer via Harbormaster" {
		t.Errorf("stop = %v", out)
	}
	wantStatus(t, omer.post("/api/tasks/"+prodID+"/stop", nil), http.StatusNotFound)

	// viewer sees Prod tasks but may not stop them; has no view on the Dev standalone task.
	viewer := env.login("viewer")
	resp = viewer.post("/api/tasks/"+prodID+"/stop", nil)
	wantStatus(t, resp, http.StatusForbidden)
	if _, msg := errorCode(t, resp); msg != "You need Operate on Prod" {
		t.Errorf("message = %q", msg)
	}
	wantStatus(t, viewer.post("/api/tasks/"+standaloneID+"/stop", nil), http.StatusNotFound)
	wantStatus(t, env.login("operator").post("/api/tasks/"+prodID+"/stop", nil), http.StatusOK)
}

func TestWrite_RequiresCSRF(t *testing.T) {
	env := newWriteEnv(t)
	resp := env.login("pavel").do(http.MethodPost, "/api/services/"+paymentsID()+"/restart", nil, map[string]string{"X-CSRF-Token": ""})
	wantStatus(t, resp, http.StatusForbidden)
	if code, _ := errorCode(t, resp); code != "csrf" {
		t.Errorf("code = %q, want csrf", code)
	}
}

func TestWrite_MissingRequiredFieldsAre422(t *testing.T) {
	env := newWriteEnv(t)
	admin := env.login("pavel")
	id := paymentsID()
	for _, tc := range []struct {
		path, message string
		body          map[string]any
	}{
		{"/desired-count", "desired is required.", map[string]any{}},
		{"/deploy", "revision is required.", map[string]any{}},
		{"/revisions", "base_revision is required.", map[string]any{"definition": map[string]any{"family": "payments-api"}}},
	} {
		resp := admin.post("/api/services/"+id+tc.path, tc.body)
		wantStatus(t, resp, http.StatusUnprocessableEntity)
		if code, msg := errorCode(t, resp); code != "invalid" || msg != tc.message {
			t.Errorf("%s: error = %s %q", tc.path, code, msg)
		}
	}
	var dc struct {
		Desired int `json:"desired"`
	}
	decode(t, admin.get("/api/services/"+id+"/desired-count"), &dc)
	if dc.Desired != 3 {
		t.Errorf("desired count changed to %d by a body without desired", dc.Desired)
	}
}

func TestRegister_AWSValidationIs422Verbatim(t *testing.T) {
	env := newWriteEnv(t)
	admin := env.login("pavel")
	draft := draftFor(t, admin)
	env.fk.FailNext("RegisterTaskDefinition", &smithy.GenericAPIError{Code: "InvalidParameterException", Message: "Container.image should not be null or empty."})
	resp := admin.post("/api/services/"+paymentsID()+"/revisions", map[string]any{"base_revision": 42, "definition": draft})
	wantStatus(t, resp, http.StatusUnprocessableEntity)
	if code, msg := errorCode(t, resp); code != "invalid" || msg != "Container.image should not be null or empty." {
		t.Errorf("error = %s %q", code, msg)
	}
}

func TestWrite_ThrottleIs503(t *testing.T) {
	env := newWriteEnv(t)
	admin := env.login("pavel")
	atomic.StoreInt32(&env.fk.Throttle, 1)
	resp := admin.post("/api/services/"+paymentsID()+"/restart", nil)
	wantStatus(t, resp, http.StatusServiceUnavailable)
	if code, _ := errorCode(t, resp); code != "throttled" {
		t.Errorf("code = %q, want throttled", code)
	}
}

// foldedKeyAttacks are drafts that try to slip a role or secret past the non-admin lock with a
// key spelled in another letter case. encoding/json would decode them into the real field.
func foldedKeyAttacks() map[string]func(draft map[string]any) {
	const prodAdmin = "arn:aws:iam::" + homelabAccountID + ":role/prod-admin"
	const stolen = "arn:aws:ssm:us-east-1:" + homelabAccountID + ":parameter/stolen/KEY"
	first := func(draft map[string]any) map[string]any {
		return draft["containerDefinitions"].([]any)[0].(map[string]any)
	}
	return map[string]func(map[string]any){
		"taskRoleArn duplicate":      func(d map[string]any) { d["taskrolearn"] = prodAdmin },
		"executionRoleArn duplicate": func(d map[string]any) { d["executionrolearn"] = prodAdmin },
		"taskRoleArn lone":           func(d map[string]any) { delete(d, "taskRoleArn"); d["taskrolearn"] = prodAdmin },
		"secrets valueFrom duplicate": func(d map[string]any) {
			first(d)["secrets"] = []any{map[string]any{"name": "X", "valueFrom": "arn:aws:ssm:us-east-1:" + homelabAccountID + ":parameter/payments/DB_PASS", "valuefrom": stolen}}
		},
		"secrets valueFrom lone": func(d map[string]any) {
			first(d)["secrets"] = []any{map[string]any{"name": "X", "valuefrom": stolen}}
		},
		"secretOptions valueFrom": func(d map[string]any) {
			first(d)["logConfiguration"] = map[string]any{"logDriver": "splunk", "secretOptions": []any{
				map[string]any{"name": "token", "valuefrom": stolen}}}
		},
		"repositoryCredentials": func(d map[string]any) {
			first(d)["repositoryCredentials"] = map[string]any{"credentialsparameter": stolen}
		},
	}
}

func TestRegister_NonAdminFoldedKeyBypassIs422AndNothingIsRegistered(t *testing.T) {
	for name, attack := range foldedKeyAttacks() {
		t.Run(name, func(t *testing.T) {
			env := newWriteEnv(t)
			draft := draftFor(t, env.login("pavel"))
			attack(draft)
			before := env.fk.Calls()["RegisterTaskDefinition"]
			resp := env.login("editor").post("/api/services/"+paymentsID()+"/revisions", map[string]any{"base_revision": 42, "definition": draft})
			wantStatus(t, resp, http.StatusUnprocessableEntity)
			if got := env.fk.Calls()["RegisterTaskDefinition"]; got != before {
				t.Errorf("RegisterTaskDefinition called %d time(s), want none", got-before)
			}
		})
	}
}

func TestRegister_UnknownDraftKeyIs422(t *testing.T) {
	env := newWriteEnv(t)
	admin := env.login("pavel")
	draft := draftFor(t, admin)
	draft["taskRoleArnn"] = "typo"
	resp := admin.post("/api/services/"+paymentsID()+"/revisions", map[string]any{"base_revision": 42, "definition": draft})
	wantStatus(t, resp, http.StatusUnprocessableEntity)
	if _, msg := errorCode(t, resp); !strings.Contains(msg, "unknown key") {
		t.Errorf("message = %q", msg)
	}
	if got := env.fk.Calls()["RegisterTaskDefinition"]; got != 0 {
		t.Errorf("RegisterTaskDefinition called %d time(s)", got)
	}
}

// dropTable breaks the recording step of an action without touching what the handler reads first.
func dropTable(t *testing.T, env writeEnv, table string) {
	t.Helper()
	if _, err := env.st.Pool.Exec(t.Context(), `drop table `+table+` cascade`); err != nil {
		t.Fatal(err)
	}
}

func TestRegister_RecordFailureAfterAWSAcceptedIs201WithWarning(t *testing.T) {
	env := newWriteEnv(t)
	draft := draftFor(t, env.login("pavel"))
	dropTable(t, env, "revision_records")
	resp := env.login("pavel").post("/api/services/"+paymentsID()+"/revisions", map[string]any{"base_revision": 42, "definition": draft})
	wantStatus(t, resp, http.StatusCreated)
	var out map[string]any
	decode(t, resp, &out)
	if warning, _ := out["warning"].(string); !strings.Contains(warning, "could not record") || out["revision"] != float64(43) {
		t.Errorf("response = %v, want revision 43 with a recording warning", out)
	}
	if got := env.fk.Calls()["RegisterTaskDefinition"]; got != 1 {
		t.Errorf("RegisterTaskDefinition calls = %d, want 1", got)
	}
}

func TestDeploy_RecordFailureAfterAWSAcceptedIs202WithWarning(t *testing.T) {
	env := newWriteEnv(t)
	dropTable(t, env, "deployment_history")
	resp := env.login("deployer").post("/api/services/"+paymentsID()+"/deploy", map[string]any{"revision": 41})
	wantStatus(t, resp, http.StatusAccepted)
	var out map[string]any
	decode(t, resp, &out)
	if warning, _ := out["warning"].(string); !strings.Contains(warning, "could not record") || out["deployment_id"] == "" {
		t.Errorf("response = %v", out)
	}
}

// postRaw sends body verbatim, which apiClient.post cannot do for malformed JSON.
func postRaw(c *apiClient, path, body string) *http.Response {
	c.t.Helper()
	req, err := http.NewRequest(http.MethodPost, c.base+path, strings.NewReader(body))
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", c.csrf)
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	return resp
}

func TestChangeBodies_TrailingJSONAndUnknownFieldsAreRejected(t *testing.T) {
	env := newWriteEnv(t)
	deployer := env.login("deployer")
	for name, tc := range map[string]struct{ path, body string }{
		"trailing JSON":           {"/deploy", `{"revision": 41}{"revision": 40}`},
		"unknown field on deploy": {"/deploy", `{"revision": 41, "revison": 40}`},
		"unknown field on count":  {"/desired-count", `{"desired": 3, "force": true}`},
	} {
		t.Run(name, func(t *testing.T) {
			resp := postRaw(deployer, "/api/services/"+paymentsID()+tc.path, tc.body)
			wantStatus(t, resp, http.StatusUnprocessableEntity)
		})
	}
	if got := env.fk.Calls()["UpdateService"]; got != 0 {
		t.Errorf("UpdateService called %d time(s), want none", got)
	}
}
