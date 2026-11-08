package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/store"
)

const homelabRoleARN = "arn:aws:iam::482109375512:role/harbormaster-access"

type testProbeResult struct {
	Regions []struct {
		Region   string `json:"region"`
		OK       bool   `json:"ok"`
		Clusters int    `json:"clusters"`
		Error    string `json:"error"`
	} `json:"regions"`
}

func TestAccounts_TestUsesProber(t *testing.T) {
	srv, _, st := newTestServer(t)
	seedAccounts(t, st)
	sam := login(t, srv, "sam", testPassword)

	var known testProbeResult
	decode(t, sam.post("/api/accounts/test", map[string]any{
		"role_arn": homelabRoleARN, "external_id": "x", "regions": []string{"us-east-1", "eu-west-1"},
	}), &known)
	if len(known.Regions) != 2 {
		t.Fatalf("regions = %+v, want 2 results", known.Regions)
	}
	if !known.Regions[0].OK || known.Regions[0].Clusters != 3 || !known.Regions[1].OK || known.Regions[1].Clusters != 1 {
		t.Errorf("known account results = %+v, want us-east-1 ok with 3 clusters and eu-west-1 ok with 1", known.Regions)
	}

	var unknown testProbeResult
	decode(t, sam.post("/api/accounts/test", map[string]any{
		"role_arn": "arn:aws:iam::111111111111:role/harbormaster-access", "external_id": "x", "regions": []string{"us-east-1"},
	}), &unknown)
	if len(unknown.Regions) != 1 || unknown.Regions[0].OK || unknown.Regions[0].Error == "" {
		t.Errorf("unknown account results = %+v, want one failed region with an error", unknown.Regions)
	}
}

func TestAccounts_CreateParsesAccountID(t *testing.T) {
	srv, _, _ := newTestServer(t)
	sam := login(t, srv, "sam", testPassword)

	resp := sam.post("/api/accounts", map[string]any{
		"name": "homelab-admin", "role_arn": homelabRoleARN, "external_id": "x", "regions": []string{"us-east-1"},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", resp.StatusCode)
	}
	var created struct {
		AccountID string `json:"account_id"`
		Name      string `json:"name"`
	}
	decode(t, resp, &created)
	if created.AccountID != "482109375512" || created.Name != "homelab-admin" {
		t.Errorf("created = %+v, want account_id 482109375512 for homelab-admin", created)
	}

	resp = sam.post("/api/accounts", map[string]any{
		"name": "bad", "role_arn": "arn:aws:iam::12345:role/x", "external_id": "x", "regions": []string{"us-east-1"},
	})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("bad ARN status = %d, want 422", resp.StatusCode)
	}
	if _, msg := errorCode(t, resp); msg != "Role ARN must look like arn:aws:iam::123456789012:role/name" {
		t.Errorf("bad ARN message = %q", msg)
	}
}

func TestAccounts_DeleteRemovesClustersFromAPI(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedAccounts(t, st)
	pollAll(t, st, fk)
	sam := login(t, srv, "sam", testPassword)

	var accounts struct {
		Items []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"items"`
	}
	decode(t, sam.get("/api/accounts"), &accounts)
	var stagingID int64
	for _, a := range accounts.Items {
		if a.Name == "staging-example" {
			stagingID = a.ID
		}
	}
	if stagingID == 0 {
		t.Fatalf("staging-example not listed in %+v", accounts.Items)
	}

	if resp := sam.do(http.MethodDelete, "/api/accounts/"+itoa(stagingID), nil, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", resp.StatusCode)
	}

	var clusters struct {
		Items []json.RawMessage `json:"items"`
	}
	decode(t, sam.get("/api/clusters"), &clusters)
	if len(clusters.Items) != 5 {
		t.Errorf("clusters after delete = %d, want 5", len(clusters.Items))
	}
}

func TestAudit_NewestFirstPaged(t *testing.T) {
	srv, _, st := newTestServer(t)
	base := time.Now().Add(time.Hour)
	for i := range 5 {
		if err := st.Audit(t.Context(), store.AuditEntry{
			TS: base.Add(time.Duration(i) * time.Minute), ActorUsername: "sam",
			Action: "test_seed", Target: "seed", Summary: fmt.Sprintf("seed %d", i), IP: "127.0.0.1",
		}); err != nil {
			t.Fatal(err)
		}
	}
	sam := login(t, srv, "sam", testPassword)

	type auditPage struct {
		Items []struct {
			Summary string         `json:"summary"`
			Details map[string]any `json:"details"`
		} `json:"items"`
		Next *string `json:"next"`
	}
	var first auditPage
	decode(t, sam.get("/api/audit?limit=2"), &first)
	if len(first.Items) != 2 || first.Items[0].Summary != "seed 4" || first.Items[1].Summary != "seed 3" {
		t.Fatalf("first page = %+v, want seed 4 then seed 3", first.Items)
	}
	if first.Items[0].Details != nil {
		t.Errorf("details = %v, want null for an entry without details", first.Items[0].Details)
	}
	if first.Next == nil {
		t.Fatal("first page has no next cursor")
	}

	var second auditPage
	decode(t, sam.get("/api/audit?limit=2&cursor="+*first.Next), &second)
	if len(second.Items) != 2 || second.Items[0].Summary != "seed 2" || second.Items[1].Summary != "seed 1" {
		t.Errorf("second page = %+v, want seed 2 then seed 1", second.Items)
	}
}

func TestAccounts_NonAdmin403(t *testing.T) {
	srv, _, _ := newTestServer(t)
	omer := login(t, srv, "omer", testPassword)
	requests := []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/api/accounts", nil},
		{http.MethodGet, "/api/accounts/external-id", nil},
		{http.MethodPost, "/api/accounts/test", map[string]any{"role_arn": homelabRoleARN, "external_id": "x", "regions": []string{"us-east-1"}}},
		{http.MethodPost, "/api/accounts", map[string]any{"name": "a", "role_arn": homelabRoleARN, "external_id": "x", "regions": []string{"us-east-1"}}},
		{http.MethodPatch, "/api/accounts/1", map[string]any{"name": "a", "regions": []string{"us-east-1"}}},
		{http.MethodDelete, "/api/accounts/1", nil},
		{http.MethodGet, "/api/audit", nil},
	}
	for _, tc := range requests {
		resp := omer.do(tc.method, tc.path, tc.body, nil)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s = %d, want 403", tc.method, tc.path, resp.StatusCode)
		}
		resp.Body.Close()
	}
}
