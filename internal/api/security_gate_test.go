package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/smithy-go"

	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/devseed"
	"github.com/rcss-labs/harbormaster/internal/model"
)

var (
	certRenewerKey = model.ServiceKey{ClusterKey: edgeNginxKey.ClusterKey, Service: "cert-renewer"}
	stagingAPIKey  = model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: stagingAccountID, Region: "us-east-1", Cluster: "staging-web"}, Service: "api"}
)

// ecrImage is the image reference of repository:tag in the registry of the service's account and region.
func ecrImage(key model.ServiceKey, repository, tag string) string {
	return key.AccountID + ".dkr.ecr." + key.Region + ".amazonaws.com/" + repository + ":" + tag
}

func (e *securityEnv) tagID(t *testing.T, name string) authz.TagID {
	t.Helper()
	tags, err := e.st.ListTags(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, tag := range tags {
		if tag.Name == name {
			return tag.ID
		}
	}
	t.Fatalf("no tag %q", name)
	return 0
}

// addUser creates a user with the given actions per tag name and returns a signed-in client.
func (e *securityEnv) addUser(t *testing.T, name string, perms map[string]authz.ActionSet) *apiClient {
	t.Helper()
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	user, err := e.st.CreateUser(t.Context(), name, hash, false, false)
	if err != nil {
		t.Fatal(err)
	}
	for tag, actions := range perms {
		if err := e.st.SetPermission(t.Context(), user.ID, e.tagID(t, tag), actions); err != nil {
			t.Fatal(err)
		}
	}
	return login(t, e.srv, name, testPassword)
}

func (e *securityEnv) setting(t *testing.T, key model.ServiceKey, on bool) {
	t.Helper()
	if _, err := e.st.SetPreDeployScan(t.Context(), key, on, 0, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func (e *securityEnv) tagService(t *testing.T, key model.ServiceKey, tag string) {
	t.Helper()
	id := e.tagID(t, tag)
	if err := e.st.SetServiceTag(t.Context(), key, &id); err != nil {
		t.Fatal(err)
	}
}

// definitionOf returns the service's deployed definition and revision as the edit screen would read them.
func definitionOf(t *testing.T, c *apiClient, key model.ServiceKey) (map[string]any, int) {
	t.Helper()
	var out struct {
		Definition       map[string]any `json:"definition"`
		DeployedRevision int            `json:"deployed_revision"`
	}
	resp := c.get("/api/services/" + key.ID() + "/task-definition")
	wantStatus(t, resp, http.StatusOK)
	decode(t, resp, &out)
	return out.Definition, out.DeployedRevision
}

// setImage sets the image of the named container; a name that is not there is added as a container.
func setImage(def map[string]any, container, image string) {
	containers := def["containerDefinitions"].([]any)
	for _, entry := range containers {
		if c := entry.(map[string]any); c["name"] == container {
			c["image"] = image
			return
		}
	}
	def["containerDefinitions"] = append(containers, map[string]any{"name": container, "image": image, "essential": false})
}

// gateBody is a change set that registers def, optionally answering the gate.
func gateBody(revision int, def map[string]any, gate *ScanGateRequest) map[string]any {
	body := map[string]any{"base_revision": revision, "task_definition": map[string]any{"definition": def}}
	if gate != nil {
		body["scan_gate"] = gate
	}
	return body
}

// applyImages submits a change set that sets the images of the given containers (container, image pairs).
func applyImages(t *testing.T, c *apiClient, key model.ServiceKey, gate *ScanGateRequest, containerImage ...string) *http.Response {
	t.Helper()
	def, revision := definitionOf(t, c, key)
	for i := 0; i < len(containerImage); i += 2 {
		setImage(def, containerImage[i], containerImage[i+1])
	}
	return c.post("/api/services/"+key.ID()+"/changeset", gateBody(revision, def, gate))
}

type gateResponse struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	ScanGate ScanGateErrorDTO `json:"scan_gate"`
}

// wantGate checks the answer is the 409 scan_gate pause and returns its rows by container.
func wantGate(t *testing.T, resp *http.Response) map[string]ScanGateImageDTO {
	t.Helper()
	if resp.StatusCode != http.StatusConflict {
		code, message := "", ""
		if resp.StatusCode >= 400 {
			code, message = errorCode(t, resp)
		}
		t.Fatalf("status %d (%s: %s), want 409 scan_gate", resp.StatusCode, code, message)
	}
	var out gateResponse
	decode(t, resp, &out)
	if out.Error.Code != "scan_gate" || out.Error.Message == "" || out.ScanGate.Message == "" {
		t.Fatalf("409 body = %+v, want code scan_gate with messages", out)
	}
	rows := map[string]ScanGateImageDTO{}
	for _, row := range out.ScanGate.Images {
		rows[row.Container] = row
	}
	return rows
}

func wantApplied(t *testing.T, resp *http.Response) ChangeSetResponse {
	t.Helper()
	wantStatus(t, resp, http.StatusOK)
	var out ChangeSetResponse
	decode(t, resp, &out)
	return out
}

func anyway(rows ...ScanGateImageDTO) *ScanGateRequest {
	gate := &ScanGateRequest{DeployAnyway: true}
	for _, row := range rows {
		gate.Images = append(gate.Images, ScanGateAck{Container: row.Container, Ref: row.Ref})
	}
	return gate
}

func (e *securityEnv) deployAnywayRows(t *testing.T) int {
	t.Helper()
	return countRows(t, e.st, `select count(*) from operation_log where details->>'op' = 'deploy_anyway'`)
}

func (e *securityEnv) preDeployRequests(t *testing.T) int {
	t.Helper()
	return countRows(t, e.st, `select count(*) from scan_requests where source = 'pre_deploy'`)
}

func (e *securityEnv) scanRuns(t *testing.T) int {
	t.Helper()
	return countRows(t, e.st, `select count(*) from scan_runs`)
}

func TestChangeSet_SettingOffIsNotGated(t *testing.T) {
	env := newSecurityEnv(t)
	env.setting(t, edgeNginxKey, false)

	out := wantApplied(t, applyImages(t, env.dana, edgeNginxKey, nil, "edge-nginx", ecrImage(edgeNginxKey, "edge-nginx", "1.27.3")))
	if out.Status != "applied" {
		t.Fatalf("status = %q, want applied", out.Status)
	}
	if n := countRows(t, env.st, `select count(*) from scan_images`); n != 0 || len(env.scanner.Launches()) != 0 {
		t.Fatalf("%d images stored and %d scanner launches with the setting off, want none", n, len(env.scanner.Launches()))
	}
}

func TestChangeSet_ScanningOffIsNotGated(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	dana := login(t, srv, "dana", devseed.Password)

	// The seed turns the setting on for edge-nginx; with scanning off the gate must not look at it.
	wantApplied(t, applyImages(t, dana, edgeNginxKey, nil, "edge-nginx", ecrImage(edgeNginxKey, "edge-nginx", "1.27.3")))
	if n := countRows(t, st, `select count(*) from scan_images`); n != 0 {
		t.Fatalf("%d images stored while scanning is off", n)
	}
}

func TestChangeSet_GateQueuesThenPassesWhenClean(t *testing.T) {
	env := newSecurityEnv(t)
	image := ecrImage(edgeNginxKey, "edge-nginx", "1.27.3")

	rows := wantGate(t, applyImages(t, env.dana, edgeNginxKey, nil, "edge-nginx", image))
	digest := env.fk.Estate.ImageDigestFor("edge-nginx:1.27.3")
	row := rows["edge-nginx"]
	if len(rows) != 1 || row.State != scanStateQueued || row.Digest != digest || row.Ref != digest || row.Image != image {
		t.Fatalf("rows = %+v, want one queued row for %s", rows, digest)
	}
	if n := countRows(t, env.st, `select count(*) from scan_requests where source = 'pre_deploy' and digest = $1 and container = 'edge-nginx'
		and service_name = 'edge-nginx' and requested_by is not null`, digest); n != 1 {
		t.Fatalf("%d pre_deploy requests for the digest, want 1", n)
	}
	if env.fk.Calls()["RegisterTaskDefinition"] != 0 {
		t.Fatal("a task definition was registered while the gate paused the apply")
	}

	env.scanAll(t)
	runs := env.scanRuns(t)
	if out := wantApplied(t, applyImages(t, env.dana, edgeNginxKey, nil, "edge-nginx", image)); out.Status != "applied" {
		t.Fatalf("status = %q, want applied", out.Status)
	}
	if env.scanRuns(t) != runs {
		t.Fatal("the second apply started another scan run")
	}
}

func TestChangeSet_GateReusesFreshResult(t *testing.T) {
	env := newSecurityEnv(t)
	env.setting(t, certRenewerKey, true)
	shared := ecrImage(edgeNginxKey, "edge-nginx", "1.27.3")

	wantGate(t, applyImages(t, env.dana, edgeNginxKey, nil, "edge-nginx", shared))
	env.scanAll(t)
	wantApplied(t, applyImages(t, env.dana, edgeNginxKey, nil, "edge-nginx", shared))
	runs := env.scanRuns(t)

	wantApplied(t, applyImages(t, env.dana, certRenewerKey, nil, "cert-renewer", shared))
	if env.scanRuns(t) != runs {
		t.Fatal("a result already on hand started another run")
	}
	digest := env.fk.Estate.ImageDigestFor("edge-nginx:1.27.3")
	if n := countRows(t, env.st, `select count(*) from scan_requests where source = 'pre_deploy' and digest = $1 and service_name = 'cert-renewer'`, digest); n != 1 {
		t.Fatalf("%d requests for cert-renewer, want 1", n)
	}
}

func TestChangeSet_GateBindsToDigestResolvedAtDeploy(t *testing.T) {
	env := newSecurityEnv(t)
	mustRequestScan(t, env.dana, edgeNginxKey.ID(), "edge-nginx", "1.27.3")
	env.scanAll(t)
	oldDigest := env.fk.Estate.ImageDigestFor("edge-nginx:1.27.3")
	newDigest := env.fk.Estate.MoveTag("edge-nginx", "1.27.3")
	image := ecrImage(edgeNginxKey, "edge-nginx", "1.27.3")

	row := wantGate(t, applyImages(t, env.dana, edgeNginxKey, nil, "edge-nginx", image))["edge-nginx"]
	if row.State != scanStateQueued || row.Digest != newDigest || row.Digest == oldDigest {
		t.Fatalf("row = %+v, want queued for the moved digest %s", row, newDigest)
	}
	stale := &ScanGateRequest{DeployAnyway: true, Images: []ScanGateAck{{Container: "edge-nginx", Ref: oldDigest}}}
	wantGate(t, applyImages(t, env.dana, edgeNginxKey, stale, "edge-nginx", image))
}

func TestChangeSet_GateSeesCaseFoldedKeys(t *testing.T) {
	env := newSecurityEnv(t)
	mustRequestScan(t, env.dana, edgeNginxKey.ID(), "edge-nginx", "1.27.1")
	env.scanAll(t)

	def, revision := definitionOf(t, env.dana, edgeNginxKey)
	container := def["containerDefinitions"].([]any)[0].(map[string]any)
	delete(container, "image")
	container["Image"] = ecrImage(edgeNginxKey, "edge-nginx", "1.27.1")
	def["ContainerDefinitions"] = def["containerDefinitions"]
	delete(def, "containerDefinitions")

	rows := wantGate(t, env.dana.post("/api/services/"+edgeNginxKey.ID()+"/changeset", gateBody(revision, def, nil)))
	if rows["edge-nginx"].State != "critical" {
		t.Fatalf("rows = %+v, want the critical image seen under folded keys", rows)
	}
}

func TestChangeSet_GateScansSubmittedRepository(t *testing.T) {
	env := newSecurityEnv(t)
	env.setting(t, certRenewerKey, true)

	row := wantGate(t, applyImages(t, env.dana, certRenewerKey, nil, "cert-renewer", ecrImage(certRenewerKey, "edge-nginx", "1.27.3")))["cert-renewer"]
	digest := env.fk.Estate.ImageDigestFor("edge-nginx:1.27.3")
	if row.Digest != digest {
		t.Fatalf("row = %+v, want the digest of edge-nginx:1.27.3", row)
	}
	if n := countRows(t, env.st, `select count(*) from scan_images where digest = $1 and repository = 'edge-nginx'`, digest); n != 1 {
		t.Fatal("the scan was not filed under the submitted repository")
	}
}

func TestChangeSet_GateNewContainerIsScanned(t *testing.T) {
	env := newSecurityEnv(t)
	env.setting(t, certRenewerKey, true)

	rows := wantGate(t, applyImages(t, env.dana, certRenewerKey, nil, "extra", ecrImage(certRenewerKey, "edge-nginx", "1.27.3")))
	if row, ok := rows["extra"]; !ok || row.State != scanStateQueued {
		t.Fatalf("rows = %+v, want a queued row for the new container", rows)
	}
}

func TestChangeSet_GateDigestPinnedImage(t *testing.T) {
	env := newSecurityEnv(t)
	digest := env.fk.Estate.ImageDigestFor("edge-nginx:1.27.3")
	pinned := edgeNginxKey.AccountID + ".dkr.ecr." + edgeNginxKey.Region + ".amazonaws.com/edge-nginx@" + digest

	row := wantGate(t, applyImages(t, env.dana, edgeNginxKey, nil, "edge-nginx", pinned))["edge-nginx"]
	if row.State != scanStateQueued || row.Digest != digest {
		t.Fatalf("row = %+v, want queued for the pinned digest", row)
	}
}

func TestChangeSet_GateCriticalPausesAndDeployAnywayIsLogged(t *testing.T) {
	env := newSecurityEnv(t)
	image := ecrImage(edgeNginxKey, "edge-nginx", "1.27.1")
	digest := env.fk.Estate.ImageDigestFor("edge-nginx:1.27.1")

	if row := wantGate(t, applyImages(t, env.dana, edgeNginxKey, nil, "edge-nginx", image))["edge-nginx"]; row.State != scanStateQueued {
		t.Fatalf("first row = %+v, want queued", row)
	}
	env.scanAll(t)
	row := wantGate(t, applyImages(t, env.dana, edgeNginxKey, nil, "edge-nginx", image))["edge-nginx"]
	if row.State != "critical" || row.Open.Critical != 1 || row.Digest != digest {
		t.Fatalf("row = %+v, want critical with 1 open critical", row)
	}

	wrong := &ScanGateRequest{DeployAnyway: true, Images: []ScanGateAck{{Container: "sidecar", Ref: digest}}}
	wantGate(t, applyImages(t, env.dana, edgeNginxKey, wrong, "edge-nginx", image))
	if env.deployAnywayRows(t) != 0 {
		t.Fatal("a refused acknowledgement was logged")
	}

	wantApplied(t, applyImages(t, env.dana, edgeNginxKey, anyway(row), "edge-nginx", image))
	if n := countRows(t, env.st, `select count(*) from operation_log where details->>'op' = 'deploy_anyway' and service_name = 'edge-nginx'
		and actor_username = 'dana' and actor_user_id is not null and details::text like '%' || $1 || '%'`, digest); n != 1 {
		t.Fatalf("%d deploy_anyway operations naming dana and the digest, want 1", n)
	}
	entries := auditByAction(t, env.st, "security.deploy_anyway")
	if len(entries) != 1 || entries[0].ActorUsername != "dana" || entries[0].Target != "service:"+edgeNginxKey.ID() ||
		!strings.Contains(entries[0].Summary, "edge-nginx") {
		t.Fatalf("audit entries = %+v, want one by dana for the service", entries)
	}
}

func TestChangeSet_DeployAnywayNotLoggedWhenApplyFails(t *testing.T) {
	env := newSecurityEnv(t)
	image := ecrImage(edgeNginxKey, "edge-nginx", "1.27.1")
	wantGate(t, applyImages(t, env.dana, edgeNginxKey, nil, "edge-nginx", image))
	env.scanAll(t)
	row := wantGate(t, applyImages(t, env.dana, edgeNginxKey, nil, "edge-nginx", image))["edge-nginx"]

	def, _ := definitionOf(t, env.dana, edgeNginxKey)
	setImage(def, "edge-nginx", image)
	resp := env.dana.post("/api/services/"+edgeNginxKey.ID()+"/changeset", gateBody(18, def, anyway(row)))
	wantStatus(t, resp, http.StatusConflict)
	if code, _ := errorCode(t, resp); code != "conflict" {
		t.Fatalf("code = %q, want the stale base conflict", code)
	}
	if env.deployAnywayRows(t) != 0 || len(auditByAction(t, env.st, "security.deploy_anyway")) != 0 {
		t.Fatal("a Deploy anyway was logged although the apply failed")
	}
}

func TestChangeSet_RegisterOnlyDeployAnywayIsWordedRegistered(t *testing.T) {
	env := newSecurityEnv(t)
	editor := env.addUser(t, "editor", map[string]authz.ActionSet{"Dev": authz.ActionSet(authz.View | authz.Edit)})
	image := ecrImage(edgeNginxKey, "edge-nginx", "1.27.3")

	row := wantGate(t, applyImages(t, editor, edgeNginxKey, nil, "edge-nginx", image))["edge-nginx"]
	out := wantApplied(t, applyImages(t, editor, edgeNginxKey, anyway(row), "edge-nginx", image))
	if out.Status != "registered_only" {
		t.Fatalf("status = %q, want registered_only", out.Status)
	}
	if n := countRows(t, env.st, `select count(*) from operation_log where details->>'op' = 'deploy_anyway' and text like 'Registered anyway%'`); n != 1 {
		t.Fatalf("%d operations worded Registered anyway, want 1", n)
	}
}

func TestChangeSet_GateAcceptedCriticalPasses(t *testing.T) {
	env := newSecurityEnv(t)
	holder := env.addUser(t, "risk", map[string]authz.ActionSet{"Dev": authz.Presets["Deployer"] | authz.ActionSet(authz.AcceptRisk)})
	image := ecrImage(edgeNginxKey, "edge-nginx", "1.27.1")
	wantGate(t, applyImages(t, holder, edgeNginxKey, nil, "edge-nginx", image))
	env.scanAll(t)
	digest := env.fk.Estate.ImageDigestFor("edge-nginx:1.27.1")
	if row := wantGate(t, applyImages(t, holder, edgeNginxKey, nil, "edge-nginx", image))["edge-nginx"]; row.State != "critical" {
		t.Fatalf("row = %+v, want critical before the acceptance", row)
	}

	accept(t, holder, digest, edgeNginxCritical(), http.StatusCreated)
	wantApplied(t, applyImages(t, holder, edgeNginxKey, nil, "edge-nginx", image))
	if env.deployAnywayRows(t) != 0 {
		t.Fatal("an accepted critical was logged as Deploy anyway")
	}
}

func TestChangeSet_GateFailurePausesWithReason(t *testing.T) {
	env := newSecurityEnv(t)
	env.setting(t, certRenewerKey, true)
	env.scanner.FailPull("cert-renewer")
	image := ecrImage(certRenewerKey, "cert-renewer", "0.2.2")

	wantGate(t, applyImages(t, env.dana, certRenewerKey, nil, "cert-renewer", image))
	env.scanAll(t)
	row := wantGate(t, applyImages(t, env.dana, certRenewerKey, nil, "cert-renewer", image))["cert-renewer"]
	if row.State != scanStateFailed || row.Message != pullDeniedMessage {
		t.Fatalf("row = %+v, want failed with %q", row, pullDeniedMessage)
	}
	wantApplied(t, applyImages(t, env.dana, certRenewerKey, anyway(row), "cert-renewer", image))
}

func TestChangeSet_GateUnsupportedSidecarImage(t *testing.T) {
	env := newSecurityEnv(t)
	env.setting(t, securityPaymentsKey, true)
	otel := "public.ecr.aws/aws-observability/aws-otel-collector:v0.41.0"

	row := wantGate(t, applyImages(t, env.sam, securityPaymentsKey, nil, "otel-collector", otel))["otel-collector"]
	if row.State != scanStateUnsupported || row.Message != nonECRMessage || row.Ref != otel || row.Digest != "" {
		t.Fatalf("row = %+v, want unsupported with the copy and the image as ref", row)
	}
	wantApplied(t, applyImages(t, env.sam, securityPaymentsKey, anyway(row), "otel-collector", otel))
}

func TestGate_FailedImageStaysFailedUnderGateModeAndNothingIsLaunched(t *testing.T) {
	env := newSecurityEnv(t)
	env.setting(t, certRenewerKey, true)
	env.scanner.FailPull("cert-renewer")
	image := ecrImage(certRenewerKey, "cert-renewer", "0.2.2")
	wantGate(t, applyImages(t, env.dana, certRenewerKey, nil, "cert-renewer", image))
	env.scanAll(t)
	launches, runs := len(env.scanner.Launches()), env.scanRuns(t)

	for range 3 {
		if row := wantGate(t, applyImages(t, env.dana, certRenewerKey, nil, "cert-renewer", image))["cert-renewer"]; row.State != scanStateFailed {
			t.Fatalf("row = %+v, want failed", row)
		}
		env.scanAll(t)
	}
	digest := env.fk.Estate.ImageDigestFor("cert-renewer:0.2.2")
	img, err := env.st.GetScanImage(t.Context(), digest)
	if err != nil {
		t.Fatal(err)
	}
	if img.Status != "failed" || len(env.scanner.Launches()) != launches || env.scanRuns(t) != runs {
		t.Fatalf("image %s, %d launches (was %d), %d runs (was %d): the gate must not requeue a failed image",
			img.Status, len(env.scanner.Launches()), launches, env.scanRuns(t), runs)
	}
}

func TestChangeSet_GateECRThrottledOffersDeployAnyway(t *testing.T) {
	env := newSecurityEnv(t)
	image := ecrImage(edgeNginxKey, "edge-nginx", "1.27.3")
	env.fk.FailAlways("DescribeImages", &smithy.GenericAPIError{Code: "ThrottlingException", Message: "Rate exceeded"})

	row := wantGate(t, applyImages(t, env.dana, edgeNginxKey, nil, "edge-nginx", image))["edge-nginx"]
	want := "The image could not be checked in ECR (ThrottlingException). You can deploy anyway."
	if row.State != scanStateFailed || row.Message != want || row.Ref != image || row.Digest != "" {
		t.Fatalf("row = %+v, want failed with %q and the image as ref", row, want)
	}
	wantApplied(t, applyImages(t, env.dana, edgeNginxKey, anyway(row), "edge-nginx", image))
}

func TestChangeSet_GateUnconnectedRegistryIsUnsupported(t *testing.T) {
	env := newSecurityEnv(t)
	image := "999999999999.dkr.ecr.eu-central-1.amazonaws.com/edge-nginx:1.27.3"

	row := wantGate(t, applyImages(t, env.dana, edgeNginxKey, nil, "edge-nginx", image))["edge-nginx"]
	if row.State != scanStateUnsupported || row.Message != "Connect AWS account 999999999999 to scan this image." {
		t.Fatalf("row = %+v, want unsupported naming the account", row)
	}
}

func TestChangeSet_GateTimeoutReleasesIntoDeployAnyway(t *testing.T) {
	clock := newTestClock()
	env := newSecurityEnvClock(t, clock.Now)
	env.scanner.Delay = time.Hour
	image := ecrImage(edgeNginxKey, "edge-nginx", "1.27.3")

	wantGate(t, applyImages(t, env.dana, edgeNginxKey, nil, "edge-nginx", image))
	if err := env.runner.Cycle(t.Context()); err != nil {
		t.Fatal(err)
	}
	if row := wantGate(t, applyImages(t, env.dana, edgeNginxKey, nil, "edge-nginx", image))["edge-nginx"]; row.State != scanStateScanning {
		t.Fatalf("row = %+v, want scanning while the task runs", row)
	}

	// The first timeout retries once with a larger task; the second ends the scan.
	for range 2 {
		clock.Advance(scanTestConfig().TaskTimeout + time.Minute)
		if err := env.runner.Cycle(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	row := wantGate(t, applyImages(t, env.dana, edgeNginxKey, nil, "edge-nginx", image))["edge-nginx"]
	if row.State != scanStateFailed || row.Message != "The scan did not finish in time." {
		t.Fatalf("row = %+v, want failed after the timeout", row)
	}
	wantApplied(t, applyImages(t, env.dana, edgeNginxKey, anyway(row), "edge-nginx", image))
}

func TestChangeSet_GateLeaderDeadReleasesAfterTimeout(t *testing.T) {
	timeout := scanTestConfig().TaskTimeout
	const lateMessage = "The scan did not finish in time."

	t.Run("queued", func(t *testing.T) {
		clock := newTestClock()
		env := newSecurityEnvClock(t, clock.Now)
		image := ecrImage(edgeNginxKey, "edge-nginx", "1.27.3")
		digest := env.fk.Estate.ImageDigestFor("edge-nginx:1.27.3")

		wantGate(t, applyImages(t, env.dana, edgeNginxKey, nil, "edge-nginx", image))
		queuedAt := func() time.Time {
			img, err := env.st.GetScanImage(t.Context(), digest)
			if err != nil || img.QueuedAt == nil {
				t.Fatalf("image %v, queued_at %v", err, img.QueuedAt)
			}
			return *img.QueuedAt
		}
		first := queuedAt()
		clock.Advance(timeout / 2)
		if row := wantGate(t, applyImages(t, env.dana, edgeNginxKey, nil, "edge-nginx", image))["edge-nginx"]; row.State != scanStateQueued {
			t.Fatalf("row = %+v, want still queued before the timeout", row)
		}
		if !queuedAt().Equal(first) {
			t.Fatal("a repeated apply moved queued_at and reset the clock")
		}
		clock.Advance(timeout/2 + time.Minute)
		if row := wantGate(t, applyImages(t, env.dana, edgeNginxKey, nil, "edge-nginx", image))["edge-nginx"]; row.State != scanStateFailed || row.Message != lateMessage {
			t.Fatalf("row = %+v, want failed after the timeout with no loop running", row)
		}
	})

	t.Run("scanning", func(t *testing.T) {
		clock := newTestClock()
		env := newSecurityEnvClock(t, clock.Now)
		env.scanner.Delay = time.Hour
		image := ecrImage(edgeNginxKey, "edge-nginx", "1.27.3")

		wantGate(t, applyImages(t, env.dana, edgeNginxKey, nil, "edge-nginx", image))
		if err := env.runner.Cycle(t.Context()); err != nil {
			t.Fatal(err)
		}
		clock.Advance(timeout + time.Minute)
		if row := wantGate(t, applyImages(t, env.dana, edgeNginxKey, nil, "edge-nginx", image))["edge-nginx"]; row.State != scanStateScanning {
			t.Fatalf("row = %+v, want scanning until the run is timeout + 5 min old", row)
		}
		clock.Advance(5 * time.Minute)
		if row := wantGate(t, applyImages(t, env.dana, edgeNginxKey, nil, "edge-nginx", image))["edge-nginx"]; row.State != scanStateFailed || row.Message != lateMessage {
			t.Fatalf("row = %+v, want failed once the run is timeout + 5 min old", row)
		}
	})
}

func TestChangeSet_GateDBStaleReportsDbStale(t *testing.T) {
	env := newSecurityEnv(t)
	env.scanner.ForceStale(true)
	image := ecrImage(edgeNginxKey, "edge-nginx", "1.27.3")

	wantGate(t, applyImages(t, env.dana, edgeNginxKey, nil, "edge-nginx", image))
	if err := env.runner.Cycle(t.Context()); err != nil {
		t.Fatal(err)
	}
	row := wantGate(t, applyImages(t, env.dana, edgeNginxKey, nil, "edge-nginx", image))["edge-nginx"]
	if row.State != scanStateDBStale || row.Message == "" {
		t.Fatalf("row = %+v, want db_stale with the reason", row)
	}
}

func TestChangeSet_GateRefusesDirectCallWhenSettingOn(t *testing.T) {
	env := newSecurityEnv(t)
	image := ecrImage(edgeNginxKey, "edge-nginx", "1.27.1")
	wantGate(t, applyImages(t, env.dana, edgeNginxKey, nil, "edge-nginx", image))
	env.scanAll(t)

	// A scripted call that omits scan_gate, or sets it without acknowledging the image, never passes.
	for _, gate := range []*ScanGateRequest{nil, {DeployAnyway: true}, {Images: []ScanGateAck{{Container: "edge-nginx", Ref: env.fk.Estate.ImageDigestFor("edge-nginx:1.27.1")}}}} {
		wantGate(t, applyImages(t, env.dana, edgeNginxKey, gate, "edge-nginx", image))
	}
	if env.fk.Calls()["RegisterTaskDefinition"] != 0 {
		t.Fatal("a direct call registered a task definition past the gate")
	}
}

func TestChangeSet_GateRefusesViewerBeforeQueueing(t *testing.T) {
	env := newSecurityEnv(t)
	before := env.fk.Calls()["DescribeImages"]

	resp := applyImages(t, env.omer, edgeNginxKey, nil, "edge-nginx", ecrImage(edgeNginxKey, "edge-nginx", "1.27.3"))
	wantStatus(t, resp, http.StatusForbidden)
	if n := countRows(t, env.st, `select count(*) from scan_requests`); n != 0 || env.fk.Calls()["DescribeImages"] != before {
		t.Fatalf("%d requests and %d ECR calls for a caller without the actions, want none", n, env.fk.Calls()["DescribeImages"]-before)
	}
}

func TestChangeSet_RollbackByRevisionIsNeverGated(t *testing.T) {
	env := newSecurityEnv(t)
	env.setting(t, edgeNginxKey, true)
	_, revision := definitionOf(t, env.dana, edgeNginxKey)

	// Roll back to the previous revision, then change only the desired count.
	resp := env.dana.post("/api/services/"+edgeNginxKey.ID()+"/changeset", map[string]any{
		"base_revision": revision, "task_definition": map[string]any{"revision": revision - 1},
	})
	wantStatus(t, resp, http.StatusOK)
	var settings DeploySettingsResponse
	decode(t, env.dana.get("/api/services/"+edgeNginxKey.ID()+"/deploy-settings"), &settings)
	resp = env.dana.post("/api/services/"+edgeNginxKey.ID()+"/changeset", map[string]any{
		"base_version": settings.Version, "service": map[string]any{"desired_count": 3},
	})
	wantStatus(t, resp, http.StatusOK)
	if env.preDeployRequests(t) != 0 || countRows(t, env.st, `select count(*) from scan_images`) != 0 {
		t.Fatal("a rollback or a non-image change reached the gate")
	}
}

func TestChangeSet_MultiContainerGatesEveryChangedImage(t *testing.T) {
	env := newSecurityEnv(t)
	env.setting(t, stagingAPIKey, true)
	apiImage, migratorImage := ecrImage(stagingAPIKey, "api", "1.19.1-rc.2"), ecrImage(stagingAPIKey, "migrator", "1.4.3")

	rows := wantGate(t, applyImages(t, env.dana, stagingAPIKey, nil, "api", apiImage, "migrator", migratorImage))
	if len(rows) != 2 || rows["api"].State != scanStateQueued || rows["migrator"].State != scanStateQueued {
		t.Fatalf("rows = %+v, want one queued row per changed container", rows)
	}
	rest := wantGate(t, applyImages(t, env.dana, stagingAPIKey, anyway(rows["api"]), "api", apiImage, "migrator", migratorImage))
	if rest["migrator"].State != scanStateQueued {
		t.Fatalf("rows = %+v, want the unacknowledged container named", rest)
	}
	wantApplied(t, applyImages(t, env.dana, stagingAPIKey, anyway(rows["api"], rows["migrator"]), "api", apiImage, "migrator", migratorImage))
}

func TestRegisterRevision_IsGatedToo(t *testing.T) {
	env := newSecurityEnv(t)
	def, revision := definitionOf(t, env.dana, edgeNginxKey)
	setImage(def, "edge-nginx", ecrImage(edgeNginxKey, "edge-nginx", "1.27.3"))

	rows := wantGate(t, env.dana.post("/api/services/"+edgeNginxKey.ID()+"/revisions", map[string]any{"base_revision": revision, "definition": def}))
	if rows["edge-nginx"].State != scanStateQueued || env.fk.Calls()["RegisterTaskDefinition"] != 0 {
		t.Fatalf("rows = %+v, registrations = %d, want a queued row and nothing registered", rows, env.fk.Calls()["RegisterTaskDefinition"])
	}
}

func TestRegisterRevision_DeployAnywayPasses(t *testing.T) {
	env := newSecurityEnv(t)
	def, revision := definitionOf(t, env.dana, edgeNginxKey)
	setImage(def, "edge-nginx", ecrImage(edgeNginxKey, "edge-nginx", "1.27.3"))
	path := "/api/services/" + edgeNginxKey.ID() + "/revisions"
	row := wantGate(t, env.dana.post(path, map[string]any{"base_revision": revision, "definition": def}))["edge-nginx"]

	wantStatus(t, env.dana.post(path, map[string]any{"base_revision": revision, "definition": def, "scan_gate": anyway(row)}), http.StatusCreated)
	if n := countRows(t, env.st, `select count(*) from operation_log where details->>'op' = 'deploy_anyway' and text like 'Registered anyway%'`); n != 1 {
		t.Fatalf("%d operations worded Registered anyway, want 1", n)
	}
	entries := auditByAction(t, env.st, "security.deploy_anyway")
	if len(entries) != 1 || !strings.HasPrefix(entries[0].Summary, "Registered anyway") {
		t.Fatalf("audit entries = %+v, want one worded Registered anyway", entries)
	}
}

func TestChangeSet_DuplicateContainerNamesAreRefusedBeforeAnything(t *testing.T) {
	env := newSecurityEnv(t)
	def, revision := definitionOf(t, env.dana, edgeNginxKey)
	live := def["containerDefinitions"].([]any)[0].(map[string]any)
	evil := map[string]any{}
	for k, v := range live {
		evil[k] = v
	}
	evil["image"] = ecrImage(edgeNginxKey, "edge-nginx", "1.27.1")
	def["containerDefinitions"] = []any{evil, live}

	wantStatus(t, env.dana.post("/api/services/"+edgeNginxKey.ID()+"/changeset", gateBody(revision, def, nil)), http.StatusUnprocessableEntity)
	if env.fk.Calls()["RegisterTaskDefinition"] != 0 || countRows(t, env.st, `select count(*) from scan_images`) != 0 {
		t.Fatal("a duplicate container name registered a definition or queued a scan")
	}
}

func TestChangeSet_GateResolveTimeoutOffersDeployAnyway(t *testing.T) {
	old := gateResolveTimeout
	gateResolveTimeout = 100 * time.Millisecond
	t.Cleanup(func() { gateResolveTimeout = old })
	env := newSecurityEnv(t)
	env.fk.ECRDelay = time.Hour
	image := ecrImage(edgeNginxKey, "edge-nginx", "1.27.3")

	row := wantGate(t, applyImages(t, env.dana, edgeNginxKey, nil, "edge-nginx", image))["edge-nginx"]
	want := "The image could not be checked in ECR (timeout). You can deploy anyway."
	if row.State != scanStateFailed || row.Message != want || row.Ref != image {
		t.Fatalf("row = %+v, want failed with %q", row, want)
	}
	wantApplied(t, applyImages(t, env.dana, edgeNginxKey, anyway(row), "edge-nginx", image))
}

func TestScanCopy_TerminalTimeoutDoesNotPromiseARetry(t *testing.T) {
	env := newSecurityEnv(t)
	status := mustRequestScan(t, env.dana, edgeNginxKey.ID(), "edge-nginx", "1.27.3")
	digest := status.Image.Digest
	if err := env.st.FailScanImages(t.Context(), []string{digest}, "timeout", ""); err != nil {
		t.Fatal(err)
	}

	var detail SecurityImageDetailDTO
	decode(t, env.dana.get("/api/security/images/"+digest), &detail)
	if strings.Contains(detail.FailureAction, "retried once") || !strings.Contains(detail.FailureAction, "Rescan") {
		t.Errorf("image failure_action = %q, want no retry promise and a Rescan hint", detail.FailureAction)
	}
	got := scanStatus(t, env.dana, edgeNginxKey.ID(), "digest="+digest)
	if got.State != scanStateFailed || strings.Contains(got.Message, "retried once") {
		t.Errorf("scan status = %+v, want failed without a retry promise", got)
	}
}

func TestChangeSet_PartialDeployAnywayIsNotLoggedAsDeployed(t *testing.T) {
	env := newSecurityEnv(t)
	image := ecrImage(edgeNginxKey, "edge-nginx", "1.27.3")
	row := wantGate(t, applyImages(t, env.dana, edgeNginxKey, nil, "edge-nginx", image))["edge-nginx"]
	def, revision := definitionOf(t, env.dana, edgeNginxKey)
	setImage(def, "edge-nginx", image)
	var settings DeploySettingsResponse
	decode(t, env.dana.get("/api/services/"+edgeNginxKey.ID()+"/deploy-settings"), &settings)
	env.fk.FailNext("UpdateService", &smithy.GenericAPIError{Code: "ServerException", Message: "boom"})

	body := gateBody(revision, def, anyway(row))
	body["base_version"] = settings.Version
	body["service"] = map[string]any{"desired_count": 3}
	out := wantApplied(t, env.dana.post("/api/services/"+edgeNginxKey.ID()+"/changeset", body))
	if out.Status != "partial" {
		t.Fatalf("status = %q, want partial", out.Status)
	}
	if n := countRows(t, env.st, `select count(*) from operation_log where details->>'op' = 'deploy_anyway' and text like 'Registered anyway%'`); n != 1 {
		t.Fatalf("%d deploy_anyway operations worded Registered anyway, want 1", n)
	}
	if n := countRows(t, env.st, `select count(*) from operation_log where text like 'Deployed anyway%'`); n != 0 {
		t.Fatal("a change set that deployed nothing was logged as Deployed anyway")
	}
}

func TestChangeSet_GateBaselineOfAnotherFamilyGatesEverything(t *testing.T) {
	env := newSecurityEnv(t)
	def, revision := definitionOf(t, env.dana, edgeNginxKey)
	def["family"] = "some-other-family" // the image strings are unchanged from the base revision

	rows := wantGate(t, env.dana.post("/api/services/"+edgeNginxKey.ID()+"/changeset", gateBody(revision, def, nil)))
	if rows["edge-nginx"].State == "" {
		t.Fatalf("rows = %+v, want the container gated when the baseline family differs", rows)
	}
}
