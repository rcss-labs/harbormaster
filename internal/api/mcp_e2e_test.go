package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/apitoken"
	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/logs"
	"github.com/rcss-labs/harbormaster/internal/mcpgw"
	"github.com/rcss-labs/harbormaster/internal/oauthsrv"
	"github.com/rcss-labs/harbormaster/internal/scanjob"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// logSearchQuery narrows the fake's canned lines: an unfiltered search over it returns hundreds of rows, more
// than one tool result may hold (a real answer to that is the "too large" tool error, which has its own test).
const logSearchQuery = "filter @message like /ERROR/"

// mcpEnv is the real API handler (tokens on, scanning on, fast log search) with a gateway in front of it.
type mcpEnv struct {
	securityEnv
	api     *Server
	gateway *httptest.Server
	logs    *syncBuffer
}

func newMCPEnv(t *testing.T) *mcpEnv { return newMCPEnvWith(t, nil) }

// newMCPEnvWith is newMCPEnv with OAuth on when docs is set: Harbormaster serves the authorization server (trusting
// docs' client document host) and the gateway relays its token endpoint.
func newMCPEnvWith(t *testing.T, docs *oauthDocs) *mcpEnv {
	t.Helper()
	env := &mcpEnv{}
	srv, api, fk, st, _ := newServerCustomAPI(t, true, func(s *Server) {
		env.fk = s.Clients.(*fake.Fake)
		env.scanner = fake.NewScanner(env.fk.Estate)
		env.runner = &scanjob.Runner{Store: s.Store, Launcher: env.scanner, Bucket: env.scanner, DB: env.scanner,
			Cfg: scanTestConfig(), Now: time.Now, Log: s.Log}
		s.Cfg.Scan = scanTestConfig()
		s.Scan = env.runner
		s.Cfg.APITokens = true
		s.Cfg.TokenMaxTTL = 90 * 24 * time.Hour
		s.Cfg.TokenMaxTTLSSO = 7 * 24 * time.Hour
		s.Cfg.TokenMaxPerUser = 10
		s.Cfg.TokenLogSearchMaxGB = 2
		s.Cfg.TokenLogSearchDailyGB = 20
		if docs != nil {
			s.Cfg.OAuth, s.Cfg.MCPPublicURL, s.Cfg.OAuthAccessTTL = true, oauthTestIssuer, time.Hour
			s.Cfg.OAuthClientHosts = []string{"127.0.0.1"}
			s.Cfg.OAuthRedirectURIs = []string{oauthTestRedirect}
			s.OAuth = oauthsrv.NewService(s.Store, s.Cfg)
			s.OAuth.Clients = docs.resolver
		}
		// auth.NewService copied the config before this hook ran, so rebuild it with tokens on.
		s.Auth = auth.NewService(s.Store, s.Cfg)
		s.LogSearch = &logs.Searcher{Clients: s.Clients, Now: s.Now, PollEvery: 5 * time.Millisecond, Timeout: 3 * time.Second}
	})
	env.srv, env.api, env.fk, env.st = srv, api, fk, st
	seedMockupTags(t, st)
	for _, u := range []string{"oncall", "omer", "dana"} {
		setPassword(t, st, u)
	}
	pollAll(t, st, fk)
	env.sam = login(t, srv, "sam", testPassword)
	env.omer = login(t, srv, "omer", testPassword)

	upstream, err := mcpgw.NewUpstream(srv.URL, 20*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	env.logs = &syncBuffer{}
	cfg := mcpgw.Config{UpstreamURL: srv.URL, PublicURL: "https://mcp.example.com", MaxResultBytes: 140000, UpstreamTimeout: 20 * time.Second}
	if docs != nil {
		cfg.AuthorizeURL = srv.URL
	}
	env.gateway = httptest.NewServer(mcpgw.NewHandler(cfg, upstream, slog.New(slog.NewTextHandler(env.logs, nil))))
	t.Cleanup(env.gateway.Close)
	return env
}

// mintToken stores a personal read token for username and returns the secret string and its row.
func (e *mcpEnv) mintToken(t *testing.T, username string) (string, store.APIToken) {
	t.Helper()
	user, err := e.st.GetUserByUsername(t.Context(), username)
	if err != nil {
		t.Fatal(err)
	}
	raw, parsed, err := apitoken.Generate(apitoken.KindPAT)
	if err != nil {
		t.Fatal(err)
	}
	row, err := e.st.CreateAPIToken(t.Context(), store.APIToken{
		PublicID: apitoken.DisplayID(parsed), UserID: user.ID, Kind: string(apitoken.KindPAT), Name: "e2e",
		SecretHash: apitoken.Hash(parsed.Secret), Scope: string(auth.ScopeRead), AuthSource: "password",
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}, 10)
	if err != nil {
		t.Fatal(err)
	}
	return raw, row
}

// gatewayClient makes MCP calls through the gateway with one token.
type gatewayClient struct {
	t     *testing.T
	url   string
	token string
	id    int
}

func (e *mcpEnv) client(t *testing.T, token string) *gatewayClient {
	return &gatewayClient{t: t, url: e.gateway.URL + "/mcp", token: token}
}

// rpc posts one JSON-RPC request and returns the HTTP response.
func (c *gatewayClient) rpc(method string, params any) *http.Response {
	c.t.Helper()
	c.id++
	message := map[string]any{"jsonrpc": "2.0", "id": c.id, "method": method}
	if params != nil {
		message["params"] = params
	}
	raw, err := json.Marshal(message)
	if err != nil {
		c.t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, c.url, bytes.NewReader(raw))
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	return resp
}

// call runs one tool and returns the HTTP status, isError and the single text item.
func (c *gatewayClient) call(name string, args map[string]any) (status int, isError bool, text string) {
	c.t.Helper()
	resp := c.rpc("tools/call", map[string]any{"name": name, "arguments": args})
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, false, string(body)
	}
	var out struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &out); err != nil || len(out.Result.Content) != 1 {
		c.t.Fatalf("%s: unreadable answer %q: %v", name, body, err)
	}
	return resp.StatusCode, out.Result.IsError, out.Result.Content[0].Text
}

// mustCall runs a tool that must succeed and returns its JSON text.
func (c *gatewayClient) mustCall(name string, args map[string]any) string {
	c.t.Helper()
	status, isError, text := c.call(name, args)
	if status != http.StatusOK || isError {
		c.t.Fatalf("%s %v: HTTP %d isError=%v: %s", name, args, status, isError, text)
	}
	return text
}

// TestTokenRoutes_MatchTheGatewayTable: the routes marked for tokens are exactly the routes the gateway
// declares, so adding a route to either side alone fails. It lives here, not in token_routes_test.go, so it
// cannot collide with phase 1's own test file.
func TestTokenRoutes_MatchTheGatewayTable(t *testing.T) {
	env := newMCPEnv(t)
	var marked []string
	for _, route := range env.api.Routes() {
		if route.Opts.Token != "" {
			marked = append(marked, route.Pattern)
		}
	}
	sort.Strings(marked)
	want := mcpgw.UpstreamRoutes()
	if !slices.Equal(marked, want) {
		inTable := map[string]bool{}
		for _, r := range want {
			inTable[r] = true
		}
		inAPI := map[string]bool{}
		for _, r := range marked {
			inAPI[r] = true
		}
		for _, r := range marked {
			if !inTable[r] {
				t.Errorf("marked for tokens but not in the gateway table: %s", r)
			}
		}
		for _, r := range want {
			if !inAPI[r] {
				t.Errorf("in the gateway table but not marked for tokens: %s", r)
			}
		}
	}
	// Every token-readable route is a read; the only POSTs are the two log searches.
	for _, route := range marked {
		if strings.HasPrefix(route, "POST ") && route != "POST /api/log-search" && route != "POST /api/log-search/estimate" {
			t.Errorf("token-readable write route: %s", route)
		}
		if strings.HasPrefix(route, "DELETE ") || strings.HasPrefix(route, "PUT ") || strings.HasPrefix(route, "PATCH ") {
			t.Errorf("token-readable write route: %s", route)
		}
	}
}

// discovered holds ids found through the gateway itself, so the argument table uses real estate values.
type discovered struct {
	cluster, service, task, vpc, vpcAccount, vpcRegion, digest string
	revision                                                   int
}

func discover(t *testing.T, env *mcpEnv, c *gatewayClient) discovered {
	t.Helper()
	d := discovered{service: webFrontendID(), cluster: clusterID(sandboxAccountID, "us-east-1", "core-services")}
	d.vpc, d.vpcAccount, d.vpcRegion = vpcMain, sandboxAccountID, "us-east-1"
	d.revision = 1

	var tasks struct {
		Items []struct {
			ShortID string `json:"short_id"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(c.mustCall("list_service_tasks", map[string]any{"service_id": d.service})), &tasks); err != nil || len(tasks.Items) == 0 {
		t.Fatalf("list_service_tasks gave no task id (%v): %+v", err, tasks)
	}
	d.task = tasks.Items[0].ShortID

	var revisions struct {
		Items []struct {
			Revision int `json:"revision"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(c.mustCall("list_revisions", map[string]any{"service_id": d.service})), &revisions); err == nil && len(revisions.Items) > 0 {
		d.revision = revisions.Items[0].Revision
	}

	// A scanned image gives get_image_security a real digest.
	status := mustRequestScan(t, env.sam, securityPaymentsKey.ID(), "payments-api", "1.18.2")
	env.scanAll(t)
	d.digest = status.Image.Digest
	return d
}

func TestMCP_EveryToolAnswersAgainstTheRealAPI(t *testing.T) {
	env := newMCPEnv(t)
	secret, _ := env.mintToken(t, "sam")
	c := env.client(t, secret)
	d := discover(t, env, c)

	scope := map[string]any{"account": sandboxAccountID, "region": "us-east-1"}
	with := func(extra map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range scope {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	args := map[string]map[string]any{
		"whoami":                    nil,
		"get_overview":              nil,
		"list_clusters":             nil,
		"get_cluster":               {"cluster_id": d.cluster},
		"list_services":             nil,
		"get_service":               {"service_id": d.service},
		"list_service_tasks":        {"service_id": d.service},
		"list_tasks":                nil,
		"search":                    {"q": "web"},
		"get_task_definition":       {"service_id": d.service},
		"list_revisions":            {"service_id": d.service},
		"get_revision":              {"service_id": d.service, "revision": d.revision},
		"get_scaling":               {"service_id": d.service},
		"get_target_health":         {"service_id": d.service},
		"get_service_timeline":      {"service_id": d.service, "range": "24h"},
		"list_service_operations":   {"service_id": d.service},
		"get_service_metrics":       {"service_id": d.service},
		"get_service_offhours":      {"service_id": d.service},
		"get_desired_count":         {"service_id": d.service},
		"get_arm64_readiness":       {"service_id": d.service},
		"get_timeline_task":         {"service_id": d.service, "task_id": d.task},
		"list_alerts":               nil,
		"get_service_logs":          {"service_id": d.service},
		"list_log_search_services":  nil,
		"estimate_log_search":       {"service_ids": []string{d.service}, "range": "1h"},
		"search_logs":               {"service_ids": []string{d.service}, "range": "1h", "query": logSearchQuery},
		"list_security":             nil,
		"get_service_security":      {"service_id": securityPaymentsKey.ID()},
		"get_image_security":        {"digest": d.digest},
		"get_cost_analytics":        nil,
		"get_capacity_analytics":    nil,
		"get_reliability_analytics": nil,
		"get_delivery_analytics":    nil,
		"get_operations_analytics":  nil,
		"list_networks":             nil,
		"list_region_vpcs":          {"account": sandboxAccountID, "region": "us-east-1"},
		"get_vpc_tree":              with(map[string]any{"vpc_id": d.vpc}),
		"list_storage":              nil,
		"search_config":             {"q": "web"},
	}
	tools := mcpgw.Tools()
	if len(args) != len(tools) {
		t.Fatalf("the argument table has %d entries for %d tools", len(args), len(tools))
	}
	for _, tool := range tools {
		t.Run(tool.Name, func(t *testing.T) {
			toolArgs, ok := args[tool.Name]
			if !ok {
				t.Fatalf("no arguments for %s", tool.Name)
			}
			status, isError, text := c.call(tool.Name, toolArgs)
			if status != http.StatusOK || isError {
				t.Fatalf("HTTP %d isError=%v: %s", status, isError, text)
			}
			if !json.Valid([]byte(text)) {
				t.Errorf("the text is not valid JSON: %.200s", text)
			}
		})
	}
}

func TestMCP_WhoamiIsTheTokensUser(t *testing.T) {
	env := newMCPEnv(t)
	secret, row := env.mintToken(t, "omer")
	var who WhoAmIDTO
	if err := json.Unmarshal([]byte(env.client(t, secret).mustCall("whoami", nil)), &who); err != nil {
		t.Fatal(err)
	}
	if who.User.Username != "omer" || who.User.Admin || who.Token.PublicID != row.PublicID || who.Token.Scope != "read" {
		t.Errorf("whoami = %+v", who)
	}
}

func TestMCP_VisibilityIsTheUsers(t *testing.T) {
	env := newMCPEnv(t)
	listIDs := func(raw string) []string {
		var out struct {
			Items []struct {
				ID string `json:"id"`
			} `json:"items"`
		}
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			t.Fatalf("decode %.200s: %v", raw, err)
		}
		ids := make([]string, 0, len(out.Items))
		for _, item := range out.Items {
			ids = append(ids, item.ID)
		}
		sort.Strings(ids)
		return ids
	}

	secret, _ := env.mintToken(t, "omer")
	viaGateway := listIDs(env.client(t, secret).mustCall("list_services", nil))

	resp := env.omer.get("/api/services")
	var sessionBody bytes.Buffer
	if _, err := io.Copy(&sessionBody, resp.Body); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	viaSession := listIDs(sessionBody.String())

	if len(viaSession) == 0 {
		t.Fatal("omer's session sees no services; the fixture is wrong")
	}
	if !slices.Equal(viaGateway, viaSession) {
		t.Errorf("gateway services %v differ from the session's %v", viaGateway, viaSession)
	}

	// The estate is wider than omer's view, so the equality above is not vacuous.
	samSecret, _ := env.mintToken(t, "sam")
	admin := listIDs(env.client(t, samSecret).mustCall("list_services", nil))
	if len(admin) <= len(viaGateway) {
		t.Errorf("admin sees %d services, omer %d: omer's view is not narrower", len(admin), len(viaGateway))
	}
	for _, id := range viaGateway {
		if !slices.Contains(admin, id) {
			t.Errorf("omer sees %s, which the admin does not", id)
		}
	}

	// A service omer cannot see is not found, whatever the tool.
	hidden := paymentsID()
	if slices.Contains(viaGateway, hidden) {
		t.Fatalf("fixture: omer can see %s", hidden)
	}
	c := env.client(t, secret)
	for _, tool := range []string{"get_service", "get_task_definition", "get_scaling", "list_service_tasks"} {
		status, isError, text := c.call(tool, map[string]any{"service_id": hidden})
		if status != http.StatusOK || !isError {
			t.Errorf("%s on a hidden service: HTTP %d isError=%v %.120s, want a tool error", tool, status, isError, text)
		}
	}
	// Narrowing by a tag omer cannot see never widens the answer.
	narrowed := listIDs(c.mustCall("list_services", map[string]any{"tag": []string{"999999"}}))
	for _, id := range narrowed {
		if !slices.Contains(viaGateway, id) {
			t.Errorf("a tag filter revealed %s", id)
		}
	}
}

func TestMCP_RevokedTokenStopsOnTheNextCall(t *testing.T) {
	env := newMCPEnv(t)
	secret, row := env.mintToken(t, "omer")
	c := env.client(t, secret)
	c.mustCall("get_overview", nil)

	if err := env.st.DeleteAPIToken(t.Context(), row.ID, row.UserID); err != nil {
		t.Fatal(err)
	}
	status, _, _ := c.call("get_overview", nil)
	if status != http.StatusUnauthorized {
		t.Errorf("tool call after revocation: HTTP %d, want 401", status)
	}
	resp := c.rpc("tools/list", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("WWW-Authenticate") == "" {
		t.Errorf("tools/list after revocation: HTTP %d WWW-Authenticate %q, want 401 with the header", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}
}

func TestMCP_ExpiredAndUnknownTokensAre401(t *testing.T) {
	env := newMCPEnv(t)
	// Well-formed but never issued.
	raw, _, err := apitoken.Generate(apitoken.KindPAT)
	if err != nil {
		t.Fatal(err)
	}
	if status, _, _ := env.client(t, raw).call("get_overview", nil); status != http.StatusUnauthorized {
		t.Errorf("unknown token: HTTP %d, want 401", status)
	}
	// Right id, wrong secret.
	good, _ := env.mintToken(t, "omer")
	parsed, ok := apitoken.Parse(good)
	if !ok {
		t.Fatal("minted token does not parse")
	}
	forged := "hmp_" + parsed.ID + "_" + strings.Repeat("A", len(parsed.Secret))
	if status, _, _ := env.client(t, forged).call("get_overview", nil); status != http.StatusUnauthorized {
		t.Errorf("wrong secret: HTTP %d, want 401", status)
	}
}

// A hostile argument reaches the real API as one segment of the declared route and finds nothing.
func TestMCP_HostileArgumentsCannotReachOtherRoutes(t *testing.T) {
	env := newMCPEnv(t)
	secret, _ := env.mintToken(t, "sam")
	c := env.client(t, secret)
	for _, id := range []string{"../users", "../tokens", "x/../../users", "..%2fusers", "a?b=c", "%2e%2e/users"} {
		for _, tool := range []string{"get_service", "get_cluster"} {
			arg := "service_id"
			if tool == "get_cluster" {
				arg = "cluster_id"
			}
			status, isError, text := c.call(tool, map[string]any{arg: id})
			if status != http.StatusOK || !isError {
				t.Errorf("%s %q: HTTP %d isError=%v %.120s, want a not-found tool error", tool, id, status, isError, text)
			}
			if strings.Contains(text, `"username"`) {
				t.Errorf("%s %q returned user data: %.200s", tool, id, text)
			}
		}
	}
}

func TestMCP_LogSearchSpendsTheTokenBudget(t *testing.T) {
	env := newMCPEnv(t)
	secret, _ := env.mintToken(t, "sam")
	c := env.client(t, secret)
	before := whoamiBudget(t, c)
	c.mustCall("search_logs", map[string]any{"service_ids": []string{webFrontendID()}, "range": "1h", "query": logSearchQuery})
	after := whoamiBudget(t, c)
	if after.DailyBytes != before.DailyBytes || after.DailyBytes == 0 {
		t.Errorf("budget changed shape: before %+v after %+v", before, after)
	}
	if after.UsedBytes <= before.UsedBytes {
		t.Errorf("a token log search did not spend budget: used %d before, %d after", before.UsedBytes, after.UsedBytes)
	}
}

func whoamiBudget(t *testing.T, c *gatewayClient) WhoAmILogSearchDTO {
	t.Helper()
	var who WhoAmIDTO
	if err := json.Unmarshal([]byte(c.mustCall("whoami", nil)), &who); err != nil {
		t.Fatal(err)
	}
	return who.LogSearch
}

// The gateway never forwards the answer it got to its own log, even against the real API.
func TestMCP_GatewayLogHoldsNeitherTokenNorResult(t *testing.T) {
	env := newMCPEnv(t)
	secret, row := env.mintToken(t, "sam")
	c := env.client(t, secret)
	text := c.mustCall("get_service", map[string]any{"service_id": webFrontendID()})
	if !strings.Contains(text, "web-frontend") {
		t.Fatalf("unexpected body %.200s", text)
	}
	log := env.logs.String()
	parsed, _ := apitoken.Parse(secret)
	for _, forbidden := range []string{secret, parsed.Secret, "web-frontend"} {
		if strings.Contains(log, forbidden) {
			t.Errorf("gateway log contains %q: %s", forbidden, log)
		}
	}
	if !strings.Contains(log, row.PublicID) {
		t.Errorf("gateway log lacks the display id %s: %s", row.PublicID, log)
	}
}

// oauthThroughGateway is a connector's side of the flow: a signed-in user approves in Harbormaster's UI, and the
// token exchange and every MCP call go through the gateway.
type oauthThroughGateway struct {
	*mcpEnv
	oauth *oauthAPIEnv
}

func newOAuthThroughGateway(t *testing.T) *oauthThroughGateway {
	t.Helper()
	docs := newOAuthDocs(t)
	env := newMCPEnvWith(t, docs)
	return &oauthThroughGateway{mcpEnv: env, oauth: &oauthAPIEnv{srv: env.srv, api: env.api, st: env.st, logs: env.logs,
		docs: docs, sam: env.sam, omer: env.omer}}
}

// gatewayToken posts a token request form to the gateway's relay.
func (e *oauthThroughGateway) gatewayToken(t *testing.T, form url.Values) (int, tokenResponse, http.Header) {
	t.Helper()
	resp, err := http.Post(e.gateway.URL+"/oauth/token", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out tokenResponse
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode, out, resp.Header
}

func whoamiOf(t *testing.T, c *gatewayClient) WhoAmIDTO {
	t.Helper()
	var who WhoAmIDTO
	if err := json.Unmarshal([]byte(c.mustCall("whoami", nil)), &who); err != nil {
		t.Fatal(err)
	}
	return who
}

// TestMCP_OAuthEndToEnd: code exchange, whoami, refresh and whoami again, through the real API handler and the gateway.
func TestMCP_OAuthEndToEnd(t *testing.T) {
	env := newOAuthThroughGateway(t)
	code := env.oauth.approve(t, env.omer, env.oauth.request())

	status, first, header := env.gatewayToken(t, env.oauth.codeForm(code))
	if status != http.StatusOK {
		t.Fatalf("code exchange through the gateway: HTTP %d", status)
	}
	if first.TokenType != "Bearer" || first.ExpiresIn != 3600 || first.Scope != "harbormaster:read" ||
		!strings.HasPrefix(first.AccessToken, "hmo_") || !strings.HasPrefix(first.RefreshToken, "hmr_") {
		t.Fatalf("token response = %+v", first)
	}
	if !strings.Contains(header.Get("Cache-Control"), "no-store") {
		t.Errorf("Cache-Control = %q, want no-store", header.Get("Cache-Control"))
	}

	// The access token is omer's, as a connector: the same tools as a personal token.
	who := whoamiOf(t, env.client(t, first.AccessToken))
	if who.User.Username != "omer" || who.User.Admin || who.Token.Kind != "oauth_access" || who.Token.Name != "Claude" ||
		who.Token.Scope != "read" {
		t.Fatalf("whoami = %+v", who)
	}
	env.client(t, first.AccessToken).mustCall("get_overview", nil)

	// Refresh through the gateway: a new pair, and whoami works with the new access token.
	status, second, _ := env.gatewayToken(t, env.oauth.refreshForm(first.RefreshToken))
	if status != http.StatusOK {
		t.Fatalf("refresh through the gateway: HTTP %d", status)
	}
	if second.AccessToken == first.AccessToken || second.RefreshToken == first.RefreshToken {
		t.Fatal("refresh reissued a token")
	}
	again := whoamiOf(t, env.client(t, second.AccessToken))
	if again.User.Username != "omer" || again.Token.PublicID == who.Token.PublicID {
		t.Fatalf("whoami after refresh = %+v", again)
	}
	// The access token it replaced is gone, and a refresh token is no MCP credential.
	if st, _, _ := env.client(t, first.AccessToken).call("whoami", nil); st != http.StatusUnauthorized {
		t.Errorf("the replaced access token: HTTP %d, want 401", st)
	}
	if st, _, _ := env.client(t, second.RefreshToken).call("whoami", nil); st != http.StatusUnauthorized {
		t.Errorf("a refresh token on /mcp: HTTP %d, want 401", st)
	}
	// A code is single-use through the relay too.
	if status, _, _ := env.gatewayToken(t, env.oauth.codeForm(code)); status != http.StatusBadRequest {
		t.Errorf("code replay through the gateway: HTTP %d, want 400", status)
	}
}

func TestMCP_OAuthAccessIsBudgetedAndRevocable(t *testing.T) {
	env := newOAuthThroughGateway(t)
	status, issued, _ := env.gatewayToken(t, env.oauth.codeForm(env.oauth.approve(t, env.sam, env.oauth.request())))
	if status != http.StatusOK {
		t.Fatalf("exchange: HTTP %d", status)
	}
	c := env.client(t, issued.AccessToken)
	before := whoamiBudget(t, c)
	c.mustCall("search_logs", map[string]any{"service_ids": []string{webFrontendID()}, "range": "1h", "query": logSearchQuery})
	after := whoamiBudget(t, c)
	if before.DailyBytes == 0 || after.UsedBytes <= before.UsedBytes {
		t.Errorf("an OAuth token's log search did not spend budget: %+v then %+v", before, after)
	}

	// Revoking the grant from the UI stops the very next call.
	resp := env.sam.get("/api/tokens")
	wantStatus(t, resp, http.StatusOK)
	var list struct {
		Grants []GrantDTO `json:"grants"`
	}
	decode(t, resp, &list)
	if len(list.Grants) != 1 {
		t.Fatalf("grants = %+v", list.Grants)
	}
	wantStatus(t, env.sam.do(http.MethodDelete, "/api/oauth/grants/"+strconv.FormatInt(list.Grants[0].ID, 10), nil, nil), http.StatusNoContent)
	if st, _, _ := c.call("get_overview", nil); st != http.StatusUnauthorized {
		t.Errorf("a call after the grant was revoked: HTTP %d, want 401", st)
	}
}

func TestMCP_OAuthNothingSecretReachesTheGatewayLog(t *testing.T) {
	env := newOAuthThroughGateway(t)
	code := env.oauth.approve(t, env.omer, env.oauth.request())
	_, first, _ := env.gatewayToken(t, env.oauth.codeForm(code))
	_, second, _ := env.gatewayToken(t, env.oauth.refreshForm(first.RefreshToken))
	env.client(t, second.AccessToken).mustCall("whoami", nil)
	env.gatewayToken(t, env.oauth.codeForm(code)) // an error answer
	assertNoSecrets(t, "gateway log", env.logs.String(), code, first.AccessToken, first.RefreshToken,
		second.AccessToken, second.RefreshToken, oauthTestVerifier)
}
