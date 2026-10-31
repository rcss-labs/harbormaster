package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
)

// etagWriter buffers the status and body so the ETag can be computed before anything is sent.
type etagWriter struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (e *etagWriter) WriteHeader(status int) {
	if e.status == 0 {
		e.status = status
	}
}

func (e *etagWriter) Write(p []byte) (int, error) {
	if e.status == 0 {
		e.status = http.StatusOK
	}
	return e.body.Write(p)
}

// etagMiddleware adds a content-hash ETag to 200 JSON GET responses and answers 304 on If-None-Match.
// Do not wrap streaming or WebSocket routes (the response is buffered).
func etagMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}
		buf := &etagWriter{ResponseWriter: w}
		next.ServeHTTP(buf, r)
		status := buf.status
		if status == 0 {
			status = http.StatusOK
		}
		if status == http.StatusOK && strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
			sum := sha256.Sum256(buf.body.Bytes())
			etag := `"` + hex.EncodeToString(sum[:])[:32] + `"`
			w.Header().Set("ETag", etag)
			if etagMatches(r.Header.Get("If-None-Match"), etag) {
				w.Header().Del("Content-Type")
				w.Header().Del("Content-Length")
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
		w.WriteHeader(status)
		_, _ = w.Write(buf.body.Bytes())
	})
}

func etagMatches(header, etag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimPrefix(strings.TrimSpace(candidate), "W/")
		if candidate == etag || candidate == "*" {
			return true
		}
	}
	return false
}
