package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/store"
)

func withARM64(s *Server) {
	withCostAnalytics(s)
	s.Cfg.ARM64.Check = true
}

// armEnv is an estate polled once with every x86 Fargate image tracked, so a test can say what the platform check found.
type armEnv struct {
	srv *httptest.Server
	st  *store.Store
}

func (e armEnv) mark(t *testing.T, imageContains, status, detail string, known, isIndex bool, platforms ...string) {
	t.Helper()
	rows, err := e.st.Pool.Query(t.Context(), `select image from image_refs where image like $1`, "%"+imageContains+"%")
	if err != nil {
		t.Fatal(err)
	}
	var images []string
	for rows.Next() {
		var image string
		if err := rows.Scan(&image); err != nil {
			t.Fatal(err)
		}
		images = append(images, image)
	}
	rows.Close()
	if len(images) == 0 {
		t.Fatalf("no tracked image contains %q", imageContains)
	}
	for _, image := range images {
		digest := "sha256:" + strings.Repeat("a", 63) + string(rune('a'+len(image)%6))
		if err := e.st.SaveImageRefResult(t.Context(), store.ImageRef{Image: image, Registry: "reg", Repository: image, Digest: digest, Status: status, Detail: detail}, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		if known {
			if err := e.st.SaveImageManifest(t.Context(), "reg", image, digest, "m", isIndex, platforms, time.Now()); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func newARMEnv(t *testing.T) (*apiClient, armEnv, func(user string) *apiClient) {
	t.Helper()
	srv, fk, st, _ := newServerCustom(t, true, withARM64)
	seedMockupTags(t, st)
	setPassword(t, st, "dana")
	forceProviders(fk, "FARGATE")
	pollAll(t, st, fk)
	if _, err := st.TrackX86FargateImages(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	as := func(user string) *apiClient { return login(t, srv, user, testPassword) }
	return as("sam"), armEnv{srv: srv, st: st}, as
}

func armService(t *testing.T, dto CostDTO, name string) CostARM64ServiceDTO {
	t.Helper()
	for _, svc := range dto.ARM64.Services {
		if svc.Service == name {
			return svc
		}
	}
	t.Fatalf("service %q missing from arm64.services (listed: %d)", name, len(dto.ARM64.Services))
	return CostARM64ServiceDTO{}
}

func TestAnalyticsCost_ARM64ListsReadyBlockedUnknown(t *testing.T) {
	sam, env, _ := newARMEnv(t)
	env.mark(t, "/payments-api:", "ok", "", true, true, "linux/amd64", "linux/arm64")
	env.mark(t, "aws-otel-collector", "ok", "", true, true, "linux/amd64", "linux/arm64")
	env.mark(t, "/catalog-api:", "ok", "", true, false)
	got := getCost(t, sam, "")

	if !got.ARM64.CheckEnabled {
		t.Fatal("check_enabled = false with the check on")
	}
	ready := armService(t, got, "payments-api")
	if ready.Status != "ready" || ready.SavingMonthly == nil || *ready.SavingMonthly <= 0 || !ready.CanEdit {
		t.Fatalf("payments-api = %+v, want ready with a saving, editable for an admin", ready)
	}
	if len(ready.Images) != 2 || ready.Images[0].State != "arm64" {
		t.Fatalf("payments-api images = %+v, want two with an arm64 build", ready.Images)
	}
	blocked := armService(t, got, "catalog-api")
	if blocked.Status != "blocked" || blocked.SavingMonthly == nil || len(blocked.Images) != 1 ||
		blocked.Images[0].State != "no_arm64" || blocked.Images[0].Reason != "single_architecture" || !strings.Contains(blocked.Images[0].Image, "catalog-api") {
		t.Fatalf("catalog-api = %+v, want blocked by its single-architecture image, with the saving at stake", blocked)
	}
	unknown := armService(t, got, "edge-nginx")
	if unknown.Status != "unknown" || unknown.Images[0].State != "unchecked" || unknown.Images[0].Reason != "pending" {
		t.Fatalf("edge-nginx = %+v, want unknown, pending", unknown)
	}
	for _, svc := range got.ARM64.Services {
		if svc.Service == "cert-renewer" {
			t.Errorf("cert-renewer (EC2) is listed: %+v", svc)
		}
		if svc.Status != "ready" && svc.Status != "blocked" && svc.Status != "unknown" {
			t.Errorf("%s listed with status %q, want only ready, blocked or unknown", svc.Service, svc.Status)
		}
	}
	if got.ARM64.AlreadyARM64 != 0 {
		t.Errorf("already_arm64 = %d, want 0", got.ARM64.AlreadyARM64)
	}
}

func TestAnalyticsCost_ARM64OffListsNothing(t *testing.T) {
	srv, fk, st, _ := newServerCustom(t, true, withCostAnalytics) // HM_ARM64_CHECK is off
	seedMockupTags(t, st)
	forceProviders(fk, "FARGATE")
	pollAll(t, st, fk)
	got := getCost(t, login(t, srv, "sam", testPassword), "")
	if got.ARM64.CheckEnabled || got.ARM64.Services == nil || len(got.ARM64.Services) != 0 {
		t.Fatalf("arm64 = %+v, want the check off and an empty, never null, list", got.ARM64)
	}
}

func TestAnalyticsCost_ARM64NamesAccountForMissingPermission(t *testing.T) {
	sam, env, _ := newARMEnv(t)
	env.mark(t, "/catalog-api:", "no_permission", "482109375512", false, false)
	env.mark(t, "/edge-nginx:", "not_connected", "731022849906", false, false)
	got := getCost(t, sam, "")
	denied := armService(t, got, "catalog-api").Images[0]
	if denied.Reason != "no_permission" || denied.Account != "sandbox-admin" {
		t.Fatalf("catalog-api image = %+v, want the account name, not its id", denied)
	}
	if offline := armService(t, got, "edge-nginx").Images[0]; offline.Reason != "not_connected" || offline.Account == "" || offline.Account == "731022849906" {
		t.Fatalf("edge-nginx image = %+v, want the name of the unconnected account (or its id when unnamed)", offline)
	}
}

func TestAnalyticsCost_ARM64RespectsVisibility(t *testing.T) {
	_, env, as := newARMEnv(t)
	env.mark(t, "/payments-api:", "ok", "", true, false)
	omer := as("omer") // Dev and Staging only: payments-api is Prod
	resp := omer.get("/api/analytics/cost")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if strings.Contains(string(body), "payments-api") {
		t.Fatal("an invisible service, or an image used only by it, appears in the ARM64 section")
	}
	var dto CostDTO
	if err := json.Unmarshal(body, &dto); err != nil {
		t.Fatal(err)
	}
	for _, svc := range dto.ARM64.Services {
		if svc.Service == "payments-api" {
			t.Fatal("payments-api listed for a user who cannot see it")
		}
	}
}
