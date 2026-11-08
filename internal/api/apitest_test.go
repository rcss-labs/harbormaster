package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/deploy"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

// testPassword is the password of every user created by newTestServer.
const testPassword = "a-good-password-1"

// syncBuffer is a goroutine-safe log sink so tests can read what the server logged (setup token).
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func testConfig() config.Config {
	return config.Config{
		SessionKey:      "test-key",
		SessionIdle:     12 * time.Hour,
		SessionAbsolute: 7 * 24 * time.Hour,
		LoginMaxPerUser: 5,
		LoginMaxPerIP:   30,
		LoginWindow:     15 * time.Minute,
		PageSizeDefault: 100,
		PageSizeMax:     500,
		CookieSecure:    false,
	}
}

// newServerWithLog builds a Server over a fresh schema and the mockup estate. seedUsers creates
// admin "sam" and non-admin "omer" (both with testPassword) before the setup token is generated.
func newServerWithLog(t *testing.T, seedUsers bool) (*httptest.Server, *fake.Fake, *store.Store, *syncBuffer) {
	t.Helper()
	return newServerCustom(t, seedUsers, nil)
}

// newServerCustom is newServerWithLog with a hook that adjusts the Server before New (plan 05: inject
// a failing metrics fetcher).
func newServerCustom(t *testing.T, seedUsers bool, mutate func(*Server)) (*httptest.Server, *fake.Fake, *store.Store, *syncBuffer) {
	t.Helper()
	srv, _, fk, st, logs := newServerCustomAPI(t, seedUsers, mutate)
	return srv, fk, st, logs
}

// newServerCustomAPI is newServerCustom that also returns the *Server the handler runs on, for tests that
// call its methods (BeginDrain).
func newServerCustomAPI(t *testing.T, seedUsers bool, mutate func(*Server)) (*httptest.Server, *Server, *fake.Fake, *store.Store, *syncBuffer) {
	t.Helper()
	st := storetest.Open(t)
	logs := &syncBuffer{}
	log := slog.New(slog.NewTextHandler(logs, nil))
	cfg := testConfig()
	if seedUsers {
		hash, err := auth.HashPassword(testPassword)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.CreateUser(t.Context(), "sam", hash, true, false); err != nil {
			t.Fatal(err)
		}
		if _, err := st.CreateUser(t.Context(), "omer", hash, false, false); err != nil {
			t.Fatal(err)
		}
	}
	setup, err := auth.NewSetup(t.Context(), st, log)
	if err != nil {
		t.Fatal(err)
	}
	fk := fake.New(fake.MockupEstate(time.Now))
	server := Server{
		Store: st, Auth: auth.NewService(st, cfg), Setup: setup,
		Clients: fk, Prober: fk, Cfg: cfg, Log: log, Now: time.Now,
		Deploy: &deploy.Service{Store: st, Clients: fk, Scaling: st, MaxDesiredCount: 50, Now: time.Now},
	}
	if mutate != nil {
		mutate(&server)
	}
	api := New(server)
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)
	return srv, api, fk, st, logs
}

// newTestServer returns a running API over the mockup estate with users sam (admin) and omer.
func newTestServer(t *testing.T) (*httptest.Server, *fake.Fake, *store.Store) {
	srv, fk, st, _ := newServerWithLog(t, true)
	return srv, fk, st
}

// apiClient is an HTTP client with a cookie jar that adds X-CSRF-Token to every non-GET request.
type apiClient struct {
	t    *testing.T
	http *http.Client
	base string
	csrf string
}

func newAPIClient(t *testing.T, srv *httptest.Server) *apiClient {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &apiClient{t: t, http: &http.Client{Jar: jar}, base: srv.URL}
}

// login signs in and returns a client carrying the session cookie and CSRF token.
func login(t *testing.T, srv *httptest.Server, username, password string) *apiClient {
	t.Helper()
	c := newAPIClient(t, srv)
	resp := c.do(http.MethodPost, "/api/auth/login", map[string]string{"username": username, "password": password}, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("login %s: %d %s", username, resp.StatusCode, b)
	}
	var out struct {
		User struct {
			CSRFToken string `json:"csrf_token"`
		} `json:"user"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	c.csrf = out.User.CSRFToken
	return c
}

// do sends a JSON request; extra headers override defaults (use "X-CSRF-Token": "" to omit the token).
func (c *apiClient) do(method, path string, body any, headers map[string]string) *http.Response {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			c.t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.base+path, rd)
	if err != nil {
		c.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet && c.csrf != "" {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	for k, v := range headers {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	return resp
}

func (c *apiClient) get(path string) *http.Response { return c.do(http.MethodGet, path, nil, nil) }

func (c *apiClient) post(path string, body any) *http.Response {
	return c.do(http.MethodPost, path, body, nil)
}

// decode reads and closes resp, decoding the JSON body into v.
func decode(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("decode body (status %d): %v", resp.StatusCode, err)
	}
}

// errorCode decodes the {"error":{"code","message"}} body.
func errorCode(t *testing.T, resp *http.Response) (code, message string) {
	t.Helper()
	var out struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	decode(t, resp, &out)
	return out.Error.Code, out.Error.Message
}

var setupTokenPattern = regexp.MustCompile(`setup token (\S+)`)

// setupTokenFrom extracts the one-time setup token from the server log.
func setupTokenFrom(t *testing.T, logs *syncBuffer) string {
	t.Helper()
	m := setupTokenPattern.FindStringSubmatch(logs.String())
	if m == nil {
		t.Fatalf("no setup token in log: %q", logs.String())
	}
	return strings.Trim(m[1], `"`)
}
