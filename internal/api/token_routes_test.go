package api

import (
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// pinnedTokenRoutes is the security boundary of token access: the routes an access token may call. It is a literal on
// purpose. 38 come from the design's tool table (phase 1) and GET /api/tokens/current is the whoami route.
var pinnedTokenRoutes = []string{
	"GET /api/alerts",
	"GET /api/analytics/capacity",
	"GET /api/analytics/cost",
	"GET /api/analytics/delivery",
	"GET /api/analytics/operations",
	"GET /api/analytics/reliability",
	"GET /api/clusters",
	"GET /api/clusters/{clusterId}",
	"GET /api/config-search",
	"GET /api/log-search/services",
	"GET /api/networks",
	"GET /api/networks/{account}/{region}",
	"GET /api/overview",
	"GET /api/search",
	"GET /api/security",
	"GET /api/security/images/{digest}",
	"GET /api/security/services/{serviceId}",
	"GET /api/services",
	"GET /api/services/{serviceId}",
	"GET /api/services/{serviceId}/arm64-readiness",
	"GET /api/services/{serviceId}/desired-count",
	"GET /api/services/{serviceId}/logs",
	"GET /api/services/{serviceId}/metrics",
	"GET /api/services/{serviceId}/offhours",
	"GET /api/services/{serviceId}/operations",
	"GET /api/services/{serviceId}/revisions",
	"GET /api/services/{serviceId}/revisions/{revision}",
	"GET /api/services/{serviceId}/scaling",
	"GET /api/services/{serviceId}/target-health",
	"GET /api/services/{serviceId}/task-definition",
	"GET /api/services/{serviceId}/tasks",
	"GET /api/services/{serviceId}/timeline",
	"GET /api/services/{serviceId}/timeline/tasks/{taskId}",
	"GET /api/storage",
	"GET /api/tasks",
	"GET /api/tokens/current",
	"GET /api/vpcs/{vpcId}/tree",
	"POST /api/log-search",
	"POST /api/log-search/estimate",
}

func TestTokenRoutes_PinnedList(t *testing.T) {
	_, api, _, _, _ := newServerCustomAPI(t, true, withTokens(nil, nil))
	var got []string
	for _, route := range api.Routes() {
		if route.Opts.Token != "" {
			got = append(got, route.Pattern)
		}
	}
	slices.Sort(got)
	want := slices.Clone(pinnedTokenRoutes)
	slices.Sort(want)
	if len(want) != 39 {
		t.Fatalf("the pinned list has %d entries, want exactly 39 (38 tool routes plus GET /api/tokens/current)", len(want))
	}
	if !slices.Equal(got, want) {
		var extra, missing []string
		for _, p := range got {
			if !slices.Contains(want, p) {
				extra = append(extra, p)
			}
		}
		for _, p := range want {
			if !slices.Contains(got, p) {
				missing = append(missing, p)
			}
		}
		t.Fatalf("token-readable routes changed; this list is the security boundary, update it deliberately.\nnewly readable: %v\nno longer readable: %v", extra, missing)
	}
}

func TestTokenRoutes_NoneIsAdminOrWrite(t *testing.T) {
	_, api, _, _, _ := newServerCustomAPI(t, true, withTokens(nil, nil))
	marked := 0
	for _, route := range api.Routes() {
		if route.Opts.Token == "" {
			continue
		}
		marked++
		if route.Opts.Admin {
			t.Errorf("%s is marked for tokens and admin-only", route.Pattern)
		}
		if route.Opts.Public {
			t.Errorf("%s is marked for tokens and public", route.Pattern)
		}
		if route.Opts.Token != auth.ScopeRead {
			t.Errorf("%s has token scope %q, want read", route.Pattern, route.Opts.Token)
		}
		isGet := strings.HasPrefix(route.Pattern, "GET ")
		searchPost := route.Pattern == "POST /api/log-search" || route.Pattern == "POST /api/log-search/estimate"
		if !isGet && !searchPost {
			t.Errorf("%s is a write route marked for tokens", route.Pattern)
		}
	}
	if marked == 0 {
		t.Fatal("no route is marked for tokens")
	}
}

var pathParam = regexp.MustCompile(`\{[^}]+\}`)

// concretePath turns a mux pattern into a requestable path, filling every wildcard with fill.
func concretePath(pattern, fill string) (method, path string) {
	method, path, _ = strings.Cut(pattern, " ")
	return method, pathParam.ReplaceAllString(path, fill)
}

func TestTokenCredential_RefusedOnEveryOtherRoute(t *testing.T) {
	srv, _, st := newTokenServer(t)
	admin := tokenClient(t, srv, st, "sam") // an admin's token must be refused as well
	_, api, _, _, _ := newServerCustomAPI(t, true, withTokens(nil, nil))

	checked := 0
	for _, route := range api.Routes() {
		if route.Opts.Token != "" || route.Opts.Public {
			continue
		}
		method, path := concretePath(route.Pattern, "x")
		resp := admin.do(method, path, map[string]any{}, nil)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s with an admin's token: status %d, want 403", route.Pattern, resp.StatusCode)
			resp.Body.Close()
			continue
		}
		if code, _ := errorCode(t, resp); code != "token_not_allowed" {
			t.Errorf("%s: code %q, want token_not_allowed", route.Pattern, code)
		}
		checked++
	}
	if checked < 80 {
		t.Errorf("only %d unmarked routes were checked; the registry looks incomplete", checked)
	}
	for _, path := range []string{"/api/nope", "/api/services/x/nope", "/api/"} {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
			resp := admin.do(method, path, nil, nil)
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("%s %s with a token: status %d, want 403 (no probing which paths exist)", method, path, resp.StatusCode)
			}
			resp.Body.Close()
		}
	}
}

// viewerOfEverything is a non-admin with View and Logs on every tag.
func viewerOfEverything(t *testing.T, st *store.Store, name string) {
	t.Helper()
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	user, err := st.CreateUser(t.Context(), name, hash, false, false)
	if err != nil {
		t.Fatal(err)
	}
	tags, err := st.ListTags(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, tag := range tags {
		if err := st.SetPermission(t.Context(), user.ID, tag.ID, authz.ActionSet(authz.View|authz.Logs)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTokenCredential_AllowedRoutesAnswer(t *testing.T) {
	srv, fk, st := newTokenServer(t)
	seedAccounts(t, st)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	viewerOfEverything(t, st, "viewer")
	c := tokenClient(t, srv, st, "viewer")
	_, api, _, _, _ := newServerCustomAPI(t, true, withTokens(nil, nil))

	checked := 0
	for _, route := range api.Routes() {
		if route.Opts.Token == "" || !strings.HasPrefix(route.Pattern, "GET ") {
			continue
		}
		_, path := concretePath(route.Pattern, "x")
		resp := c.get(path)
		resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			t.Errorf("%s answered %d to a viewer's token", route.Pattern, resp.StatusCode)
		}
		checked++
	}
	if checked != 37 {
		t.Errorf("checked %d GET routes, want 37", checked)
	}
	// A real id answers 200, not only 404.
	for _, path := range []string{"/api/overview", "/api/services/" + paymentsID(), "/api/services/" + paymentsID() + "/tasks"} {
		resp := c.get(path)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s with a viewer's token: status %d, want 200", path, resp.StatusCode)
		}
	}
}

func TestReadOnlySubject_AdminCannotActBeyondViewAndLogs(t *testing.T) {
	const restart = "POST /api/services/{serviceId}/restart"
	hs, _, hfk, hst, _ := newServerCustomAPI(t, true, withTokens(nil, func(s *Server) {
		// Layer 1 bypassed by mistake: the write route is marked for tokens.
		s.testTokenRoutes = map[string]auth.Scope{restart: auth.ScopeRead}
	}))
	seedAccounts(t, hst)
	seedMockupTags(t, hst)
	pollAll(t, hst, hfk)

	admin := tokenClient(t, hs, hst, "sam")
	resp := admin.post("/api/services/"+paymentsID()+"/restart", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("admin token on a marked write route: status %d, want 403 from the handler", resp.StatusCode)
	}
	if code, _ := errorCode(t, resp); code == "token_not_allowed" {
		t.Fatal("refused by the route gate; the test must prove the read-only subject alone holds")
	}
	// Control: the same admin as a session may restart, so the refusal above came from the read-only subject.
	session := login(t, hs, "sam", testPassword)
	resp = session.post("/api/services/"+paymentsID()+"/restart", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("control: admin session restart status %d, want 202", resp.StatusCode)
	}
}

func TestTokenCredential_DesiredCountReadYesWriteNo(t *testing.T) {
	srv, fk, st := newTokenServer(t)
	seedAccounts(t, st)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	c := tokenClient(t, srv, st, "sam")
	path := "/api/services/" + paymentsID() + "/desired-count"
	resp := c.get(path)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET desired-count with a token: status %d, want 200", resp.StatusCode)
	}
	resp = c.post(path, map[string]any{"desired": 3})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("POST desired-count with a token: status %d, want 403", resp.StatusCode)
	}
	if code, _ := errorCode(t, resp); code != "token_not_allowed" {
		t.Errorf("code %q, want token_not_allowed", code)
	}
}

func TestSessionCredential_UnaffectedByTokenGate(t *testing.T) {
	srv, _, _ := newTokenServer(t)
	c := login(t, srv, "sam", testPassword)
	for _, path := range []string{"/api/me", "/api/users", "/api/tokens"} {
		resp := c.get(path)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("session GET %s: status %d, want 200", path, resp.StatusCode)
		}
	}
	resp := c.get("/api/nope")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("session on an unknown path: status %d, want the usual 404", resp.StatusCode)
	}
}
