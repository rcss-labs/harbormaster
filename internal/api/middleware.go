package api

import (
	"bufio"
	"net"
	"net/http"
	"runtime/debug"

	"github.com/rcss-labs/harbormaster/internal/auth"
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

func (s *Server) requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := s.now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		if r.URL.Path == "/healthz" {
			return
		}
		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}
		s.log().Info("request", "method", r.Method, "path", r.URL.Path, "status", status,
			"duration_ms", s.now().Sub(start).Milliseconds(), "ip", s.clientIP(r))
	})
}

// routeOptions selects the per-route middleware.
type routeOptions struct {
	Public bool // no session required: skips RequireAuth, RequirePasswordChanged and RequireCSRF (login, setup)
	Admin  bool // RequireAdmin
	NoETag bool // do not buffer the response (streaming, WebSocket)
}

// protect wraps h in the per-route chain:
// RequireAuth -> RequirePasswordChanged -> RequireCSRF -> RequireAdmin -> handler -> ETag (GET only).
func protect(h http.Handler, opts routeOptions) http.Handler {
	if !opts.NoETag {
		h = etagMiddleware(h)
	}
	if opts.Public {
		return h
	}
	if opts.Admin {
		h = auth.RequireAdmin(h)
	}
	h = auth.RequireCSRF(h)
	h = auth.RequirePasswordChanged(h)
	return auth.RequireAuth(h)
}
