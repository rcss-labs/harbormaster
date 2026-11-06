package api

import (
	"net/http"
	"testing"
)

func getServicesList(t *testing.T, c *apiClient, query string) ServiceListDTO {
	t.Helper()
	resp := c.get("/api/services" + query)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/services%s: status %d, want 200", query, resp.StatusCode)
	}
	var out ServiceListDTO
	decode(t, resp, &out)
	return out
}

func serviceListItem(t *testing.T, list ServiceListDTO, name string) ServiceListItemDTO {
	t.Helper()
	for _, item := range list.Items {
		if item.Name == name {
			return item
		}
	}
	t.Fatalf("service %q not in list of %d", name, len(list.Items))
	return ServiceListItemDTO{}
}

func TestServicesList_AdminSeesEstate(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	list := getServicesList(t, login(t, srv, "pavel", testPassword), "")

	if len(list.Items) != 15 || list.Total != 15 || list.Truncated {
		t.Fatalf("items %d total %d truncated %v, want 15, 15, false", len(list.Items), list.Total, list.Truncated)
	}
	if list.PolledAt.IsZero() {
		t.Error("polled_at is zero")
	}
	if payments := serviceListItem(t, list, "payments-api"); !payments.TagInherited {
		t.Errorf("payments-api tag_inherited = false, want true (tag %q)", payments.Tag.Name)
	}
	proxy := serviceListItem(t, list, "artifact-proxy")
	if proxy.Tag.Name != "Prod" || proxy.TagInherited {
		t.Errorf("artifact-proxy tag %q inherited %v, want Prod and false", proxy.Tag.Name, proxy.TagInherited)
	}
	if proxy.Cluster.ID == "" || proxy.Cluster.Name != "ci-tools" || proxy.Family == "" || proxy.Revision == 0 {
		t.Errorf("artifact-proxy row incomplete: %+v", proxy)
	}
}

func TestServicesList_ScopeAndTag(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	ids := lookupTagIDs(t, st)
	pavel := login(t, srv, "pavel", testPassword)

	if got := getServicesList(t, pavel, "?"+ids.Prod); len(got.Items) != 5 {
		t.Errorf("Prod services = %d, want 5", len(got.Items))
	}
	if got := getServicesList(t, pavel, "?account="+stagingAccountID); len(got.Items) != 2 {
		t.Errorf("staging account services = %d, want 2", len(got.Items))
	}
	resp := pavel.get("/api/services?tag=abc")
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("tag=abc status %d, want 422", resp.StatusCode)
	}
}

func TestServicesList_NonAdminNeverWidens(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	ids := lookupTagIDs(t, st)
	omer := login(t, srv, "omer", testPassword)

	if got := getServicesList(t, omer, "?"+ids.Prod); len(got.Items) != 0 {
		t.Errorf("omer services under Prod = %d, want 0", len(got.Items))
	}
	all := getServicesList(t, omer, "")
	if len(all.Items) == 0 {
		t.Fatal("omer sees no services, want the Dev ones")
	}
	for _, item := range all.Items {
		if item.Tag.Name == "Prod" {
			t.Errorf("omer sees Prod service %s", item.Name)
		}
		if item.Can.Terminal {
			t.Errorf("omer can.terminal on %s, want false", item.Name)
		}
	}
}

func TestServicesList_AttentionText(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	// Make the image-resizer deployment fail, as ECS reports after a circuit-breaker trip.
	_, err := st.Pool.Exec(t.Context(),
		`update services set deployments = (
		     select jsonb_agg(d || '{"RolloutState":"FAILED"}'::jsonb) from jsonb_array_elements(deployments) d)
		 where service_name = 'image-resizer'`)
	if err != nil {
		t.Fatal(err)
	}
	list := getServicesList(t, login(t, srv, "pavel", testPassword), "")

	resizer := serviceListItem(t, list, "image-resizer")
	if resizer.Attention == "" {
		t.Error("image-resizer attention is empty after the alerts tier ran")
	}
	if resizer.Rollout == nil || *resizer.Rollout != "failed" {
		t.Errorf("image-resizer rollout = %v, want failed", resizer.Rollout)
	}
}

func TestServicesList_Truncated(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	previous := servicesListCap
	servicesListCap = 3
	t.Cleanup(func() { servicesListCap = previous })

	list := getServicesList(t, login(t, srv, "pavel", testPassword), "")
	if len(list.Items) != 3 || list.Total != 15 || !list.Truncated {
		t.Errorf("items %d total %d truncated %v, want 3, 15, true", len(list.Items), list.Total, list.Truncated)
	}
}
