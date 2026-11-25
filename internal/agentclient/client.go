// Package agentclient is Harbormaster's client for the separate agent
// service. It declares its own wire types and imports nothing from
// internal/agent.
package agentclient

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/rcss-labs/harbormaster/internal/agentresult"
)

var (
	// ErrAgentUnavailable: the agent could not be reached, refused the call, or ended a stream early.
	ErrAgentUnavailable = errors.New("the agent is unavailable")
	// ErrAgentBusy: the agent is running its maximum number of concurrent runs.
	ErrAgentBusy = errors.New("the agent is busy")
	// ErrBadRequest: the agent refused the request as malformed (400). Such an error is ErrAgentUnavailable too.
	ErrBadRequest = errors.New("the agent refused the request")
	// ErrUnknownModel: the agent does not offer the requested provider and model.
	ErrUnknownModel = errors.New("the agent does not offer that model")
	// ErrNoPaidCall: it is certain that a failed test call called no model. It comes beside the error that says why.
	ErrNoPaidCall = errors.New("no paid call was made")
)

const (
	catalogueTTL        = 60 * time.Second
	catalogueTimeout    = 10 * time.Second
	testTimeout         = 60 * time.Second
	maxLineBytes        = 1 << 20
	maxResponseBytes    = 1 << 20
	microUSDPerMTokUnit = 1_000_000
)

// Message is one chat turn sent to the agent.
type Message struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

// Page is the screen the user is looking at.
type Page struct {
	Path      string `json:"path"`
	ServiceID string `json:"service_id"`
	ClusterID string `json:"cluster_id"`
}

// Task is the investigation an investigator run works on. It mirrors the agent service's wire type.
type Task struct {
	Kind    string      `json:"kind"`
	Subject TaskSubject `json:"subject,omitzero"`
	Alert   TaskAlert   `json:"alert,omitzero"`
	Review  *TaskReview `json:"review,omitempty"`
	Scope   *TaskScope  `json:"scope,omitempty"`
}

// TaskReview is what a review task asks for.
type TaskReview struct {
	Type   string `json:"type"`
	Range  string `json:"range"`
	Period string `json:"period"`
}

// TaskScope is the set of services a review covers.
type TaskScope struct {
	Entries       []ScopeEntry `json:"entries"`
	ServicesTotal int          `json:"services_total"`
}

// ScopeEntry is one tag, cluster or account a review covers.
type ScopeEntry struct {
	Kind      string `json:"kind"`
	TagID     string `json:"tag_id,omitempty"`
	Name      string `json:"name,omitempty"`
	ClusterID string `json:"cluster_id,omitempty"`
	AccountID string `json:"account_id,omitempty"`
	Region    string `json:"region,omitempty"`
}

// TaskSubject is what an investigation is about.
type TaskSubject struct {
	Kind       string   `json:"kind"`
	ServiceIDs []string `json:"service_ids"`
	ClusterID  string   `json:"cluster_id"`
	AccountID  string   `json:"account_id"`
	Region     string   `json:"region"`
}

// TaskAlert is the alert that started an investigation.
type TaskAlert struct {
	Kind     string    `json:"kind"`
	Title    string    `json:"title"`
	Text     string    `json:"text"`
	OpenedAt time.Time `json:"opened_at"`
}

// RunRequest is the body of POST /v1/runs.
type RunRequest struct {
	RunID           uuid.UUID `json:"run_id"`
	Role            string    `json:"role"`
	Provider        string    `json:"provider"`
	Model           string    `json:"model"`
	Messages        []Message `json:"messages"`
	Page            Page      `json:"page"`
	MaxCostMicroUSD int64     `json:"max_cost_micro_usd"`
	Task            *Task     `json:"task,omitempty"`
}

// Event is one NDJSON line of a run stream. Types: text, tool_start,
// tool_end, usage, notice, result, done, error. A result event carries its
// fields flat on the line; Run decodes them into Result.
type Event struct {
	Type         string              `json:"type"`
	Delta        string              `json:"delta,omitempty"`
	Tool         string              `json:"tool,omitempty"`
	OK           bool                `json:"ok,omitempty"`
	InputTokens  int64               `json:"input_tokens,omitempty"`
	OutputTokens int64               `json:"output_tokens,omitempty"`
	CostMicroUSD int64               `json:"cost_micro_usd,omitempty"`
	Provisional  bool                `json:"provisional,omitempty"`
	Notice       string              `json:"notice,omitempty"`
	Code         string              `json:"code,omitempty"`
	Message      string              `json:"message,omitempty"`
	Result       *agentresult.Result `json:"-"`
}

// Model is one model the agent offers, with its prices in micro-dollars per million tokens.
type Model struct {
	ID                    string
	InputMicroUSDPerMTok  int64
	OutputMicroUSDPerMTok int64
	ContextTokens         int64
}

// MaxRunCostMicroUSD is the most a run of maxRunTokens can cost: every token
// at the higher of the two prices. It repeats provider.Model's rule on
// purpose, so this package need not import internal/agent.
func (m Model) MaxRunCostMicroUSD(maxRunTokens int64) int64 {
	price := max(m.InputMicroUSDPerMTok, m.OutputMicroUSDPerMTok)
	return maxRunTokens * price / microUSDPerMTokUnit
}

// ProviderModels lists the models of one provider.
type ProviderModels struct {
	Provider string
	Models   []Model
}

// Catalogue is the agent's offer and run limits.
type Catalogue struct {
	Providers    []ProviderModels
	MaxRunTokens int64
	RunTimeout   time.Duration
	Roles        []string
	ReviewTypes  []string
}

// Supports reports whether the agent offers role.
// An agent that lists no roles is a phase 1 agent: it offers chat and test only.
func (c Catalogue) Supports(role string) bool {
	if c.Roles == nil {
		return role == "chat" || role == "test"
	}
	return slices.Contains(c.Roles, role)
}

// SupportsReview reports whether the agent offers the reviewer role and the review type.
func (c Catalogue) SupportsReview(reviewType string) bool {
	return c.Supports("reviewer") && slices.Contains(c.ReviewTypes, reviewType)
}

// Find returns the model the agent offers under a provider and model id.
func (c Catalogue) Find(provider, id string) (Model, bool) {
	for _, p := range c.Providers {
		if p.Provider != provider {
			continue
		}
		for _, m := range p.Models {
			if m.ID == id {
				return m, true
			}
		}
	}
	return Model{}, false
}

// TestResult is the answer to a "Test model" call.
type TestResult struct {
	OK           bool   `json:"ok"`
	Kind         string `json:"kind"`
	Message      string `json:"message"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	CostMicroUSD int64  `json:"cost_micro_usd"`
	// UsageKnown is false when the answer did not state a cost, or not a possible one.
	UsageKnown bool `json:"-"`
}

// Client talks to one agent base URL and never follows a redirect.
type Client struct {
	base   *url.URL
	client *http.Client
	now    func() time.Time

	// catalogueTimeout and testTimeout bound those calls; fields so a test can shorten them.
	catalogueTimeout, testTimeout time.Duration

	mu        sync.Mutex
	cached    Catalogue
	cachedAt  time.Time
	haveCache bool
}

// New returns the client for baseURL (scheme and host only). caPEM adds root
// certificates to the system pool for this client only; empty means the
// system pool alone.
func New(baseURL string, caPEM string) (*Client, error) {
	base, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("agent base URL: %w", err)
	}
	if (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" {
		return nil, fmt.Errorf("agent base URL must be an absolute http(s) URL, got %q", baseURL)
	}
	if (base.Path != "" && base.Path != "/") || base.RawQuery != "" || base.Fragment != "" || base.User != nil {
		return nil, fmt.Errorf("agent base URL must be scheme://host[:port] only, got %q", baseURL)
	}

	// No proxy from the environment: the request goes straight to the configured host.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	if caPEM != "" {
		roots, err := x509.SystemCertPool()
		if err != nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM([]byte(caPEM)) {
			return nil, errors.New("agent CA PEM holds no certificate")
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	}
	return &Client{
		base: &url.URL{Scheme: base.Scheme, Host: base.Host},
		// No overall timeout: a run stream lasts minutes and the context governs it.
		client: &http.Client{
			Transport:     transport,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		now:              time.Now,
		catalogueTimeout: catalogueTimeout,
		testTimeout:      testTimeout,
	}, nil
}

func (c *Client) newRequest(ctx context.Context, method, path, bearer string, body any) (*http.Request, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(raw)
	}
	u := url.URL{Scheme: c.base.Scheme, Host: c.base.Host, Path: path}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	return req, nil
}

// do sends req and maps a transport failure or any non-200 answer to a
// sentinel error; a 404 means an unknown model only for a run. On success the
// caller closes the body.
func (c *Client) do(req *http.Request, notFoundIsUnknownModel bool) (*http.Response, error) {
	resp, err := c.client.Do(req)
	if err != nil {
		if ctxErr := req.Context().Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("%w: %v", ErrAgentUnavailable, err)
	}
	if resp.StatusCode == http.StatusOK {
		return resp, nil
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
	switch resp.StatusCode {
	case http.StatusTooManyRequests:
		return nil, ErrAgentBusy
	case http.StatusNotFound:
		if notFoundIsUnknownModel {
			return nil, ErrUnknownModel
		}
		return nil, &statusError{resp.StatusCode}
	default:
		return nil, &statusError{resp.StatusCode}
	}
}

// statusError is an answer that says the agent did not do what was asked.
type statusError struct{ code int }

func (e *statusError) Error() string {
	return fmt.Sprintf("%v: status %d", ErrAgentUnavailable, e.code)
}
func (e *statusError) Unwrap() error { return ErrAgentUnavailable }
func (e *statusError) Is(target error) bool {
	return target == ErrBadRequest && e.code == http.StatusBadRequest
}

type catalogueWire struct {
	Providers []struct {
		Provider string `json:"provider"`
		Models   []struct {
			ID            string `json:"id"`
			InputPerMTok  int64  `json:"input_per_mtok_micro_usd"`
			OutputPerMTok int64  `json:"output_per_mtok_micro_usd"`
			ContextTokens int64  `json:"context_tokens"`
		} `json:"models"`
	} `json:"providers"`
	Roles       []string `json:"roles"`
	ReviewTypes []string `json:"review_types"`
	Limits      struct {
		MaxRunTokens      int64 `json:"max_run_tokens"`
		RunTimeoutSeconds int64 `json:"run_timeout_seconds"`
	} `json:"limits"`
}

// ownTimeout turns the expiry of a context the client derived itself into
// ErrAgentUnavailable; the caller's own cancellation or deadline passes through.
func ownTimeout(parent context.Context, err error) error {
	if err != nil && errors.Is(err, context.DeadlineExceeded) && parent.Err() == nil {
		return fmt.Errorf("%w: timed out", ErrAgentUnavailable)
	}
	return err
}

// Catalogue returns the agent's models and limits, cached for 60 seconds. The
// cache lock is not held during the network call.
func (c *Client) Catalogue(ctx context.Context) (Catalogue, error) {
	c.mu.Lock()
	if c.haveCache && c.now().Sub(c.cachedAt) < catalogueTTL {
		cached := c.cached
		c.mu.Unlock()
		return cached, nil
	}
	c.mu.Unlock()
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, c.catalogueTimeout)
	defer cancel()
	req, err := c.newRequest(ctx, http.MethodGet, "/v1/catalogue", "", nil)
	if err != nil {
		return Catalogue{}, err
	}
	resp, err := c.do(req, false)
	if err != nil {
		return Catalogue{}, ownTimeout(parent, err)
	}
	defer resp.Body.Close()
	var wire catalogueWire
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&wire); err != nil {
		return Catalogue{}, ownTimeout(parent, fmt.Errorf("%w: malformed catalogue", ErrAgentUnavailable))
	}
	cat := Catalogue{
		MaxRunTokens: wire.Limits.MaxRunTokens,
		RunTimeout:   time.Duration(wire.Limits.RunTimeoutSeconds) * time.Second,
		Roles:        wire.Roles,
		ReviewTypes:  wire.ReviewTypes,
	}
	for _, p := range wire.Providers {
		pm := ProviderModels{Provider: p.Provider}
		for _, m := range p.Models {
			pm.Models = append(pm.Models, Model{
				ID:                    m.ID,
				InputMicroUSDPerMTok:  m.InputPerMTok,
				OutputMicroUSDPerMTok: m.OutputPerMTok,
				ContextTokens:         m.ContextTokens,
			})
		}
		cat.Providers = append(cat.Providers, pm)
	}
	c.mu.Lock()
	c.cached, c.cachedAt, c.haveCache = cat, c.now(), true
	c.mu.Unlock()
	return cat, nil
}

// Run starts a run and calls onEvent for each event, in order, until the
// stream ends. It returns nil after a done or error event; a stream that ends
// without either returns ErrAgentUnavailable. Cancelling ctx closes the
// connection and returns the context's error.
func (c *Client) Run(ctx context.Context, bearer string, req RunRequest, onEvent func(Event)) error {
	httpReq, err := c.newRequest(ctx, http.MethodPost, "/v1/runs", bearer, req)
	if err != nil {
		return err
	}
	resp, err := c.do(httpReq, true)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64<<10), maxLineBytes)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			return fmt.Errorf("%w: malformed event", ErrAgentUnavailable)
		}
		if ev.Type == "result" {
			var res agentresult.Result
			if err := json.Unmarshal(line, &res); err != nil {
				return fmt.Errorf("%w: malformed result", ErrAgentUnavailable)
			}
			ev.Result = &res
		}
		onEvent(ev)
		if ev.Type == "done" || ev.Type == "error" {
			return nil
		}
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("%w: stream read failed", ErrAgentUnavailable)
	}
	return fmt.Errorf("%w: stream ended early", ErrAgentUnavailable)
}

// Test asks the agent to make one small call to provider and model. An error that is not ErrNoPaidCall may have been
// paid for: the request was on its way, and the agent did not refuse it before calling the model.
func (c *Client) Test(ctx context.Context, bearer, provider, model string) (TestResult, error) {
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, c.testTimeout)
	defer cancel()
	var connected atomic.Bool // whether the request may have left; a retry on a new connection starts over
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GetConn: func(string) { connected.Store(false) },
		GotConn: func(httptrace.GotConnInfo) { connected.Store(true) },
	})
	body := map[string]string{"provider": provider, "model": model}
	req, err := c.newRequest(ctx, http.MethodPost, "/v1/test", bearer, body)
	if err != nil {
		return TestResult{}, err
	}
	resp, err := c.do(req, false)
	if err != nil {
		err = ownTimeout(parent, err)
		var status *statusError
		refused := errors.Is(err, ErrAgentBusy) ||
			(errors.As(err, &status) && (status.code == http.StatusUnauthorized || status.code == http.StatusNotFound))
		if refused || !connected.Load() {
			err = fmt.Errorf("%w: %w", ErrNoPaidCall, err)
		}
		return TestResult{}, err
	}
	defer resp.Body.Close()
	var answer struct {
		TestResult
		CostMicroUSD *int64 `json:"cost_micro_usd"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&answer); err != nil {
		return TestResult{}, fmt.Errorf("%w: malformed test answer", ErrAgentUnavailable)
	}
	res := answer.TestResult
	if answer.CostMicroUSD != nil {
		res.CostMicroUSD = *answer.CostMicroUSD
		res.UsageKnown = res.CostMicroUSD >= 0 && res.InputTokens >= 0 && res.OutputTokens >= 0
	}
	return res, nil
}
