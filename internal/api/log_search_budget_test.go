package api

import (
	"net/http"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/authz"
)

type estimateBody struct {
	StoredBytes      int64   `json:"stored_bytes"`
	CostUSD          float64 `json:"cost_usd"`
	Groups           int     `json:"groups"`
	UnknownGroups    int     `json:"unknown_groups"`
	ScanLimitBytes   int64   `json:"scan_limit_bytes"`
	ExceedsScanLimit bool    `json:"exceeds_scan_limit"`
}

const (
	paymentsGroup = "/ecs/core-services/payments-api"
	catalogGroup  = "/ecs/eu-apps/catalog-api"
)

func TestLogSearchEstimate_SumsStoredBytesAndPrices(t *testing.T) {
	env := newLogSearchEnv(t, nil)
	env.fk.Estate.SetLogGroupStoredBytes(paymentsGroup, 3<<30)
	env.fk.Estate.SetLogGroupStoredBytes(catalogGroup, 1<<30)
	c := login(t, env.srv, "sam", testPassword)

	resp := c.post("/api/log-search/estimate", searchReq(paymentsID(), catalogID()))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var body estimateBody
	decode(t, resp, &body)
	if body.StoredBytes != 4<<30 || body.Groups != 2 || body.UnknownGroups != 0 {
		t.Errorf("stored=%d groups=%d unknown=%d, want 4 GiB over 2 groups", body.StoredBytes, body.Groups, body.UnknownGroups)
	}
	if want := 4 * 0.005; body.CostUSD != want {
		t.Errorf("cost %v, want %v ($0.005 per GiB)", body.CostUSD, want)
	}
	if n := env.fk.Calls()["StartQuery"]; n != 0 {
		t.Errorf("StartQuery called %d times by an estimate", n)
	}
}

func TestLogSearchEstimate_WarnsWhenOverTheScanLimit(t *testing.T) {
	env := newLogSearchEnv(t, nil)
	env.api.LogSearch.MaxBytes = 2 << 30
	env.fk.Estate.SetLogGroupStoredBytes(paymentsGroup, 3<<30)
	c := login(t, env.srv, "sam", testPassword)

	var body estimateBody
	decode(t, c.post("/api/log-search/estimate", searchReq(paymentsID())), &body)
	if !body.ExceedsScanLimit || body.ScanLimitBytes != 2<<30 {
		t.Errorf("exceeds=%v limit=%d, want true and 2 GiB", body.ExceedsScanLimit, body.ScanLimitBytes)
	}

	env.fk.Estate.SetLogGroupStoredBytes(paymentsGroup, 1<<30)
	decode(t, c.post("/api/log-search/estimate", searchReq(paymentsID())), &body)
	if body.ExceedsScanLimit {
		t.Error("exceeds_scan_limit = true for 1 GiB under a 2 GiB limit")
	}
}

func TestLogSearchEstimate_UsesTheSearchAuthorization(t *testing.T) {
	env := newLogSearchEnv(t, nil)
	env.fk.Estate.SetLogGroupStoredBytes(paymentsGroup, 3<<30)

	// omer cannot see Prod: 404 for the whole request, and nothing is read from AWS.
	omer := login(t, env.srv, "omer", testPassword)
	before := env.fk.Calls()["DescribeLogGroups"]
	resp := omer.post("/api/log-search/estimate", searchReq(paymentsID()))
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("invisible service: status %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
	if n := env.fk.Calls()["DescribeLogGroups"]; n != before {
		t.Errorf("DescribeLogGroups called %d times for a service the caller cannot see", n-before)
	}

	// A user with View but not Logs gets the same 403 the search answers.
	user, err := env.st.CreateUser(t.Context(), "prod-viewer", mustHash(t), false, false)
	if err != nil {
		t.Fatal(err)
	}
	tags, err := env.st.ListTags(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, tag := range tags {
		if tag.Name == "Prod" {
			if err := env.st.SetPermission(t.Context(), user.ID, tag.ID, authz.ActionSet(authz.View)); err != nil {
				t.Fatal(err)
			}
		}
	}
	resp = login(t, env.srv, "prod-viewer", testPassword).post("/api/log-search/estimate", searchReq(paymentsID()))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("view without logs: status %d, want 403", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestLogSearchEstimate_OnlyCountsRequestedServices(t *testing.T) {
	env := newLogSearchEnv(t, nil)
	env.fk.Estate.SetLogGroupStoredBytes(paymentsGroup, 3<<30)
	env.fk.Estate.SetLogGroupStoredBytes(catalogGroup, 1<<30)
	c := login(t, env.srv, "sam", testPassword)

	var body estimateBody
	decode(t, c.post("/api/log-search/estimate", searchReq(catalogID())), &body)
	if body.StoredBytes != 1<<30 || body.Groups != 1 {
		t.Errorf("stored=%d groups=%d, want only the catalog group (1 GiB)", body.StoredBytes, body.Groups)
	}
}

func TestLogSearchEstimate_ValidatesLikeTheSearch(t *testing.T) {
	env := newLogSearchEnv(t, nil)
	c := login(t, env.srv, "sam", testPassword)
	for name, req := range map[string]map[string]any{
		"no services": {"service_ids": []string{}, "range": "1h"},
		"bad range":   {"service_ids": []string{paymentsID()}, "range": "5y"},
	} {
		resp := c.post("/api/log-search/estimate", req)
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%s: status %d, want 422", name, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

func TestLogSearch_StopsAtTheScanLimitAndSaysSo(t *testing.T) {
	env := newLogSearchEnv(t, nil)
	env.api.LogSearch.MaxBytes = 1 // every fake query scans more than one byte
	c := login(t, env.srv, "sam", testPassword)

	resp := c.post("/api/log-search", searchReq(paymentsID()))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var body struct {
		ScanLimitReached bool    `json:"scan_limit_reached"`
		ScanLimitBytes   int64   `json:"scan_limit_bytes"`
		BytesScanned     int64   `json:"bytes_scanned"`
		CostUSD          float64 `json:"cost_usd"`
		Targets          []struct {
			State   string `json:"state"`
			Message string `json:"message"`
		} `json:"targets"`
		Services []struct {
			State string `json:"state"`
		} `json:"services"`
	}
	decode(t, resp, &body)
	if !body.ScanLimitReached || body.ScanLimitBytes != 1 {
		t.Errorf("scan_limit_reached=%v scan_limit_bytes=%d, want true and 1", body.ScanLimitReached, body.ScanLimitBytes)
	}
	if len(body.Targets) != 1 || body.Targets[0].State != "scan_limit" || body.Targets[0].Message == "" {
		t.Errorf("targets %+v, want one in state scan_limit with a message", body.Targets)
	}
	if body.BytesScanned <= 1 || body.CostUSD <= 0 {
		t.Errorf("bytes=%d cost=%v, want the bytes scanned and their cost kept", body.BytesScanned, body.CostUSD)
	}
	if len(env.fk.StoppedQueries()) != 1 {
		t.Errorf("StopQuery calls = %d, want 1", len(env.fk.StoppedQueries()))
	}
	if len(body.Services) != 1 || body.Services[0].State != "ok" {
		t.Errorf("services %+v: a partial result is not a failed service", body.Services)
	}
}
