package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/store"
)

type savedBody struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	Query          string     `json:"query"`
	ServiceIDs     []string   `json:"service_ids"`
	HiddenServices int        `json:"hidden_services"`
	Range          string     `json:"range"`
	From           *time.Time `json:"from"`
	To             *time.Time `json:"to"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type savedListBody struct {
	Items []savedBody `json:"items"`
}

func savedReq(name string, serviceIDs ...string) map[string]any {
	return map[string]any{"name": name, "query": "filter @message like /ERROR/", "service_ids": serviceIDs, "range": "1h"}
}

func listSaved(t *testing.T, c *apiClient) savedListBody {
	t.Helper()
	var list savedListBody
	resp := c.get("/api/log-search/saved")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status %d", resp.StatusCode)
	}
	decode(t, resp, &list)
	return list
}

func TestSavedLogSearch_RoundTrip(t *testing.T) {
	env := newLogSearchEnv(t, nil)
	c := login(t, env.srv, "omer", testPassword)
	to := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	from := to.Add(-time.Hour)
	body := map[string]any{"name": "  Checkout errors  ", "query": "filter @message like /ERROR/",
		"service_ids": []string{catalogID()}, "range": "custom", "from": from, "to": to}

	resp := c.post("/api/log-search/saved", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save status %d", resp.StatusCode)
	}
	var saved savedBody
	decode(t, resp, &saved)
	if saved.ID == "" || saved.Name != "Checkout errors" || saved.HiddenServices != 0 {
		t.Fatalf("saved %+v", saved)
	}

	list := listSaved(t, c)
	if len(list.Items) != 1 {
		t.Fatalf("%d saved searches, want 1", len(list.Items))
	}
	got := list.Items[0]
	if got.ID != saved.ID || got.Name != "Checkout errors" || got.Query != body["query"] || got.Range != "custom" ||
		!slices.Equal(got.ServiceIDs, []string{catalogID()}) || got.From == nil || !got.From.Equal(from) ||
		got.To == nil || !got.To.Equal(to) {
		t.Errorf("listed %+v, want the saved search", got)
	}

	if resp := c.do(http.MethodDelete, "/api/log-search/saved/"+saved.ID, nil, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status %d", resp.StatusCode)
	}
	if after := listSaved(t, c); after.Items == nil || len(after.Items) != 0 {
		t.Errorf("after delete items = %v, want an empty list", after.Items)
	}
}

func TestSavedLogSearch_HidesServicesNoLongerReadable(t *testing.T) {
	env := newLogSearchEnv(t, nil)
	c := login(t, env.srv, "omer", testPassword)
	if resp := c.post("/api/log-search/saved", savedReq("Catalog", catalogID())); resp.StatusCode != http.StatusOK {
		t.Fatalf("save status %d", resp.StatusCode)
	}

	// Revoke Logs on Dev, keeping View: catalog-api stays visible but is no longer readable.
	ctx := t.Context()
	omer, err := env.st.GetUserByUsername(ctx, "omer")
	if err != nil {
		t.Fatal(err)
	}
	dev, err := env.st.DevTag(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.st.SetPermission(ctx, omer.ID, dev.ID, authz.ActionSet(authz.View)); err != nil {
		t.Fatal(err)
	}

	resp := c.get("/api/log-search/saved")
	raw, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	var list savedListBody
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("%d saved searches, want 1", len(list.Items))
	}
	got := list.Items[0]
	if got.ServiceIDs == nil || len(got.ServiceIDs) != 0 || got.HiddenServices != 1 {
		t.Errorf("service_ids %v hidden_services %d, want an empty list and 1", got.ServiceIDs, got.HiddenServices)
	}
	// The hidden service's identity must not reach the page.
	if strings.Contains(string(raw), catalogID()) || strings.Contains(string(raw), "catalog-api") {
		t.Errorf("response leaks the hidden service: %s", raw)
	}
}

func TestSavedLogSearch_RejectsUnreadableService(t *testing.T) {
	env := newLogSearchEnv(t, nil)
	c := login(t, env.srv, "omer", testPassword)
	cases := []struct {
		name   string
		ids    []string
		status int
	}{
		// omer cannot see Prod at all: 404 for the whole request.
		{"invisible", []string{paymentsID()}, http.StatusNotFound},
		// omer sees Staging with View only: 403.
		{"visible without Logs", []string{stagingWebID()}, http.StatusForbidden},
		// One invisible id makes the whole request 404, whichever order it comes in.
		{"invisible beside visible without Logs", []string{stagingWebID(), paymentsID()}, http.StatusNotFound},
		{"invisible first", []string{paymentsID(), stagingWebID()}, http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := c.post("/api/log-search/saved", savedReq("Unreadable", tc.ids...))
			if resp.StatusCode != tc.status {
				t.Fatalf("status %d, want %d", resp.StatusCode, tc.status)
			}
			resp.Body.Close()
		})
	}
	if list := listSaved(t, c); len(list.Items) != 0 {
		t.Errorf("%d saved searches kept after rejected saves", len(list.Items))
	}
}

func TestSavedLogSearch_OtherUsersIdIs404(t *testing.T) {
	env := newLogSearchEnv(t, nil)
	sam := login(t, env.srv, "sam", testPassword)
	var saved savedBody
	decode(t, sam.post("/api/log-search/saved", savedReq("Payments", paymentsID())), &saved)
	if saved.ID == "" {
		t.Fatal("sam's save returned no id")
	}

	dana := login(t, env.srv, "dana", testPassword)
	if resp := dana.do(http.MethodDelete, "/api/log-search/saved/"+saved.ID, nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("dana deleting sam's search: status %d, want 404", resp.StatusCode)
	}
	if list := listSaved(t, sam); len(list.Items) != 1 {
		t.Errorf("sam has %d saved searches after dana's delete, want 1", len(list.Items))
	}
}

func TestSavedLogSearch_LimitIs409(t *testing.T) {
	env := newLogSearchEnv(t, nil)
	c := login(t, env.srv, "omer", testPassword)
	for i := range store.MaxSavedLogSearches {
		if resp := c.post("/api/log-search/saved", savedReq(fmt.Sprintf("search %02d", i), catalogID())); resp.StatusCode != http.StatusOK {
			t.Fatalf("save %d: status %d", i, resp.StatusCode)
		}
	}
	resp := c.post("/api/log-search/saved", savedReq("one too many", catalogID()))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status %d, want 409", resp.StatusCode)
	}
	if code, msg := errorCode(t, resp); code != "conflict" || msg != "You can save up to 50 queries. Delete one first." {
		t.Errorf("code %q message %q", code, msg)
	}
}

func TestSavedLogSearch_Validation(t *testing.T) {
	env := newLogSearchEnv(t, nil)
	c := login(t, env.srv, "omer", testPassword)
	withQuery := func(query string) map[string]any {
		body := savedReq("Valid", catalogID())
		body["query"] = query
		return body
	}
	cases := []struct {
		name string
		body map[string]any
		want string
	}{
		{"empty name", savedReq("   ", catalogID()), "Name"},
		{"81 character name", savedReq(strings.Repeat("n", 81), catalogID()), "Name"},
		{"no services", savedReq("Valid"), "Choose at least one service."},
		{"bad range", map[string]any{"name": "Valid", "query": "", "service_ids": []string{catalogID()}, "range": "2h"}, "Choose a time range"},
		{"query the search would refuse", withQuery("stats count(*)"), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := c.post("/api/log-search/saved", tc.body)
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("status %d, want 422", resp.StatusCode)
			}
			if code, msg := errorCode(t, resp); code != "invalid" || !strings.Contains(msg, tc.want) {
				t.Errorf("code %q message %q, want it to contain %q", code, msg, tc.want)
			}
		})
	}
	if list := listSaved(t, c); len(list.Items) != 0 {
		t.Errorf("%d saved searches kept after invalid saves", len(list.Items))
	}
}
