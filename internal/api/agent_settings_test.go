package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/agentclient"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// settingsAgent is a fake agent for the settings page: a catalogue, and /v1/test answering from a script.
type settingsAgent struct {
	srv *httptest.Server

	mu      sync.Mutex
	tests   int
	bearers []string
	// onTest answers a test call; the default answers ok at a cost of 1234 micro-dollars.
	onTest func(w http.ResponseWriter, r *http.Request)
}

func newSettingsAgent(t *testing.T) *settingsAgent {
	t.Helper()
	sa := &settingsAgent{}
	sa.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/catalogue":
			_, _ = w.Write([]byte(fakeCatalogue))
		case "/v1/test":
			sa.mu.Lock()
			sa.tests++
			sa.bearers = append(sa.bearers, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
			script := sa.onTest
			sa.mu.Unlock()
			if script != nil {
				script(w, r)
				return
			}
			_, _ = w.Write([]byte(`{"ok":true,"kind":"ok","message":"The model answered.","input_tokens":5,"output_tokens":2,"cost_micro_usd":1234}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(sa.srv.Close)
	return sa
}

func (sa *settingsAgent) testCalls() int {
	sa.mu.Lock()
	defer sa.mu.Unlock()
	return sa.tests
}

type settingsEnv struct {
	srv   *httptest.Server
	st    *store.Store
	api   *Server
	agent *settingsAgent
	sam   *apiClient // admin
	omer  *apiClient // not an admin
}

// newSettingsEnv builds a server with the agent on and a 10 USD budget with fake-1 chosen for chat. down points
// the client at a server that is already closed.
func newSettingsEnv(t *testing.T, down bool) *settingsEnv {
	t.Helper()
	sa := newSettingsAgent(t)
	if down {
		sa.srv.Close()
	}
	srv, api, _, st, _ := newServerCustomAPI(t, true, withAgent(func(s *Server) {
		client, err := agentclient.New(sa.srv.URL, "")
		if err != nil {
			t.Fatal(err)
		}
		s.Agent = client
		s.AgentHeartbeat = time.Hour
	}))
	setChatSettings(t, st, 10_000_000, "fake", "fake-1", nil)
	return &settingsEnv{srv: srv, st: st, api: api, agent: sa,
		sam: login(t, srv, "sam", testPassword), omer: login(t, srv, "omer", testPassword)}
}

type settingsBody struct {
	Budget     string                    `json:"monthly_budget_usd"`
	Unredacted []string                  `json:"unredacted_providers"`
	RoleModels map[string]map[string]any `json:"role_models"`
	Catalogue  struct {
		Providers []struct {
			Provider string `json:"provider"`
			Models   []struct {
				ID string `json:"id"`
			} `json:"models"`
		}
	} `json:"catalogue"`
	Reachable bool `json:"agent_reachable"`
	SpendRaw  struct {
		Month    string `json:"month"`
		Settled  string `json:"settled_usd"`
		Reserved string `json:"reserved_usd"`
	} `json:"spend"`
}

func getSettings(t *testing.T, c *apiClient) settingsBody {
	t.Helper()
	resp := c.get("/api/agent/settings")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET settings: status %d", resp.StatusCode)
	}
	var out settingsBody
	decode(t, resp, &out)
	return out
}

func putSettings(c *apiClient, budget string, unredacted []string, chat map[string]string) *http.Response {
	roles := map[string]any{}
	if chat != nil {
		roles["chat"] = chat
	}
	if unredacted == nil {
		unredacted = []string{}
	}
	return c.do(http.MethodPut, "/api/agent/settings", map[string]any{
		"monthly_budget_usd": budget, "unredacted_providers": unredacted, "role_models": roles}, nil)
}

func fakeChat() map[string]string { return map[string]string{"provider": "fake", "model": "fake-1"} }

func TestAgentSettings_AdminOnly(t *testing.T) {
	env := newSettingsEnv(t, false)
	omer, err := env.st.GetUserByUsername(t.Context(), "omer")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []struct{ method, path string }{
		{"GET", "/api/agent/settings"}, {"PUT", "/api/agent/settings"}, {"POST", "/api/agent/settings/test"},
		{"GET", "/api/agent/usage"}, {"POST", fmt.Sprintf("/api/users/%d/ai-chat", omer.ID)},
	} {
		resp := env.omer.do(r.method, r.path, map[string]any{"on": true}, nil)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s as a non-admin: status %d, want 403", r.method, r.path, resp.StatusCode)
		}
		resp.Body.Close()
	}
	if env.agent.testCalls() != 0 {
		t.Error("a non-admin reached the agent")
	}
}

func TestAgentSettings_RoundTrip(t *testing.T) {
	env := newSettingsEnv(t, false)
	resp := putSettings(env.sam, "30.5", []string{"fake"}, fakeChat())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT: status %d", resp.StatusCode)
	}
	var saved settingsBody
	decode(t, resp, &saved)
	got := getSettings(t, env.sam)
	for _, s := range []settingsBody{saved, got} {
		chat := s.RoleModels["chat"]
		if s.Budget != "30.50" || len(s.Unredacted) != 1 || s.Unredacted[0] != "fake" ||
			chat["provider"] != "fake" || chat["model"] != "fake-1" || chat["available"] != true || !s.Reachable ||
			len(s.Catalogue.Providers) != 1 || s.Catalogue.Providers[0].Models[0].ID != "fake-1" ||
			s.SpendRaw.Month != env.api.now().UTC().Format("2006-01") || s.SpendRaw.Settled != "0.00" || s.SpendRaw.Reserved != "0.00" {
			t.Errorf("settings = %+v", s)
		}
	}
	stored, err := env.st.GetAgentSettings(t.Context())
	if err != nil || stored.MonthlyBudgetMicroUSD != 30_500_000 {
		t.Errorf("stored budget = %d, %v; want 30500000", stored.MonthlyBudgetMicroUSD, err)
	}
	// Clearing the chat model is a save too.
	resp = putSettings(env.sam, "30.50", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT without a chat model: status %d", resp.StatusCode)
	}
	resp.Body.Close()
	if _, ok := getSettings(t, env.sam).RoleModels["chat"]; ok {
		t.Error("chat model still set")
	}
}

func TestAgentSettings_RejectsModelNotInCatalogue(t *testing.T) {
	env := newSettingsEnv(t, false)
	for name, args := range map[string]struct {
		unredacted []string
		chat       map[string]string
	}{
		"unknown model":        {nil, map[string]string{"provider": "fake", "model": "nope"}},
		"unknown provider":     {nil, map[string]string{"provider": "openai", "model": "fake-1"}},
		"unredacted not in it": {[]string{"openai"}, fakeChat()},
	} {
		resp := putSettings(env.sam, "99", args.unredacted, args.chat)
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%s: status %d, want 422", name, resp.StatusCode)
		}
		resp.Body.Close()
	}
	if stored, _ := env.st.GetAgentSettings(t.Context()); stored.MonthlyBudgetMicroUSD != 10_000_000 {
		t.Errorf("a refused save changed the budget to %d", stored.MonthlyBudgetMicroUSD)
	}
}

func TestAgentSettings_FlagsStaleModelAsUnavailable(t *testing.T) {
	env := newSettingsEnv(t, false)
	setChatSettings(t, env.st, 10_000_000, "fake", "retired-1", nil)
	chat := getSettings(t, env.sam).RoleModels["chat"]
	if chat["model"] != "retired-1" || chat["available"] != false {
		t.Errorf("chat = %v, want retired-1 flagged unavailable", chat)
	}
	// R42: resubmitting the stale model unchanged is allowed, so the admin can still change the budget.
	resp := putSettings(env.sam, "12", nil, map[string]string{"provider": "fake", "model": "retired-1"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unchanged stale model: status %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
	got := getSettings(t, env.sam)
	if got.Budget != "12.00" || got.RoleModels["chat"]["available"] != false {
		t.Errorf("after saving = %+v", got)
	}
	// A changed model must be in the catalogue, even when it replaces a stale one.
	resp = putSettings(env.sam, "12", nil, map[string]string{"provider": "fake", "model": "other-gone"})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("changed to a model not offered: status %d, want 422", resp.StatusCode)
	}
	resp.Body.Close()
	resp = putSettings(env.sam, "12", nil, fakeChat())
	if resp.StatusCode != http.StatusOK {
		t.Errorf("changed to an offered model: status %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestAgentSettings_BudgetParsing(t *testing.T) {
	env := newSettingsEnv(t, false)
	accepted := map[string]int64{
		"25": 25_000_000, "25.00": 25_000_000, "0.5": 500_000, "0": 0, "0.01": 10_000,
		"1000000": 1_000_000_000_000, "1000000.00": 1_000_000_000_000, "007": 7_000_000,
	}
	for in, want := range accepted {
		resp := putSettings(env.sam, in, nil, fakeChat())
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%q: status %d, want 200", in, resp.StatusCode)
			resp.Body.Close()
			continue
		}
		resp.Body.Close()
		if stored, _ := env.st.GetAgentSettings(t.Context()); stored.MonthlyBudgetMicroUSD != want {
			t.Errorf("%q stored as %d micro-dollars, want %d", in, stored.MonthlyBudgetMicroUSD, want)
		}
	}
	setChatSettings(t, env.st, 777_000, "fake", "fake-1", nil)
	for _, in := range []string{"-1", "1e3", "abc", "", " 25", "25 ", ".5", "25.", "25.001", "0.005", "1,000", "+5",
		"1000000.01", "1000001", "99999999999999999999", "٣"} {
		resp := putSettings(env.sam, in, nil, fakeChat())
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%q: status %d, want 422", in, resp.StatusCode)
		}
		resp.Body.Close()
	}
	if stored, _ := env.st.GetAgentSettings(t.Context()); stored.MonthlyBudgetMicroUSD != 777_000 {
		t.Errorf("a refused amount changed the budget to %d", stored.MonthlyBudgetMicroUSD)
	}
}

func TestAgentSettings_SpendRoundsUp(t *testing.T) {
	env := newSettingsEnv(t, false)
	now := env.api.now()
	for _, cost := range []int64{10_000, 1} { // exactly one cent, then one micro-dollar more than none
		seedRun(t, env.st, "chat", "done", cost, now)
	}
	// 10001 micro-dollars is not a whole number of cents; a partly used cent counts as a cent: 0.02.
	if got := getSettings(t, env.sam).SpendRaw.Settled; got != "0.02" {
		t.Errorf("settled = %q, want 0.02 (rounded up)", got)
	}
	// A reservation held by a running run shows rounded up as well.
	run := store.AgentRun{ID: uuid.New(), Role: "chat", Provider: "fake", Model: "fake-1", ReservedMicroUSD: 20_001}
	if err := env.st.ReserveAgentRun(t.Context(), run, time.Hour, now); err != nil {
		t.Fatal(err)
	}
	if got := getSettings(t, env.sam).SpendRaw.Reserved; got != "0.03" {
		t.Errorf("reserved = %q, want 0.03 (rounded up)", got)
	}
}

func TestAgentSettings_DropsRemovedProviderFromUnredacted(t *testing.T) {
	env := newSettingsEnv(t, false)
	setChatSettings(t, env.st, 10_000_000, "fake", "fake-1", []string{"gone", "fake"})
	// Keeping the stored entries as they are works although "gone" is no longer offered.
	resp := putSettings(env.sam, "10", []string{"gone", "fake"}, fakeChat())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("keeping a stored entry: status %d", resp.StatusCode)
	}
	resp.Body.Close()
	// Dropping it works too.
	resp = putSettings(env.sam, "10", []string{"fake"}, fakeChat())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("dropping the removed provider: status %d", resp.StatusCode)
	}
	resp.Body.Close()
	if got := getSettings(t, env.sam).Unredacted; len(got) != 1 || got[0] != "fake" {
		t.Errorf("unredacted = %v, want [fake]", got)
	}
	// An entry that was not stored and is not offered cannot be added.
	resp = putSettings(env.sam, "10", []string{"fake", "gone"}, fakeChat())
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("adding a provider not offered: status %d, want 422", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestAgentSettings_AgentUnreachable(t *testing.T) {
	env := newSettingsEnv(t, true)
	got := getSettings(t, env.sam)
	if got.Reachable || len(got.Catalogue.Providers) != 0 || got.Budget != "10.00" {
		t.Errorf("settings with the agent down = %+v", got)
	}
	if got.RoleModels["chat"]["available"] == false {
		t.Error("a model is flagged unavailable only because the agent cannot be asked")
	}
	// R24: the budget can still change while role models and redaction stay as they are.
	resp := putSettings(env.sam, "11", nil, fakeChat())
	if resp.StatusCode != http.StatusOK {
		t.Errorf("budget-only save with the agent down: status %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
	for name, resp := range map[string]*http.Response{
		"model changed":      putSettings(env.sam, "11", nil, map[string]string{"provider": "fake", "model": "fake-2"}),
		"redaction changed":  putSettings(env.sam, "11", []string{"fake"}, fakeChat()),
		"chat model removed": putSettings(env.sam, "11", nil, nil),
	} {
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s with the agent down: status %d, want 503", name, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

func settingsAuditRows(t *testing.T, st *store.Store, action string) []store.AuditEntry {
	t.Helper()
	entries, err := st.ListAudit(t.Context(), 100, 0)
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

func TestAgentSettings_Audited(t *testing.T) {
	env := newSettingsEnv(t, false)
	setChatSettings(t, env.st, 25_000_000, "fake", "fake-1", []string{"gone"})
	resp := putSettings(env.sam, "30", []string{"fake"}, fakeChat())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT: status %d", resp.StatusCode)
	}
	resp.Body.Close()
	rows := settingsAuditRows(t, env.st, "agent_settings_update")
	if len(rows) != 1 {
		t.Fatalf("%d audit rows, want 1", len(rows))
	}
	row := rows[0]
	d := row.Details
	if row.ActorUsername != "sam" || d["old_budget_usd"] != "25.00" || d["new_budget_usd"] != "30.00" ||
		d["old_chat_model"] != "fake/fake-1" || d["new_chat_model"] != "fake/fake-1" ||
		fmt.Sprint(d["old_unredacted_providers"]) != "[gone]" || fmt.Sprint(d["new_unredacted_providers"]) != "[fake]" {
		t.Errorf("audit row = %+v", row)
	}
	for key := range d {
		if strings.Contains(strings.ToLower(key), "key") && !strings.HasSuffix(key, "usd") {
			t.Errorf("audit detail %q looks like a key", key)
		}
	}
}

func TestAgentSettings_RefusedSaveIsNotAudited(t *testing.T) {
	env := newSettingsEnv(t, false)
	resp := putSettings(env.sam, "abc", nil, fakeChat())
	resp.Body.Close()
	if n := len(settingsAuditRows(t, env.st, "agent_settings_update")); n != 0 {
		t.Errorf("%d audit rows for a refused save", n)
	}
}

func postTest(c *apiClient, provider, model string) *http.Response {
	return c.post("/api/agent/settings/test", map[string]string{"provider": provider, "model": model})
}

func runRows(t *testing.T, st *store.Store, role string) (n int, states string) {
	t.Helper()
	rows, err := st.Pool.Query(t.Context(), `select state from agent_runs where role = $1 order by started_at`, role)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var all []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		all = append(all, s)
	}
	return len(all), strings.Join(all, ",")
}

func TestAgentTest_CountsTowardBudget(t *testing.T) {
	env := newSettingsEnv(t, false)
	var duringReserved, duringTokens int64
	env.agent.mu.Lock()
	env.agent.onTest = func(w http.ResponseWriter, r *http.Request) {
		_, duringReserved, _ = env.st.AgentSpend(r.Context(), env.api.now())
		duringTokens = int64(agentCount(t, env.st, `select count(*) from api_tokens where kind = 'run'`))
		_, _ = w.Write([]byte(`{"ok":true,"kind":"ok","message":"The model answered.","cost_micro_usd":1234}`))
	}
	env.agent.mu.Unlock()

	resp := postTest(env.sam, "fake", "fake-1")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("test call: status %d", resp.StatusCode)
	}
	var out struct {
		OK      bool   `json:"ok"`
		Kind    string `json:"kind"`
		Message string `json:"message"`
	}
	decode(t, resp, &out)
	if !out.OK || out.Kind != "ok" || out.Message != "The model answered." {
		t.Errorf("result = %+v", out)
	}
	// R18: while it runs the call holds MaxRunCostMicroUSD(2000) of fake-1 (3 USD per million tokens): 6000.
	if duringReserved != 6000 {
		t.Errorf("reserved during the call = %d, want 6000", duringReserved)
	}
	if duringTokens != 1 {
		t.Errorf("%d run tokens during the call, want 1", duringTokens)
	}
	settled, reserved, err := env.st.AgentSpend(t.Context(), env.api.now())
	if err != nil || settled != 1234 || reserved != 0 {
		t.Errorf("spend after = %d settled, %d reserved, %v; want 1234 and 0", settled, reserved, err)
	}
	if n, states := runRows(t, env.st, "test"); n != 1 || states != "done" {
		t.Errorf("test runs = %d (%s), want one done", n, states)
	}
	if n := agentCount(t, env.st, `select count(*) from api_tokens where kind = 'run'`); n != 0 {
		t.Errorf("%d run tokens left", n)
	}
	// The cost shows in the settings spend and the usage, rounded up to the cent.
	if got := getSettings(t, env.sam).SpendRaw.Settled; got != "0.01" {
		t.Errorf("settings settled = %q, want 0.01", got)
	}
}

func TestAgentTest_EveryPathSettlesOnceAndDeletesTheToken(t *testing.T) {
	cases := map[string]struct {
		script         func(w http.ResponseWriter, r *http.Request)
		status         int
		wantState      string
		wantSettled    int64
		wantOK         bool
		wantKind       string
		skipBodyDecode bool
	}{
		"model answers": {nil, 200, "done", 1234, true, "ok", false},
		"model refuses": {func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"ok":false,"kind":"auth","message":"The provider refused the key.","cost_micro_usd":0}`))
		}, 200, "failed", 0, false, "auth", false},
		"refused but billed": {func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"ok":false,"kind":"model","message":"Bad answer.","cost_micro_usd":50}`))
		}, 200, "failed", 50, false, "model", false},
		"agent errors": {func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) },
			503, "failed", 0, false, "", true},
		"malformed answer": {func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`not json`)) },
			503, "failed", 0, false, "", true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			env := newSettingsEnv(t, false)
			env.agent.mu.Lock()
			env.agent.onTest = tc.script
			env.agent.mu.Unlock()
			resp := postTest(env.sam, "fake", "fake-1")
			if resp.StatusCode != tc.status {
				t.Fatalf("status %d, want %d", resp.StatusCode, tc.status)
			}
			if !tc.skipBodyDecode {
				var out struct {
					OK   bool   `json:"ok"`
					Kind string `json:"kind"`
				}
				decode(t, resp, &out)
				if out.OK != tc.wantOK || out.Kind != tc.wantKind {
					t.Errorf("result = %+v", out)
				}
			} else {
				resp.Body.Close()
			}
			if n, states := runRows(t, env.st, "test"); n != 1 || states != tc.wantState {
				t.Errorf("test runs = %d (%s), want one %s", n, states, tc.wantState)
			}
			if settled, reserved, _ := env.st.AgentSpend(t.Context(), env.api.now()); settled != tc.wantSettled || reserved != 0 {
				t.Errorf("spend = %d settled, %d reserved; want %d and 0", settled, reserved, tc.wantSettled)
			}
			if n := agentCount(t, env.st, `select count(*) from api_tokens where kind = 'run'`); n != 0 {
				t.Errorf("%d run tokens left", n)
			}
		})
	}
}

func TestAgentTest_RefusalsReserveNothing(t *testing.T) {
	env := newSettingsEnv(t, false)
	resp := postTest(env.sam, "fake", "not-offered")
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("model not offered: status %d, want 422", resp.StatusCode)
	}
	resp.Body.Close()
	setChatSettings(t, env.st, 0, "fake", "fake-1", nil)
	resp = postTest(env.sam, "fake", "fake-1")
	if resp.StatusCode != http.StatusPaymentRequired {
		t.Errorf("no allowance: status %d, want 402", resp.StatusCode)
	}
	resp.Body.Close()
	if n, _ := runRows(t, env.st, "test"); n != 0 || env.agent.testCalls() != 0 {
		t.Errorf("%d test runs and %d agent calls after refusals, want none", n, env.agent.testCalls())
	}
}

func TestAgentTest_AgentDownIs503(t *testing.T) {
	env := newSettingsEnv(t, true)
	resp := postTest(env.sam, "fake", "fake-1")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status %d, want 503", resp.StatusCode)
	}
	resp.Body.Close()
	if n, _ := runRows(t, env.st, "test"); n != 0 {
		t.Errorf("%d test runs with the agent down", n)
	}
}

// seedRun records one finished run for role at when, with the given cost.
func seedRun(t *testing.T, st *store.Store, role, state string, cost int64, when time.Time) {
	t.Helper()
	run := store.AgentRun{ID: uuid.New(), Role: role, Provider: "fake", Model: "fake-1"}
	if err := st.ReserveAgentRun(t.Context(), run, time.Hour, when); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordAgentRunUsage(t.Context(), run.ID, 10, 5, cost, when); err != nil {
		t.Fatal(err)
	}
	if err := st.SettleAgentRun(t.Context(), run.ID, state, nil, when); err != nil {
		t.Fatal(err)
	}
}

func TestAgentUsage_GroupsByDayAndRole(t *testing.T) {
	env := newSettingsEnv(t, false)
	now := env.api.now().UTC()
	start := time.Date(now.Year(), now.Month(), 1, 12, 0, 0, 0, time.UTC)
	day2 := start.AddDate(0, 0, 1)
	seedRun(t, env.st, "chat", "done", 10_001, start)
	seedRun(t, env.st, "chat", "failed", 5_000, start)
	seedRun(t, env.st, "test", "done", 1_000, start)
	seedRun(t, env.st, "chat", "done", 2_000_000, day2)
	// A running run is not usage yet.
	running := store.AgentRun{ID: uuid.New(), Role: "chat", Provider: "fake", Model: "fake-1", ReservedMicroUSD: 1}
	if err := env.st.ReserveAgentRun(t.Context(), running, time.Hour, day2); err != nil {
		t.Fatal(err)
	}
	resp := env.sam.get("/api/agent/usage")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var out struct {
		Month string `json:"month"`
		Days  []struct {
			Day          string `json:"day"`
			Role         string `json:"role"`
			Runs         int64  `json:"runs"`
			CostUSD      string `json:"cost_usd"`
			InputTokens  int64  `json:"input_tokens"`
			OutputTokens int64  `json:"output_tokens"`
		} `json:"days"`
	}
	decode(t, resp, &out)
	d1, d2 := start.Format("2006-01-02"), day2.Format("2006-01-02")
	want := []string{
		fmt.Sprintf("%s chat 2 0.02 20 10", d1), // 15001 micro-dollars rounds up to 2 cents
		fmt.Sprintf("%s test 1 0.01 10 5", d1),
		fmt.Sprintf("%s chat 1 2.00 10 5", d2),
	}
	if out.Month != start.Format("2006-01") || len(out.Days) != len(want) {
		t.Fatalf("usage = %+v", out)
	}
	for i, d := range out.Days {
		got := fmt.Sprintf("%s %s %d %s %d %d", d.Day, d.Role, d.Runs, d.CostUSD, d.InputTokens, d.OutputTokens)
		if got != want[i] {
			t.Errorf("row %d = %q, want %q", i, got, want[i])
		}
	}
}

func TestAgentUsage_EmptyIsAnEmptyList(t *testing.T) {
	env := newSettingsEnv(t, false)
	resp := env.sam.get("/api/agent/usage")
	var raw map[string]any
	decode(t, resp, &raw)
	if days, ok := raw["days"].([]any); !ok || len(days) != 0 {
		t.Errorf("days = %#v, want an empty list", raw["days"])
	}
}

func TestSetUserAIChat_AdminOnlyAndAudited(t *testing.T) {
	env := newSettingsEnv(t, false)
	omer, err := env.st.GetUserByUsername(t.Context(), "omer")
	if err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/users/%d/ai-chat", omer.ID)
	if resp := env.omer.post(path, map[string]bool{"on": true}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("as a non-admin: status %d, want 403", resp.StatusCode)
	}
	flag := func() bool {
		u, _ := env.st.GetUserByUsername(t.Context(), "omer")
		return u.AIChat
	}
	if flag() {
		t.Fatal("a refused call set the flag")
	}
	resp := env.sam.post(path, map[string]bool{"on": true})
	if resp.StatusCode != http.StatusNoContent || !flag() {
		t.Fatalf("grant: status %d, flag %v", resp.StatusCode, flag())
	}
	resp.Body.Close()
	var users struct {
		Items []struct {
			Username string `json:"username"`
			AIChat   bool   `json:"ai_chat"`
		} `json:"items"`
	}
	decode(t, env.sam.get("/api/users"), &users)
	for _, u := range users.Items {
		if u.Username == "omer" && !u.AIChat {
			t.Error("the users list does not show ai_chat")
		}
	}
	resp = env.sam.post(path, map[string]bool{"on": false})
	if resp.StatusCode != http.StatusNoContent || flag() {
		t.Fatalf("revoke: status %d, flag %v", resp.StatusCode, flag())
	}
	resp.Body.Close()
	rows := settingsAuditRows(t, env.st, "user_ai_chat")
	if len(rows) != 2 {
		t.Fatalf("%d audit rows, want 2 (grant and revoke)", len(rows))
	}
	// Newest first: the revoke.
	if rows[0].Details["on"] != false || rows[1].Details["on"] != true || rows[0].Target != "user:omer" || rows[0].ActorUsername != "sam" {
		t.Errorf("audit rows = %+v", rows)
	}
	if resp := env.sam.post("/api/users/99999/ai-chat", map[string]bool{"on": true}); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown user: status %d, want 404", resp.StatusCode)
	}
}
