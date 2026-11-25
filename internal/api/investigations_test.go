package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

func invKey(cluster, service string) model.ServiceKey {
	return model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: sandboxAccountID, Region: "us-east-1", Cluster: cluster}, Service: service}
}

// The mockup estate: background-jobs is tagged Dev, core-services is tagged Prod.
var (
	devSvc   = invKey("background-jobs", "image-resizer")
	devSvc2  = invKey("background-jobs", "email-sender")
	prodSvc  = invKey("core-services", "notifications")
	prodSvc2 = invKey("core-services", "payments-api")
	goneSvc  = invKey("background-jobs", "retired-service")
)

type invEnv struct {
	srv *httptest.Server
	api *Server
	st  *store.Store
	fk  *fake.Fake
	sam *apiClient
}

// newInvEnv builds the API with the agent on, the mockup estate polled and tagged, and admin sam signed in.
func newInvEnv(t *testing.T) *invEnv {
	t.Helper()
	srv, api, fk, st, _ := newServerCustomAPI(t, true, withAgent(nil))
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	return &invEnv{srv: srv, api: api, st: st, fk: fk, sam: login(t, srv, "sam", testPassword)}
}

func (e *invEnv) tagID(t *testing.T, name string) authz.TagID {
	t.Helper()
	tags, err := e.st.ListTags(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, tag := range tags {
		if tag.Name == name {
			return tag.ID
		}
	}
	t.Fatalf("no tag %q", name)
	return 0
}

// grant sets the actions a user holds on a tag.
func (e *invEnv) grant(t *testing.T, username, tag string, actions authz.ActionSet) {
	t.Helper()
	user, err := e.st.GetUserByUsername(t.Context(), username)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetPermission(t.Context(), user.ID, e.tagID(t, tag), actions); err != nil {
		t.Fatal(err)
	}
}

// reader creates a non-admin user with the given grants (tag name to actions) and signs them in.
func (e *invEnv) reader(t *testing.T, username string, grants map[string]authz.ActionSet) *apiClient {
	t.Helper()
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.CreateUser(t.Context(), username, hash, false, false); err != nil {
		t.Fatal(err)
	}
	for tag, actions := range grants {
		e.grant(t, username, tag, actions)
	}
	return login(t, e.srv, username, testPassword)
}

const (
	viewOnly = authz.ActionSet(authz.View)
	viewLogs = authz.ActionSet(authz.View | authz.Logs)
)

type invSeed struct {
	kind, state, reason, summary string
	key                          model.ServiceKey // service subject; for a group, the account, region and cluster
	members                      []store.InvestigationMember
	replaced                     bool
	requested                    time.Time
}

// seedInv writes an investigation row directly, so each test states exactly the row it needs.
func (e *invEnv) seedInv(t *testing.T, s invSeed) int64 {
	t.Helper()
	if s.kind == "" {
		s.kind = "service"
	}
	if s.state == "" {
		s.state = "done"
	}
	if s.requested.IsZero() {
		s.requested = time.Now().UTC()
	}
	members := "["
	for i, m := range s.members {
		if i > 0 {
			members += ","
		}
		members += fmt.Sprintf(`{"cluster":%q,"service":%q}`, m.Cluster, m.Service)
	}
	members += "]"
	service := s.key.Service
	if s.kind != "service" {
		service = ""
	}
	var id int64
	err := e.st.Pool.QueryRow(t.Context(), `insert into agent_investigations
		(subject_kind, subject_key, account_id, region, cluster_name, service_name, members, alert_kind, alert_title, alert_text,
		 alert_opened_at, state, reason, summary, summary_replaced, requested_at, started_at, finished_at)
		values ($1, $2, $3, $4, $5, $6, $7::jsonb, 'service_failing', 'The service is failing', 'detail',
		 $8::timestamptz, $9::text, $10, $11, $12, $8::timestamptz, case when $9::text = 'requested' then null else $8::timestamptz end,
		 case when $9::text in ('done', 'failed', 'skipped') then $8::timestamptz else null end)
		returning id`,
		s.kind, fmt.Sprintf("%s/%s/%d", s.kind, s.key.ID(), s.requested.UnixNano()), s.key.AccountID, s.key.Region, s.key.Cluster, service,
		members, s.requested, s.state, s.reason, s.summary, s.replaced).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (e *invEnv) seedEvidence(t *testing.T, id int64, ordinal int, tool, action string, key model.ServiceKey, note, excerpt string) {
	t.Helper()
	if _, err := e.st.Pool.Exec(t.Context(), `insert into agent_investigation_evidence
		(investigation_id, ordinal, tool, action, account_id, region, cluster_name, service_name, note, excerpt)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		id, ordinal, tool, action, key.AccountID, key.Region, key.Cluster, key.Service, note, excerpt); err != nil {
		t.Fatal(err)
	}
}

// seedIncident opens an alert incident for the service and links it to the investigation.
func (e *invEnv) seedIncident(t *testing.T, id int64, key model.ServiceKey, kind string) int64 {
	t.Helper()
	var incident int64
	if err := e.st.Pool.QueryRow(t.Context(), `insert into notification_incidents
		(account_id, region, cluster_name, service_name, kind, severity, title, text, opened_at)
		values ($1, $2, $3, $4, $5, 'danger', 'The service is failing', 'detail', now()) returning id`,
		key.AccountID, key.Region, key.Cluster, key.Service, kind).Scan(&incident); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Pool.Exec(t.Context(), `insert into agent_investigation_incidents (investigation_id, incident_id) values ($1, $2)`, id, incident); err != nil {
		t.Fatal(err)
	}
	return incident
}

// dropChecks removes the check constraints of a table's column, so a test can write a row the schema refuses. The
// test schema is private to the test.
func (e *invEnv) dropChecks(t *testing.T, table, column string) {
	t.Helper()
	rows, err := e.st.Pool.Query(t.Context(), `select conname from pg_constraint
		where conrelid = $1::regclass and contype = 'c' and pg_get_constraintdef(oid) ilike '%' || $2 || ' = any%'`, table, column)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	rows.Close()
	if len(names) == 0 {
		t.Fatalf("no check on %s.%s", table, column)
	}
	for _, name := range names {
		if _, err := e.st.Pool.Exec(t.Context(), fmt.Sprintf(`alter table %s drop constraint %s`, table, name)); err != nil {
			t.Fatal(err)
		}
	}
}

func getInvestigation(t *testing.T, c *apiClient, id int64) InvestigationDetailDTO {
	t.Helper()
	resp := c.get(fmt.Sprintf("/api/investigations/%d", id))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET investigation %d: status %d, want 200", id, resp.StatusCode)
	}
	var out InvestigationDetailDTO
	decode(t, resp, &out)
	return out
}

func invBody(t *testing.T, c *apiClient, path string) (int, string) {
	t.Helper()
	resp := c.get(path)
	defer resp.Body.Close()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return resp.StatusCode, sb.String()
}

func wantNotFound(t *testing.T, c *apiClient, path string) {
	t.Helper()
	resp := c.get(path)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET %s: status %d, want 404", path, resp.StatusCode)
		resp.Body.Close()
		return
	}
	if code, _ := errorCode(t, resp); code != "not_found" {
		t.Errorf("GET %s: code %q, want not_found", path, code)
	}
}

func ordinals(items []InvestigationEvidenceDTO) []int {
	out := make([]int, 0, len(items))
	for _, item := range items {
		out = append(out, item.Ordinal)
	}
	return out
}

func investigationPaths(id, incident int64) []string {
	return []string{
		"/api/services/" + devSvc.ID() + "/investigations",
		fmt.Sprintf("/api/investigations/%d", id),
		fmt.Sprintf("/api/investigations/by-incident/%d", incident),
	}
}

func TestInvestigationRoutes_AbsentWhenDisabled(t *testing.T) {
	srv, _, _ := newTestServer(t)
	c := login(t, srv, "sam", testPassword)
	for _, path := range investigationPaths(1, 1) {
		resp := c.get(path)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s with the agent off: status %d, want 404", path, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

func TestInvestigationRoutes_RefuseAccessTokens(t *testing.T) {
	srv, api, _, st, _ := newServerCustomAPI(t, true, withTokens(func(c *config.Config) {
		c.AgentURL, c.AgentRunTokenTTL = testAgentURL, 15*time.Minute
	}, nil))
	clients := map[string]*apiClient{
		"personal token": tokenClient(t, srv, st, "sam"),
		"run token":      runTokenClient(t, srv, api, st, "sam"),
	}
	oauth := newOAuthAPIEnvWith(t, true, withAgent(nil))
	granted := oauth.connect(t, oauth.sam)
	paths := investigationPaths(1, 1)
	for name, c := range clients {
		for _, path := range paths {
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
	for _, path := range paths {
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

func TestInvestigation_NeedsViewOnTheService(t *testing.T) {
	e := newInvEnv(t)
	id := e.seedInv(t, invSeed{key: devSvc, summary: "The image resizer ran out of memory."})
	incident := e.seedIncident(t, id, devSvc, "service_failing")
	viewer := e.reader(t, "vera", map[string]authz.ActionSet{"Dev": viewOnly})
	blind := e.reader(t, "bram", map[string]authz.ActionSet{"Prod": viewOnly})

	got := getInvestigation(t, viewer, id)
	if got.Summary != "The image resizer ran out of memory." || got.Subject.ServiceID != devSvc.ID() {
		t.Fatalf("viewer got %+v", got.InvestigationDTO)
	}
	for _, path := range investigationPaths(id, incident) {
		wantNotFound(t, blind, path)
	}
}

func TestInvestigation_OtherServiceIs404NotForbidden(t *testing.T) {
	e := newInvEnv(t)
	id := e.seedInv(t, invSeed{key: prodSvc, summary: "Secret findings about the Prod service."})
	incident := e.seedIncident(t, id, prodSvc, "service_failing")
	viewer := e.reader(t, "vera", map[string]authz.ActionSet{"Dev": viewLogs})

	status, body := invBody(t, viewer, fmt.Sprintf("/api/investigations/%d", id))
	if status != http.StatusNotFound || strings.Contains(body, "Secret") {
		t.Fatalf("status %d body %s, want 404 with nothing revealed", status, body)
	}
	wantNotFound(t, viewer, fmt.Sprintf("/api/investigations/by-incident/%d", incident))
	wantNotFound(t, viewer, "/api/services/"+prodSvc.ID()+"/investigations")
	wantNotFound(t, viewer, "/api/investigations/999999")
	wantNotFound(t, viewer, "/api/investigations/not-a-number")
}

func TestInvestigation_EvidenceHiddenWithoutLogs(t *testing.T) {
	e := newInvEnv(t)
	id := e.seedInv(t, invSeed{key: devSvc, summary: "Findings."})
	e.seedEvidence(t, id, 1, "describe_service", "view", devSvc, "the service state", "desired 2, running 0")
	e.seedEvidence(t, id, 2, "search_logs", "logs", devSvc, "the error lines", "java.lang.OutOfMemoryError")
	e.seedEvidence(t, id, 3, "tail_logs", "logs", devSvc2, "more lines", "killed by the kernel")
	viewer := e.reader(t, "vera", map[string]authz.ActionSet{"Dev": viewOnly})
	logger := e.reader(t, "lena", map[string]authz.ActionSet{"Dev": viewLogs})

	got := getInvestigation(t, viewer, id)
	if !slices.Equal(ordinals(got.Evidence), []int{1}) || got.EvidenceHidden != 2 {
		t.Fatalf("View-only reader: evidence %v hidden %d, want [1] and 2", ordinals(got.Evidence), got.EvidenceHidden)
	}
	for _, reader := range []*apiClient{logger, e.sam} {
		got = getInvestigation(t, reader, id)
		if !slices.Equal(ordinals(got.Evidence), []int{1, 2, 3}) || got.EvidenceHidden != 0 {
			t.Fatalf("evidence %v hidden %d, want all three and 0", ordinals(got.Evidence), got.EvidenceHidden)
		}
	}
}

func TestInvestigation_NoteIsHiddenWithItsExcerpt(t *testing.T) {
	e := newInvEnv(t)
	id := e.seedInv(t, invSeed{key: devSvc, summary: "Findings."})
	e.seedEvidence(t, id, 1, "describe_service", "view", devSvc, "plain state", "desired 2")
	e.seedEvidence(t, id, 2, "tail_logs_unique_tool", "logs", devSvc2, "NOTE-ABOUT-THE-LOG-LINES", "EXCERPT-OF-THE-LOG-LINES")
	viewer := e.reader(t, "vera", map[string]authz.ActionSet{"Dev": viewOnly})

	status, body := invBody(t, viewer, fmt.Sprintf("/api/investigations/%d", id))
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	for _, leak := range []string{"NOTE-ABOUT-THE-LOG-LINES", "EXCERPT-OF-THE-LOG-LINES", "tail_logs_unique_tool", "email-sender", devSvc2.ID()} {
		if strings.Contains(body, leak) {
			t.Errorf("the body of a View-only reader contains %q: %s", leak, body)
		}
	}
	if !strings.Contains(body, "plain state") {
		t.Errorf("the visible note is missing: %s", body)
	}
}

func TestInvestigation_EvidenceOfAnotherServiceNeedsThatService(t *testing.T) {
	e := newInvEnv(t)
	id := e.seedInv(t, invSeed{key: devSvc, summary: "Findings."})
	e.seedEvidence(t, id, 1, "describe_service", "view", devSvc, "own state", "desired 2")
	e.seedEvidence(t, id, 2, "describe_service", "view", prodSvc, "prod state", "desired 4")
	e.seedEvidence(t, id, 3, "search_logs", "logs", prodSvc2, "prod lines", "boom")
	devOnly := e.reader(t, "vera", map[string]authz.ActionSet{"Dev": viewLogs})
	prodView := e.reader(t, "pia", map[string]authz.ActionSet{"Dev": viewLogs, "Prod": viewOnly})
	prodLogs := e.reader(t, "pol", map[string]authz.ActionSet{"Dev": viewOnly, "Prod": viewLogs})

	for name, tc := range map[string]struct {
		client *apiClient
		want   []int
	}{
		"Dev only":              {devOnly, []int{1}},
		"Prod View, Dev Logs":   {prodView, []int{1, 2}},
		"Prod Logs, Dev View":   {prodLogs, []int{1, 2, 3}},
		"admin sees everything": {e.sam, []int{1, 2, 3}},
	} {
		got := getInvestigation(t, tc.client, id)
		if !slices.Equal(ordinals(got.Evidence), tc.want) || got.EvidenceHidden != 3-len(tc.want) {
			t.Errorf("%s: evidence %v hidden %d, want %v and %d", name, ordinals(got.Evidence), got.EvidenceHidden, tc.want, 3-len(tc.want))
		}
	}
}

func TestInvestigation_LosingLogsHidesEvidenceOnTheNextRequest(t *testing.T) {
	e := newInvEnv(t)
	id := e.seedInv(t, invSeed{key: devSvc, summary: "Findings."})
	e.seedEvidence(t, id, 1, "search_logs", "logs", devSvc, "NOTE-FROM-THE-LOGS", "EXCERPT-FROM-THE-LOGS")
	reader := e.reader(t, "vera", map[string]authz.ActionSet{"Dev": viewLogs})

	if got := getInvestigation(t, reader, id); len(got.Evidence) != 1 || got.EvidenceHidden != 0 {
		t.Fatalf("before: evidence %d hidden %d, want 1 and 0", len(got.Evidence), got.EvidenceHidden)
	}
	e.grant(t, "vera", "Dev", viewOnly)
	status, body := invBody(t, reader, fmt.Sprintf("/api/investigations/%d", id))
	if status != http.StatusOK || strings.Contains(body, "FROM-THE-LOGS") {
		t.Fatalf("after losing Logs: status %d body %s, want the evidence gone", status, body)
	}
	e.grant(t, "vera", "Dev", viewLogs)
	if got := getInvestigation(t, reader, id); len(got.Evidence) != 1 {
		t.Fatalf("after getting Logs back: evidence %d, want 1", len(got.Evidence))
	}
}

func TestInvestigation_TagChangeIsFollowed(t *testing.T) {
	e := newInvEnv(t)
	id := e.seedInv(t, invSeed{key: devSvc, summary: "Findings."})
	e.seedEvidence(t, id, 1, "describe_service", "view", devSvc2, "sender state", "desired 1")
	reader := e.reader(t, "vera", map[string]authz.ActionSet{"Dev": viewLogs})

	if got := getInvestigation(t, reader, id); len(got.Evidence) != 1 {
		t.Fatalf("before the move: evidence %d, want 1", len(got.Evidence))
	}
	prod := e.tagID(t, "Prod")
	if err := e.st.SetServiceTag(t.Context(), devSvc2, &prod); err != nil {
		t.Fatal(err)
	}
	if got := getInvestigation(t, reader, id); len(got.Evidence) != 0 || got.EvidenceHidden != 1 {
		t.Fatalf("after the evidence service moved to Prod: evidence %d hidden %d, want 0 and 1", len(got.Evidence), got.EvidenceHidden)
	}
	if err := e.st.SetServiceTag(t.Context(), devSvc, &prod); err != nil {
		t.Fatal(err)
	}
	wantNotFound(t, reader, fmt.Sprintf("/api/investigations/%d", id))
}

func TestInvestigation_UnknownActionIsHiddenEvenFromAdmins(t *testing.T) {
	e := newInvEnv(t)
	e.dropChecks(t, "agent_investigation_evidence", "action")
	id := e.seedInv(t, invSeed{key: devSvc, summary: "Findings."})
	e.seedEvidence(t, id, 1, "describe_service", "view", devSvc, "fine", "ok")
	e.seedEvidence(t, id, 2, "restart_service", "operate", devSvc, "NOTE-OF-AN-UNKNOWN-ACTION", "EXCERPT-OF-AN-UNKNOWN-ACTION")

	status, body := invBody(t, e.sam, fmt.Sprintf("/api/investigations/%d", id))
	if status != http.StatusOK || strings.Contains(body, "UNKNOWN-ACTION") || strings.Contains(body, "restart_service") {
		t.Fatalf("admin: status %d body %s, want the unknown action hidden", status, body)
	}
	if got := getInvestigation(t, e.sam, id); !slices.Equal(ordinals(got.Evidence), []int{1}) || got.EvidenceHidden != 1 {
		t.Fatalf("admin: evidence %v hidden %d, want [1] and 1", ordinals(got.Evidence), got.EvidenceHidden)
	}
}

func TestInvestigation_DeletedServiceIsAdminOnly(t *testing.T) {
	e := newInvEnv(t)
	reader := e.reader(t, "vera", map[string]authz.ActionSet{"Dev": viewLogs, "Prod": viewLogs, "Staging": viewLogs})

	gone := e.seedInv(t, invSeed{key: goneSvc, summary: "About a service that is gone."})
	if got := getInvestigation(t, e.sam, gone); got.Summary == "" {
		t.Error("admin: the investigation of a deleted service is empty")
	}
	wantNotFound(t, reader, fmt.Sprintf("/api/investigations/%d", gone))

	live := e.seedInv(t, invSeed{key: devSvc, summary: "Findings."})
	e.seedEvidence(t, live, 1, "describe_service", "view", goneSvc, "NOTE-OF-A-GONE-SERVICE", "EXCERPT-OF-A-GONE-SERVICE")
	if got := getInvestigation(t, e.sam, live); len(got.Evidence) != 1 {
		t.Errorf("admin: evidence %d, want the item of the deleted service", len(got.Evidence))
	}
	status, body := invBody(t, reader, fmt.Sprintf("/api/investigations/%d", live))
	if status != http.StatusOK || strings.Contains(body, "GONE-SERVICE") {
		t.Errorf("reader: status %d body %s, want the item of a deleted service hidden", status, body)
	}
}

func TestInvestigation_GroupNeedsEveryMember(t *testing.T) {
	e := newInvEnv(t)
	devMember := store.InvestigationMember{Cluster: devSvc.Cluster, Service: devSvc.Service}
	prodMember := store.InvestigationMember{Cluster: prodSvc.Cluster, Service: prodSvc.Service}
	account := invKey("", "")
	both := e.seedInv(t, invSeed{kind: "account", key: account, members: []store.InvestigationMember{devMember, prodMember}, summary: "Group findings."})
	none := e.seedInv(t, invSeed{kind: "account", key: account, summary: "No members."})
	missing := e.seedInv(t, invSeed{kind: "account", key: account, summary: "A member is gone.",
		members: []store.InvestigationMember{devMember, {Cluster: goneSvc.Cluster, Service: goneSvc.Service}}})
	cluster := e.seedInv(t, invSeed{kind: "cluster", key: invKey("core-services", ""), summary: "Cluster findings.",
		members: []store.InvestigationMember{prodMember, {Cluster: prodSvc2.Cluster, Service: prodSvc2.Service}}})
	devOnly := e.reader(t, "vera", map[string]authz.ActionSet{"Dev": viewOnly})
	devAndProd := e.reader(t, "pia", map[string]authz.ActionSet{"Dev": viewOnly, "Prod": viewOnly})

	wantNotFound(t, devOnly, fmt.Sprintf("/api/investigations/%d", both))
	if got := getInvestigation(t, devAndProd, both); got.Summary != "Group findings." || len(got.Members) != 2 || got.Subject.Kind != "account" {
		t.Errorf("reader of every member: %+v members %d", got.InvestigationDTO, len(got.Members))
	}
	wantNotFound(t, devOnly, fmt.Sprintf("/api/investigations/%d", cluster))
	if got := getInvestigation(t, devAndProd, cluster); got.Subject.ClusterID != invKey("core-services", "").ClusterKey.ID() {
		t.Errorf("cluster subject = %+v", got.Subject)
	}
	for _, id := range []int64{none, missing} {
		wantNotFound(t, devAndProd, fmt.Sprintf("/api/investigations/%d", id))
		if got := getInvestigation(t, e.sam, id); got.Members == nil {
			t.Errorf("admin: members of %d is null, want an array", id)
		}
	}
}

func TestInvestigation_SummaryOnlyWhenDone(t *testing.T) {
	e := newInvEnv(t)
	for _, state := range []string{"requested", "running", "failed", "skipped"} {
		id := e.seedInv(t, invSeed{key: devSvc, state: state, summary: "LEFTOVER-SUMMARY", replaced: true})
		got := getInvestigation(t, e.sam, id)
		if got.Summary != "" || got.SummaryReplaced || got.State != state {
			t.Errorf("%s: summary %q replaced %v state %q, want an empty summary", state, got.Summary, got.SummaryReplaced, got.State)
		}
	}
	done := e.seedInv(t, invSeed{key: devSvc, state: "done", summary: "The finished summary.", replaced: true})
	if got := getInvestigation(t, e.sam, done); got.Summary != "The finished summary." || !got.SummaryReplaced || got.FinishedAt == nil {
		t.Errorf("done: %+v", got.InvestigationDTO)
	}
	requested := getInvestigation(t, e.sam, e.seedInv(t, invSeed{key: devSvc, state: "requested"}))
	if requested.StartedAt != nil || requested.FinishedAt != nil {
		t.Errorf("requested: started %v finished %v, want null", requested.StartedAt, requested.FinishedAt)
	}
}

func TestInvestigation_UnknownKindOrStateIs404ForEveryone(t *testing.T) {
	e := newInvEnv(t)
	e.dropChecks(t, "agent_investigations", "state")
	e.dropChecks(t, "agent_investigations", "subject_kind")
	badState := e.seedInv(t, invSeed{key: devSvc, state: "paused", summary: "Findings."})
	badKind := e.seedInv(t, invSeed{key: devSvc, kind: "region", summary: "Findings."})
	for _, id := range []int64{badState, badKind} {
		wantNotFound(t, e.sam, fmt.Sprintf("/api/investigations/%d", id))
	}
	status, body := invBody(t, e.sam, "/api/services/"+devSvc.ID()+"/investigations")
	if status != http.StatusOK || strings.Contains(body, "paused") {
		t.Errorf("list: status %d body %s, want the unknown state left out", status, body)
	}
}

func TestInvestigation_SurvivesItsIncident(t *testing.T) {
	e := newInvEnv(t)
	id := e.seedInv(t, invSeed{key: devSvc, summary: "Findings."})
	incident := e.seedIncident(t, id, devSvc, "service_failing")

	byIncident := fmt.Sprintf("/api/investigations/by-incident/%d", incident)
	resp := e.sam.get(byIncident)
	var linked InvestigationDetailDTO
	decode(t, resp, &linked)
	if linked.ID != id {
		t.Fatalf("by-incident id = %d, want %d", linked.ID, id)
	}
	if _, err := e.st.Pool.Exec(t.Context(), `delete from notification_incidents where id = $1`, incident); err != nil {
		t.Fatal(err)
	}
	if got := getInvestigation(t, e.sam, id); got.Summary != "Findings." {
		t.Errorf("after the incident is deleted the investigation reads %+v", got.InvestigationDTO)
	}
	wantNotFound(t, e.sam, byIncident)
}

func TestInvestigationList_NewestFirstAndCapped(t *testing.T) {
	e := newInvEnv(t)
	base := time.Now().UTC().Add(-2 * time.Hour)
	var ids []int64
	for i := 0; i < 55; i++ {
		ids = append(ids, e.seedInv(t, invSeed{key: devSvc, summary: "s", requested: base.Add(time.Duration(i) * time.Minute)}))
	}
	e.seedInv(t, invSeed{key: devSvc2, summary: "another service", requested: base.Add(time.Hour)})
	viewer := e.reader(t, "vera", map[string]authz.ActionSet{"Dev": viewOnly})

	resp := viewer.get("/api/services/" + devSvc.ID() + "/investigations")
	var list ItemsResponse[InvestigationDTO]
	decode(t, resp, &list)
	if len(list.Items) != 50 {
		t.Fatalf("items = %d, want 50", len(list.Items))
	}
	for i, item := range list.Items {
		if want := ids[len(ids)-1-i]; item.ID != want {
			t.Fatalf("item %d id = %d, want %d (newest first)", i, item.ID, want)
		}
		if item.Subject.ServiceID != devSvc.ID() {
			t.Fatalf("item %d is of service %q", i, item.Subject.ServiceID)
		}
	}
	empty := e.reader(t, "emil", map[string]authz.ActionSet{"Dev": viewOnly})
	resp = empty.get("/api/services/" + devSvc2.ID() + "/investigations")
	decode(t, resp, &list)
	if list.Items == nil || len(list.Items) != 1 {
		t.Errorf("other service: items %v", list.Items)
	}
	e.st.Pool.Exec(t.Context(), `delete from agent_investigations`) //nolint:errcheck // test cleanup
	resp = viewer.get("/api/services/" + devSvc.ID() + "/investigations")
	decode(t, resp, &list)
	if list.Items == nil || len(list.Items) != 0 {
		t.Errorf("no investigations: items = %#v, want an empty array", list.Items)
	}
}

func rawAlerts(t *testing.T, c *apiClient) map[string]map[string]any {
	t.Helper()
	resp := c.get("/api/alerts")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/alerts: status %d", resp.StatusCode)
	}
	var body struct {
		Items []map[string]any `json:"items"`
	}
	decode(t, resp, &body)
	out := map[string]map[string]any{}
	for _, item := range body.Items {
		out[item["service"].(string)] = item
	}
	return out
}

func (e *invEnv) twoAlerts(t *testing.T) {
	t.Helper()
	replaceAlerts(t, e.st, e.fk,
		serviceAlert(sandboxAccountID, "us-east-1", devSvc.Cluster, devSvc.Service, model.AlertDanger, "image-resizer danger"),
		serviceAlert(sandboxAccountID, "us-east-1", devSvc2.Cluster, devSvc2.Service, model.AlertWarning, "email-sender warning"),
	)
}

func TestAlerts_LinkTheOpenInvestigation(t *testing.T) {
	e := newInvEnv(t)
	e.twoAlerts(t)
	id := e.seedInv(t, invSeed{key: devSvc, state: "failed", reason: "budget"})
	e.seedIncident(t, id, devSvc, "service_failing")

	items := rawAlerts(t, e.sam)
	link, ok := items["image-resizer"]["investigation"].(map[string]any)
	if !ok || int64(link["id"].(float64)) != id || link["state"] != "failed" || link["reason"] != "budget" {
		t.Fatalf("image-resizer investigation = %#v, want id %d, failed, budget", items["image-resizer"]["investigation"], id)
	}
	if value, present := items["email-sender"]["investigation"]; !present || value != nil {
		t.Errorf("email-sender investigation = %#v (present %v), want an explicit null", value, present)
	}
}

func TestAlerts_NoInvestigationMemberForTokens(t *testing.T) {
	srv, fk, st, _ := newServerCustom(t, true, withTokens(func(c *config.Config) {
		c.AgentURL, c.AgentRunTokenTTL = testAgentURL, 15*time.Minute
	}, nil))
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	e := &invEnv{srv: srv, st: st, fk: fk, sam: login(t, srv, "sam", testPassword)}
	e.twoAlerts(t)
	e.seedIncident(t, e.seedInv(t, invSeed{key: devSvc, state: "running"}), devSvc, "service_failing")

	if _, present := rawAlerts(t, e.sam)["image-resizer"]["investigation"]; !present {
		t.Fatal("a session caller has no investigation member")
	}
	for _, item := range rawAlerts(t, tokenClient(t, srv, st, "sam")) {
		if _, present := item["investigation"]; present {
			t.Errorf("a token caller got the investigation member: %#v", item)
		}
	}
}

func TestAlerts_NoInvestigationMemberWhenTheAgentIsOff(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	e := &invEnv{srv: srv, st: st, fk: fk}
	e.twoAlerts(t)
	for _, item := range rawAlerts(t, login(t, srv, "sam", testPassword)) {
		if _, present := item["investigation"]; present {
			t.Errorf("with the agent off an alert carries %#v", item)
		}
	}
}

func TestAlerts_NoLinkAfterResolve(t *testing.T) {
	e := newInvEnv(t)
	e.twoAlerts(t)
	id := e.seedInv(t, invSeed{key: devSvc, state: "done", summary: "Findings."})
	incident := e.seedIncident(t, id, devSvc, "service_failing")
	if rawAlerts(t, e.sam)["image-resizer"]["investigation"] == nil {
		t.Fatal("the open incident has no link")
	}
	if _, err := e.st.Pool.Exec(t.Context(), `update notification_incidents set resolved_at = now() where id = $1`, incident); err != nil {
		t.Fatal(err)
	}
	if value := rawAlerts(t, e.sam)["image-resizer"]["investigation"]; value != nil {
		t.Errorf("after the incident resolved the alert links %#v", value)
	}
}
