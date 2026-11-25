package api

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"regexp"
	"runtime/debug"
	"strconv"

	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// statusRecorder captures the response status for the request log. It forwards Flush and Hijack so
// streaming and WebSocket routes keep working, and exposes Unwrap for http.ResponseController.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(p []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(p)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return http.NewResponseController(s.ResponseWriter).Hijack()
}

// recoverMiddleware turns a handler panic into a logged 500.
func (s *Server) recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				if rec == http.ErrAbortHandler {
					panic(rec)
				}
				s.log().Error("panic in handler", "panic", rec, "path", r.URL.Path, "stack", string(debug.Stack()))
				writeError(w, http.StatusInternalServerError, "internal", "Something went wrong on our side.")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// requestLogInfo carries what inner middleware learns about a request back out to the request log, which runs before
// authentication. It holds only values that are safe to log.
type requestLogInfo struct {
	tokenID string // the token's public id (hmp_<id>) when an access token authenticated the request
}

type requestLogKey struct{}

func (s *Server) requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := s.now()
		rec := &statusRecorder{ResponseWriter: w}
		info := &requestLogInfo{}
		next.ServeHTTP(rec, r.WithContext(context.WithValue(r.Context(), requestLogKey{}, info)))
		if r.URL.Path == "/healthz" {
			return
		}
		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}
		attrs := []any{"method", r.Method, "path", loggedPath(r.URL.Path), "status", status,
			"duration_ms", s.now().Sub(start).Milliseconds(), "ip", s.clientIP(r)}
		if info.tokenID != "" {
			attrs = append(attrs, "token_id", info.tokenID)
		}
		s.log().Info("request", attrs...)
	})
}

// tokenRequests runs after auth.Middleware. For a request authenticated by an access token it records the token's
// public id for the request log and takes one unit from a token bucket shared across web tasks through Rates: the
// user's own (120 burst, 2 per second), or for the agent identity its separate bucket, so a background run neither
// starves people nor is starved by them. An empty bucket is 429 rate_limited with Retry-After; a user of an unknown
// kind is refused with 403. Session and anonymous requests pass untouched.
func (s *Server) tokenRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cred, ok := auth.CredentialFrom(r.Context())
		if !ok || cred.Kind != auth.CredentialToken {
			next.ServeHTTP(w, r)
			return
		}
		if info, ok := r.Context().Value(requestLogKey{}).(*requestLogInfo); ok {
			info.tokenID = cred.TokenPublicID
		}
		user, _ := auth.UserFrom(r.Context())
		bucket, burst, perSecond, known := tokenBucketFor(user, s.Cfg)
		if !known {
			writeError(w, http.StatusForbidden, "forbidden", "This account cannot use access tokens.")
			return
		}
		if !s.allowRate(r.Context(), bucket, strconv.FormatInt(user.ID, 10), burst, perSecond) {
			w.Header().Set("Retry-After", "1")
			if user.Kind == store.UserKindAgent {
				writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many requests from the AI agent. Wait a moment and try again.")
				return
			}
			writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many requests with access tokens. Wait a moment and try again.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// fakeTelegramPath matches the fake Telegram receiver's path, which carries the bot token like the real Bot API URL.
var fakeTelegramPath = regexp.MustCompile(`^(/fake-notify/telegram/bot)[^/]*(/.*)?$`)

// loggedPath is the path as the request log may show it: a bot token in the fake receiver's path is replaced.
func loggedPath(path string) string {
	return fakeTelegramPath.ReplaceAllString(path, "${1}***${2}")
}

// routeOptions selects the per-route middleware.
type routeOptions struct {
	Public bool // no session required: skips RequireAuth, RequirePasswordChanged and RequireCSRF (login, setup)
	Admin  bool // RequireAdmin
	NoETag bool // do not buffer the response (streaming, WebSocket)
	// Token is the access token scope this route admits. Empty, the default, refuses every access token with 403
	// token_not_allowed. Only the read routes the MCP gateway calls are marked auth.ScopeRead; the list is pinned by
	// a test because it is the security boundary of token access. Never mark an Admin route or a route that changes
	// anything.
	Token auth.Scope
}

// protect wraps h in the per-route chain:
// Public routes: RefuseToken -> handler -> ETag. Others: RequireAuth -> RequireTokenScope -> RequirePasswordChanged -> RequireCSRF -> RequireAdmin -> handler -> ETag (GET only).
func protect(h http.Handler, opts routeOptions) http.Handler {
	if !opts.NoETag {
		h = etagMiddleware(h)
	}
	if opts.Public {
		return auth.RefuseToken(h)
	}
	if opts.Admin {
		h = auth.RequireAdmin(h)
	}
	h = auth.RequireCSRF(h)
	h = auth.RequirePasswordChanged(h)
	h = auth.RequireTokenScope(opts.Token)(h)
	return auth.RequireAuth(h)
}
