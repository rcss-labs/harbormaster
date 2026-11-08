package api

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/smithy-go"

	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/logs"
	"github.com/rcss-labs/harbormaster/internal/store"
)

type logSearchEnv struct {
	srv     *httptest.Server
	fk      *fake.Fake
	st      *store.Store
	limiter *logs.TailLimiter
}

// newLogSearchEnv builds a server whose searcher polls fast, so a search over the fake takes milliseconds.
func newLogSearchEnv(t *testing.T, prepare func(*fake.Estate), wrap ...func(awsx.Clients) awsx.Clients) *logSearchEnv {
	t.Helper()
	env := &logSearchEnv{limiter: logs.NewTailLimiter(1)}
	var srv *httptest.Server
	srv, env.fk, env.st, _ = newServerCustom(t, true, func(s *Server) {
		if prepare != nil {
			prepare(s.Clients.(*fake.Fake).Estate)
		}
		for _, w := range wrap {
			s.Clients = w(s.Clients)
		}
		s.LogSearch = &logs.Searcher{Clients: s.Clients, Now: s.Now, PollEvery: 5 * time.Millisecond}
		s.SearchLimiter = env.limiter
	})
	env.srv = srv
	seedMockupTags(t, env.st)
	for _, u := range []string{"oncall", "omer", "dana"} {
		setPassword(t, env.st, u)
	}
	pollAll(t, env.st, env.fk)
	return env
}

// denyLogsAccount makes StartQuery in one account fail the way a role without logs:StartQuery does.
type denyLogsAccount struct {
	awsx.Clients
	account string
}

func (d denyLogsAccount) Logs(ctx context.Context, t awsx.Target) (awsx.LogsAPI, error) {
	api, err := d.Clients.Logs(ctx, t)
	if err != nil || t.AccountID != d.account {
		return api, err
	}
	return deniedLogs{LogsAPI: api}, nil
}

type deniedLogs struct{ awsx.LogsAPI }

func (deniedLogs) StartQuery(context.Context, *cloudwatchlogs.StartQueryInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.StartQueryOutput, error) {
	return nil, &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "not allowed"}
}

func mustHash(t *testing.T) string {
	t.Helper()
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

type searchBody struct {
	Rows []struct {
		T         time.Time `json:"t"`
		ServiceID string    `json:"service_id"`
		Container string    `json:"container"`
		TaskID    string    `json:"task_id"`
		Message   string    `json:"message"`
	} `json:"rows"`
	RecordsMatched int64   `json:"records_matched"`
	BytesScanned   int64   `json:"bytes_scanned"`
	Capped         bool    `json:"capped"`
	CostUSD        float64 `json:"cost_usd"`
	Services       []struct {
		ID, Name, State, Message string
	} `json:"services"`
	Targets []struct {
		AccountID   string `json:"account_id"`
		AccountName string `json:"account_name"`
		Region      string `json:"region"`
		State       string `json:"state"`
		Message     string `json:"message"`
	} `json:"targets"`
}

func webFrontendID() string {
	return serviceID(homelabAccountID, "us-east-1", "core-services", "web-frontend")
}

func searchReq(ids ...string) map[string]any {
	return map[string]any{"service_ids": ids, "range": "1h", "query": ""}
}

func TestLogSearchServices_OnlyLogsReadable(t *testing.T) {
	env := newLogSearchEnv(t, nil)

	// omer: Operator (View+Logs) on Dev, Viewer (View only) on Staging.
	var operator struct {
		Items []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Cluster     string `json:"cluster"`
			AccountID   string `json:"account_id"`
			AccountName string `json:"account_name"`
			Region      string `json:"region"`
		} `json:"items"`
	}
	omer := login(t, env.srv, "omer", testPassword)
	before := env.fk.Calls()
	resp := omer.get("/api/log-search/services")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	decode(t, resp, &operator)
	var names []string
	for _, item := range operator.Items {
		names = append(names, item.Cluster+"/"+item.Name)
		if item.AccountName == "" || item.Region == "" || item.AccountID == "" {
			t.Errorf("incomplete item %+v", item)
		}
	}
	want := []string{"background-jobs/email-sender", "background-jobs/image-resizer", "background-jobs/report-worker",
		"ci-tools/jenkins-controller", "edge-proxy/cert-renewer", "edge-proxy/edge-nginx", "eu-apps/catalog-api", "eu-apps/search-indexer"}
	for _, w := range want {
		if !slices.Contains(names, w) {
			t.Errorf("operator list lacks %s: %v", w, names)
		}
	}
	for _, n := range names {
		if strings.HasPrefix(n, "staging-web/") || strings.HasPrefix(n, "core-services/") || n == "ci-tools/artifact-proxy" {
			t.Errorf("operator list has %s, which they cannot read logs of", n)
		}
	}
	sortKeys := make([]string, 0, len(operator.Items))
	for _, item := range operator.Items {
		sortKeys = append(sortKeys, item.AccountName+"/"+item.Cluster+"/"+item.Name)
	}
	if !slices.IsSorted(sortKeys) {
		t.Errorf("not sorted by account name, cluster, name: %v", sortKeys)
	}
	if after := env.fk.Calls(); !maps.Equal(before, after) {
		t.Errorf("listing made AWS calls: before %v, after %v", before, after)
	}

	// A user with View only anywhere sees nothing.
	hash := mustHash(t)
	viewer, err := env.st.CreateUser(t.Context(), "viewer", hash, false, false)
	if err != nil {
		t.Fatal(err)
	}
	dev, err := env.st.DevTag(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := env.st.SetPermission(t.Context(), viewer.ID, dev.ID, authz.ActionSet(authz.View)); err != nil {
		t.Fatal(err)
	}
	var none struct {
		Items []json.RawMessage `json:"items"`
	}
	decode(t, login(t, env.srv, "viewer", testPassword).get("/api/log-search/services"), &none)
	if none.Items == nil || len(none.Items) != 0 {
		t.Errorf("viewer items = %v, want an empty list", none.Items)
	}
}

func TestLogSearch_NotVisibleIs404AndNoAWSCall(t *testing.T) {
	env := newLogSearchEnv(t, nil)
	c := login(t, env.srv, "omer", testPassword)
	// omer has no permission on Prod at all: the service is invisible.
	resp := c.post("/api/log-search", searchReq(catalogID(), paymentsID()))
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
	if n := env.fk.Calls()["StartQuery"]; n != 0 {
		t.Errorf("StartQuery called %d times", n)
	}
}

func TestLogSearch_ViewWithoutLogsIs403(t *testing.T) {
	env := newLogSearchEnv(t, nil)
	hash := mustHash(t)
	user, err := env.st.CreateUser(t.Context(), "prod-viewer", hash, false, false)
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
	resp := login(t, env.srv, "prod-viewer", testPassword).post("/api/log-search", searchReq(paymentsID()))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d, want 403", resp.StatusCode)
	}
	if _, msg := errorCode(t, resp); msg != "You need Logs on Prod" {
		t.Errorf("message %q", msg)
	}
	if n := env.fk.Calls()["StartQuery"]; n != 0 {
		t.Errorf("StartQuery called %d times", n)
	}
}

func TestLogSearch_Validation(t *testing.T) {
	env := newLogSearchEnv(t, nil)
	c := login(t, env.srv, "sam", testPassword)
	now := time.Now().UTC()
	many := make([]string, 21)
	for i := range many {
		many[i] = serviceID(homelabAccountID, "us-east-1", "core-services", "svc-"+strings.Repeat("x", i+1))
	}
	custom := func(from, to time.Time) map[string]any {
		return map[string]any{"service_ids": []string{paymentsID()}, "range": "custom", "from": from, "to": to, "query": ""}
	}
	withQuery := func(q string) map[string]any {
		return map[string]any{"service_ids": []string{paymentsID()}, "range": "1h", "query": q}
	}
	cases := []struct {
		name string
		body map[string]any
		want string
	}{
		{"no services", searchReq(), "Choose at least one service."},
		{"too many services", searchReq(many...), "Choose at most 20 services."},
		{"bad service id", searchReq("not-an-id"), "That is not a valid service."},
		{"bad range", map[string]any{"service_ids": []string{paymentsID()}, "range": "2h", "query": ""}, "Choose a time range"},
		{"custom without times", map[string]any{"service_ids": []string{paymentsID()}, "range": "custom", "query": ""}, "Custom ranges need a start and an end."},
		{"custom backwards", custom(now, now.Add(-time.Hour)), "The start must be before the end."},
		{"8 day custom", custom(now.Add(-8*24*time.Hour), now), "Choose a range of at most 7 days."},
		{"future end", custom(now.Add(-time.Hour), now.Add(time.Hour)), "The end of the range cannot be in the future."},
		{"long query", withQuery(strings.Repeat("a", 4001)), "The query is too long"},
		{"stats query", withQuery("stats count(*) by bin(1m)"), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := c.post("/api/log-search", tc.body)
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("status %d, want 422", resp.StatusCode)
			}
			code, msg := errorCode(t, resp)
			if code != "invalid" || !strings.Contains(msg, tc.want) || msg == "" {
				t.Errorf("code %q message %q, want it to contain %q", code, msg, tc.want)
			}
		})
	}
	if n := env.fk.Calls()["StartQuery"]; n != 0 {
		t.Errorf("StartQuery called %d times", n)
	}
}

func TestLogSearch_ReturnsRowsAndCost(t *testing.T) {
	env := newLogSearchEnv(t, nil)
	c := login(t, env.srv, "sam", testPassword)
	resp := c.post("/api/log-search", searchReq(paymentsID(), catalogID()))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var body searchBody
	decode(t, resp, &body)
	if len(body.Rows) == 0 || body.BytesScanned <= 0 {
		t.Fatalf("rows %d bytes %d", len(body.Rows), body.BytesScanned)
	}
	seen := map[string]bool{}
	for i, row := range body.Rows {
		seen[row.ServiceID] = true
		if row.TaskID == "" || row.Container == "" {
			t.Fatalf("incomplete row %+v", row)
		}
		if i > 0 && row.T.After(body.Rows[i-1].T) {
			t.Fatalf("row %d is newer than row %d: not newest first", i, i-1)
		}
	}
	if !seen[paymentsID()] || !seen[catalogID()] || len(seen) != 2 {
		t.Errorf("rows cover services %v, want exactly the two requested", seen)
	}
	if want := float64(body.BytesScanned) / (1 << 30) * 0.005; body.CostUSD != want {
		t.Errorf("cost %v, want %v", body.CostUSD, want)
	}
	if len(body.Services) != 2 || len(body.Targets) != 2 {
		t.Errorf("services %d targets %d, want 2 and 2 (two regions)", len(body.Services), len(body.Targets))
	}
	for _, target := range body.Targets {
		if target.State != "ok" || target.AccountName == "" {
			t.Errorf("target %+v", target)
		}
	}
}

func TestLogSearch_RowsOutsideScopeAreDropped(t *testing.T) {
	const sharedGroup = "/ecs/core-services/payments-api"
	env := newLogSearchEnv(t, func(e *fake.Estate) {
		// web-frontend now logs to payments-api's group (stream prefix ecs/web-frontend/).
		e.Mu.Lock()
		for _, td := range e.TaskDefinitions {
			for _, cd := range td.Definition.ContainerDefinitions {
				if aws.ToString(cd.Name) == "web-frontend" && cd.LogConfiguration != nil {
					cd.LogConfiguration.Options["awslogs-group"] = sharedGroup
				}
			}
		}
		e.Mu.Unlock()
		at := time.Now().Add(-10 * time.Minute)
		e.SetLogLines(sharedGroup, []fake.LogLine{
			{Stream: "ecs/payments-api/aaaaaaaa11112222", Timestamp: at, Message: "from payments"},
			{Stream: "ecs/web-frontend/bbbbbbbb33334444", Timestamp: at.Add(time.Second), Message: "from web"},
		})
	})
	c := login(t, env.srv, "sam", testPassword)
	var body searchBody
	decode(t, c.post("/api/log-search", searchReq(paymentsID())), &body)
	if len(body.Rows) != 1 || body.Rows[0].ServiceID != paymentsID() || body.Rows[0].Message != "from payments" {
		t.Fatalf("rows = %+v, want only payments' line", body.Rows)
	}
	var both searchBody
	decode(t, c.post("/api/log-search", searchReq(paymentsID(), webFrontendID())), &both)
	if len(both.Rows) != 2 {
		t.Errorf("both requested: %d rows, want 2", len(both.Rows))
	}
}

func TestLogSearch_NoPermissionAccountNamed(t *testing.T) {
	env := newLogSearchEnv(t, nil, func(c awsx.Clients) awsx.Clients { return denyLogsAccount{Clients: c, account: stagingAccountID} })
	c := login(t, env.srv, "sam", testPassword)
	var body searchBody
	decode(t, c.post("/api/log-search", searchReq(paymentsID(), stagingWebID())), &body)
	var denied bool
	for _, target := range body.Targets {
		if target.AccountID == stagingAccountID {
			denied = true
			if target.State != "no_permission" || target.AccountName != "staging-example" ||
				!strings.Contains(target.Message, "staging-example") || strings.Contains(target.Message, stagingAccountID) {
				t.Errorf("target %+v", target)
			}
		}
	}
	if !denied {
		t.Fatalf("no staging target in %+v", body.Targets)
	}
	if len(body.Rows) == 0 {
		t.Error("rows of the other account are missing")
	}
	for _, s := range body.Services {
		want := "ok"
		if s.ID == stagingWebID() {
			want = "target_failed"
		}
		if s.State != want {
			t.Errorf("service %s state %s, want %s", s.Name, s.State, want)
		}
	}
}

// postWithContext sends a log search that the caller can abandon.
func postWithContext(ctx context.Context, c *apiClient, body any) (*http.Response, error) {
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/log-search", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", c.csrf)
	return c.http.Do(req)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestLogSearch_OneRunningPerUser(t *testing.T) {
	env := newLogSearchEnv(t, nil)
	env.fk.InsightsAlwaysRunning = true
	first := login(t, env.srv, "sam", testPassword)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if resp, err := postWithContext(ctx, first, searchReq(paymentsID())); err == nil {
			resp.Body.Close()
		}
	}()
	waitFor(t, "the first search to start", func() bool { return env.fk.Calls()["StartQuery"] >= 1 })

	resp := first.post("/api/log-search", searchReq(paymentsID()))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("second search status %d, want 409", resp.StatusCode)
	}
	if code, msg := errorCode(t, resp); code != "conflict" || msg != "You already have a log search running." {
		t.Errorf("code %q message %q", code, msg)
	}

	// Another user is not blocked: dana (Prod View+Logs) starts her own search, which also runs until stopped.
	other := login(t, env.srv, "dana", testPassword)
	otherCtx, otherCancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer otherCancel()
	if resp, err := postWithContext(otherCtx, other, searchReq(paymentsID())); err == nil {
		t.Errorf("dana's search returned %d before being stopped; it should have been running", resp.StatusCode)
		resp.Body.Close()
	}
	if env.fk.Calls()["StartQuery"] < 2 {
		t.Error("dana's search never started a query: she was blocked")
	}
	cancel()
	<-done
}

func TestLogSearch_RateLimited(t *testing.T) {
	env := newLogSearchEnv(t, nil)
	c := login(t, env.srv, "sam", testPassword)
	for i := 1; i <= 6; i++ {
		resp := c.post("/api/log-search", searchReq(paymentsID()))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("search %d: status %d", i, resp.StatusCode)
		}
		resp.Body.Close()
	}
	resp := c.post("/api/log-search", searchReq(paymentsID()))
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("seventh search: status %d, want 429", resp.StatusCode)
	}
	if code, _ := errorCode(t, resp); code != "rate_limited" {
		t.Errorf("code %q", code)
	}
}

func auditRows(t *testing.T, st *store.Store, action string) []store.AuditEntry {
	t.Helper()
	entries, err := st.ListAudit(t.Context(), 200, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.AuditEntry
	for _, e := range entries {
		if e.Action == action {
			out = append(out, e)
		}
	}
	return out
}

func TestLogSearch_AbortReleasesSlotAndAudits(t *testing.T) {
	env := newLogSearchEnv(t, nil)
	env.fk.InsightsAlwaysRunning = true
	c := login(t, env.srv, "sam", testPassword)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		body := searchReq(paymentsID())
		body["query"] = "filter @message like /needle/"
		if resp, err := postWithContext(ctx, c, body); err == nil {
			resp.Body.Close()
		}
	}()
	waitFor(t, "the search to start", func() bool { return env.fk.Calls()["StartQuery"] >= 1 })
	cancel()
	<-done
	waitFor(t, "the audit row", func() bool { return len(auditRows(t, env.st, "log_search")) == 1 })

	if len(env.fk.StoppedQueries()) == 0 {
		t.Error("the running query was not stopped")
	}
	waitFor(t, "the slot to be released", func() bool {
		user, err := env.st.GetUserByUsername(t.Context(), "sam")
		return err == nil && env.limiter.Count(user.ID) == 0
	})
	row := auditRows(t, env.st, "log_search")[0]
	if row.Details["query"] != "filter @message like /needle/" {
		t.Errorf("audit details query = %v", row.Details["query"])
	}
}

func TestLogSearch_AuditsNotOperations(t *testing.T) {
	env := newLogSearchEnv(t, nil)
	c := login(t, env.srv, "sam", testPassword)
	body := searchReq(paymentsID(), catalogID())
	body["query"] = "filter @message like /ERROR/"
	resp := c.post("/api/log-search", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	resp.Body.Close()
	rows := auditRows(t, env.st, "log_search")
	if len(rows) != 1 {
		t.Fatalf("%d audit rows, want 1", len(rows))
	}
	row := rows[0]
	if row.Summary != "Searched logs of 2 services" || row.Target != "payments-api +1" && row.Target != "catalog-api +1" || row.ActorUsername != "sam" {
		t.Errorf("audit row %+v", row)
	}
	for _, key := range []string{"service_ids", "service_names", "range", "from", "to", "query", "bytes_scanned", "records_matched", "target_states"} {
		if _, ok := row.Details[key]; !ok {
			t.Errorf("details lack %q: %v", key, row.Details)
		}
	}
	var operations int
	if err := env.st.Pool.QueryRow(t.Context(), `select count(*) from operation_log`).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if operations != 0 {
		t.Errorf("%d operation_log rows, want 0", operations)
	}
}
