package mcpgw

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/rcss-labs/harbormaster/internal/apitoken"
)

// maxRequestBodyBytes bounds one MCP request body.
const maxRequestBodyBytes = 256 << 10

// wwwAuthenticate is sent with every 401 while OAuth is off.
const wwwAuthenticate = `Bearer realm="harbormaster-mcp"`

// serverInstructions is returned at initialize.
const serverInstructions = "This server is read-only: its tools can look at the ECS estate in Harbormaster but cannot change anything. " +
	"Every answer is what the signed-in Harbormaster user is allowed to see, nothing more. " +
	"Data comes from Harbormaster's last poll of AWS and carries polled_at, the time it was read."

// whoamiRoute validates a token for every MCP method other than tools/call.
var whoamiRoute = Route{Method: http.MethodGet, Pattern: "/api/tokens/current"}

// failureLimiter counts upstream 401s per client IP; a blocked IP gets 429
// without an upstream call. Malformed tokens never count: they cost
// Harbormaster nothing.
type failureLimiter interface {
	Blocked(ip string) bool
	Failed(ip string)
	RetryAfter(ip string) time.Duration
}

// requestState carries one HTTP request's identity into the SDK's tool
// handlers (through the request context) and the tool handler's outcome back.
type requestState struct {
	bearer       string
	forwardedFor string

	mu             sync.Mutex
	unauthorized   bool
	tool           string
	upstreamStatus int
}

type requestStateKey struct{}

func (s *requestState) record(tool string, status int, unauthorized bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tool = tool
	s.upstreamStatus = status
	s.unauthorized = s.unauthorized || unauthorized
}

func (s *requestState) snapshot() (tool string, status int, unauthorized bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tool, s.upstreamStatus, s.unauthorized
}

type gateway struct {
	cfg     Config
	up      *Upstream
	log     *slog.Logger
	limiter failureLimiter
	mcp     http.Handler
}

// NewHandler serves POST /mcp (stateless Streamable HTTP, JSON responses),
// GET /healthz (no upstream call) and 404 for anything else.
func NewHandler(cfg Config, up *Upstream, log *slog.Logger) http.Handler {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	up.maxBodyBytes = int64(upstreamReadCap(cfg.MaxResultBytes))
	g := &gateway{cfg: cfg, up: up, log: log, limiter: newFailureLimiter(failureLimit, failureWindow, time.Now)}

	server := mcp.NewServer(
		&mcp.Implementation{Name: "harbormaster", Title: "Harbormaster", Version: Version},
		// No SDK logger: the gateway logs only what it decides to (never a token, body or result).
		// Tools are the only capability (no logging, and the tool list never changes at runtime).
		&mcp.ServerOptions{
			Instructions: serverInstructions,
			Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{ListChanged: false}},
		},
	)
	for _, tool := range Tools() {
		server.AddTool(sdkTool(tool), g.toolHandler(tool))
	}
	g.mcp = mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, MaxRequestBodyBytes: maxRequestBodyBytes},
	)
	return g
}

func sdkTool(t Tool) *mcp.Tool {
	openWorld := false
	return &mcp.Tool{
		Name:        t.Name,
		Title:       t.Title,
		Description: t.Description,
		InputSchema: t.InputSchema(),
		Annotations: &mcp.ToolAnnotations{Title: t.Title, ReadOnlyHint: true, OpenWorldHint: &openWorld},
	}
}

func (g *gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/healthz":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ok")
	case "/mcp":
		g.serveMCP(w, r)
	case "/oauth/token":
		g.serveOAuthToken(w, r)
	case "/oauth/authorize":
		g.serveOAuthAuthorize(w, r)
	case protectedResourcePath, protectedResourceMCPPath, authorizationServerPath:
		g.serveOAuthMetadata(w, r)
	default:
		http.NotFound(w, r)
	}
}

// serveMCP runs the design's request path: origin, size, token shape, failure
// limiter, token check for non-call methods, then the SDK, whose answer is
// replaced by HTTP 401 when a tool call's upstream answered 401.
func (g *gateway) serveMCP(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	ip := clientIP(r, g.cfg.TrustedProxy)

	if origin := r.Header.Get("Origin"); origin != "" && !slices.Contains(g.cfg.AllowedOrigins, origin) {
		writeJSONError(w, http.StatusForbidden, "origin_not_allowed")
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.ContentLength > maxRequestBodyBytes {
		writeJSONError(w, http.StatusRequestEntityTooLarge, "request_too_large")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBodyBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSONError(w, http.StatusRequestEntityTooLarge, "request_too_large")
			return
		}
		writeJSONError(w, http.StatusBadRequest, "unreadable_request")
		return
	}

	bearer, ok := bearerToken(r.Header.Get("Authorization"))
	var parsed apitoken.Parsed
	if ok {
		parsed, ok = apitoken.Parse(bearer)
	}
	if !ok {
		// A malformed token costs Harbormaster nothing: no upstream call, and it does not count as a failure.
		g.writeUnauthorized(w)
		g.logRequest("", "", 0, started, ip, "", http.StatusUnauthorized)
		return
	}
	displayID := apitoken.DisplayID(parsed)

	if g.limiter.Blocked(ip) {
		g.writeBlocked(w, ip)
		g.logRequest("", "", 0, started, ip, displayID, http.StatusTooManyRequests)
		return
	}

	state := &requestState{bearer: bearer, forwardedFor: forwardedFor(r, g.cfg.TrustedProxy)}
	method, err := jsonRPCMethod(body)
	if err != nil {
		// Batches and ambiguous keys are refused before any upstream call.
		writeJSONRPCError(w, http.StatusBadRequest, err.Error())
		g.logRequest("", "", 0, started, ip, displayID, http.StatusBadRequest)
		return
	}
	if method != "tools/call" {
		// One token check per non-call method, so a bad token is a real HTTP 401 at connect time.
		resp, err := g.up.Do(r.Context(), state.bearer, state.forwardedFor, Call{Route: whoamiRoute})
		switch {
		case err != nil:
			g.log.Warn("token check failed", "client_ip", ip, "token", displayID, "error", describeTransportError(err))
			writeJSONError(w, http.StatusBadGateway, "upstream_unavailable")
			g.logRequest(method, "", 0, started, ip, displayID, http.StatusBadGateway)
			return
		case resp.Status == http.StatusUnauthorized:
			g.limiter.Failed(ip)
			g.writeUnauthorized(w)
			g.logRequest(method, "", resp.Status, started, ip, displayID, http.StatusUnauthorized)
			return
		case resp.Status == http.StatusTooManyRequests:
			if resp.RetryAfter != "" {
				w.Header().Set("Retry-After", resp.RetryAfter)
			}
			writeJSONError(w, http.StatusTooManyRequests, "rate_limited")
			g.logRequest(method, "", resp.Status, started, ip, displayID, http.StatusTooManyRequests)
			return
		case resp.Status < 200 || resp.Status > 299:
			writeJSONError(w, http.StatusBadGateway, "upstream_unavailable")
			g.logRequest(method, "", resp.Status, started, ip, displayID, http.StatusBadGateway)
			return
		}
		state.record("", resp.Status, false)
	}

	inner := r.Clone(context.WithValue(r.Context(), requestStateKey{}, state))
	inner.Body = io.NopCloser(bytes.NewReader(body))
	inner.ContentLength = int64(len(body))
	buffered := newBufferedResponse()
	g.mcp.ServeHTTP(buffered, inner)

	tool, upstreamStatus, unauthorized := state.snapshot()
	if unauthorized {
		g.limiter.Failed(ip)
		g.writeUnauthorized(w)
		g.logRequest(method, tool, upstreamStatus, started, ip, displayID, http.StatusUnauthorized)
		return
	}
	buffered.copyTo(w)
	g.logRequest(method, tool, upstreamStatus, started, ip, displayID, buffered.status)
}

// toolHandler forwards one tool call to its single upstream route.
func (g *gateway) toolHandler(tool Tool) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		state, ok := ctx.Value(requestStateKey{}).(*requestState)
		if !ok {
			// Only reachable if the SDK stopped carrying request context values; fail closed.
			g.log.Error("tool call without request state", "tool", tool.Name)
			return toolError(unavailableMessage), nil
		}
		args := map[string]any{}
		if req.Params != nil && len(req.Params.Arguments) > 0 {
			decoder := json.NewDecoder(bytes.NewReader(req.Params.Arguments))
			decoder.UseNumber()
			if err := decoder.Decode(&args); err != nil {
				state.record(tool.Name, 0, false)
				return toolError("The arguments must be a JSON object."), nil
			}
		}
		call, err := tool.BuildCall(args)
		if err != nil {
			state.record(tool.Name, 0, false)
			var argErr *ArgumentError
			if errors.As(err, &argErr) {
				return toolError(argErr.Message), nil
			}
			g.log.Error("build call failed", "tool", tool.Name, "error", err)
			return toolError("This tool could not build its request."), nil
		}

		resp, err := g.up.Do(ctx, state.bearer, state.forwardedFor, call)
		if err != nil {
			state.record(tool.Name, 0, false)
			g.log.Warn("upstream call failed", "tool", tool.Name, "error", describeTransportError(err))
			return toolError(unavailableMessage), nil
		}
		state.record(tool.Name, resp.Status, resp.Status == http.StatusUnauthorized)
		switch {
		case resp.Status == http.StatusUnauthorized:
			// Discarded by serveMCP, which answers HTTP 401 instead.
			return toolError("Unauthenticated."), nil
		case resp.Status >= 200 && resp.Status <= 299:
			if len(resp.Body) > g.cfg.MaxResultBytes {
				return tooLargeResult(len(resp.Body)), nil
			}
			return textResult(resp.Body), nil
		default:
			return upstreamErrorResult(resp), nil
		}
	}
}

// logRequest writes the one line per request the design allows: no token,
// header, body or result, only the token's public display id.
func (g *gateway) logRequest(method, tool string, upstreamStatus int, started time.Time, ip, displayID string, status int) {
	attrs := []any{
		"method", method,
		"status", status,
		"duration_ms", time.Since(started).Milliseconds(),
		"client_ip", ip,
		"token", displayID,
	}
	if tool != "" {
		attrs = append(attrs, "tool", tool)
	}
	if upstreamStatus != 0 {
		attrs = append(attrs, "upstream_status", upstreamStatus)
	}
	g.log.Info("mcp request", attrs...)
}

func (g *gateway) writeBlocked(w http.ResponseWriter, ip string) {
	w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds(g.limiter.RetryAfter(ip))))
	writeJSONError(w, http.StatusTooManyRequests, "rate_limited")
}

// describeTransportError drops the request URL from an HTTP client error, so
// tool arguments in the path or query do not reach the log.
func describeTransportError(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if errors.Is(urlErr.Err, context.DeadlineExceeded) {
			return "timeout"
		}
		return urlErr.Err.Error()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return err.Error()
}

// bearerToken extracts the token from an Authorization header.
func bearerToken(header string) (string, bool) {
	scheme, token, found := strings.Cut(strings.TrimSpace(header), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != ""
}

// jsonRPCMethod returns the method of a single JSON-RPC message, or "" for
// anything else (a response or unparsable input), which is then treated as
// "not tools/call" and so gets the token check.
//
// It errors on a body the gateway and the SDK could read differently: a batch
// (removed in MCP 2025-06-18; the SDK would run every call in it), a repeated
// top-level key, or a key that matches "jsonrpc", "id", "method" or "params"
// only ignoring case (encoding/json matches keys case-insensitively, the SDK
// does not). Keys are read case-sensitively here, as the SDK reads them.
func jsonRPCMethod(body []byte) (string, error) {
	if trimmed := bytes.TrimLeft(body, " \t\r\n"); len(trimmed) > 0 && trimmed[0] == '[' {
		return "", errors.New("JSON-RPC batches are not supported")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if open, err := decoder.Token(); err != nil || open != json.Delim('{') {
		return "", nil
	}
	method := ""
	seen := map[string]bool{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return "", nil
		}
		key, _ := keyToken.(string)
		if seen[key] {
			return "", errors.New("duplicate key in the JSON-RPC message")
		}
		seen[key] = true
		for _, reserved := range []string{"jsonrpc", "id", "method", "params"} {
			if key != reserved && strings.EqualFold(key, reserved) {
				return "", errors.New("ambiguous key in the JSON-RPC message")
			}
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return "", nil
		}
		if key == "method" {
			if json.Unmarshal(value, &method) != nil {
				method = ""
			}
		}
	}
	return method, nil
}

// clientIP is the peer address, unless trustedProxy says exactly one trusted
// proxy (the load balancer) appends the real client address, in which case it
// is the rightmost non-empty X-Forwarded-For entry. Same rule as
// internal/api/scope.go (clientIP).
func clientIP(r *http.Request, trustedProxy bool) string {
	if trustedProxy {
		hops := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
		for i := len(hops) - 1; i >= 0; i-- {
			if hop := strings.TrimSpace(hops[i]); hop != "" {
				return hop
			}
		}
	}
	return peerIP(r)
}

func peerIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// forwardedFor is the X-Forwarded-For sent upstream: exactly one entry. Behind
// the trusted public load balancer it is the rightmost incoming entry, the
// client address that load balancer appended; otherwise it is the direct peer.
// Client-supplied entries are never forwarded: Harbormaster's internal load
// balancer appends this gateway's address, and auth.ClientIP reads one hop
// past it, so any extra entry here would be taken for the client.
func forwardedFor(r *http.Request, trustedProxy bool) string {
	if trustedProxy {
		return clientIP(r, true)
	}
	return peerIP(r)
}

// writeUnauthorized answers 401. With OAuth on, the challenge points at the
// protected resource metadata so a connector can discover where to sign in.
func (g *gateway) writeUnauthorized(w http.ResponseWriter) {
	challenge := wwwAuthenticate
	if g.cfg.OAuthEnabled() {
		challenge = oauthChallenge(g.cfg)
	}
	w.Header().Set("WWW-Authenticate", challenge)
	writeJSONError(w, http.StatusUnauthorized, "unauthenticated")
}

func writeJSONError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, `{"error":`+strconv.Quote(code)+`}`)
}

// writeJSONRPCError answers with a JSON-RPC 2.0 error object (id null).
func writeJSONRPCError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":null,"error":{"code":-32600,"message":`+strconv.Quote(message)+`}}`)
}

// bufferedResponse holds the SDK's answer until serveMCP knows whether a tool
// call's upstream answered 401. Responses are single JSON bodies (JSONResponse
// mode), bounded by the result cap.
type bufferedResponse struct {
	header      http.Header
	status      int
	wroteHeader bool
	body        bytes.Buffer
}

func newBufferedResponse() *bufferedResponse {
	return &bufferedResponse{header: http.Header{}, status: http.StatusOK}
}

func (b *bufferedResponse) Header() http.Header { return b.header }

func (b *bufferedResponse) WriteHeader(status int) {
	if !b.wroteHeader {
		b.status, b.wroteHeader = status, true
	}
}

func (b *bufferedResponse) Write(p []byte) (int, error) {
	b.wroteHeader = true
	return b.body.Write(p)
}

// Flush is a no-op: nothing reaches the client until copyTo.
func (b *bufferedResponse) Flush() {}

func (b *bufferedResponse) copyTo(w http.ResponseWriter) {
	for name, values := range b.header {
		w.Header()[name] = values
	}
	w.WriteHeader(b.status)
	_, _ = w.Write(b.body.Bytes())
}
