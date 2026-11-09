package api

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/authz"
)

func edgeNginxCritical() AcceptRiskRequest {
	return AcceptRiskRequest{VulnID: "CVE-0000-1042", PackageName: "openssl", PackageVersion: "3.0.13", Reason: "patched in the next base image", Days: 30}
}

func acceptancesPath(digest string) string {
	return "/api/security/images/" + url.PathEscape(digest) + "/acceptances"
}

func acceptRaw(c *apiClient, digest string, body AcceptRiskRequest) *http.Response {
	return c.post(acceptancesPath(digest), body)
}

// accept posts an acceptance that must answer want and returns the ids written.
func accept(t *testing.T, c *apiClient, digest string, body AcceptRiskRequest, want int) AcceptRiskResponse {
	t.Helper()
	resp := acceptRaw(c, digest, body)
	wantStatus(t, resp, want)
	var out AcceptRiskResponse
	if want == http.StatusCreated {
		decode(t, resp, &out)
	}
	return out
}

func revoke(c *apiClient, id int64) *http.Response {
	return c.do(http.MethodDelete, fmt.Sprintf("/api/security/acceptances/%d", id), nil, nil)
}

// scannedCriticalEdgeNginx scans edge-nginx:1.27.1 (one critical) from the button and returns its digest.
func scannedCriticalEdgeNginx(t *testing.T, env *securityEnv) string {
	t.Helper()
	mustRequestScan(t, env.dana, edgeNginxKey.ID(), "edge-nginx", "1.27.1")
	env.scanAll(t)
	return env.fk.Estate.ImageDigestFor("edge-nginx:1.27.1")
}

func (e *securityEnv) riskHolder(t *testing.T, name string) *apiClient {
	t.Helper()
	return e.addUser(t, name, map[string]authz.ActionSet{"Dev": authz.Presets["Deployer"] | authz.ActionSet(authz.AcceptRisk)})
}

// twoServicesRequestCritical makes edge-nginx a Staging service and cert-renewer a Prod one, both with the setting on,
// and has both ask for the same critical image: edge-nginx:1.27.1 in the registry they share.
func twoServicesRequestCritical(t *testing.T, env *securityEnv) (digest string) {
	t.Helper()
	env.tagService(t, edgeNginxKey, "Staging")
	env.tagService(t, certRenewerKey, "Prod")
	env.setting(t, certRenewerKey, true)
	image := ecrImage(edgeNginxKey, "edge-nginx", "1.27.1")
	wantGate(t, applyImages(t, env.sam, edgeNginxKey, nil, "edge-nginx", image))
	wantGate(t, applyImages(t, env.sam, certRenewerKey, nil, "cert-renewer", image))
	env.scanAll(t)
	return env.fk.Estate.ImageDigestFor("edge-nginx:1.27.1")
}

func stagingAndProdHolders(env *securityEnv, t *testing.T) (staging, prod *apiClient) {
	t.Helper()
	risk := authz.Presets["Deployer"] | authz.ActionSet(authz.AcceptRisk)
	return env.addUser(t, "stg", map[string]authz.ActionSet{"Staging": risk}),
		env.addUser(t, "prd", map[string]authz.ActionSet{"Prod": risk})
}

func TestAcceptRisk_HidesOnlyForTheServicesItWasMadeFor(t *testing.T) {
	env := newSecurityEnv(t)
	digest := twoServicesRequestCritical(t, env)
	staging, prod := stagingAndProdHolders(env, t)
	image := ecrImage(edgeNginxKey, "edge-nginx", "1.27.1")

	for _, gate := range []struct {
		c   *apiClient
		key string
	}{{staging, "edge-nginx"}, {prod, "cert-renewer"}} {
		key := edgeNginxKey
		if gate.key == "cert-renewer" {
			key = certRenewerKey
		}
		if row := wantGate(t, applyImages(t, gate.c, key, nil, gate.key, image))[gate.key]; row.State != "critical" {
			t.Fatalf("%s row = %+v, want critical before any acceptance", gate.key, row)
		}
	}

	// Default services: the requesting services the caller holds Accept risk on, so Staging only.
	if ids := accept(t, staging, digest, edgeNginxCritical(), http.StatusCreated); len(ids.IDs) != 1 {
		t.Fatalf("staging holder wrote %d acceptances, want 1", len(ids.IDs))
	}
	wantApplied(t, applyImages(t, staging, edgeNginxKey, nil, "edge-nginx", image))
	if row := wantGate(t, applyImages(t, prod, certRenewerKey, nil, "cert-renewer", image))["cert-renewer"]; row.State != "critical" || row.Open.Critical != 1 {
		t.Fatalf("prod row = %+v, want still critical", row)
	}
	if got := securityImage(t, env.sam, digest); got.Accepted != 0 || got.Open.Critical != 1 {
		t.Fatalf("list after Staging only: open critical %d accepted %d, want 1 and 0", got.Open.Critical, got.Accepted)
	}

	accept(t, prod, digest, edgeNginxCritical(), http.StatusCreated)
	wantApplied(t, applyImages(t, prod, certRenewerKey, nil, "cert-renewer", image))
	if got := securityImage(t, env.sam, digest); got.Accepted != 1 || got.Open.Critical != 0 {
		t.Fatalf("list after both: open critical %d accepted %d, want 0 and 1", got.Open.Critical, got.Accepted)
	}
}

func securityImage(t *testing.T, c *apiClient, digest string) SecurityImageDTO {
	t.Helper()
	for _, img := range securityList(t, c).Images {
		if img.Digest == digest {
			return img
		}
	}
	t.Fatalf("image %s not in the list", digest)
	return SecurityImageDTO{}
}

func TestAcceptRisk_NeedsPermissionOnEachChosenService(t *testing.T) {
	env := newSecurityEnv(t)
	digest := twoServicesRequestCritical(t, env)
	risk := authz.Presets["Deployer"] | authz.ActionSet(authz.AcceptRisk)
	sees := env.addUser(t, "sees-prod", map[string]authz.ActionSet{"Staging": risk, "Prod": authz.Presets["Viewer"]})
	blind := env.addUser(t, "blind", map[string]authz.ActionSet{"Staging": risk})
	both := []string{edgeNginxKey.ID(), certRenewerKey.ID()}

	body := edgeNginxCritical()
	body.ServiceIDs = both
	resp := acceptRaw(sees, digest, body)
	wantStatus(t, resp, http.StatusForbidden)
	if _, message := errorCode(t, resp); !strings.Contains(message, "Prod") {
		t.Fatalf("message %q should name Prod for a caller who can see it", message)
	}
	resp = acceptRaw(blind, digest, body)
	wantStatus(t, resp, http.StatusForbidden)
	if _, message := errorCode(t, resp); strings.Contains(message, "Prod") {
		t.Fatalf("message %q names a tag the caller cannot see", message)
	}
	if n := countRows(t, env.st, `select count(*) from scan_risk_acceptances`); n != 0 {
		t.Fatalf("%d acceptances written by refused calls, want none", n)
	}
	if ids := accept(t, env.sam, digest, withServices(edgeNginxCritical(), both...), http.StatusCreated); len(ids.IDs) != 2 {
		t.Fatalf("admin wrote %d acceptances, want one per service", len(ids.IDs))
	}
}

func withServices(body AcceptRiskRequest, ids ...string) AcceptRiskRequest {
	body.ServiceIDs = ids
	return body
}

func TestAcceptRisk_LaterRequesterStillCounts(t *testing.T) {
	env := newSecurityEnv(t)
	env.tagService(t, edgeNginxKey, "Staging")
	env.setting(t, certRenewerKey, true)
	staging, _ := stagingAndProdHolders(env, t)
	image := ecrImage(edgeNginxKey, "edge-nginx", "1.27.1")
	wantGate(t, applyImages(t, env.sam, edgeNginxKey, nil, "edge-nginx", image))
	env.scanAll(t)
	digest := env.fk.Estate.ImageDigestFor("edge-nginx:1.27.1")
	accept(t, staging, digest, edgeNginxCritical(), http.StatusCreated)

	// A Dev service asks for the same digest afterwards: the acceptance was not made for it.
	row := wantGate(t, applyImages(t, env.dana, certRenewerKey, nil, "cert-renewer", image))["cert-renewer"]
	if row.State != "critical" || row.Open.Critical != 1 {
		t.Fatalf("row = %+v, want critical for the later requester", row)
	}
}

func TestAcceptRisk_ExpiresAndResurfaces(t *testing.T) {
	env := newSecurityEnv(t)
	holder := env.riskHolder(t, "risk")
	digest := scannedCriticalEdgeNginx(t, env)
	image := ecrImage(edgeNginxKey, "edge-nginx", "1.27.1")
	body := edgeNginxCritical()
	body.Days = 1

	ids := accept(t, holder, digest, body, http.StatusCreated)
	if got := securityImage(t, holder, digest); got.Open.Critical != 0 || got.Accepted != 1 {
		t.Fatalf("after accepting: open critical %d accepted %d, want 0 and 1", got.Open.Critical, got.Accepted)
	}
	if _, err := env.st.Pool.Exec(t.Context(), `update scan_risk_acceptances set expires_at = now() - interval '1 minute' where id = $1`, ids.IDs[0]); err != nil {
		t.Fatal(err)
	}
	if got := securityImage(t, holder, digest); got.Open.Critical != 1 || got.Accepted != 0 {
		t.Fatalf("after expiry: open critical %d accepted %d, want 1 and 0", got.Open.Critical, got.Accepted)
	}
	if row := wantGate(t, applyImages(t, holder, edgeNginxKey, nil, "edge-nginx", image))["edge-nginx"]; row.State != "critical" {
		t.Fatalf("row = %+v, want the gate to count the finding again", row)
	}
}

func TestAcceptRisk_ValidatesInput(t *testing.T) {
	env := newSecurityEnv(t)
	holder := env.riskHolder(t, "risk")
	digest := scannedCriticalEdgeNginx(t, env)
	nobody := env.addUser(t, "nobody", nil)

	mutate := func(f func(*AcceptRiskRequest)) AcceptRiskRequest {
		body := edgeNginxCritical()
		f(&body)
		return body
	}
	for name, body := range map[string]AcceptRiskRequest{
		"empty reason":       mutate(func(b *AcceptRiskRequest) { b.Reason = "" }),
		"blank reason":       mutate(func(b *AcceptRiskRequest) { b.Reason = "   " }),
		"long reason":        mutate(func(b *AcceptRiskRequest) { b.Reason = strings.Repeat("x", 501) }),
		"zero days":          mutate(func(b *AcceptRiskRequest) { b.Days = 0 }),
		"too many days":      mutate(func(b *AcceptRiskRequest) { b.Days = 31 }),
		"unknown finding":    mutate(func(b *AcceptRiskRequest) { b.VulnID = "CVE-0000-9999" }),
		"wrong version":      mutate(func(b *AcceptRiskRequest) { b.PackageVersion = "9.9" }),
		"not a requester":    mutate(func(b *AcceptRiskRequest) { b.ServiceIDs = []string{certRenewerKey.ID()} }),
		"malformed service":  mutate(func(b *AcceptRiskRequest) { b.ServiceIDs = []string{"nope"} }),
		"duplicated service": mutate(func(b *AcceptRiskRequest) { b.ServiceIDs = []string{edgeNginxKey.ID(), edgeNginxKey.ID()} }),
	} {
		t.Run(name, func(t *testing.T) {
			wantStatus(t, acceptRaw(holder, digest, body), http.StatusUnprocessableEntity)
		})
	}
	wantStatus(t, acceptRaw(nobody, digest, edgeNginxCritical()), http.StatusNotFound)
	wantStatus(t, acceptRaw(holder, "sha256:"+strings.Repeat("a", 64), edgeNginxCritical()), http.StatusNotFound)
	if n := countRows(t, env.st, `select count(*) from scan_risk_acceptances`); n != 0 {
		t.Fatalf("%d acceptances written by refused calls, want none", n)
	}

	max := mutate(func(b *AcceptRiskRequest) { b.Days = 30; b.Reason = strings.Repeat("x", 500) })
	accept(t, holder, digest, max, http.StatusCreated)
	if n := countRows(t, env.st, `select count(*) from scan_risk_acceptances where expires_at > now() + interval '29 days 23 hours'
		and expires_at < now() + interval '30 days 1 hour'`); n != 1 {
		t.Fatal("a 30 day acceptance does not expire 30 days from now")
	}
}

func TestAcceptRisk_NeedsAcceptRiskOnSomeRequester(t *testing.T) {
	env := newSecurityEnv(t)
	digest := scannedCriticalEdgeNginx(t, env)

	resp := acceptRaw(env.dana, digest, edgeNginxCritical()) // Full on Dev, but Full has no Accept risk
	wantStatus(t, resp, http.StatusForbidden)
	if _, message := errorCode(t, resp); message != "You need Accept risk on a service that requested this image" {
		t.Fatalf("message = %q", message)
	}
}

func TestAcceptRisk_DuplicateIs409(t *testing.T) {
	env := newSecurityEnv(t)
	holder := env.riskHolder(t, "risk")
	digest := scannedCriticalEdgeNginx(t, env)

	accept(t, holder, digest, edgeNginxCritical(), http.StatusCreated)
	wantStatus(t, acceptRaw(holder, digest, edgeNginxCritical()), http.StatusConflict)
	if n := countRows(t, env.st, `select count(*) from scan_risk_acceptances`); n != 1 {
		t.Fatalf("%d acceptances after a duplicate, want 1", n)
	}
}

func TestAcceptRisk_IsAuditedWithTheServicesListed(t *testing.T) {
	env := newSecurityEnv(t)
	holder := env.riskHolder(t, "risk")
	digest := scannedCriticalEdgeNginx(t, env)

	accept(t, holder, digest, edgeNginxCritical(), http.StatusCreated)
	entries := auditByAction(t, env.st, "security.accept")
	want := "Accepted CVE-0000-1042 in openssl on edge-nginx:1.27.1 for 30 days for edge-nginx."
	if len(entries) != 1 || entries[0].ActorUsername != "risk" || entries[0].ActorUserID == nil || entries[0].Target != "image:"+strings.TrimPrefix(digest, "sha256:")[:12] ||
		entries[0].Summary != want {
		t.Fatalf("audit entries = %+v, want one by risk with summary %q", entries, want)
	}
	if n := countRows(t, env.st, `select count(*) from scan_risk_acceptances where accepted_by is not null`); n != 1 {
		t.Fatal("the acceptance does not carry the user id")
	}
}

func TestRevokeAcceptance_ByAccepterAdminOrServiceHolder(t *testing.T) {
	env := newSecurityEnv(t)
	accepter, other := env.riskHolder(t, "risk"), env.riskHolder(t, "risk2")
	digest := scannedCriticalEdgeNginx(t, env)
	findings := []AcceptRiskRequest{
		edgeNginxCritical(),
		{VulnID: "CVE-0000-2210", PackageName: "nginx", PackageVersion: "1.27.2", Reason: "not reachable", Days: 7},
		{VulnID: "CVE-0000-3188", PackageName: "zlib", PackageVersion: "1.3", Reason: "not reachable", Days: 7},
	}
	var ids []int64
	for _, finding := range findings {
		ids = append(ids, accept(t, accepter, digest, finding, http.StatusCreated).IDs[0])
	}

	wantStatus(t, revoke(env.dana, ids[0]), http.StatusForbidden) // sees the image, holds no Accept risk, did not accept
	wantStatus(t, revoke(env.omer, ids[0]), http.StatusForbidden)
	wantStatus(t, revoke(accepter, ids[0]), http.StatusNoContent)
	wantStatus(t, revoke(env.sam, ids[1]), http.StatusNoContent)
	wantStatus(t, revoke(other, ids[2]), http.StatusNoContent)
	wantStatus(t, revoke(env.sam, ids[0]), http.StatusConflict)
	wantStatus(t, revoke(env.sam, 987654), http.StatusNotFound)

	entries := auditByAction(t, env.st, "security.revoke")
	if len(entries) != 3 || entries[0].ActorUserID == nil {
		t.Fatalf("audit entries = %+v, want three revocations with the user id", entries)
	}
	if n := countRows(t, env.st, `select count(*) from scan_risk_acceptances where revoked_at is not null and revoked_by is not null`); n != 3 {
		t.Fatalf("%d acceptances revoked with a user, want 3", n)
	}
}

func TestRevokeAcceptance_InvisibleIs404(t *testing.T) {
	env := newSecurityEnv(t)
	holder := env.riskHolder(t, "risk")
	nobody := env.addUser(t, "nobody", nil)
	digest := scannedCriticalEdgeNginx(t, env)
	id := accept(t, holder, digest, edgeNginxCritical(), http.StatusCreated).IDs[0]

	wantStatus(t, revoke(nobody, id), http.StatusNotFound)
	if n := countRows(t, env.st, `select count(*) from scan_risk_acceptances where revoked_at is not null`); n != 0 {
		t.Fatal("an invisible caller revoked an acceptance")
	}
}

func rescanPath(digest string) string {
	return "/api/security/images/" + url.PathEscape(digest) + "/rescan"
}

func TestRescan_QueuesAndAudits(t *testing.T) {
	env := newSecurityEnv(t)
	digest := scannedCriticalEdgeNginx(t, env)
	runs := env.scanRuns(t)

	wantStatus(t, env.omer.post(rescanPath(digest), nil), http.StatusForbidden) // no Deploy
	wantStatus(t, env.addUser(t, "nobody", nil).post(rescanPath(digest), nil), http.StatusNotFound)
	wantStatus(t, env.dana.post(rescanPath(digest), nil), http.StatusAccepted)

	img, err := env.st.GetScanImage(t.Context(), digest)
	if err != nil || img.Status != "queued" {
		t.Fatalf("image %+v (%v), want queued", img, err)
	}
	entries := auditByAction(t, env.st, "security.rescan")
	if len(entries) != 1 || entries[0].ActorUsername != "dana" || entries[0].Target != "image:"+strings.TrimPrefix(digest, "sha256:")[:12] {
		t.Fatalf("audit entries = %+v, want one rescan by dana", entries)
	}
	env.scanAll(t)
	if env.scanRuns(t) != runs+1 {
		t.Fatalf("%d runs after the rescan, want %d", env.scanRuns(t), runs+1)
	}
}

func TestRescan_AlreadyQueuedIs409(t *testing.T) {
	env := newSecurityEnv(t)
	status := mustRequestScan(t, env.dana, edgeNginxKey.ID(), "edge-nginx", "1.27.1") // queued, not yet scanned

	wantStatus(t, env.dana.post(rescanPath(status.Image.Digest), nil), http.StatusConflict)
	if len(auditByAction(t, env.st, "security.rescan")) != 0 {
		t.Fatal("a refused rescan was audited")
	}
}

func packageAcceptancesPath(digest string) string {
	return "/api/security/images/" + url.PathEscape(digest) + "/package-acceptances"
}

func opensslPackage() AcceptPackageRiskRequest {
	return AcceptPackageRiskRequest{PackageName: "openssl", Reason: "patched in the next base image", Days: 30}
}

// addFinding stores one more finding on a scanned image, so a package can carry several vulnerabilities and versions.
func addFinding(t *testing.T, env *securityEnv, digest, vulnID, severity, pkg, version string) {
	t.Helper()
	if _, err := env.st.Pool.Exec(t.Context(), `insert into scan_findings (digest, vuln_id, package_name, package_version, severity,
		first_seen_at, last_seen_at, run_id) values ($1, $2, $3, $4, $5, now(), now(), 0)`, digest, vulnID, pkg, version, severity); err != nil {
		t.Fatal(err)
	}
}

func TestAcceptPackageRisk_AcceptsEveryOpenFindingOfThePackage(t *testing.T) {
	env := newSecurityEnv(t)
	holder := env.riskHolder(t, "risk")
	digest := scannedCriticalEdgeNginx(t, env)
	addFinding(t, env, digest, "CVE-0000-1043", "high", "openssl", "3.0.13")
	addFinding(t, env, digest, "CVE-0000-1044", "medium", "openssl", "3.0.11")
	accept(t, holder, digest, edgeNginxCritical(), http.StatusCreated) // already accepted: the package accept skips it

	resp := holder.post(packageAcceptancesPath(digest), opensslPackage())
	wantStatus(t, resp, http.StatusCreated)
	var out AcceptRiskResponse
	decode(t, resp, &out)
	if len(out.IDs) != 2 {
		t.Fatalf("wrote %d acceptances, want the 2 openssl findings not yet accepted", len(out.IDs))
	}
	if n := countRows(t, env.st, `select count(*) from scan_risk_acceptances where package_name <> 'openssl'`); n != 0 {
		t.Fatalf("%d acceptances outside openssl, want none", n)
	}
	if got := securityImage(t, holder, digest); got.Accepted != 3 || got.Open.Critical != 0 || got.Open.High != 1 || got.Open.Medium != 1 {
		t.Fatalf("open %+v accepted %d, want nginx high and zlib medium open and 3 accepted", got.Open, got.Accepted)
	}
	entries := auditByAction(t, env.st, "security.accept_package")
	want := "Accepted 2 findings in openssl on edge-nginx:1.27.1 for 30 days for edge-nginx."
	if len(entries) != 1 || entries[0].ActorUsername != "risk" || entries[0].Summary != want {
		t.Fatalf("audit entries = %+v, want one by risk with summary %q", entries, want)
	}

	wantStatus(t, holder.post(packageAcceptancesPath(digest), opensslPackage()), http.StatusConflict)
}

func TestAcceptPackageRisk_ValidatesInputAndPermission(t *testing.T) {
	env := newSecurityEnv(t)
	holder := env.riskHolder(t, "risk")
	nobody := env.addUser(t, "nobody", nil)
	digest := scannedCriticalEdgeNginx(t, env)

	mutate := func(f func(*AcceptPackageRiskRequest)) AcceptPackageRiskRequest {
		body := opensslPackage()
		f(&body)
		return body
	}
	for name, body := range map[string]AcceptPackageRiskRequest{
		"empty reason":    mutate(func(b *AcceptPackageRiskRequest) { b.Reason = "" }),
		"zero days":       mutate(func(b *AcceptPackageRiskRequest) { b.Days = 0 }),
		"too many days":   mutate(func(b *AcceptPackageRiskRequest) { b.Days = 31 }),
		"unknown package": mutate(func(b *AcceptPackageRiskRequest) { b.PackageName = "left-pad" }),
		"not a requester": mutate(func(b *AcceptPackageRiskRequest) { b.ServiceIDs = []string{certRenewerKey.ID()} }),
	} {
		t.Run(name, func(t *testing.T) {
			wantStatus(t, holder.post(packageAcceptancesPath(digest), body), http.StatusUnprocessableEntity)
		})
	}
	wantStatus(t, nobody.post(packageAcceptancesPath(digest), opensslPackage()), http.StatusNotFound)
	wantStatus(t, env.dana.post(packageAcceptancesPath(digest), opensslPackage()), http.StatusForbidden) // Full has no Accept risk
	if n := countRows(t, env.st, `select count(*) from scan_risk_acceptances`); n != 0 {
		t.Fatalf("%d acceptances written by refused calls, want none", n)
	}
}
