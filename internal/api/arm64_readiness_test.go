package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/model"
)

var catalogAPIKey = model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: "482109375512", Region: "eu-west-1", Cluster: "eu-apps"}, Service: "catalog-api"}

func TestARM64Readiness_NotVisibleIs404(t *testing.T) {
	_, env, as := newARMEnv(t)
	env.mark(t, "/payments-api:", "ok", "", true, false)
	resp := as("omer").get("/api/services/" + paymentsID() + "/arm64-readiness") // payments-api is Prod; omer sees Dev and Staging
	wantStatus(t, resp, http.StatusNotFound)
}

func TestARM64Readiness_ReturnsBlockers(t *testing.T) {
	sam, env, _ := newARMEnv(t)
	env.mark(t, "/catalog-api:", "ok", "", true, false)
	var out ARM64ReadinessDTO
	resp := sam.get("/api/services/" + catalogAPIKey.ID() + "/arm64-readiness")
	wantStatus(t, resp, http.StatusOK)
	decode(t, resp, &out)
	if out.Status != "blocked" || len(out.Images) != 1 || out.Images[0].State != "no_arm64" || out.Images[0].Reason != "single_architecture" ||
		!strings.Contains(out.Images[0].Image, "catalog-api") {
		t.Fatalf("readiness = %+v, want blocked by the single-architecture image", out)
	}
	if out.CheckedAt == nil {
		t.Error("checked_at is null although the image was checked")
	}
}

func TestARM64Readiness_UncheckedImageIsUnknownNeverReady(t *testing.T) {
	sam, _, _ := newARMEnv(t)
	var out ARM64ReadinessDTO
	resp := sam.get("/api/services/" + catalogAPIKey.ID() + "/arm64-readiness")
	wantStatus(t, resp, http.StatusOK)
	decode(t, resp, &out)
	if out.Status != "unknown" || out.Images[0].Reason != "pending" || out.CheckedAt != nil {
		t.Fatalf("readiness = %+v, want unknown while nothing was checked", out)
	}
}
