package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/limits"
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

// runTokenClientRedacting is runTokenClient with the redaction the provider's setting would give.
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

// unlimitedRate never empties a bucket. The route walk below makes as many requests as the registry has routes, and
// that count grows with the product; the shared per-user bucket is covered by TestRunToken_SharesTheUsersRateBucket.
type unlimitedRate struct{ limits.Rate }

func (unlimitedRate) Allow(context.Context, string, string, int, float64) (bool, error) {
	return true, nil
}

// Every route that is not marked for tokens refuses a run token, an admin's included: with the agent on alone, and with
// access tokens and OAuth on beside it so every optional route is registered. Public routes count too.
func TestRunToken_RefusedOnEveryUnmarkedRoute(t *testing.T) {
	for name, mutate := range map[string]func(*Server){
		"agent only": withAgent(func(s *Server) { s.Rates = unlimitedRate{} }),
		"agent, access tokens and OAuth": withTokens(func(c *config.Config) {
			c.AgentURL, c.AgentRunTokenTTL = testAgentURL, 15*time.Minute
			c.OAuth, c.MCPPublicURL = true, "https://mcp.example.com"
			c.OAuthClientHosts, c.OAuthRedirectURIs = config.DefaultOAuthClientHosts(), config.DefaultOAuthRedirectURIs()
		}, func(s *Server) { s.Rates = unlimitedRate{} }),
	} {
		t.Run(name, func(t *testing.T) {
			srv, api, _, st, _ := newServerCustomAPI(t, true, mutate)
			admin := runTokenClient(t, srv, api, st, "sam")

			checked, public, oauth := 0, 0, 0
			for _, route := range api.Routes() {
				// The SAML ACS skips authentication altogether (onlyAPI): it never reads an Authorization header.
				if route.Opts.Token != "" || strings.HasSuffix(route.Pattern, " "+samlACSPath) {
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
				if route.Opts.Public {
					public++
				}
				if strings.Contains(route.Pattern, "/api/oauth/authorize") || strings.HasSuffix(route.Pattern, "/api/oauth/token") {
					oauth++
				}
			}
			if public == 0 {
				t.Error("no public route was checked")
			}
			if name != "agent only" && oauth == 0 {
				t.Error("no OAuth route was checked; the setup does not register them")
			}
			if checked < 80 {
				t.Errorf("only %d unmarked routes were checked; the registry looks incomplete", checked)
			}
			for _, path := range []string{"/api/nope", "/api/services/x/nope", "/api/"} {
				for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
					resp := admin.do(method, path, nil, nil)
					if resp.StatusCode != http.StatusForbidden {
						t.Errorf("%s %s with a run token: status %d, want 403 (no probing which paths exist)", method, path, resp.StatusCode)
					}
					resp.Body.Close()
				}
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

// With the agent off a run token is ignored, whether or not access tokens are on: no token-path 401, and a session
// cookie beside it is served.
func TestRunToken_NotAcceptedWithAgentOff(t *testing.T) {
	for _, apiTokens := range []bool{false, true} {
		srv, api, _, st, _ := newServerCustomAPI(t, true, func(s *Server) {
			s.Cfg.AgentRunTokenTTL = 15 * time.Minute
			s.Cfg.APITokens = apiTokens
			s.Auth = auth.NewService(s.Store, s.Cfg)
		})
		bearer := runTokenClientWith(t, api, st, "omer")
		anon := newAPIClient(t, srv)
		anon.bearer = bearer
		resp := anon.get("/api/overview")
		if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("WWW-Authenticate") != "" {
			t.Errorf("tokens=%v: status %d www=%q, want an anonymous 401 with no WWW-Authenticate", apiTokens, resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
		}
		session := login(t, srv, "omer", testPassword)
		session.bearer = bearer
		if r := session.get("/api/overview"); r.StatusCode != http.StatusOK {
			t.Errorf("tokens=%v: session beside an ignored run token: status %d, want 200", apiTokens, r.StatusCode)
		}
	}
}

// runTokenClientWith mints a run token for username with the agent switched on in the minting service only.
func runTokenClientWith(t *testing.T, api *Server, st *store.Store, username string) string {
	t.Helper()
	user, err := st.GetUserByUsername(t.Context(), username)
	if err != nil {
		t.Fatal(err)
	}
	cfg := api.Cfg
	cfg.AgentURL = testAgentURL
	raw, err := auth.NewService(st, cfg).MintRunToken(t.Context(), user, uuid.New(), true)
	if err != nil {
		t.Fatal(err)
	}
	return raw
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

// HM_TOKEN_LOG_SEARCH_MAX_GB is the smaller limit here: the run token's search is capped at it, not at the larger
// remaining daily budget, as a personal token's is.
func TestRunToken_LogSearchCapIsTheSmallerLimit(t *testing.T) {
	env := newTokenLogSearchEnv(t, func(cfg *config.Config) {
		cfg.AgentURL, cfg.AgentRunTokenTTL = testAgentURL, 15*time.Minute
		cfg.TokenLogSearchMaxGB, cfg.TokenLogSearchDailyGB = 1, 20
	})
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
	var body tokenSearchBody
	resp := run.post("/api/log-search", searchReq(paymentsID()))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	decode(t, resp, &body)
	if body.ScanLimitBytes != gib {
		t.Errorf("scan_limit_bytes = %d, want 1 GiB (HM_TOKEN_LOG_SEARCH_MAX_GB), not the 20 GiB budget", body.ScanLimitBytes)
	}
}

// A run token and a personal token of one user draw on the same per-user rate bucket.
func TestRunToken_SharesTheUsersRateBucket(t *testing.T) {
	srv, api, _, st, _ := newServerCustomAPI(t, true, withTokens(func(c *config.Config) {
		c.AgentURL, c.AgentRunTokenTTL = testAgentURL, 15*time.Minute
	}, nil))
	personal := tokenClient(t, srv, st, "sam")
	runClient := runTokenClient(t, srv, api, st, "sam") // minted first: minting is not what this test measures
	limited := false
	for i := 0; i < 300 && !limited; i++ {
		resp := personal.get("/api/tokens/current")
		resp.Body.Close()
		limited = resp.StatusCode == http.StatusTooManyRequests
	}
	if !limited {
		t.Fatal("the personal token was never rate limited")
	}
	if resp := runClient.get("/api/tokens/current"); resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("run token of the same user: status %d, want 429 from the shared bucket", resp.StatusCode)
	}
	if resp := runTokenClient(t, srv, api, st, "omer").get("/api/tokens/current"); resp.StatusCode != http.StatusOK {
		t.Errorf("run token of another user: status %d, want 200", resp.StatusCode)
	}
}
