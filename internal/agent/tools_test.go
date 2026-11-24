package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/rcss-labs/harbormaster/internal/agent/provider"
	"github.com/rcss-labs/harbormaster/internal/agent/provider/fake"
	"github.com/rcss-labs/harbormaster/internal/hmtools"
	"github.com/rcss-labs/harbormaster/internal/hmtools/hmtoolstest"
)

func newTestRunner(t *testing.T, handler http.HandlerFunc) (ToolRunner, *hmtoolstest.RecordingServer) {
	t.Helper()
	server := hmtoolstest.NewRecordingServer(t, handler)
	up, err := hmtools.NewUpstream(server.URL, 5*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	return NewUpstreamToolRunner(up, 100_000), server
}

func TestUpstreamToolRunner_OneCallOneRequest(t *testing.T) {
	runner, server := newTestRunner(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"clusters":2}`))
	})
	text, isError := runner.Run(context.Background(), "run-token", "get_overview", map[string]any{"region": "eu-west-1"})
	if isError || text != `{"clusters":2}` {
		t.Fatalf("Run = %q, %v", text, isError)
	}
	seen := server.Seen()
	if len(seen) != 1 {
		t.Fatalf("%d upstream requests, want 1", len(seen))
	}
	if seen[0].Method != "GET" || seen[0].EscapedPath != "/api/overview" || seen[0].Query != "region=eu-west-1" {
		t.Errorf("request = %+v", seen[0])
	}
	if got := seen[0].Header.Get("Authorization"); got != "Bearer run-token" {
		t.Errorf("Authorization = %q", got)
	}
}

func TestUpstreamToolRunner_NothingUnknownOrInvalidReachesHarbormaster(t *testing.T) {
	runner, server := newTestRunner(t, nil)
	cases := []struct {
		name string
		tool string
		args map[string]any
	}{
		{"unknown tool", "delete_service", map[string]any{"service_id": "x"}},
		{"unknown argument", "get_overview", map[string]any{"path": "/api/admin"}},
		{"missing argument", "get_service_timeline", map[string]any{}},
	}
	for _, tc := range cases {
		if text, isError := runner.Run(context.Background(), "run-token", tc.tool, tc.args); !isError || text == "" {
			t.Errorf("%s: Run = %q, %v, want a tool error", tc.name, text, isError)
		}
	}
	if n := len(server.Seen()); n != 0 {
		t.Errorf("%d upstream requests, want 0", n)
	}
}

func TestUpstreamToolRunner_429CarriesRetryAfter(t *testing.T) {
	runner, _ := newTestRunner(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"rate_limited","message":"Slow down."}}`))
	})
	text, isError := runner.Run(context.Background(), "run-token", "get_overview", nil)
	if !isError || !strings.Contains(text, "Slow down.") || !strings.Contains(text, "7") {
		t.Errorf("Run = %q, %v, want the upstream message and the retry-after", text, isError)
	}
}

func TestUpstreamToolRunner_ServerErrorsHideTheBody(t *testing.T) {
	runner, _ := newTestRunner(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"code":"boom","message":"panic at db.internal:5432"}}`))
	})
	text, isError := runner.Run(context.Background(), "run-token", "get_overview", nil)
	if !isError || strings.Contains(text, "db.internal") {
		t.Errorf("Run = %q, %v, want a generic unavailable error", text, isError)
	}
}

func TestUpstreamToolRunner_AnswerCutAtTheReadCapNeverLeavesPartOfASecret(t *testing.T) {
	// The read cap for 100,000 result bytes is 200,000. The token starts 20
	// bytes before it, so the cap cuts it in the middle of its first part.
	const readCap = 200_000
	body := strings.Repeat("log line ok\n", (readCap-20)/12) + fakeJWT + "\n" + strings.Repeat("more output here\n", 2000)
	runner, _ := newTestRunner(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) })

	text, isError := runner.Run(context.Background(), "run-token", "get_overview", nil)
	if isError {
		t.Fatalf("Run = %.80q, error", text)
	}
	if strings.Contains(text, "eyJ") {
		t.Errorf("a piece of the token survived the cut at the read cap")
	}
	if !strings.HasSuffix(text, "the rest was left out.]") || !strings.Contains(text, "larger than") || strings.Contains(text, "more output here") {
		t.Errorf("want a note that the answer was larger than could be read and nothing after the cut; got %d bytes", len(text))
	}
}

func TestUpstreamToolRunner_TailDropNeverSplitsACharacter(t *testing.T) {
	// No delimiter in the last 4096 bytes, and the drop point lands inside a
	// two-byte character: the answer must stay valid UTF-8.
	body := "x" + strings.Repeat("é", 100_000)
	runner, _ := newTestRunner(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) })
	text, isError := runner.Run(context.Background(), "run-token", "get_overview", nil)
	if isError || !utf8.ValidString(text) || !strings.HasSuffix(text, answerTooLargeNote) {
		t.Errorf("Run valid UTF-8: %v, error %v, note at the end: %v", utf8.ValidString(text), isError, strings.HasSuffix(text, answerTooLargeNote))
	}
}

func TestUpstreamToolRunner_AnswerCutAtTheReadCapNeverLeavesPartOfAPassword(t *testing.T) {
	// The cap falls inside the password of a connection URL, after punctuation
	// that is legal in a password.
	const readCap = 200_000
	url := "postgres://app:" + "Hunter" + "2,fake;pw'x(y"
	body := strings.Repeat("log line ok\n", (readCap-len(url)-2)/12) + `"` + url
	body += strings.Repeat("z", readCap-len(body)) + "zzzz@db.example/app\n"
	runner, _ := newTestRunner(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) })
	text, isError := runner.Run(context.Background(), "run-token", "get_overview", nil)
	if isError || strings.Contains(text, "Hunter") {
		t.Errorf("part of the password survived: ...%s", text[max(0, len(text)-160):])
	}
}

func TestLoop_JSONCutAtTheReadCapNeverSendsPartOfASecret(t *testing.T) {
	// The answer is JSON with a log line near the read cap. The secret in that
	// line slides across the cap one byte at a time, so it is cut everywhere.
	const readCap = 64 << 10 // for small result caps
	password := "Zq7" + "fakeHunter, pw;tail(9"
	inner := func(v any) string {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	for name, tc := range map[string]struct {
		line   string
		secret []string // none of these may be sent
	}{
		"token":                      {"auth " + fakeJWT + " ok", []string{"eyJ", "c2lnbm"}},
		"password in JSON in string": {inner(map[string]string{"password": password, "user": "app"}), []string{"Zq7", "fakeHu", "nter, p", "tail(9"}},
		"password two strings deep":  {inner(map[string]string{"msg": inner(map[string]string{"password": password, "user": "app"})}), []string{"Zq7", "fakeHu", "nter, p", "tail(9"}},
		"token in JSON in string":    {inner(map[string]string{"header": "Bearer " + fakeJWT}), []string{"eyJ", "c2lnbm"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			encoded := inner(tc.line)
			var body string
			runner, _ := newTestRunner(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) })
			up := runner.(upstreamToolRunner).up
			runner = NewUpstreamToolRunner(up, 1000)
			for before := 0; before <= len(encoded)+12; before++ {
				const filler = `"all is well here",`
				head := `{"events":[` + strings.Repeat(filler, (readCap-before-60)/len(filler))
				head += inner(strings.Repeat("x", max(0, readCap-before-len(head)-3))) + ","
				body = head + encoded + `,"the end` + strings.Repeat(" more", 2000) + `"]}`
				p := fake.New(
					fake.ToolCalls(provider.Usage{InputTokens: 10, OutputTokens: 5}, overviewCall("c1")),
					fake.Text("Done.", provider.Usage{InputTokens: 10, OutputTokens: 5}),
				)
				// The loop's own result cap is far above the read cap, so that
				// what is near the read cap is sent.
				if _, err := run(t, newLoop(p, runner, func(l *Limits) { l.MaxResultBytes = 400_000 }), baseInput()); err != nil {
					t.Fatal(err)
				}
				sent := p.Requests()[1].Messages
				text := sent[len(sent)-1].Text
				if !strings.Contains(text, "larger than could be read") {
					t.Fatalf("%d bytes before the cap: the answer was not cut, so this proves nothing", before)
				}
				for _, piece := range tc.secret {
					if i := strings.Index(text, piece); i >= 0 {
						t.Fatalf("%d bytes before the cap: %q was sent: %q", before, piece, text[max(0, i-60):min(len(text), i+60)])
					}
				}
			}
		})
	}
}
