package mcpgw

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"slices"
	"time"
)

// maxTokenFormBytes bounds one token request body.
const maxTokenFormBytes = 8 << 10

// serveOAuthToken relays POST /oauth/token to Harbormaster and nothing else.
// The form carries codes, verifiers and refresh tokens, so it and the answer
// are never logged; only the status and the caller's address are.
func (g *gateway) serveOAuthToken(w http.ResponseWriter, r *http.Request) {
	if !g.cfg.OAuthEnabled() {
		http.NotFound(w, r)
		return
	}
	started := time.Now()
	ip := clientIP(r, g.cfg.TrustedProxy)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")

	if origin := r.Header.Get("Origin"); origin != "" && !slices.Contains(g.cfg.AllowedOrigins, origin) {
		writeJSONError(w, http.StatusForbidden, "origin_not_allowed")
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil ||
		mediaType != "application/x-www-form-urlencoded" {
		writeOAuthError(w, http.StatusUnsupportedMediaType, "invalid_request")
		return
	}
	if g.limiter.Blocked(ip) {
		g.writeBlocked(w, ip)
		g.logRelay(started, ip, http.StatusTooManyRequests, 0)
		return
	}
	if r.ContentLength > maxTokenFormBytes {
		writeOAuthError(w, http.StatusRequestEntityTooLarge, "invalid_request")
		return
	}
	form, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxTokenFormBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeOAuthError(w, http.StatusRequestEntityTooLarge, "invalid_request")
			return
		}
		writeOAuthError(w, http.StatusBadRequest, "invalid_request")
		return
	}

	status, body, err := g.up.RelayToken(r.Context(), form, forwardedFor(r, g.cfg.TrustedProxy))
	if err != nil {
		g.log.Warn("token relay failed", "client_ip", ip, "error", describeTransportError(err))
		writeOAuthError(w, http.StatusBadGateway, "temporarily_unavailable")
		g.logRelay(started, ip, http.StatusBadGateway, 0)
		return
	}
	if status == http.StatusBadRequest || status == http.StatusUnauthorized {
		g.limiter.Failed(ip)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
	g.logRelay(started, ip, status, status)
}

// serveOAuthAuthorize is the optional redirect for a client that insists on
// the authorization endpoint sharing the issuer's host. It is a bare 302 to
// the private URL with the query intact; it reads and stores nothing.
func (g *gateway) serveOAuthAuthorize(w http.ResponseWriter, r *http.Request) {
	if !g.cfg.OAuthEnabled() || !g.cfg.AuthorizeViaGateway {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	target := g.cfg.AuthorizeURL + "/oauth/authorize"
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, target, http.StatusFound)
}

// writeOAuthError writes an RFC 6749 error body (no description, no echo of input).
func writeOAuthError(w http.ResponseWriter, status int, code string) {
	writeJSONError(w, status, code)
}

// logRelay is the one line per relayed request: never the form or the answer.
func (g *gateway) logRelay(started time.Time, ip string, status, upstreamStatus int) {
	attrs := []any{
		"method", "oauth/token",
		"status", status,
		"duration_ms", time.Since(started).Milliseconds(),
		"client_ip", ip,
	}
	if upstreamStatus != 0 {
		attrs = append(attrs, "upstream_status", upstreamStatus)
	}
	g.log.Info("oauth request", attrs...)
}
