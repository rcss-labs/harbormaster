package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/smithy-go"

	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/devseed"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/scan"
	"github.com/rcss-labs/harbormaster/internal/scanjob"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const (
	nonECRMessage     = "Only Amazon ECR private images are scanned in this version."
	pullDeniedMessage = "The harbormaster-scan role in account 482109375512 is missing, or the repository policy denies pulls."
)

var (
	edgeNginxKey  = model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: homelabAccountID, Region: "eu-central-1", Cluster: "edge-proxy"}, Service: "edge-nginx"}
	certRenewerID = serviceID(homelabAccountID, "eu-central-1", "edge-proxy", "cert-renewer")
	paymentsKey   = model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: homelabAccountID, Region: "us-east-1", Cluster: "core-services"}, Service: "payments-api"}
)

// securityEnv is a server with image scanning on and the fake scanner behind it.
type securityEnv struct {
	srv     *httptest.Server
	fk      *fake.Fake
	st      *store.Store
	scanner *fake.Scanner
	runner  *scanjob.Runner
	sam     *apiClient // global admin
	dana    *apiClient // Full on Dev, Deployer on Staging, View and Logs on Prod
	omer    *apiClient // Operator on Dev, Viewer on Staging
}

func scanTestConfig() config.ScanConfig {
	return config.ScanConfig{
		Enabled: true, RoleName: "harbormaster-scan", Interval: time.Hour, TaskTimeout: 30 * time.Minute,
		DBRefresh: 24 * time.Hour, DBMaxAge: 120 * time.Hour, MaxTasks: 1, BatchSize: 25, MaxImageMB: 4096,
		ClusterARN: "arn:aws:ecs:us-east-1:000000000000:cluster/harbormaster",
	}
}

func newSecurityEnv(t *testing.T) *securityEnv { return newSecurityEnvClock(t, time.Now) }

// newSecurityEnvClock is newSecurityEnv with the server and the scan loop reading now, so a test can move time.
func newSecurityEnvClock(t *testing.T, now func() time.Time) *securityEnv {
	t.Helper()
	env := &securityEnv{}
	srv, fk, st, _ := newServerCustom(t, true, func(s *Server) {
		env.fk = s.Clients.(*fake.Fake)
		env.scanner = fake.NewScanner(env.fk.Estate)
		env.runner = &scanjob.Runner{Store: s.Store, Launcher: env.scanner, Bucket: env.scanner, DB: env.scanner,
			Cfg: scanTestConfig(), Now: now, Log: s.Log}
		s.Now = now
		s.Cfg.Scan = scanTestConfig()
		s.Scan = env.runner
	})
	env.srv, env.fk, env.st = srv, fk, st
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	env.sam = login(t, srv, "sam", testPassword)
	env.dana = login(t, srv, "dana", devseed.Password)
	env.omer = login(t, srv, "omer", testPassword)
	return env
}

// scanAll runs scan cycles until no run is active and nothing is due.
func (e *securityEnv) scanAll(t *testing.T) {
	t.Helper()
	for range 10 {
		if err := e.runner.Cycle(t.Context()); err != nil {
			t.Fatalf("scan cycle: %v", err)
		}
		active, err := e.st.ActiveScanRuns(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		due, err := e.st.DueScanImages(t.Context(), time.Now(), 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(active) == 0 && len(due) == 0 {
			return
		}
	}
	t.Fatal("scan cycles did not settle")
}

func requestScan(t *testing.T, c *apiClient, serviceID, container, tag string, query string) (*http.Response, ScanStatusDTO) {
	t.Helper()
	resp := c.post("/api/security/services/"+serviceID+"/scans"+query, ScanRequestBody{Container: container, Tag: tag})
	var out ScanStatusDTO
	if resp.StatusCode == http.StatusOK {
		decode(t, resp, &out)
	}
	return resp, out
}

// mustRequestScan requests a scan that is expected to be accepted.
func mustRequestScan(t *testing.T, c *apiClient, serviceID, container, tag string) ScanStatusDTO {
	t.Helper()
	resp, status := requestScan(t, c, serviceID, container, tag, "")
	if resp.StatusCode != http.StatusOK || status.Image == nil {
		t.Fatalf("request for %s:%s = %d %+v, want 200 with an image", container, tag, resp.StatusCode, status)
	}
	return status
}

func scanStatus(t *testing.T, c *apiClient, serviceID, query string) ScanStatusDTO {
	t.Helper()
	resp := c.get("/api/security/services/" + serviceID + "/scans?" + query)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET scans?%s = %d", query, resp.StatusCode)
	}
	var out ScanStatusDTO
	decode(t, resp, &out)
	return out
}

func securityList(t *testing.T, c *apiClient) SecurityDTO {
	t.Helper()
	resp := c.get("/api/security")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/security = %d", resp.StatusCode)
	}
	var out SecurityDTO
	decode(t, resp, &out)
	return out
}

func TestSecurity_OffReturnsHintAndRefusesWrites(t *testing.T) {
	srv, fk, st, _ := newServerWithLog(t, true)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	sam := login(t, srv, "sam", testPassword)
	edge := edgeNginxKey.ID()

	list := securityList(t, sam)
	if list.Enabled || list.Hint != scanningOffHint {
		t.Fatalf("list enabled=%v hint=%q, want off with the hint", list.Enabled, list.Hint)
	}
	if list.Images == nil || len(list.Images) != 0 {
		t.Fatalf("images = %#v, want an empty array", list.Images)
	}

	resp := sam.get("/api/security/services/" + edge)
	var service SecurityServiceDTO
	decode(t, resp, &service)
	var detail SecurityImageDetailDTO
	decode(t, sam.get("/api/security/images/sha256:"+strings.Repeat("a", 64)), &detail)
	if detail.Enabled || detail.Hint != scanningOffHint {
		t.Fatalf("detail while off = enabled %v hint %q, want off with the hint", detail.Enabled, detail.Hint)
	}
	if service.Enabled || service.Images == nil {
		t.Fatalf("service = %+v, want off with an empty image list", service)
	}
	if status := scanStatus(t, sam, edge, "container=edge-nginx&tag=1.27.2"); status.Enabled || status.State != scanStateNone {
		t.Fatalf("scan status = %+v, want off", status)
	}

	for name, resp := range map[string]*http.Response{
		"setting": sam.do(http.MethodPut, "/api/security/services/"+edge+"/setting", SecuritySettingRequest{PreDeployScan: new(true)}, nil),
		"scan":    sam.post("/api/security/services/"+edge+"/scans", ScanRequestBody{Container: "edge-nginx", Tag: "1.27.2"}),
	} {
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("%s while off = %d, want 409", name, resp.StatusCode)
		}
		if code, _ := errorCode(t, resp); code != "scanning_off" {
			t.Fatalf("%s while off: code %q, want scanning_off", name, code)
		}
	}
	if n := countRows(t, st, `select count(*) from scan_images`); n != 0 {
		t.Fatalf("%d scan images stored while off", n)
	}
}

func TestScanSetting_DefaultOffNeedsDeployAndIsLogged(t *testing.T) {
	env := newSecurityEnv(t)
	edge := edgeNginxKey.ID()
	path := "/api/security/services/" + stagingWebID() + "/setting"

	resp := env.omer.do(http.MethodPut, "/api/security/services/"+edge+"/setting", SecuritySettingRequest{PreDeployScan: new(true)}, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("omer (Operator on Dev) = %d, want 403", resp.StatusCode)
	}
	if _, message := errorCode(t, resp); !strings.Contains(message, "Dev") {
		t.Fatalf("message %q should name Dev", message)
	}

	var before SecurityServiceDTO
	decode(t, env.dana.get("/api/security/services/"+stagingWebID()), &before)
	if !before.Enabled || before.PreDeployScan || !before.CanChangeSetting {
		t.Fatalf("before = %+v, want enabled, off, changeable by dana", before)
	}

	for range 2 {
		resp := env.dana.do(http.MethodPut, path, SecuritySettingRequest{PreDeployScan: new(true)}, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("dana PUT setting = %d, want 200", resp.StatusCode)
		}
		var after SecurityServiceDTO
		decode(t, resp, &after)
		if !after.PreDeployScan {
			t.Fatalf("after = %+v, want the setting on", after)
		}
	}
	operations := countRows(t, env.st, `select count(*) from operation_log where service_name = 'web' and source = 'harbormaster'
		and details->>'op' = 'scan_setting' and text = 'Turned pre-deploy image scanning on.'`)
	if operations != 1 {
		t.Fatalf("%d operation entries after two identical PUTs, want 1", operations)
	}
	entries := auditByAction(t, env.st, "security.setting")
	if len(entries) != 1 || entries[0].Target != "service:"+stagingWebID() || entries[0].ActorUsername != "dana" {
		t.Fatalf("audit entries = %+v, want one by dana for service:%s", entries, stagingWebID())
	}
	if n := countRows(t, env.st, `select count(*) from service_scan_settings where service_name = 'web' and updated_by is not null`); n != 1 {
		t.Fatalf("setting row without the user id: %d", n)
	}

	if resp := env.dana.do(http.MethodPut, path, SecuritySettingRequest{PreDeployScan: new(false)}, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("turning off = %d", resp.StatusCode)
	}
	if n := countRows(t, env.st, `select count(*) from operation_log where service_name = 'web' and details->>'op' = 'scan_setting'`); n != 2 {
		t.Fatalf("%d operation entries after turning off, want 2", n)
	}
}

func TestScanRequest_ResolvesTagAndQueues(t *testing.T) {
	env := newSecurityEnv(t)
	edge := edgeNginxKey.ID()

	resp, status := requestScan(t, env.dana, edge, "edge-nginx", "1.27.2", "")
	if resp.StatusCode != http.StatusOK || !status.Enabled || status.State != scanStateQueued {
		t.Fatalf("request = %d %+v, want 200 queued", resp.StatusCode, status)
	}
	wantDigest := env.fk.Estate.ImageDigestFor("edge-nginx:1.27.2")
	if status.Image == nil || status.Image.Digest != wantDigest || status.Image.Tag != "1.27.2" {
		t.Fatalf("image = %+v, want digest %s", status.Image, wantDigest)
	}
	if n := countRows(t, env.st, `select count(*) from scan_requests where digest = $1 and source = 'manual' and container = 'edge-nginx'
		and service_name = 'edge-nginx' and requested_by is not null`, wantDigest); n != 1 {
		t.Fatalf("%d manual scan_requests rows for the digest, want 1", n)
	}

	if n := countRows(t, env.st, `select count(*) from scan_images where digest = $1 and manifest_media_type = 'application/vnd.docker.distribution.manifest.v2+json'`, wantDigest); n != 1 {
		t.Fatal("the manifest media type reported by ECR was not stored with the image")
	}

	env.scanAll(t)
	done := scanStatus(t, env.dana, edge, "container=edge-nginx&tag=1.27.2")
	if done.State != scanStateScanned || done.Image == nil || done.Image.Open.Critical != 1 {
		t.Fatalf("after scanning = %+v, want scanned with 1 critical", done)
	}
	byDigest := scanStatus(t, env.dana, edge, "digest="+url.QueryEscape(wantDigest))
	if byDigest.State != scanStateScanned {
		t.Fatalf("poll by digest = %+v, want scanned", byDigest)
	}

	var service SecurityServiceDTO
	decode(t, env.dana.get("/api/security/services/"+edge), &service)
	if len(service.Images) != 1 || service.Images[0].Digest != wantDigest || service.Images[0].Open.Critical != 1 {
		t.Fatalf("service images = %+v, want the scanned image", service.Images)
	}
}

func TestScanRequest_ButtonWorksWithSettingOff(t *testing.T) {
	env := newSecurityEnv(t)

	var service SecurityServiceDTO
	decode(t, env.dana.get("/api/security/services/"+certRenewerID), &service)
	if service.PreDeployScan {
		t.Fatal("setting should default to off")
	}
	resp, status := requestScan(t, env.dana, certRenewerID, "cert-renewer", "0.2.1", "")
	if resp.StatusCode != http.StatusOK || status.State != scanStateQueued {
		t.Fatalf("button with the setting off = %d %+v, want 200 queued", resp.StatusCode, status)
	}
}

func TestScanRequest_RefusesNonECRAndUnknownTag(t *testing.T) {
	env := newSecurityEnv(t)
	payments := paymentsKey.ID()

	resp, _ := requestScan(t, env.sam, payments, "otel-collector", "v0.40.0", "")
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("otel sidecar = %d, want 422", resp.StatusCode)
	}
	if _, message := errorCode(t, resp); message != nonECRMessage {
		t.Fatalf("message = %q, want %q", message, nonECRMessage)
	}

	resp, _ = requestScan(t, env.sam, edgeNginxKey.ID(), "edge-nginx", "9.9.9", "")
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("unknown tag = %d, want 422", resp.StatusCode)
	}
	if _, message := errorCode(t, resp); message != "Tag 9.9.9 is not in ECR." {
		t.Fatalf("message = %q", message)
	}

	digest := env.fk.Estate.ImageDigestFor("edge-nginx:1.27.2")
	for _, tag := range []string{"x@" + digest, "1.27.2@" + digest, "bad tag", "-leading-dash", strings.Repeat("a", 129)} {
		resp, _ = requestScan(t, env.sam, edgeNginxKey.ID(), "edge-nginx", tag, "")
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("tag %q = %d, want 422", tag, resp.StatusCode)
		}
	}

	resp, _ = requestScan(t, env.sam, edgeNginxKey.ID(), "no-such-container", "1.27.2", "")
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("unknown container = %d, want 422", resp.StatusCode)
	}
	if n := countRows(t, env.st, `select count(*) from scan_images`); n != 0 {
		t.Fatalf("%d scan images stored by refused requests", n)
	}
}

func TestScanRequest_ECRThrottledIs503(t *testing.T) {
	env := newSecurityEnv(t)
	env.fk.FailNext("DescribeImages", &smithy.GenericAPIError{Code: "ThrottlingException", Message: "Rate exceeded"})

	resp, _ := requestScan(t, env.dana, edgeNginxKey.ID(), "edge-nginx", "1.27.2", "")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("throttled = %d, want 503", resp.StatusCode)
	}
	if got := resp.Header.Get("Retry-After"); got != "30" {
		t.Fatalf("Retry-After = %q, want 30", got)
	}
	if n := countRows(t, env.st, `select count(*) from scan_images`); n != 0 {
		t.Fatalf("%d scan images stored by a throttled request", n)
	}
}

func TestScanRequest_ReusesFreshResult(t *testing.T) {
	env := newSecurityEnv(t)
	edge := edgeNginxKey.ID()
	mustRequestScan(t, env.dana, edge, "edge-nginx", "1.27.2")
	env.scanAll(t)
	launches := len(env.scanner.Launches())
	var firstAt time.Time
	if err := env.st.Pool.QueryRow(t.Context(), `select requested_at from scan_requests where service_name = 'edge-nginx'`).Scan(&firstAt); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)

	resp, status := requestScan(t, env.dana, edge, "edge-nginx", "1.27.2", "")
	if resp.StatusCode != http.StatusOK || status.State != scanStateScanned {
		t.Fatalf("second request = %d %+v, want scanned", resp.StatusCode, status)
	}
	env.scanAll(t)
	if got := len(env.scanner.Launches()); got != launches {
		t.Fatalf("launches = %d after a reused request, want %d", got, launches)
	}
	var secondAt time.Time
	if err := env.st.Pool.QueryRow(t.Context(), `select requested_at from scan_requests where service_name = 'edge-nginx'`).Scan(&secondAt); err != nil {
		t.Fatal(err)
	}
	if !secondAt.After(firstAt) {
		t.Fatalf("request time %v did not move past %v", secondAt, firstAt)
	}

	resp, status = requestScan(t, env.dana, edge, "edge-nginx", "1.27.2", "?rescan=true")
	if resp.StatusCode != http.StatusOK || status.State != scanStateQueued {
		t.Fatalf("rescan = %d %+v, want queued", resp.StatusCode, status)
	}
	env.scanAll(t)
	if got := len(env.scanner.Launches()); got != launches+1 {
		t.Fatalf("launches = %d after a rescan, want %d", got, launches+1)
	}
}

func TestScanRequest_FailedIsRequeuedByTheButton(t *testing.T) {
	env := newSecurityEnv(t)
	env.scanner.FailPull("cert-renewer")

	mustRequestScan(t, env.dana, certRenewerID, "cert-renewer", "0.2.1")
	env.scanAll(t)
	if status := scanStatus(t, env.dana, certRenewerID, "container=cert-renewer&tag=0.2.1"); status.State != scanStateFailed {
		t.Fatalf("first scan = %+v, want failed", status)
	}
	launches := len(env.scanner.Launches())

	resp, status := requestScan(t, env.dana, certRenewerID, "cert-renewer", "0.2.1", "")
	if resp.StatusCode != http.StatusOK || status.State != scanStateQueued {
		t.Fatalf("button on a failed image = %d %+v, want queued", resp.StatusCode, status)
	}
	env.scanAll(t)
	if got := len(env.scanner.Launches()); got != launches+1 {
		t.Fatalf("launches = %d, want %d: the failed image should be scanned again", got, launches+1)
	}
}

func TestScanRequest_StaleResultIsRequeued(t *testing.T) {
	env := newSecurityEnv(t)
	edge := edgeNginxKey.ID()
	mustRequestScan(t, env.dana, edge, "edge-nginx", "1.27.2")
	env.scanAll(t)
	launches := len(env.scanner.Launches())
	if _, err := env.st.Pool.Exec(t.Context(), `update scan_images set matched_db_built_at = now() - interval '200 hours'`); err != nil {
		t.Fatal(err)
	}

	resp, status := requestScan(t, env.dana, edge, "edge-nginx", "1.27.2", "")
	if resp.StatusCode != http.StatusOK || status.State != scanStateQueued {
		t.Fatalf("request against stale data = %d %+v, want queued", resp.StatusCode, status)
	}
	env.scanAll(t)
	if got := len(env.scanner.Launches()); got != launches+1 {
		t.Fatalf("launches = %d, want %d: stale data should be matched again", got, launches+1)
	}
}

func TestScanRequest_NeedsDeployAndInvisibleIs404(t *testing.T) {
	env := newSecurityEnv(t)

	resp, _ := requestScan(t, env.omer, edgeNginxKey.ID(), "edge-nginx", "1.27.2", "")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("omer without Deploy = %d, want 403", resp.StatusCode)
	}
	if _, message := errorCode(t, resp); !strings.Contains(message, "Dev") {
		t.Fatalf("message %q should name Dev", message)
	}

	payments := paymentsKey.ID()
	resp, _ = requestScan(t, env.omer, payments, "payments-api", "1.18.2", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("omer on a Prod service = %d, want 404", resp.StatusCode)
	}
	for _, path := range []string{"/api/security/services/" + payments, "/api/security/services/" + payments + "/scans?container=payments-api&tag=1.18.2"} {
		if got := env.omer.get(path).StatusCode; got != http.StatusNotFound {
			t.Fatalf("omer GET %s = %d, want 404", path, got)
		}
	}
	// dana may see Prod but holds no Deploy there
	resp, _ = requestScan(t, env.dana, payments, "payments-api", "1.18.2", "")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("dana (View on Prod) = %d, want 403", resp.StatusCode)
	}
	if n := countRows(t, env.st, `select count(*) from scan_images`); n != 0 {
		t.Fatalf("%d scan images stored by refused requests", n)
	}
}

func TestSecurity_ListShowsOnlyRequestedAndVisible(t *testing.T) {
	env := newSecurityEnv(t)
	if list := securityList(t, env.sam); len(list.Images) != 0 {
		t.Fatalf("images before any request = %d, want none", len(list.Images))
	}
	devStatus := mustRequestScan(t, env.sam, edgeNginxKey.ID(), "edge-nginx", "1.27.2")
	prodStatus := mustRequestScan(t, env.sam, paymentsKey.ID(), "payments-api", "1.18.2")
	env.scanAll(t)

	list := securityList(t, env.omer)
	if !list.Enabled || list.Note != securityNote || list.Hint != "" {
		t.Fatalf("list = enabled %v note %q hint %q", list.Enabled, list.Note, list.Hint)
	}
	if len(list.Images) != 1 || list.Images[0].Digest != devStatus.Image.Digest {
		t.Fatalf("omer sees %d images, want only the Dev one", len(list.Images))
	}
	image := list.Images[0]
	if len(image.Requests) != 1 || image.Requests[0].Service != "edge-nginx" || image.Requests[0].PermissionTag.Name != "Dev" {
		t.Fatalf("requests = %+v, want the edge-nginx request tagged Dev", image.Requests)
	}
	if image.Can.Accept || image.Can.Rescan {
		t.Fatalf("omer can = %+v, want neither", image.Can)
	}
	if got := env.omer.get("/api/security/images/" + url.PathEscape(prodStatus.Image.Digest)).StatusCode; got != http.StatusNotFound {
		t.Fatalf("detail of a Prod-only image for omer = %d, want 404", got)
	}
	if got := env.omer.get("/api/security/images/" + url.PathEscape(devStatus.Image.Digest)).StatusCode; got != http.StatusOK {
		t.Fatalf("detail of the Dev image for omer = %d, want 200", got)
	}

	// The same image requested later by a Prod service stays invisible to omer, and ordering follows the newest request.
	if _, err := env.st.AddScanRequest(t.Context(), store.ScanRequest{Digest: devStatus.Image.Digest, Key: paymentsKey,
		Container: "payments-api", Image: image.Image, Tag: image.Tag, Source: "manual", RequestedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if got := securityList(t, env.omer).Images[0].Requests; len(got) != 1 {
		t.Fatalf("omer sees %d requests after a Prod service asked too, want 1", len(got))
	}
	samList := securityList(t, env.sam)
	if len(samList.Images) != 2 || len(samList.Images[0].Requests)+len(samList.Images[1].Requests) != 3 {
		t.Fatalf("sam sees %d images, want 2 with 3 requests in all", len(samList.Images))
	}
	if !samList.Images[0].Can.Accept && !samList.Images[1].Can.Accept {
		t.Fatal("a global admin should be able to accept risk")
	}
	for _, image := range securityList(t, env.dana).Images {
		if want := image.Digest == devStatus.Image.Digest; image.Can.Rescan != want {
			t.Fatalf("dana can.rescan on %s = %v, want %v (Deploy on Dev only)", image.Repository, image.Can.Rescan, want)
		}
	}
}

func TestSecurity_FailedImageShowsReason(t *testing.T) {
	env := newSecurityEnv(t)
	env.scanner.FailPull("cert-renewer")
	mustRequestScan(t, env.dana, certRenewerID, "cert-renewer", "0.2.1")
	env.scanAll(t)

	list := securityList(t, env.dana)
	if len(list.Images) != 1 {
		t.Fatalf("images = %d, want 1", len(list.Images))
	}
	image := list.Images[0]
	if image.Status != scan.StatusFailed || image.FailureCode != scan.CodePullDenied || image.FailureAction != pullDeniedMessage {
		t.Fatalf("image = status %q code %q action %q", image.Status, image.FailureCode, image.FailureAction)
	}
	status := scanStatus(t, env.dana, certRenewerID, "container=cert-renewer&tag=0.2.1")
	if status.State != scanStateFailed || status.Message != pullDeniedMessage {
		t.Fatalf("status = %+v, want failed with the pull_denied copy", status)
	}
}

func TestSecurity_RefreshingIgnoredAfterFifteenMinutes(t *testing.T) {
	env := newSecurityEnv(t)
	edge := edgeNginxKey.ID()
	mustRequestScan(t, env.dana, edge, "edge-nginx", "1.27.2")
	builtAt := time.Now().Add(-6 * time.Hour)

	for _, tc := range []struct {
		name       string
		since      time.Duration
		refreshing bool
		state      string
	}{
		{"recent marker", 5 * time.Minute, true, scanStateRefreshingDB},
		{"stale marker", 20 * time.Minute, false, scanStateQueued},
	} {
		since := time.Now().Add(-tc.since)
		if err := env.st.SetScanDBState(t.Context(), store.ScanDBState{BuiltAt: &builtAt, RefreshingSince: &since}); err != nil {
			t.Fatal(err)
		}
		if got := securityList(t, env.dana).DB.Refreshing; got != tc.refreshing {
			t.Fatalf("%s: db.refreshing = %v, want %v", tc.name, got, tc.refreshing)
		}
		status := scanStatus(t, env.dana, edge, "container=edge-nginx&tag=1.27.2")
		if status.State != tc.state {
			t.Fatalf("%s: state = %q, want %q", tc.name, status.State, tc.state)
		}
		if tc.refreshing && status.Message != updatingVulnDataText {
			t.Fatalf("%s: message = %q", tc.name, status.Message)
		}
	}
}

func TestSecurity_DetailListsFindingsWithAcceptancesOfVisibleServices(t *testing.T) {
	env := newSecurityEnv(t)
	status := mustRequestScan(t, env.dana, edgeNginxKey.ID(), "edge-nginx", "1.27.2")
	env.scanAll(t)
	digest := status.Image.Digest
	// A Prod service asked for the same image too.
	if _, err := env.st.AddScanRequest(t.Context(), store.ScanRequest{Digest: digest, Key: paymentsKey, Container: "payments-api",
		Image: status.Image.Image, Tag: "1.27.2", Source: "manual", RequestedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	dana, err := env.st.GetUserByUsername(t.Context(), "dana")
	if err != nil {
		t.Fatal(err)
	}
	sam, err := env.st.GetUserByUsername(t.Context(), "sam")
	if err != nil {
		t.Fatal(err)
	}
	accept := func(by int64, service model.ServiceKey) {
		t.Helper()
		base := store.ScanAcceptance{Digest: digest, VulnID: "CVE-0000-1042", PackageName: "openssl", PackageVersion: "3.0.13",
			Reason: "patched in the base image next week", AcceptedBy: &by, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(7 * 24 * time.Hour)}
		if _, err := env.st.CreateScanAcceptances(t.Context(), base, []model.ServiceKey{service}); err != nil {
			t.Fatal(err)
		}
	}
	accept(dana.ID, edgeNginxKey)

	detailOf := func(c *apiClient) SecurityImageDetailDTO {
		t.Helper()
		resp := c.get("/api/security/images/" + url.PathEscape(digest))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("detail = %d", resp.StatusCode)
		}
		var detail SecurityImageDetailDTO
		decode(t, resp, &detail)
		if !detail.Enabled || len(detail.Findings) != 3 || detail.Findings[0].VulnID != "CVE-0000-1042" {
			t.Fatalf("detail = enabled %v findings %+v, want three, the critical first", detail.Enabled, detail.Findings)
		}
		return detail
	}
	finding := func(c *apiClient) SecurityFindingDTO { return detailOf(c).Findings[0] }

	// omer sees only the Dev acceptance; every service he can see has accepted, so the finding is accepted for him and
	// the counts say the same.
	omers := detailOf(env.omer)
	if f := omers.Findings[0]; len(f.Acceptances) != 1 || f.Acceptances[0].Service != "edge-nginx" || f.Acceptances[0].CanRevoke || !f.AcceptedEverywhere {
		t.Fatalf("omer's finding = %+v", f)
	}
	if omers.Open.Critical != 0 || omers.Accepted != 1 {
		t.Fatalf("omer's counts: open critical %d accepted %d, want 0 and 1", omers.Open.Critical, omers.Accepted)
	}
	// the Prod service has not accepted, so for sam it is open and not accepted everywhere
	sams := detailOf(env.sam)
	if f := sams.Findings[0]; len(f.Acceptances) != 1 || !f.Acceptances[0].CanRevoke || f.AcceptedEverywhere {
		t.Fatalf("sam's finding = %+v", f)
	}
	if sams.Open.Critical != 1 || sams.Accepted != 0 {
		t.Fatalf("sam's counts: open critical %d accepted %d, want 1 and 0", sams.Open.Critical, sams.Accepted)
	}
	// the accepter may revoke without holding Accept risk
	if danas := finding(env.dana); len(danas.Acceptances) != 1 || !danas.Acceptances[0].CanRevoke || danas.Acceptances[0].By != "dana" {
		t.Fatalf("dana's view = %+v", danas.Acceptances)
	}
	accept(sam.ID, paymentsKey)
	both := detailOf(env.sam)
	if f := both.Findings[0]; len(f.Acceptances) != 2 || !f.AcceptedEverywhere || both.Open.Critical != 0 || both.Accepted != 1 {
		t.Fatalf("sam's view after both accepted = %+v open critical %d accepted %d", f, both.Open.Critical, both.Accepted)
	}
	if finding(env.omer).Acceptances[0].ServiceID != edgeNginxKey.ID() {
		t.Fatal("omer's acceptance should name the edge-nginx service id")
	}
}

func TestSecurity_ListCountsCoverTheServicesTheCallerCanSee(t *testing.T) {
	env := newSecurityEnv(t)
	status := mustRequestScan(t, env.dana, edgeNginxKey.ID(), "edge-nginx", "1.27.2")
	env.scanAll(t)
	digest := status.Image.Digest
	if _, err := env.st.AddScanRequest(t.Context(), store.ScanRequest{Digest: digest, Key: paymentsKey, Container: "payments-api",
		Image: status.Image.Image, Tag: "1.27.2", Source: "manual", RequestedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	dana, err := env.st.GetUserByUsername(t.Context(), "dana")
	if err != nil {
		t.Fatal(err)
	}
	// The Dev service accepts all three findings; the Prod service accepts none.
	for _, f := range []struct{ id, pkg, version string }{{"CVE-0000-1042", "openssl", "3.0.13"}, {"CVE-0000-2210", "nginx", "1.27.2"}, {"CVE-0000-3188", "zlib", "1.3"}} {
		base := store.ScanAcceptance{Digest: digest, VulnID: f.id, PackageName: f.pkg, PackageVersion: f.version, Reason: "accepted for the Dev service",
			AcceptedBy: &dana.ID, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(24 * time.Hour)}
		if _, err := env.st.CreateScanAcceptances(t.Context(), base, []model.ServiceKey{edgeNginxKey}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		who                  string
		c                    *apiClient
		wantOpen, wantAccept int
	}{{"omer sees only the Dev service", env.omer, 0, 3}, {"sam sees both services", env.sam, 3, 0}} {
		list := securityList(t, tc.c)
		if len(list.Images) != 1 {
			t.Fatalf("%s: %d images, want 1", tc.who, len(list.Images))
		}
		image := list.Images[0]
		if open := image.Open.Critical + image.Open.High + image.Open.Medium; open != tc.wantOpen || image.Accepted != tc.wantAccept {
			t.Fatalf("%s: open %d accepted %d, want %d and %d", tc.who, open, image.Accepted, tc.wantOpen, tc.wantAccept)
		}
	}
}
