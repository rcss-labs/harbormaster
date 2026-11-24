package api

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const testAgentURL = "https://agent.internal:8484"

// withAgent turns the agent on and leaves HM_API_TOKENS off, the setting run tokens must work in.
func withAgent(mutate func(*Server)) func(*Server) {
	return func(s *Server) {
		s.Cfg.AgentURL = testAgentURL
		s.Cfg.AgentRunTokenTTL = 15 * time.Minute
		s.Auth = auth.NewService(s.Store, s.Cfg)
		if mutate != nil {
			mutate(s)
		}
	}
}

// runTokenClient returns a client that authenticates as username with a freshly minted run token.
func runTokenClient(t *testing.T, srv *httptest.Server, api *Server, st *store.Store, username string) *apiClient {
	t.Helper()
	return runTokenClientRedacting(t, srv, api, st, username, true)
}

func runTokenClientRedacting(t *testing.T, srv *httptest.Server, api *Server, st *store.Store, username string, redactEnv bool) *apiClient {
	t.Helper()
	user, err := st.GetUserByUsername(t.Context(), username)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := api.Auth.MintRunToken(t.Context(), user, uuid.New(), redactEnv)
	if err != nil {
		t.Fatal(err)
	}
	c := newAPIClient(t, srv)
	c.bearer = raw
	return c
}

// Every route that is not marked for tokens refuses a run token, an admin's included, with the agent on alone and with
// access tokens on beside it.
func TestRunToken_RefusedOnEveryUnmarkedRoute(t *testing.T) {
	for name, mutate := range map[string]func(*Server){
		"agent only":          withAgent(nil),
		"agent and APITokens": withTokens(func(c *config.Config) { c.AgentURL, c.AgentRunTokenTTL = testAgentURL, 15*time.Minute }, nil),
	} {
		t.Run(name, func(t *testing.T) {
			srv, api, _, st, _ := newServerCustomAPI(t, true, mutate)
			admin := runTokenClient(t, srv, api, st, "sam")

			checked := 0
			for _, route := range api.Routes() {
				if route.Opts.Token != "" || route.Opts.Public {
					continue
				}
				method, path := concretePath(route.Pattern, "x")
				resp := admin.do(method, path, map[string]any{}, nil)
				if resp.StatusCode != http.StatusForbidden {
					t.Errorf("%s with an admin's run token: status %d, want 403", route.Pattern, resp.StatusCode)
					resp.Body.Close()
					continue
				}
				if code, _ := errorCode(t, resp); code != "token_not_allowed" {
					t.Errorf("%s: code %q, want token_not_allowed", route.Pattern, code)
				}
				checked++
			}
			if checked < 60 {
				t.Errorf("only %d unmarked routes were checked; the registry looks incomplete", checked)
			}
			for _, path := range []string{"/api/nope", "/api/services/x/nope", "/api/"} {
				resp := admin.get(path)
				if resp.StatusCode != http.StatusForbidden {
					t.Errorf("GET %s with a run token: status %d, want 403", path, resp.StatusCode)
				}
				resp.Body.Close()
			}
		})
	}
}

// The agent alone adds exactly one route, whoami, and it is on the token list.
func TestWhoAmI_AvailableToRunTokenWithAPITokensOff(t *testing.T) {
	srv, api, _, st, _ := newServerCustomAPI(t, true, withAgent(nil))
	var marked, tokenMgmt []string
	whoami := 0
	for _, route := range api.Routes() {
		if route.Opts.Token != "" {
			marked = append(marked, route.Pattern)
		}
		if route.Pattern == "GET /api/tokens/current" {
			whoami++
		} else if strings.Contains(route.Pattern, "/api/tokens") || strings.Contains(route.Pattern, "/tokens/") {
			tokenMgmt = append(tokenMgmt, route.Pattern)
		}
	}
	slices.Sort(marked)
	want := slices.Clone(pinnedTokenRoutes)
	slices.Sort(want)
	if !slices.Equal(marked, want) {
		t.Fatalf("token-readable routes with only the agent on = %v, want the pinned list", marked)
	}
	if whoami != 1 {
		t.Fatalf("whoami registered %d times, want once", whoami)
	}
	if len(tokenMgmt) != 0 {
		t.Fatalf("token management routes registered with HM_API_TOKENS off: %v", tokenMgmt)
	}

	c := runTokenClient(t, srv, api, st, "omer")
	resp := c.get("/api/tokens/current")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("whoami with a run token: status %d, want 200", resp.StatusCode)
	}
	var body WhoAmIDTO
	decode(t, resp, &body)
	if body.User.Username != "omer" || body.Token.Kind != "run" || body.Token.Scope != "read" {
		t.Errorf("whoami = %+v", body)
	}
	// A session gains nothing from it: the route looks absent, as it did before the agent existed.
	if r := login(t, srv, "omer", testPassword).get("/api/tokens/current"); r.StatusCode != http.StatusNotFound {
		t.Errorf("whoami for a session with HM_API_TOKENS off: status %d, want 404", r.StatusCode)
	}
	// Token management stays absent.
	if r := login(t, srv, "omer", testPassword).get("/api/tokens"); r.StatusCode != http.StatusNotFound {
		t.Errorf("GET /api/tokens with HM_API_TOKENS off: status %d, want 404", r.StatusCode)
	}
}

// With the agent off, a run token does not authenticate even though access tokens are on.
func TestRunToken_NotAcceptedWithAgentOff(t *testing.T) {
	srv, api, _, st, _ := newServerCustomAPI(t, true, withTokens(func(c *config.Config) { c.AgentRunTokenTTL = 15 * time.Minute }, nil))
	c := runTokenClient(t, srv, api, st, "omer")
	if resp := c.get("/api/overview"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", resp.StatusCode)
	}
}

// A search made with a run token is capped like a personal token's and charged to the same per-user ledger.
func TestRunToken_LogSearchChargedToTheUsersDailyBudget(t *testing.T) {
	env := newTokenLogSearchEnv(t, func(cfg *config.Config) {
		cfg.AgentURL, cfg.AgentRunTokenTTL = testAgentURL, 15*time.Minute
		cfg.TokenLogSearchDailyGB = 1
	}, scanning(gib))
	user, err := env.st.GetUserByUsername(t.Context(), "sam")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := env.api.Auth.MintRunToken(t.Context(), user, uuid.New(), true)
	if err != nil {
		t.Fatal(err)
	}
	run := newAPIClient(t, env.srv)
	run.bearer = raw

	addTokenUsage(t, env, "sam", gib/2) // half the day's budget already spent, as by a personal token
	var body tokenSearchBody
	resp := run.post("/api/log-search", searchReq(paymentsID()))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("run token search: status %d", resp.StatusCode)
	}
	decode(t, resp, &body)
	if body.ScanLimitBytes != gib/2 || !body.ScanLimitReached {
		t.Fatalf("scan_limit_bytes=%d reached=%v, want the 0.5 GiB remaining budget and reached", body.ScanLimitBytes, body.ScanLimitReached)
	}
	if used := tokenUsage(t, env, "sam"); used <= gib/2 {
		t.Fatalf("usage = %d, want the run token's search charged to the user's ledger", used)
	}
	// The same ledger refuses a personal token and the run token alike.
	if r := tokenClient(t, env.srv, env.st, "sam").post("/api/log-search", searchReq(paymentsID())); r.StatusCode != http.StatusTooManyRequests {
		t.Errorf("personal token after the run token spent the budget: status %d, want 429", r.StatusCode)
	}
	if r := run.post("/api/log-search", searchReq(paymentsID())); r.StatusCode != http.StatusTooManyRequests {
		t.Errorf("second run token search: status %d, want 429", r.StatusCode)
	}
}

// A run token hides environment values by its own flag, whatever HM_TOKEN_REDACT_ENV says for personal tokens.
func TestRunToken_EnvironmentRedactedByItsFlag(t *testing.T) {
	srv, api, fk, st, _ := newServerCustomAPI(t, true, withAgent(nil))
	seedAccounts(t, st)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	path := "/api/services/" + paymentsID() + "/task-definition"

	redacted := readDefinition(t, runTokenClientRedacting(t, srv, api, st, "sam", true), path)
	if !redacted.EnvironmentRedacted {
		t.Error("a run token minted with redaction sees environment values")
	}
	open := readDefinition(t, runTokenClientRedacting(t, srv, api, st, "sam", false), path)
	if open.EnvironmentRedacted {
		t.Error("a run token minted without redaction is redacted")
	}
	if env, _ := open.counts(); env == 0 {
		t.Fatal("the fixture has no environment variables; the test proves nothing")
	}
}
