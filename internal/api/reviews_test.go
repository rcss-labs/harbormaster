package api

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/model"
)

type reviewSeed struct {
	state, typ, summary string
	covered             []authz.TagID
	requested           time.Time
	replaced            bool
}

// seedRun writes a review run row directly, so each test states exactly the row it needs. The review is unlinked.
func (e *invEnv) seedRun(t *testing.T, s reviewSeed) int64 {
	t.Helper()
	if s.state == "" {
		s.state = "done"
	}
	if s.typ == "" {
		s.typ = "cost"
	}
	if s.requested.IsZero() {
		s.requested = time.Now().UTC()
	}
	covered := make([]int64, 0, len(s.covered))
	for _, tag := range s.covered {
		covered = append(covered, int64(tag))
	}
	var id int64
	err := e.st.Pool.QueryRow(t.Context(), `insert into agent_review_runs
		(review_name, review_type, period_key, period_label, scheduled_for, state, summary, summary_replaced, covered_tags,
		 services_total, scope_label, requested_at, finished_at)
		values ('Weekly cost', $1, 'x:' || gen_random_uuid()::text, 'Week 47', $2::timestamptz, $3::text, $4, $5, $6, 3, 'Everything',
		 $2::timestamptz, case when $3::text in ('done', 'failed', 'skipped') then $2::timestamptz else null end)
		returning id`, s.typ, s.requested, s.state, s.summary, s.replaced, covered).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (e *invEnv) seedRunEvidence(t *testing.T, id int64, ordinal int, action string, key model.ServiceKey, note, excerpt string) {
	t.Helper()
	if _, err := e.st.Pool.Exec(t.Context(), `insert into agent_review_evidence
		(review_run_id, ordinal, tool, action, account_id, region, cluster_name, service_name, note, excerpt)
		values ($1, $2, 'describe_service', $3, $4, $5, $6, $7, $8, $9)`,
		id, ordinal, action, key.AccountID, key.Region, key.Cluster, key.Service, note, excerpt); err != nil {
		t.Fatal(err)
	}
}

func runPath(id int64) string { return fmt.Sprintf("/api/reviews/runs/%d", id) }

func getReviewRunDetail(t *testing.T, c *apiClient, id int64) ReviewRunDetailDTO {
	t.Helper()
	resp := c.get(runPath(id))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET review run %d: status %d, want 200", id, resp.StatusCode)
	}
	var out ReviewRunDetailDTO
	decode(t, resp, &out)
	return out
}

func listReviewRunsPage(t *testing.T, c *apiClient, query string) ReviewRunsDTO {
	t.Helper()
	resp := c.get("/api/reviews/runs" + query)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET review runs%s: status %d, want 200", query, resp.StatusCode)
	}
	var out ReviewRunsDTO
	decode(t, resp, &out)
	return out
}

func runIDs(items []ReviewRunDTO) []int64 {
	out := make([]int64, 0, len(items))
	for _, item := range items {
		out = append(out, item.ID)
	}
	return out
}

func reviewPaths(id int64) []string { return []string{"/api/reviews/runs", runPath(id)} }

func TestReviewRoutes_AbsentWhenDisabled(t *testing.T) {
	srv, _, _ := newTestServer(t)
	c := login(t, srv, "sam", testPassword)
	for _, path := range reviewPaths(1) {
		resp := c.get(path)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s with the agent off: status %d, want 404", path, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

func TestReviewRoutes_RefuseAccessTokens(t *testing.T) {
	srv, api, _, st, _ := newServerCustomAPI(t, true, withTokens(func(c *config.Config) {
		c.AgentURL, c.AgentRunTokenTTL = testAgentURL, 15*time.Minute
	}, nil))
	clients := map[string]*apiClient{
		"personal token": tokenClient(t, srv, st, "sam"),
		"run token":      runTokenClient(t, srv, api, st, "sam"),
	}
	oauth := newOAuthAPIEnvWith(t, true, withAgent(nil))
	granted := oauth.connect(t, oauth.sam)
	for name, c := range clients {
		for _, path := range reviewPaths(1) {
			resp := c.get(path)
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("%s: GET %s: status %d, want 403", name, path, resp.StatusCode)
				resp.Body.Close()
				continue
			}
			if code, _ := errorCode(t, resp); code != "token_not_allowed" {
				t.Errorf("%s: GET %s: code %q, want token_not_allowed", name, path, code)
			}
		}
	}
	for _, path := range reviewPaths(1) {
		resp := oauth.bearer(t, http.MethodGet, path, granted.AccessToken)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("OAuth token: GET %s: status %d, want 403", path, resp.StatusCode)
			resp.Body.Close()
			continue
		}
		if code, _ := errorCode(t, resp); code != "token_not_allowed" {
			t.Errorf("OAuth token: GET %s: code %q, want token_not_allowed", path, code)
		}
	}
}

func TestReviewRun_NeedsViewOnEveryCoveredTag(t *testing.T) {
	e := newInvEnv(t)
	id := e.seedRun(t, reviewSeed{summary: "Findings.", covered: []authz.TagID{e.tagID(t, "Dev"), e.tagID(t, "Prod")}})
	one := e.reader(t, "vera", map[string]authz.ActionSet{"Dev": viewOnly})
	both := e.reader(t, "wade", map[string]authz.ActionSet{"Dev": viewOnly, "Prod": viewOnly})

	wantNotFound(t, one, runPath(id))
	if got := getReviewRunDetail(t, both, id); got.Summary != "Findings." {
		t.Errorf("reader with both tags: summary %q, want the summary", got.Summary)
	}
	if got := getReviewRunDetail(t, e.sam, id); got.Summary != "Findings." {
		t.Errorf("admin: summary %q, want the summary", got.Summary)
	}
}

func TestReviewRun_Is404NotForbidden(t *testing.T) {
	e := newInvEnv(t)
	id := e.seedRun(t, reviewSeed{summary: "SECRET-SUMMARY", covered: []authz.TagID{e.tagID(t, "Prod")}})
	nobody := e.reader(t, "vera", nil)
	status, body := invBody(t, nobody, runPath(id))
	if status != http.StatusNotFound || strings.Contains(body, "SECRET-SUMMARY") || strings.Contains(body, "Weekly cost") {
		t.Fatalf("unreadable run: status %d body %s, want 404 with nothing of the run", status, body)
	}
	missing := e.seedRun(t, reviewSeed{})
	if _, err := e.st.Pool.Exec(t.Context(), `delete from agent_review_runs where id = $1`, missing); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{runPath(missing), "/api/reviews/runs/0", "/api/reviews/runs/01", "/api/reviews/runs/-1", "/api/reviews/runs/x"} {
		wantNotFound(t, e.sam, path)
		wantNotFound(t, nobody, path)
	}
}

func TestReviewRun_LosingATagHidesItOnTheNextRequest(t *testing.T) {
	e := newInvEnv(t)
	id := e.seedRun(t, reviewSeed{summary: "Findings.", covered: []authz.TagID{e.tagID(t, "Dev"), e.tagID(t, "Prod")}})
	reader := e.reader(t, "vera", map[string]authz.ActionSet{"Dev": viewOnly, "Prod": viewOnly})

	getReviewRunDetail(t, reader, id)
	if got := listReviewRunsPage(t, reader, ""); len(got.Items) != 1 {
		t.Fatalf("before: list holds %d runs, want 1", len(got.Items))
	}
	e.grant(t, "vera", "Prod", 0)
	wantNotFound(t, reader, runPath(id))
	if got := listReviewRunsPage(t, reader, ""); len(got.Items) != 0 {
		t.Fatalf("after losing Prod: list holds %d runs, want 0", len(got.Items))
	}
	e.grant(t, "vera", "Prod", viewOnly)
	getReviewRunDetail(t, reader, id)
}

func TestReviewRun_DeletedCoveredTagIsAdminOnly(t *testing.T) {
	e := newInvEnv(t)
	tag, err := e.st.CreateTag(t.Context(), "Sandbox", "red", false)
	if err != nil {
		t.Fatal(err)
	}
	id := e.seedRun(t, reviewSeed{summary: "Findings.", covered: []authz.TagID{e.tagID(t, "Dev"), tag.ID}})
	reader := e.reader(t, "vera", map[string]authz.ActionSet{"Dev": viewOnly, "Sandbox": viewOnly})
	getReviewRunDetail(t, reader, id)

	// A stale permission row must not keep the run readable: drop the foreign key so the row survives the delete.
	var fk string
	if err := e.st.Pool.QueryRow(t.Context(), `select conname from pg_constraint where conrelid = 'permissions'::regclass and contype = 'f'
		and confrelid = 'tags'::regclass`).Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Pool.Exec(t.Context(), `alter table permissions drop constraint `+fk); err != nil {
		t.Fatal(err)
	}
	if err := e.st.DeleteTag(t.Context(), tag.ID); err != nil {
		t.Fatal(err)
	}
	wantNotFound(t, reader, runPath(id))
	if got := listReviewRunsPage(t, reader, ""); len(got.Items) != 0 {
		t.Errorf("reader: list holds %d runs, want 0", len(got.Items))
	}
	if got := getReviewRunDetail(t, e.sam, id); got.Summary != "Findings." {
		t.Errorf("admin: summary %q, want the summary", got.Summary)
	}
}

func TestReviewRun_NoCoverageIsAdminOnly(t *testing.T) {
	e := newInvEnv(t)
	id := e.seedRun(t, reviewSeed{state: "skipped", summary: ""})
	reader := e.reader(t, "vera", map[string]authz.ActionSet{"Dev": viewLogs, "Prod": viewLogs, "Staging": viewLogs})
	wantNotFound(t, reader, runPath(id))
	if got := listReviewRunsPage(t, reader, ""); len(got.Items) != 0 {
		t.Errorf("reader: list holds %d runs, want 0", len(got.Items))
	}
	getReviewRunDetail(t, e.sam, id)
	if got := listReviewRunsPage(t, e.sam, ""); !slices.Equal(runIDs(got.Items), []int64{id}) {
		t.Errorf("admin: list %v, want [%d]", runIDs(got.Items), id)
	}
}

func TestReviewRun_UnknownStateOrTypeIs404ForEveryone(t *testing.T) {
	e := newInvEnv(t)
	e.dropChecks(t, "agent_review_runs", "state")
	dev := []authz.TagID{e.tagID(t, "Dev")}
	badState := e.seedRun(t, reviewSeed{state: "archived", covered: dev, summary: "Findings."})
	badType := e.seedRun(t, reviewSeed{typ: "custom", covered: dev, summary: "Findings."})
	good := e.seedRun(t, reviewSeed{covered: dev, summary: "Findings."})
	reader := e.reader(t, "vera", map[string]authz.ActionSet{"Dev": viewOnly})
	for name, c := range map[string]*apiClient{"admin": e.sam, "reader": reader} {
		wantNotFound(t, c, runPath(badState))
		wantNotFound(t, c, runPath(badType))
		getReviewRunDetail(t, c, good)
		if got := listReviewRunsPage(t, c, ""); !slices.Equal(runIDs(got.Items), []int64{good}) {
			t.Errorf("%s: list %v, want only [%d]", name, runIDs(got.Items), good)
		}
	}
}

func TestReviewRun_EvidenceIsFilteredPerItem(t *testing.T) {
	e := newInvEnv(t)
	id := e.seedRun(t, reviewSeed{summary: "Findings.", covered: []authz.TagID{e.tagID(t, "Dev")}})
	e.seedRunEvidence(t, id, 1, "view", devSvc, "NOTE-OF-A-DEV-SERVICE", "EXCERPT-OF-A-DEV-SERVICE")
	e.seedRunEvidence(t, id, 2, "view", prodSvc, "NOTE-OF-A-PROD-SERVICE", "EXCERPT-OF-A-PROD-SERVICE")
	reader := e.reader(t, "vera", map[string]authz.ActionSet{"Dev": viewOnly})

	status, body := invBody(t, reader, runPath(id))
	if status != http.StatusOK || strings.Contains(body, "PROD-SERVICE") || !strings.Contains(body, "NOTE-OF-A-DEV-SERVICE") {
		t.Fatalf("reader: status %d body %s, want the Prod item gone and the Dev item kept", status, body)
	}
	if got := getReviewRunDetail(t, reader, id); !slices.Equal(ordinals(got.Evidence), []int{1}) || got.EvidenceHidden != 1 {
		t.Errorf("reader: evidence %v hidden %d, want [1] and 1", ordinals(got.Evidence), got.EvidenceHidden)
	}
	if got := getReviewRunDetail(t, e.sam, id); len(got.Evidence) != 2 || got.EvidenceHidden != 0 {
		t.Errorf("admin: evidence %d hidden %d, want 2 and 0", len(got.Evidence), got.EvidenceHidden)
	}
	// The tag of a service moves; the next request follows it.
	e.grant(t, "vera", "Prod", viewOnly)
	if got := getReviewRunDetail(t, reader, id); len(got.Evidence) != 2 {
		t.Errorf("after getting Prod: evidence %d, want 2", len(got.Evidence))
	}
}

func TestReviewRun_LogsActionIsHiddenEvenFromAdmins(t *testing.T) {
	e := newInvEnv(t)
	var check string
	if err := e.st.Pool.QueryRow(t.Context(), `select conname from pg_constraint
		where conrelid = 'agent_review_evidence'::regclass and contype = 'c' and pg_get_constraintdef(oid) ilike '%action%'`).Scan(&check); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Pool.Exec(t.Context(), `alter table agent_review_evidence drop constraint `+check); err != nil {
		t.Fatal(err)
	}
	id := e.seedRun(t, reviewSeed{summary: "Findings.", covered: []authz.TagID{e.tagID(t, "Dev")}})
	e.seedRunEvidence(t, id, 1, "view", devSvc, "fine", "ok")
	e.seedRunEvidence(t, id, 2, "logs", devSvc, "NOTE-OF-A-LOGS-ITEM", "EXCERPT-OF-A-LOGS-ITEM")
	e.seedRunEvidence(t, id, 3, "operate", devSvc, "NOTE-OF-AN-UNKNOWN-ACTION", "EXCERPT-OF-AN-UNKNOWN-ACTION")
	reader := e.reader(t, "vera", map[string]authz.ActionSet{"Dev": viewLogs})
	for name, c := range map[string]*apiClient{"admin": e.sam, "reader with Logs": reader} {
		status, body := invBody(t, c, runPath(id))
		if status != http.StatusOK || strings.Contains(body, "LOGS-ITEM") || strings.Contains(body, "UNKNOWN-ACTION") {
			t.Fatalf("%s: status %d body %s, want only the view item", name, status, body)
		}
		if got := getReviewRunDetail(t, c, id); !slices.Equal(ordinals(got.Evidence), []int{1}) || got.EvidenceHidden != 2 {
			t.Errorf("%s: evidence %v hidden %d, want [1] and 2", name, ordinals(got.Evidence), got.EvidenceHidden)
		}
	}
}

func TestReviewRun_SummaryOnlyWhenDone(t *testing.T) {
	e := newInvEnv(t)
	dev := []authz.TagID{e.tagID(t, "Dev")}
	for _, state := range []string{"requested", "running", "failed", "skipped"} {
		id := e.seedRun(t, reviewSeed{state: state, summary: "PARTIAL-" + state, covered: dev})
		status, body := invBody(t, e.sam, runPath(id))
		if status != http.StatusOK || strings.Contains(body, "PARTIAL-") {
			t.Errorf("%s: status %d body %s, want no summary", state, status, body)
		}
	}
	id := e.seedRun(t, reviewSeed{summary: "The findings.", covered: dev, replaced: true})
	if got := getReviewRunDetail(t, e.sam, id); got.Summary != "The findings." || !got.SummaryReplaced {
		t.Errorf("done: summary %q replaced %v, want the summary and true", got.Summary, got.SummaryReplaced)
	}
}

func TestReviewRun_SurvivesItsReview(t *testing.T) {
	e := newInvEnv(t)
	var reviewID int64
	if err := e.st.Pool.QueryRow(t.Context(), `insert into agent_reviews (name, type, cadence, at_minute, timezone)
		values ('Weekly cost', 'cost', 'daily', 480, 'UTC') returning id`).Scan(&reviewID); err != nil {
		t.Fatal(err)
	}
	id := e.seedRun(t, reviewSeed{summary: "Findings.", covered: []authz.TagID{e.tagID(t, "Dev")}})
	if _, err := e.st.Pool.Exec(t.Context(), `update agent_review_runs set review_id = $1 where id = $2`, reviewID, id); err != nil {
		t.Fatal(err)
	}
	if got := getReviewRunDetail(t, e.sam, id); got.ReviewID == nil || *got.ReviewID != reviewID {
		t.Fatalf("with its review: review_id %v, want %d", got.ReviewID, reviewID)
	}
	if _, err := e.st.Pool.Exec(t.Context(), `delete from agent_reviews where id = $1`, reviewID); err != nil {
		t.Fatal(err)
	}
	got := getReviewRunDetail(t, e.sam, id)
	if got.ReviewID != nil || got.Name != "Weekly cost" || got.Summary != "Findings." {
		t.Errorf("after the review is deleted: review_id %v name %q summary %q, want null, its name and its summary", got.ReviewID, got.Name, got.Summary)
	}
}

func TestReviewRuns_ListHoldsOnlyReadableRuns(t *testing.T) {
	e := newInvEnv(t)
	dev, prod := e.tagID(t, "Dev"), e.tagID(t, "Prod")
	devRun := e.seedRun(t, reviewSeed{summary: "Dev only.", covered: []authz.TagID{dev}})
	both := e.seedRun(t, reviewSeed{summary: "Both.", covered: []authz.TagID{dev, prod}})
	none := e.seedRun(t, reviewSeed{state: "skipped"})
	reader := e.reader(t, "vera", map[string]authz.ActionSet{"Dev": viewOnly})

	got := listReviewRunsPage(t, reader, "")
	if !slices.Equal(runIDs(got.Items), []int64{devRun}) {
		t.Errorf("reader: list %v, want [%d]", runIDs(got.Items), devRun)
	}
	status, body := invBody(t, reader, "/api/reviews/runs")
	if status != http.StatusOK || strings.Contains(body, "Both.") {
		t.Errorf("reader: status %d body %s, want nothing of the other runs", status, body)
	}
	admin := listReviewRunsPage(t, e.sam, "")
	if !slices.Equal(runIDs(admin.Items), []int64{none, both, devRun}) {
		t.Errorf("admin: list %v, want [%d %d %d]", runIDs(admin.Items), none, both, devRun)
	}
}

func TestReviewRuns_NewestFirstAndPaged(t *testing.T) {
	e := newInvEnv(t)
	dev := []authz.TagID{e.tagID(t, "Dev")}
	base := time.Now().UTC().Truncate(time.Second).Add(-24 * time.Hour)
	want := make([]int64, 0, 55)
	for i := 0; i < 55; i++ {
		// Two runs share one requested_at, so the id breaks the tie.
		at := base.Add(time.Duration(i/2) * time.Minute)
		want = append([]int64{e.seedRun(t, reviewSeed{covered: dev, requested: at})}, want...)
	}
	first := listReviewRunsPage(t, e.sam, "")
	if len(first.Items) != 50 || first.NextCursor == "" || !slices.Equal(runIDs(first.Items), want[:50]) {
		t.Fatalf("first page: %d items, cursor %q, ids %v, want the newest 50 and a cursor", len(first.Items), first.NextCursor, runIDs(first.Items))
	}
	second := listReviewRunsPage(t, e.sam, "?cursor="+first.NextCursor)
	if second.NextCursor != "" || !slices.Equal(runIDs(second.Items), want[50:]) {
		t.Fatalf("second page: ids %v cursor %q, want the last 5 and no cursor", runIDs(second.Items), second.NextCursor)
	}
	small := listReviewRunsPage(t, e.sam, "?limit=3")
	if len(small.Items) != 3 || small.NextCursor == "" || !slices.Equal(runIDs(small.Items), want[:3]) {
		t.Errorf("limit 3: ids %v cursor %q, want the newest 3 and a cursor", runIDs(small.Items), small.NextCursor)
	}
	for _, query := range []string{"?cursor=nonsense", "?limit=0", "?limit=abc"} {
		resp := e.sam.get("/api/reviews/runs" + query)
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("GET %s: status %d, want 422", query, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

func TestMe_ReviewsEnabled(t *testing.T) {
	srv, _, _ := newTestServer(t)
	var off MeDTO
	decode(t, login(t, srv, "sam", testPassword).get("/api/me"), &off)
	if off.ReviewsEnabled {
		t.Error("agent off: reviews_enabled true, want false")
	}
	e := newInvEnv(t)
	e.api.Cfg.AgentReviewsPerDay = 0
	var none MeDTO
	decode(t, e.sam.get("/api/me"), &none)
	if none.ReviewsEnabled || !none.AgentEnabled {
		t.Errorf("HM_AGENT_REVIEWS_PER_DAY=0: reviews_enabled %v agent_enabled %v, want false and true", none.ReviewsEnabled, none.AgentEnabled)
	}
	e.api.Cfg.AgentReviewsPerDay = 24
	var on MeDTO
	decode(t, e.sam.get("/api/me"), &on)
	if !on.ReviewsEnabled || !on.AgentEnabled {
		t.Errorf("agent on: reviews_enabled %v agent_enabled %v, want both true", on.ReviewsEnabled, on.AgentEnabled)
	}
	reader := e.reader(t, "vera", nil)
	var plain MeDTO
	decode(t, reader.get("/api/me"), &plain)
	if !plain.ReviewsEnabled {
		t.Error("plain reader: reviews_enabled false, want true")
	}
}

// seedRunsBulk writes n done runs with the given covered tags, requested one second apart from first, in one statement.
func (e *invEnv) seedRunsBulk(t *testing.T, n int, first time.Time, covered authz.TagID) {
	t.Helper()
	if _, err := e.st.Pool.Exec(t.Context(), `insert into agent_review_runs
		(review_name, review_type, period_key, period_label, scheduled_for, state, summary, covered_tags, requested_at, finished_at)
		select 'Weekly cost', 'cost', 'x:' || gen_random_uuid()::text, 'Week 47', ts, 'done', 'Findings.', array[$3::bigint], ts, ts
		from (select $2::timestamptz + (i || ' seconds')::interval as ts from generate_series(0, $1::int - 1) i) s`,
		n, first, int64(covered)); err != nil {
		t.Fatal(err)
	}
}

// readableRunIDs lists, newest first, the ids of the runs covering only the tag.
func (e *invEnv) runIDsCovering(t *testing.T, tag authz.TagID) []int64 {
	t.Helper()
	rows, err := e.st.Pool.Query(t.Context(), `select id from agent_review_runs where covered_tags = array[$1::bigint] order by requested_at desc, id desc`, int64(tag))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out = append(out, id)
	}
	return out
}

// walkReviewRuns follows next_cursor to the end and returns every id in order and the number of pages.
func walkReviewRuns(t *testing.T, c *apiClient, query string, start ...string) ([]int64, int) {
	t.Helper()
	var ids []int64
	cursor, pages := strings.Join(start, ""), 0
	for {
		path := query
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		page := listReviewRunsPage(t, c, path)
		pages++
		ids = append(ids, runIDs(page.Items)...)
		if page.NextCursor == "" {
			return ids, pages
		}
		cursor = page.NextCursor
		if pages > 200 {
			t.Fatal("paging does not end")
		}
	}
}

func TestReviewRuns_WalkFindsEveryReadableRunAmongHiddenOnes(t *testing.T) {
	e := newInvEnv(t)
	dev, prod := e.tagID(t, "Dev"), e.tagID(t, "Prod")
	reader := e.reader(t, "vera", map[string]authz.ActionSet{"Dev": viewOnly})
	base := time.Now().UTC().Truncate(time.Second).Add(-48 * time.Hour)
	e.seedRunsBulk(t, 60, base, dev)
	e.seedRunsBulk(t, 500, base.Add(time.Hour), prod)
	e.seedRunsBulk(t, 1440, base.Add(2*time.Hour), dev)
	want := e.runIDsCovering(t, dev)
	got, pages := walkReviewRuns(t, reader, "?limit=50")
	if len(want) != 1500 || !slices.Equal(got, want) || pages != 30 {
		t.Fatalf("walk: %d runs in %d pages, want all 1500 readable runs in order in 30 pages (in order: %v)", len(got), pages, slices.Equal(got, want))
	}
}

func TestReviewRuns_HiddenNewestRunsDoNotHideTheFirstPage(t *testing.T) {
	e := newInvEnv(t)
	dev, prod := e.tagID(t, "Dev"), e.tagID(t, "Prod")
	reader := e.reader(t, "vera", map[string]authz.ActionSet{"Dev": viewOnly})
	base := time.Now().UTC().Truncate(time.Second).Add(-48 * time.Hour)
	e.seedRunsBulk(t, 60, base, dev)
	e.seedRunsBulk(t, 500, base.Add(time.Hour), prod)
	want := e.runIDsCovering(t, dev)
	first := listReviewRunsPage(t, reader, "")
	if !slices.Equal(runIDs(first.Items), want[:50]) || first.NextCursor == "" {
		t.Fatalf("first page: %d items, cursor %q, want the 50 newest readable runs and a cursor", len(first.Items), first.NextCursor)
	}
}

func TestReviewRuns_CraftedCursorOnlyMovesTheStartingPoint(t *testing.T) {
	e := newInvEnv(t)
	dev, prod := e.tagID(t, "Dev"), e.tagID(t, "Prod")
	reader := e.reader(t, "vera", map[string]authz.ActionSet{"Dev": viewOnly})
	base := time.Now().UTC().Truncate(time.Second).Add(-48 * time.Hour)
	e.seedRunsBulk(t, 20, base, dev)
	e.seedRunsBulk(t, 20, base.Add(time.Hour), prod)
	e.seedRunsBulk(t, 20, base.Add(2*time.Hour), dev)
	readable := map[int64]bool{}
	for _, id := range e.runIDsCovering(t, dev) {
		readable[id] = true
	}
	hidden := e.runIDsCovering(t, prod)
	in, err := e.st.GetReviewRun(t.Context(), hidden[5])
	if err != nil {
		t.Fatal(err)
	}
	cursors := []string{fmt.Sprintf("%d.%d", in.RequestedAt.UnixMicro(), in.ID), fmt.Sprintf("%d.%d", time.Now().Add(time.Hour).UnixMicro(), int64(1)<<40), "1.1"}
	for _, cursor := range cursors {
		ids, _ := walkReviewRuns(t, reader, "?limit=7", cursor)
		for _, id := range ids {
			if !readable[id] {
				t.Errorf("cursor %s: the list holds run %d, which the reader may not read", cursor, id)
			}
		}
	}
	ids, _ := walkReviewRuns(t, reader, "?limit=7", cursors[0])
	if len(ids) != 20 {
		t.Errorf("cursor inside the hidden block: %d runs, want the 20 older readable ones", len(ids))
	}
}

func TestReviewRuns_CursorIsTheLastReadableRunOfThePage(t *testing.T) {
	e := newInvEnv(t)
	dev, prod := e.tagID(t, "Dev"), e.tagID(t, "Prod")
	reader := e.reader(t, "vera", map[string]authz.ActionSet{"Dev": viewOnly})
	base := time.Now().UTC().Truncate(time.Second).Add(-48 * time.Hour)
	// Oldest first: H4 H3 R6 H2 R5 R4 R3 H1 R2 R1, so the newest-first order is R1 R2 H1 R3 R4 R5 H2 R6 H3 H4.
	var order []int64
	for i, tag := range []authz.TagID{prod, prod, dev, prod, dev, dev, dev, prod, dev, dev} {
		order = append([]int64{e.seedRun(t, reviewSeed{covered: []authz.TagID{tag}, requested: base.Add(time.Duration(i) * time.Minute)})}, order...)
	}
	r1, r2, r3, r4, r5, r6 := order[0], order[1], order[3], order[4], order[5], order[7]
	first := listReviewRunsPage(t, reader, "?limit=3")
	if !slices.Equal(runIDs(first.Items), []int64{r1, r2, r3}) || first.NextCursor != fmt.Sprintf("%d.%d", first.Items[2].RequestedAt.UnixMicro(), r3) {
		t.Fatalf("first page: ids %v cursor %q, want [R1 R2 R3] and the cursor of R3", runIDs(first.Items), first.NextCursor)
	}
	second := listReviewRunsPage(t, reader, "?limit=3&cursor="+first.NextCursor)
	if !slices.Equal(runIDs(second.Items), []int64{r4, r5, r6}) || second.NextCursor != "" {
		t.Fatalf("second page: ids %v cursor %q, want [R4 R5 R6] and no cursor, only hidden runs follow", runIDs(second.Items), second.NextCursor)
	}
	if ids, _ := walkReviewRuns(t, reader, "?limit=2"); !slices.Equal(ids, []int64{r1, r2, r3, r4, r5, r6}) {
		t.Errorf("walk with limit 2: %v, want every readable run and nothing else", ids)
	}
}
