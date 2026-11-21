package fakesink

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func post(t *testing.T, h http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("X-Harbormaster-Event", "opened")
	req.Header.Set("X-Harbormaster-Delivery", "7")
	req.Header.Set("Authorization", "must-not-be-recorded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

type receivedEntry struct {
	Time    time.Time         `json:"time"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
}

func received(t *testing.T, h http.Handler) []receivedEntry {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/fake-notify/received", nil))
	if rec.Code != 200 {
		t.Fatalf("GET received = %d", rec.Code)
	}
	var out []receivedEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("received is not a JSON list: %v\n%s", err, rec.Body)
	}
	return out
}

func TestFakeSink_RecordsAndFails(t *testing.T) {
	sink := New()
	sink.SlowDelay = 20 * time.Millisecond
	h := sink.Handler()

	if rec := post(t, h, "/fake-notify/webhook/ok", `{"event":"alert.raised"}`); rec.Code != 200 {
		t.Errorf("webhook ok = %d, want 200", rec.Code)
	}
	if rec := post(t, h, "/fake-notify/slack/deploys", `{"text":"hi"}`); rec.Code != 200 {
		t.Errorf("slack = %d, want 200", rec.Code)
	}
	if rec := post(t, h, "/fake-notify/pagerduty", `{"event_action":"trigger"}`); rec.Code != 202 {
		t.Errorf("pagerduty = %d, want 202", rec.Code)
	}
	if rec := post(t, h, "/fake-notify/webhook/fail", `{"event":"x"}`); rec.Code != 503 {
		t.Errorf("fail = %d, want 503", rec.Code)
	}
	start := time.Now()
	if rec := post(t, h, "/fake-notify/slack/slow", `{}`); rec.Code != 200 || time.Since(start) < 20*time.Millisecond {
		t.Errorf("slow = %d after %v, want a delayed 200", rec.Code, time.Since(start))
	}
	if rec := post(t, h, "/fake-notify/unknown/x", `{}`); rec.Code != 404 {
		t.Errorf("unknown route = %d, want 404", rec.Code)
	}
	if err := sink.Mail("hm@example.com", []string{"a@example.com"}, []byte("Subject: hi\r\n\r\nbody")); err != nil {
		t.Fatal(err)
	}

	got := received(t, h)
	if len(got) != 6 {
		t.Fatalf("received %d entries, want 6 (every post and the mail)", len(got))
	}
	first := got[0]
	if first.Path != "/fake-notify/webhook/ok" || first.Headers["X-Harbormaster-Event"] != "opened" || first.Headers["X-Harbormaster-Delivery"] != "7" {
		t.Errorf("first entry = %+v", first)
	}
	if _, kept := first.Headers["Authorization"]; kept {
		t.Error("only headers of interest are recorded")
	}
	// The body of a JSON post is embedded as JSON so tests can search for `"event":"alert.raised"`.
	if !strings.Contains(string(first.Body), `"event":"alert.raised"`) {
		t.Errorf("body = %s", first.Body)
	}
	last := got[len(got)-1]
	if last.Path != "mail" || !strings.Contains(string(last.Body), "a@example.com") || !strings.Contains(string(last.Body), "Subject: hi") {
		t.Errorf("mail entry = %+v", last)
	}
}

func TestFakeSink_KeepsTheLast200(t *testing.T) {
	sink := New()
	h := sink.Handler()
	for i := 0; i < 230; i++ {
		post(t, h, "/fake-notify/webhook/ok", `{"n":`+string(rune('0'+i%10))+`}`)
	}
	if n := len(received(t, h)); n != 200 {
		t.Fatalf("kept %d entries, want 200", n)
	}
}

func TestFakeSink_RedactsTelegramToken(t *testing.T) {
	const token = "123456789:AAHsecretsecretsecretsecretsecretsecret"
	sink := New()
	h := sink.Handler()
	path := "/fake-notify/telegram/bot" + token + "/sendMessage"

	ok := post(t, h, path, `{"chat_id":"-1001","text":"hi","parse_mode":"HTML"}`)
	if ok.Code != 200 || !strings.Contains(ok.Body.String(), `"ok":true`) || !strings.Contains(ok.Body.String(), `"message_id"`) {
		t.Errorf("ok answer = %d %s", ok.Code, ok.Body)
	}
	for chat, want := range map[string]int{"-429": 429, "-403": 403, "-400": 400} {
		rec := post(t, h, path, `{"chat_id":"`+chat+`","text":"hi"}`)
		if rec.Code != want || !strings.Contains(rec.Body.String(), `"ok":false`) {
			t.Errorf("chat %s answered %d %s, want %d", chat, rec.Code, rec.Body, want)
		}
		if strings.Contains(rec.Body.String(), token) {
			t.Errorf("chat %s: the answer echoes the token", chat)
		}
	}
	if rec := post(t, h, "/fake-notify/telegram/bot"+token+"/getMe", `{}`); rec.Code != 404 {
		t.Errorf("an unknown bot method = %d, want 404", rec.Code)
	}
	if !strings.Contains(post(t, h, path, `{"chat_id":"-429"}`).Body.String(), `"retry_after":1`) {
		t.Error("the 429 answer must carry parameters.retry_after")
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/fake-notify/received", nil))
	dump := rec.Body.String()
	if strings.Contains(dump, token) || strings.Contains(dump, "123456789") {
		t.Fatalf("the receiver kept the token:\n%s", dump)
	}
	got := received(t, h)
	if len(got) == 0 || got[0].Path != "/fake-notify/telegram/bot***/sendMessage" || !strings.Contains(string(got[0].Body), `"chat_id":"-1001"`) {
		t.Errorf("first entry = %+v, want the redacted path and the JSON body", got[0])
	}
}

func TestFakeSink_IgnoresNonJSONBodies(t *testing.T) {
	sink := New()
	h := sink.Handler()
	post(t, h, "/fake-notify/webhook/ok", "not json at all")
	got := received(t, h)
	var s string
	if err := json.Unmarshal(got[0].Body, &s); err != nil || s != "not json at all" {
		t.Fatalf("body = %s, want the raw text as a JSON string", got[0].Body)
	}
	_ = io.Discard
}
