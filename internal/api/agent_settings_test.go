package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/agentclient"
	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

// settingsAgent is a fake agent for the settings page: a catalogue, and /v1/test answering from a script.
type settingsAgent struct {
	srv *httptest.Server

	mu      sync.Mutex
	body    string // the catalogue served; fakeCatalogue when empty
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
			sa.mu.Lock()
			body := sa.body
			sa.mu.Unlock()
			if body == "" {
				body = fakeCatalogue
			}
			_, _ = w.Write([]byte(body))
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
	logs  *syncBuffer
}

// newSettingsEnv builds a server with the agent on and a 10 USD budget with fake-1 chosen for chat. down points
// the client at a server that is already closed.
func newSettingsEnv(t *testing.T, down bool) *settingsEnv {
	t.Helper()
	sa := newSettingsAgent(t)
	if down {
		sa.srv.Close()
	}
	srv, api, _, st, logs := newServerCustomAPI(t, true, withAgent(func(s *Server) {
		client, err := agentclient.New(sa.srv.URL, "")
		if err != nil {
			t.Fatal(err)
		}
		s.Agent = client
		s.AgentHeartbeat = time.Hour
	}))
	setChatSettings(t, st, 10_000_000, "fake", "fake-1", nil)
	return &settingsEnv{srv: srv, st: st, api: api, agent: sa, logs: logs,
		sam: login(t, srv, "sam", testPassword), omer: login(t, srv, "omer", testPassword)}
}

type settingsBody struct {
	Version    int64                     `json:"version"`
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

// putSettings saves with the version the server reports now, as a form loaded just before would.
func putSettings(c *apiClient, budget string, unredacted []string, chat map[string]string) *http.Response {
	resp := c.get("/api/agent/settings")
	var current struct {
		Version int64 `json:"version"`
	}
	decode(c.t, resp, &current)
	return putSettingsAt(c, current.Version, budget, unredacted, chat)
}

func putSettingsAt(c *apiClient, version int64, budget string, unredacted []string, chat map[string]string) *http.Response {
	roles := map[string]any{}
	if chat != nil {
		roles["chat"] = chat
	}
	if unredacted == nil {
		unredacted = []string{}
	}
	return c.do(http.MethodPut, "/api/agent/settings", map[string]any{
		"monthly_budget_usd": budget, "unredacted_providers": unredacted, "role_models": roles, "version": version}, nil)
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
	// R54: additions and changes need the agent; removals in the safe direction do not.
	for name, resp := range map[string]*http.Response{
		"model changed":    putSettings(env.sam, "11", nil, map[string]string{"provider": "fake", "model": "fake-2"}),
		"redaction lifted": putSettings(env.sam, "11", []string{"fake"}, fakeChat()),
	} {
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s with the agent down: status %d, want 503", name, resp.StatusCode)
		}
		resp.Body.Close()
	}
	setChatSettings(t, env.st, 10_000_000, "fake", "fake-1", []string{"fake", "gone"})
	resp = putSettings(env.sam, "11", []string{"gone"}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("dropping a provider and the chat model with the agent down: status %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
	got = getSettings(t, env.sam)
	if _, has := got.RoleModels["chat"]; has || len(got.Unredacted) != 1 || got.Unredacted[0] != "gone" {
		t.Errorf("after removals = %+v", got)
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
			_, _ = w.Write([]byte(`{"ok":false,"kind":"invalid_request","message":"Bad answer.","cost_micro_usd":50}`))
		}, 200, "failed", 50, false, "invalid_request", false},
		// The call was sent and what it cost is unknown: it settles at its reservation.
		"agent errors": {func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) },
			503, "failed", 6000, false, "", true},
		"malformed answer": {func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`not json`)) },
			503, "failed", 6000, false, "", true},
		"hangs up": {func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) }, 503, "failed", 6000, false, "", true},
		"answer without a cost": {func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"ok":true,"kind":"ok"}`)) },
			200, "done", 6000, true, "ok", false},
		"answer with a negative cost": {func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"ok":true,"kind":"ok","cost_micro_usd":-5}`))
		}, 200, "done", 6000, true, "ok", false},
		// The agent refused before any paid call.
		"token refused":   {func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }, 503, "failed", 0, false, "", true},
		"model not found": {func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) }, 503, "failed", 0, false, "", true},
		"agent busy":      {func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTooManyRequests) }, 503, "failed", 0, false, "", true},
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
	if err := st.SettleAgentRun(t.Context(), run.ID, state, nil, nil, when); err != nil {
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

func TestAgentSettings_ReportsAndChecksVersion(t *testing.T) {
	env := newSettingsEnv(t, false)
	first := getSettings(t, env.sam)
	if first.Version < 1 {
		t.Fatalf("version = %d, want at least 1", first.Version)
	}
	resp := putSettingsAt(env.sam, first.Version, "11", nil, fakeChat())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT at the current version: status %d", resp.StatusCode)
	}
	var saved settingsBody
	decode(t, resp, &saved)
	if saved.Version <= first.Version || getSettings(t, env.sam).Version != saved.Version {
		t.Errorf("version after a save = %d (GET %d), want it raised above %d", saved.Version, getSettings(t, env.sam).Version, first.Version)
	}
	resp = putSettingsAt(env.sam, first.Version, "12", nil, fakeChat())
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("PUT at a stale version: status %d, want 409", resp.StatusCode)
	}
	code, msg := errorCode(t, resp)
	if code != "settings_changed" || msg != "These settings were changed by someone else. Reload and try again." {
		t.Errorf("conflict = %q %q", code, msg)
	}
	if stored, _ := env.st.GetAgentSettings(t.Context()); stored.MonthlyBudgetMicroUSD != 11_000_000 {
		t.Errorf("a refused save changed the budget to %d", stored.MonthlyBudgetMicroUSD)
	}
	if n := len(settingsAuditRows(t, env.st, "agent_settings_update")); n != 1 {
		t.Errorf("%d audit rows, want 1 (the refused save is not audited)", n)
	}
}

func TestAgentSettings_StaleFormCannotRelift(t *testing.T) {
	env := newSettingsEnv(t, false)
	setChatSettings(t, env.st, 10_000_000, "fake", "fake-1", []string{"fake"})
	stale := getSettings(t, env.sam) // admin A opens the page: redaction lifted for fake
	// Admin B turns redaction back on.
	resp := putSettings(env.sam, "10", nil, fakeChat())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("B's save: status %d", resp.StatusCode)
	}
	resp.Body.Close()
	// A saves the old form, a new budget with redaction still lifted.
	resp = putSettingsAt(env.sam, stale.Version, "20", stale.Unredacted, fakeChat())
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("stale save: status %d, want 409", resp.StatusCode)
	}
	resp.Body.Close()
	if got := getSettings(t, env.sam); len(got.Unredacted) != 0 || got.Budget != "10.00" {
		t.Errorf("settings after the refused stale save = %+v", got)
	}
}

func TestAgentSettings_ConcurrentSavesOneWins(t *testing.T) {
	env := newSettingsEnv(t, false)
	v := getSettings(t, env.sam).Version
	statuses := make(chan int, 2)
	for _, budget := range []string{"21", "22"} {
		go func() {
			resp := putSettingsAt(env.sam, v, budget, nil, fakeChat())
			resp.Body.Close()
			statuses <- resp.StatusCode
		}()
	}
	a, b := <-statuses, <-statuses
	if !(a == http.StatusOK && b == http.StatusConflict) && !(a == http.StatusConflict && b == http.StatusOK) {
		t.Errorf("statuses %d and %d, want one 200 and one 409", a, b)
	}
	if n := len(settingsAuditRows(t, env.st, "agent_settings_update")); n != 1 {
		t.Errorf("%d audit rows, want 1", n)
	}
}

func TestAgentSettings_BodyIsStrictAndComplete(t *testing.T) {
	env := newSettingsEnv(t, false)
	v := getSettings(t, env.sam).Version
	full := func() map[string]any {
		return map[string]any{"monthly_budget_usd": "9", "unredacted_providers": []string{}, "role_models": map[string]any{}, "version": v}
	}
	cases := map[string]func(m map[string]any){
		"unredacted missing":  func(m map[string]any) { delete(m, "unredacted_providers") },
		"unredacted null":     func(m map[string]any) { m["unredacted_providers"] = nil },
		"role models missing": func(m map[string]any) { delete(m, "role_models") },
		"role models null":    func(m map[string]any) { m["role_models"] = nil },
		"version missing":     func(m map[string]any) { delete(m, "version") },
		"version null":        func(m map[string]any) { m["version"] = nil },
		"misspelled key":      func(m map[string]any) { delete(m, "unredacted_providers"); m["unredacted_provider"] = []string{} },
		"extra key":           func(m map[string]any) { m["extra"] = 1 },
		"misspelled model key": func(m map[string]any) {
			m["role_models"] = map[string]any{"chat": map[string]any{"provider": "fake", "modle": "fake-1"}}
		},
	}
	for name, mutate := range cases {
		body := full()
		mutate(body)
		resp := env.sam.do(http.MethodPut, "/api/agent/settings", body, nil)
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%s: status %d, want 422", name, resp.StatusCode)
		}
		resp.Body.Close()
	}
	if stored, _ := env.st.GetAgentSettings(t.Context()); stored.MonthlyBudgetMicroUSD != 10_000_000 {
		t.Errorf("a refused body changed the budget to %d", stored.MonthlyBudgetMicroUSD)
	}
	resp := env.sam.post("/api/agent/settings/test", map[string]string{"provider": "fake", "modle": "fake-1"})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("test body with a misspelled key: status %d, want 422", resp.StatusCode)
	}
	resp.Body.Close()
	omer, _ := env.st.GetUserByUsername(t.Context(), "omer")
	resp = env.sam.post(fmt.Sprintf("/api/users/%d/ai-chat", omer.ID), map[string]any{"on": true, "extra": 1})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("ai-chat body with an extra key: status %d, want 422", resp.StatusCode)
	}
	resp.Body.Close()
}

var agentSettingsRoutes = []struct{ method, path string }{
	{"GET", "/api/agent/settings"}, {"PUT", "/api/agent/settings"}, {"POST", "/api/agent/settings/test"},
	{"GET", "/api/agent/usage"}, {"POST", "/api/users/2/ai-chat"},
}

func TestAgentSettingsRoutes_AbsentWhenDisabled(t *testing.T) {
	srv, _, _ := newTestServer(t)
	c := login(t, srv, "sam", testPassword)
	for _, r := range agentSettingsRoutes {
		resp := c.do(r.method, r.path, map[string]any{"on": true}, nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s %s with the agent off: status %d, want 404", r.method, r.path, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

func TestAgentSettingsRoutes_RefuseTokensAndMissingCSRF(t *testing.T) {
	srv, api, _, st, _ := newServerCustomAPI(t, true, withTokens(func(c *config.Config) {
		c.AgentURL, c.AgentRunTokenTTL = testAgentURL, 15*time.Minute
	}, nil))
	clients := map[string]*apiClient{
		"personal token": tokenClient(t, srv, st, "sam"),
		"run token":      runTokenClient(t, srv, api, st, "sam"),
	}
	for name, c := range clients {
		for _, r := range agentSettingsRoutes {
			resp := c.do(r.method, r.path, map[string]any{"on": true}, nil)
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("%s: %s %s: status %d, want 403", name, r.method, r.path, resp.StatusCode)
				resp.Body.Close()
				continue
			}
			if code, _ := errorCode(t, resp); code != "token_not_allowed" {
				t.Errorf("%s: %s %s: code %q, want token_not_allowed", name, r.method, r.path, code)
			}
		}
	}
	sam := login(t, srv, "sam", testPassword)
	for _, r := range agentSettingsRoutes {
		if r.method == "GET" {
			continue
		}
		resp := sam.do(r.method, r.path, map[string]any{"on": true}, map[string]string{"X-CSRF-Token": ""})
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s without a CSRF token: status %d, want 403", r.method, r.path, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

func TestAgentTest_CleansTheAnswer(t *testing.T) {
	secret := "sk-" + "ant-api03-" + "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789abcdefgh"
	long := strings.Repeat("x", 1000)
	cases := map[string]struct{ body, wantKind string }{
		"rate_limited stays": {`{"ok":false,"kind":"rate_limited","message":"Slow down."}`, "rate_limited"},
		"auth stays":         {`{"ok":false,"kind":"auth","message":"No."}`, "auth"},
		"unknown kind":       {`{"ok":false,"kind":"<script>","message":"No."}`, "unavailable"},
		"empty kind":         {`{"ok":false,"kind":"","message":"No."}`, "unavailable"},
		"secret in message":  {`{"ok":false,"kind":"auth","message":"bad key ` + secret + ` sent"}`, "auth"},
		"very long message":  {`{"ok":false,"kind":"auth","message":"` + long + `"}`, "auth"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			env := newSettingsEnv(t, false)
			env.agent.mu.Lock()
			env.agent.onTest = func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(tc.body)) }
			env.agent.mu.Unlock()
			resp := postTest(env.sam, "fake", "fake-1")
			var out AgentTestResultDTO
			decode(t, resp, &out)
			if out.Kind != tc.wantKind {
				t.Errorf("kind = %q, want %q", out.Kind, tc.wantKind)
			}
			if strings.Contains(out.Message, secret) || strings.Contains(out.Message, "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789") {
				t.Errorf("message carries a secret: %q", out.Message)
			}
			if n := len([]rune(out.Message)); n > 300 || n == 0 {
				t.Errorf("message length %d, want 1 to 300", n)
			}
		})
	}
}

func TestAgentTest_CostAboveReservationIsRecordedInFullAndLogged(t *testing.T) {
	env := newSettingsEnv(t, false)
	env.agent.mu.Lock()
	env.agent.onTest = func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"kind":"","message":"","cost_micro_usd":50000}`))
	}
	env.agent.mu.Unlock()
	resp := postTest(env.sam, "fake", "fake-1")
	resp.Body.Close()
	if settled, _, _ := env.st.AgentSpend(t.Context(), env.api.now()); settled != 50_000 {
		t.Errorf("settled = %d, want the full 50000 (never capped at the 6000 reserved)", settled)
	}
	if !strings.Contains(env.logs.String(), "above its reservation") {
		t.Error("no log line for a cost above the reservation")
	}
}

func TestAgentTest_MintFailureIsAnInternalError(t *testing.T) {
	env := newSettingsEnv(t, false)
	if _, err := env.st.Pool.Exec(t.Context(), `alter table api_tokens rename to api_tokens_gone`); err != nil {
		t.Fatal(err)
	}
	resp := postTest(env.sam, "fake", "fake-1")
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status %d, want 500 (not the agent-unavailable answer)", resp.StatusCode)
	}
	resp.Body.Close()
	if env.agent.testCalls() != 0 {
		t.Error("the agent was called without a token")
	}
	if n, states := runRows(t, env.st, "test"); n != 1 || states != "failed" {
		t.Errorf("test runs = %d (%s), want one failed", n, states)
	}
	if !strings.Contains(env.logs.String(), "run token") {
		t.Error("the mint failure was not logged")
	}
}

func TestAgentUsage_DaysFollowUTCAcrossMidnight(t *testing.T) {
	env := newSettingsEnv(t, false)
	now := env.api.now().UTC()
	late := time.Date(now.Year(), now.Month(), 3, 23, 30, 0, 0, time.UTC)
	early := late.Add(time.Hour) // 00:30 UTC on the 4th
	seedRun(t, env.st, "chat", "done", 1_000_000, late)
	seedRun(t, env.st, "chat", "done", 2_000_000, early)
	var out AgentUsageDTO
	decode(t, env.sam.get("/api/agent/usage"), &out)
	if len(out.Days) != 2 || out.Days[0].Day != late.Format("2006-01-02") || out.Days[1].Day != early.Format("2006-01-02") ||
		out.Days[0].CostUSD != "1.00" || out.Days[1].CostUSD != "2.00" {
		t.Errorf("days = %+v, want the 23:30 and 00:30 UTC runs on two days", out.Days)
	}
}

func TestSetUserAIChat_AuditShowsTheValueItReplaced(t *testing.T) {
	env := newSettingsEnv(t, false)
	omer, _ := env.st.GetUserByUsername(t.Context(), "omer")
	was, err := env.st.ChangeUserAIChat(t.Context(), omer.ID, true)
	if err != nil || was {
		t.Fatalf("first change: was %v, %v; want false", was, err)
	}
	was, err = env.st.ChangeUserAIChat(t.Context(), omer.ID, true)
	if err != nil || !was {
		t.Fatalf("second change: was %v, %v; want true", was, err)
	}
	if _, err := env.st.ChangeUserAIChat(t.Context(), 99999, true); err == nil {
		t.Error("an unknown user did not fail")
	}
}

// A test run ends like any other run: its cost survives a failed write, and it leaves exactly one audit row.
func TestAgentTest_FailedUsageWriteIsRetriedAndTheRunIsAudited(t *testing.T) {
	env := newSettingsEnv(t, false)
	failFirstUsageWrite(t, env.st)
	resp := postTest(env.sam, "fake", "fake-1")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	resp.Body.Close()
	wantTestRun(t, env, "done", 1234)
	rows := settingsAuditRows(t, env.st, "agent_run")
	if len(rows) != 1 || rows[0].ActorUsername != "sam" || rows[0].Details["role"] != "test" || rows[0].Details["cost_micro_usd"] != float64(1234) ||
		rows[0].Details["outcome"] != "done" {
		t.Errorf("audit rows = %+v", rows)
	}
}

func wantTestRun(t *testing.T, env *settingsEnv, state string, cost int64) {
	t.Helper()
	if n, states := runRows(t, env.st, "test"); n != 1 || states != state {
		t.Errorf("test runs = %d (%s), want one %s", n, states, state)
	}
	if settled, reserved, _ := env.st.AgentSpend(t.Context(), env.api.now()); settled != cost || reserved != 0 {
		t.Errorf("spend = %d settled, %d reserved; want %d and 0", settled, reserved, cost)
	}
	if n := agentCount(t, env.st, `select count(*) from api_tokens where kind = 'run'`); n != 0 {
		t.Errorf("%d run tokens left", n)
	}
}

// An admin who leaves while the model answers has still paid for the answer.
func TestAgentTest_CostIsRecordedWhenTheAdminLeaves(t *testing.T) {
	env := newSettingsEnv(t, false)
	entered, left := make(chan struct{}), make(chan struct{})
	env.agent.mu.Lock()
	env.agent.onTest = func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-left
		time.Sleep(200 * time.Millisecond) // let the server notice that the admin has gone
		_, _ = w.Write([]byte(`{"ok":true,"kind":"ok","message":"The model answered.","cost_micro_usd":1234}`))
	}
	env.agent.mu.Unlock()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, env.sam.base+"/api/agent/settings/test", strings.NewReader(`{"provider":"fake","model":"fake-1"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-CSRF-Token", env.sam.csrf)
		if resp, err := env.sam.http.Do(req); err == nil {
			resp.Body.Close()
		}
	}()
	<-entered
	cancel()
	<-done
	close(left)
	waitFor(t, "the run to be audited", func() bool { return len(settingsAuditRows(t, env.st, "agent_run")) == 1 })
	wantTestRun(t, env, "done", 1234)
}

// A shutdown does not wait for the model: the test call is cut off and its run settled.
func TestAgentTest_DrainCutsTheCallOff(t *testing.T) {
	env := newSettingsEnv(t, false)
	entered := make(chan struct{})
	env.agent.mu.Lock()
	env.agent.onTest = func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second): // the fake does not notice that its caller has gone
		}
	}
	env.agent.mu.Unlock()
	status := make(chan int, 1)
	go func() {
		resp := postTest(env.sam, "fake", "fake-1")
		resp.Body.Close()
		status <- resp.StatusCode
	}()
	<-entered
	start := time.Now()
	env.api.BeginDrain()
	if got := <-status; got != http.StatusServiceUnavailable || time.Since(start) > 8*time.Second {
		t.Errorf("status %d after %v", got, time.Since(start))
	}
	wantTestRun(t, env, "failed", 6000) // it was sent, so what it cost is unknown
}

// An agent that cannot be reached was never paid.
func TestAgentTest_UnreachableAgentIsFree(t *testing.T) {
	env := newSettingsEnv(t, false)
	env.sam.get("/api/agent/settings").Body.Close() // the catalogue is cached from now on
	env.agent.srv.Close()
	resp := postTest(env.sam, "fake", "fake-1")
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status %d, want 503", resp.StatusCode)
	}
	wantTestRun(t, env, "failed", 0)
}

// Once the server shuts down no paid call is started.
func TestAgentTest_NotStartedWhileDraining(t *testing.T) {
	env := newSettingsEnv(t, false)
	env.api.BeginDrain()
	resp := postTest(env.sam, "fake", "fake-1")
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable || env.agent.testCalls() != 0 {
		t.Errorf("status %d after %d calls, want 503 and none", resp.StatusCode, env.agent.testCalls())
	}
	wantTestRun(t, env, "failed", 0)
}

// The page needs what one question reserves with the chosen model, and whether the allowance left cannot cover it.
func TestAgentSettings_ReportsWhatOneQuestionReserves(t *testing.T) {
	env := newSettingsEnv(t, false)
	check := func(name string, wantBelow bool) {
		t.Helper()
		chat := getSettings(t, env.sam).RoleModels["chat"]
		if chat["max_question_usd"] != "0.60" || chat["allowance_below_question"] != wantBelow {
			t.Errorf("%s: chat = %v, want max_question_usd 0.60 and allowance_below_question %v", name, chat, wantBelow)
		}
	}
	check("plenty", false)
	setChatSettings(t, env.st, 600_000, "fake", "fake-1", nil)
	check("exactly one question", false)
	setChatSettings(t, env.st, 500_000, "fake", "fake-1", nil)
	check("allowance smaller than a question", true)
	setChatSettings(t, env.st, 1_000_000, "fake", "fake-1", nil)
	seedRun(t, env.st, "chat", "done", 500_000, env.api.now())
	check("what is left is smaller than a question", true)
}

// A model that costs nothing still cannot be asked while the allowance is 0 (the store refuses it), and sums that
// reach the int64 cap must not wrap into room that is not there.
func TestAgentSettings_AllowanceBelowQuestionAtTheEdges(t *testing.T) {
	const freeCatalogue = `{"providers":[{"provider":"fake","models":[{"id":"fake-free","input_per_mtok_micro_usd":0,"output_per_mtok_micro_usd":0,"context_tokens":8000}]}],"limits":{"max_run_tokens":200000,"run_timeout_seconds":300}}`
	env := newSettingsEnv(t, false)
	env.agent.mu.Lock()
	env.agent.body = freeCatalogue
	env.agent.mu.Unlock()
	setChatSettings(t, env.st, 0, "fake", "fake-free", nil)
	if chat := getSettings(t, env.sam).RoleModels["chat"]; chat["allowance_below_question"] != true {
		t.Errorf("free model, allowance 0: chat = %v, want allowance_below_question true", chat)
	}

	// A running run holding the whole cap and a finished one that cost it: together they wrap an int64.
	const maxInt64 = int64(^uint64(0) >> 1)
	setChatSettings(t, env.st, maxInt64, "fake", "fake-free", nil)
	now := env.api.now()
	running := store.AgentRun{ID: uuid.New(), Role: "chat", Provider: "fake", Model: "fake-free", ReservedMicroUSD: maxInt64}
	if err := env.st.ReserveAgentRun(t.Context(), running, time.Hour, now); err != nil {
		t.Fatal(err)
	}
	seedRun(t, env.st, "chat", "done", maxInt64, now)
	setChatSettings(t, env.st, 1_000_000, "fake", "fake-free", nil)
	if chat := getSettings(t, env.sam).RoleModels["chat"]; chat["allowance_below_question"] != true {
		t.Errorf("settled and reserved at the cap: chat = %v, want allowance_below_question true", chat)
	}
}

func TestAgentSettings_NothingToReserveWhenTheModelIsNotKnown(t *testing.T) {
	env := newSettingsEnv(t, true)
	chat := getSettings0(t, env.sam)["role_models"].(map[string]any)["chat"].(map[string]any)
	if chat["max_question_usd"] != "" || chat["allowance_below_question"] != false {
		t.Errorf("chat = %v, want an empty max_question_usd and no warning while the agent cannot be asked", chat)
	}
}

func getSettings0(t *testing.T, c *apiClient) map[string]any {
	t.Helper()
	var out map[string]any
	decode(t, c.get("/api/agent/settings"), &out)
	return out
}

// putSettingsRoles saves with the current version and the roles as given.
func putSettingsRoles(c *apiClient, budget string, roles map[string]any) *http.Response {
	var current struct {
		Version int64 `json:"version"`
	}
	decode(c.t, c.get("/api/agent/settings"), &current)
	return c.do(http.MethodPut, "/api/agent/settings", map[string]any{
		"monthly_budget_usd": budget, "unredacted_providers": []string{}, "role_models": roles, "version": current.Version}, nil)
}

func TestAgentSettings_InvestigatorModelRoundTrip(t *testing.T) {
	env := newSettingsEnv(t, false)
	env.api.Cfg.AgentInvestigationWindow = 15 * time.Minute
	env.api.Cfg.AgentInvestigationsPerHour = 20
	env.api.Cfg.AgentInvestigationsConcurrent = 2
	env.api.Cfg.AgentInvestigationMaxWait = 10 * time.Minute

	// Before any choice: no investigator entry; the identity block is present from the first read; limits come from configuration.
	first := getSettings0(t, env.sam)
	if _, ok := first["role_models"].(map[string]any)["investigator"]; ok {
		t.Errorf("an investigator model before one was chosen: %v", first["role_models"])
	}
	if id, _ := first["identity"].(map[string]any); id["username"] != "harbormaster-agent" || fmt.Sprint(id["permissions"]) != "[]" {
		t.Errorf("identity = %v, want harbormaster-agent with an empty list", first["identity"])
	}
	if got := fmt.Sprint(first["investigator"]); got != "map[concurrent:2 max_wait_minutes:10 per_hour:20 window_minutes:15]" {
		t.Errorf("investigator limits = %s", got)
	}

	resp := putSettingsRoles(env.sam, "30", map[string]any{"chat": fakeChat(), "investigator": fakeChat()})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT: status %d", resp.StatusCode)
	}
	var saved settingsBody
	decode(t, resp, &saved)
	for _, s := range []settingsBody{saved, getSettings(t, env.sam)} {
		inv := s.RoleModels["investigator"]
		if inv["provider"] != "fake" || inv["model"] != "fake-1" || inv["available"] != true || inv["max_question_usd"] != "0.60" {
			t.Errorf("investigator = %v", inv)
		}
	}
	stored, err := env.st.GetAgentSettings(t.Context())
	if err != nil || stored.RoleModels["investigator"].Model != "fake-1" || stored.RoleModels["chat"].Model != "fake-1" {
		t.Errorf("stored roles = %v, %v", stored.RoleModels, err)
	}
	rows := settingsAuditRows(t, env.st, "agent_settings_update")
	if len(rows) != 1 || rows[0].Details["old_investigator_model"] != "" || rows[0].Details["new_investigator_model"] != "fake/fake-1" {
		t.Errorf("audit = %+v", rows)
	}

	// A model that is not offered is refused for this role as for chat; an unknown role still is.
	resp = putSettingsRoles(env.sam, "30", map[string]any{"chat": fakeChat(), "investigator": map[string]string{"provider": "fake", "model": "nope"}})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("investigator not in the catalogue: status %d, want 422", resp.StatusCode)
	}
	resp.Body.Close()
	resp = putSettingsRoles(env.sam, "30", map[string]any{"chat": fakeChat(), "reviewer": fakeChat()})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("unknown role: status %d, want 422", resp.StatusCode)
	}
	resp.Body.Close()

	// Leaving it out clears it, as for chat.
	resp = putSettingsRoles(env.sam, "30", map[string]any{"chat": fakeChat()})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT without the investigator: status %d", resp.StatusCode)
	}
	resp.Body.Close()
	if _, ok := getSettings(t, env.sam).RoleModels["investigator"]; ok {
		t.Error("investigator model still set")
	}
}

func TestAgentSettings_InvestigatorChangeIsRefusedWhileTheAgentIsDown(t *testing.T) {
	env := newSettingsEnv(t, true)
	resp := putSettingsRoles(env.sam, "10", map[string]any{"chat": fakeChat(), "investigator": fakeChat()})
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("adding an investigator model with the agent down: status %d, want 503 (R24)", resp.StatusCode)
	}
	resp.Body.Close()
	// Saving what is stored goes through, and so does dropping the model.
	stored, _ := env.st.GetAgentSettings(t.Context())
	stored.RoleModels["investigator"] = store.AgentModelRef{Provider: "fake", Model: "fake-1"}
	if err := storetest.PutAgentSettings(t.Context(), env.st, stored); err != nil {
		t.Fatal(err)
	}
	resp = putSettingsRoles(env.sam, "11", map[string]any{"chat": fakeChat(), "investigator": fakeChat()})
	if resp.StatusCode != http.StatusOK {
		t.Errorf("unchanged investigator with the agent down: status %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
	resp = putSettingsRoles(env.sam, "11", map[string]any{"chat": fakeChat()})
	if resp.StatusCode != http.StatusOK {
		t.Errorf("dropping the investigator with the agent down: status %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestAgentSettings_StaleInvestigatorModelIsFlaggedAndSavable(t *testing.T) {
	env := newSettingsEnv(t, false)
	stored, _ := env.st.GetAgentSettings(t.Context())
	stored.RoleModels["investigator"] = store.AgentModelRef{Provider: "fake", Model: "retired-1"}
	if err := storetest.PutAgentSettings(t.Context(), env.st, stored); err != nil {
		t.Fatal(err)
	}
	inv := getSettings(t, env.sam).RoleModels["investigator"]
	if inv["model"] != "retired-1" || inv["available"] != false || inv["max_question_usd"] != "" {
		t.Errorf("investigator = %v, want retired-1 flagged unavailable with nothing reserved", inv)
	}
	// R42: resubmitting it unchanged is accepted, so the allowance can still be changed.
	resp := putSettingsRoles(env.sam, "12", map[string]any{"chat": fakeChat(), "investigator": map[string]string{"provider": "fake", "model": "retired-1"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unchanged stale investigator model: status %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
	got := getSettings(t, env.sam)
	if got.Budget != "12.00" || got.RoleModels["investigator"]["available"] != false {
		t.Errorf("after saving = %+v", got)
	}
	// A changed one must be offered, even when it replaces a stale one.
	resp = putSettingsRoles(env.sam, "12", map[string]any{"chat": fakeChat(), "investigator": map[string]string{"provider": "fake", "model": "other-gone"}})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("changed to a model not offered: status %d, want 422", resp.StatusCode)
	}
	resp.Body.Close()
	resp = putSettingsRoles(env.sam, "12", map[string]any{"chat": fakeChat(), "investigator": fakeChat()})
	if resp.StatusCode != http.StatusOK {
		t.Errorf("changed to an offered model: status %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
}
