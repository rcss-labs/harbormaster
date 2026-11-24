// Package hmtoolstest gives tests a stub Harbormaster that records every request it receives. It imports nothing
// from this module, so the tool table's own tests and the gateway's tests can both use it.
package hmtoolstest

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// RecordedRequest is what a stub upstream saw.
type RecordedRequest struct {
	Method, EscapedPath, Query string
	Header                     http.Header
	Body                       string
}

// RecordingServer answers every request with handler (default 200 {}) and remembers each request.
type RecordingServer struct {
	*httptest.Server
	mu       sync.Mutex
	requests []RecordedRequest
}

// NewRecordingServer starts a RecordingServer that is closed when the test ends.
func NewRecordingServer(t *testing.T, handler http.HandlerFunc) *RecordingServer {
	t.Helper()
	rs := &RecordingServer{}
	rs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rs.mu.Lock()
		rs.requests = append(rs.requests, RecordedRequest{
			Method: r.Method, EscapedPath: r.URL.EscapedPath(), Query: r.URL.RawQuery, Header: r.Header.Clone(), Body: string(body),
		})
		rs.mu.Unlock()
		if handler != nil {
			handler(w, r)
			return
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(rs.Close)
	return rs
}

// Seen returns the requests received so far, oldest first.
func (rs *RecordingServer) Seen() []RecordedRequest {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return append([]RecordedRequest(nil), rs.requests...)
}
