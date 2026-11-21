// Package fakesink is the in-process receiver behind /fake-notify/ in fake mode. It records what Harbormaster
// sends so `make run` and the e2e suite exercise the real senders, retries and delivery log without the internet.
// It holds nothing but memory.
package fakesink

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	keep         = 200
	maxBodyBytes = 1 << 20
)

// Sink records requests and mails. SlowDelay is how long a `slow` route waits (15 s by default).
type Sink struct {
	SlowDelay time.Duration

	mu      sync.Mutex
	entries []entry
}

type entry struct {
	Time    time.Time         `json:"time"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
}

// headersOfInterest are the only request headers kept.
var headersOfInterest = []string{"Content-Type", "User-Agent", "X-Harbormaster-Event", "X-Harbormaster-Delivery", "X-Harbormaster-Signature"}

func New() *Sink { return &Sink{SlowDelay: 15 * time.Second} }

// record keeps the last `keep` entries. A JSON body is embedded as JSON so a search for `"event":"alert.raised"`
// finds it; anything else is kept as a JSON string.
func (s *Sink) record(path string, headers map[string]string, body []byte) {
	raw := json.RawMessage(body)
	if !json.Valid(body) {
		raw, _ = json.Marshal(string(body))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, entry{Time: time.Now().UTC(), Path: path, Headers: headers, Body: raw})
	if len(s.entries) > keep {
		s.entries = s.entries[len(s.entries)-keep:]
	}
}

// Mail records an email in the same list; it is the Senders.FakeMail hook.
func (s *Sink) Mail(from string, to []string, msg []byte) error {
	body, _ := json.Marshal(map[string]any{"from": from, "to": to, "message": string(msg)})
	s.record("mail", map[string]string{}, body)
	return nil
}

// Handler serves the receiver. Mount it at /fake-notify/ so request paths keep their prefix.
func (s *Sink) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/fake-notify/received" {
			s.mu.Lock()
			out := append([]entry{}, s.entries...)
			s.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(out)
			return
		}
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/fake-notify/"), "/")
		switch {
		case len(parts) == 2 && (parts[0] == "slack" || parts[0] == "webhook"):
			s.receive(w, r, r.URL.Path)
			switch parts[1] {
			case "fail":
				w.WriteHeader(http.StatusServiceUnavailable)
			case "slow":
				select {
				case <-time.After(s.SlowDelay):
				case <-r.Context().Done():
				}
				w.WriteHeader(http.StatusOK)
			default:
				w.WriteHeader(http.StatusOK)
			}
		case len(parts) == 1 && parts[0] == "pagerduty":
			s.receive(w, r, r.URL.Path)
			w.WriteHeader(http.StatusAccepted)
		default:
			http.NotFound(w, r)
		}
	})
}

// receive records the request. It must run before the response is written.
func (s *Sink) receive(_ http.ResponseWriter, r *http.Request, path string) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	headers := map[string]string{}
	for _, name := range headersOfInterest {
		if v := r.Header.Get(name); v != "" {
			headers[name] = v
		}
	}
	s.record(path, headers, body)
}
