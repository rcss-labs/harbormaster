package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/agent/provider"
	"github.com/rcss-labs/harbormaster/internal/agent/provider/fake"
	"github.com/rcss-labs/harbormaster/internal/agentclient"
)

// customBody is a custom review with request as its instructions, or without any when request is nil.
func (h *harness) customBody(request any) []byte {
	return h.reviewBody(func(b map[string]any) {
		b["task"] = wireReview(func(_, review, _ map[string]any) {
			review["type"] = "custom"
			if request != nil {
				review["instructions"] = request
			}
		})
	})
}

// requireSilentRefusal is requireRefused for a body whose text must come back nowhere, and checks the sentence.
func requireSilentRefusal(t *testing.T, name string, h *harness, p *fake.Provider, body []byte, message string) {
	t.Helper()
	resp := h.post(t, "/v1/runs", h.token, body)
	raw, _ := io.ReadAll(resp.Body)
	var answer struct {
		Error struct{ Code, Message string }
	}
	if resp.StatusCode != http.StatusBadRequest || json.Unmarshal(raw, &answer) != nil || answer.Error.Code != "invalid_request" || answer.Error.Message != message {
		t.Errorf("%s: status %d, %.300s, want 400 %q", name, resp.StatusCode, raw, message)
	}
	if strings.Contains(string(raw), "SENTINEL") || strings.Contains(h.log.String(), "SENTINEL") {
		t.Errorf("%s: the text came back", name)
	}
	if n := len(p.Requests()); n != 0 {
		t.Errorf("%s: %d provider calls", name, n)
	}
}

func TestRuns_CustomInstructionsTable(t *testing.T) {
	const (
		required = "A custom review needs instructions."
		notText  = "instructions must be valid UTF-8 without a NUL character."
		tooLong  = "instructions must hold at most 2000 characters and 8000 bytes."
		hidden   = "instructions must hold no hidden characters, and only line feeds as line breaks."
		edges    = "instructions must have only line feeds as line breaks and no white space at either end."
	)
	words := strings.Repeat("abcd ", 400)[:1991]
	for name, c := range map[string]struct {
		text    any
		message string
	}{
		"missing":             {nil, required},
		"empty":               {"", required},
		"white space":         {" \n\t", required},
		"2001 characters":     {words + " SENTINELx", tooLong},
		"8001 bytes":          {"SENTINEL" + strings.Repeat("\U0001F600", 1998) + "x", tooLong},
		"NUL":                 {"SENTINEL\x00x", notText},
		"a hidden character":  {"SENTINEL\u200bx", hidden},
		"a bidi control":      {"SENTINEL\u202ex", hidden},
		"a control character": {"SENTINEL\u0085x", hidden},
		"a leading space":     {" SENTINEL", edges},
		"a trailing break":    {"SENTINEL\n", edges},
		"an outer separator":  {"SENTINEL\u2028", edges},
		"a CR LF":             {"SENTINEL\r\nx", hidden},
		"a line separator":    {"SENTINEL by tag.\u2028Then by account.", edges},
		"a paragraph one":     {"SENTINEL by tag.\u2029Then by account.", edges},
		"one beside a secret": {"SENTINEL " + fakeJWT + "\u2028Report.", edges},
	} {
		p := fake.New(calls(submitCall(plainSummary)))
		h := newHarness(t, harnessOptions{providers: map[string]provider.Provider{"fake": p}})
		requireSilentRefusal(t, name, h, p, h.customBody(c.text), c.message)
	}
	// JSON decoding would hide a broken byte.
	for _, broken := range []string{"\xff", "\xed\xa0\x80", "\xc3"} {
		p := fake.New(calls(submitCall(plainSummary)))
		h := newHarness(t, harnessOptions{providers: map[string]provider.Provider{"fake": p}})
		body := bytes.Replace(h.customBody("SENTINEL BROKEN x"), []byte("BROKEN"), []byte(broken), 1)
		requireSilentRefusal(t, "invalid UTF-8", h, p, body, notText)
	}
	// Every other run takes such a body as it did before.
	p := fake.New(calls(submitCall(plainSummary)))
	h := newHarness(t, harnessOptions{providers: map[string]provider.Provider{"fake": p}})
	resp := h.post(t, "/v1/runs", h.token, bytes.Replace(h.reviewBody(nil), []byte("Payments"), []byte("Pay\xffments"), 1))
	if resp.StatusCode != http.StatusOK || lastEvent(t, readEvents(t, resp)).Type != "done" {
		t.Errorf("a built-in review with invalid UTF-8: status %d", resp.StatusCode)
	}

	for name, text := range map[string]string{
		"one character":          "x",
		"2000 characters":        words + " 12345678",
		"2000 x four bytes":      strings.Repeat("\U0001F600", 2000),
		"lines and tabs":         "Costs:\n\t- by tag\n\n\t- by account",
		"another script":         "Vergleiche die Kosten über die Woche. コストを比較する。",
		"a secret, for the loop": "Use the token " + fakeJWT + " here.",
	} {
		p := fake.New(calls(submitCall(plainSummary)))
		h := newHarness(t, harnessOptions{providers: map[string]provider.Provider{"fake": p}})
		client, err := agentclient.New(h.server.URL, "")
		if err != nil {
			t.Fatal(err)
		}
		done := false
		err = client.Run(context.Background(), h.token, agentclient.RunRequest{
			RunID: uuid.New(), Role: "reviewer", Provider: "fake", Model: "fake-1", MaxCostMicroUSD: 1_000_000,
			Task: &agentclient.Task{
				Kind:   "review",
				Review: &agentclient.TaskReview{Type: "custom", Range: "7d", Period: "Week 48, 2026", Instructions: text},
				Scope:  &agentclient.TaskScope{Entries: []agentclient.ScopeEntry{}, ServicesTotal: 2},
			},
		}, func(ev agentclient.Event) { done = done || ev.Type == "done" })
		if err != nil || !done || len(p.Requests()) != 1 {
			t.Errorf("%s: err %v, done %v, %d provider calls", name, err, done, len(p.Requests()))
		}
	}
}

func TestRuns_BuiltInTypeRefusesInstructions(t *testing.T) {
	for _, id := range []string{"cost", "security", "reliability", "capacity"} {
		for _, text := range []string{"SENTINEL", " ", "SENTINEL\u200b"} {
			p := fake.New(calls(submitCall(plainSummary)))
			h := newHarness(t, harnessOptions{providers: map[string]provider.Provider{"fake": p}})
			body := h.reviewBody(func(b map[string]any) {
				b["task"] = wireReview(func(_, review, _ map[string]any) { review["type"], review["instructions"] = id, text })
			})
			requireSilentRefusal(t, id, h, p, body, "Only a custom review takes instructions.")
		}
	}
	// An empty member is none.
	p := fake.New(calls(submitCall(plainSummary)))
	h := newHarness(t, harnessOptions{providers: map[string]provider.Provider{"fake": p}})
	resp := h.post(t, "/v1/runs", h.token, h.reviewBody(func(b map[string]any) {
		b["task"] = wireReview(func(_, review, _ map[string]any) { review["instructions"] = "" })
	}))
	if resp.StatusCode != http.StatusOK || lastEvent(t, readEvents(t, resp)).Type != "done" {
		t.Errorf("status %d", resp.StatusCode)
	}
}

func TestRuns_OversizedInstructionsAreRefusedQuickly(t *testing.T) {
	for name, unit := range map[string]string{"letters": "x", "breaks": "\r\n", "pairs": "PASSWORD=", "hidden": "\u200b", "quotes": `{"a":"`} {
		p := fake.New(calls(submitCall(plainSummary)))
		h := newHarness(t, harnessOptions{providers: map[string]provider.Provider{"fake": p}})
		encoded, _ := json.Marshal(unit)
		body := h.customBody("SENTINEL" + strings.Repeat(unit, 900_000/(len(encoded)-2)))
		if len(body) < 890_000 {
			t.Fatalf("%s: the body has %d bytes", name, len(body))
		}
		started := time.Now()
		requireSilentRefusal(t, name, h, p, body, "instructions must hold at most 2000 characters and 8000 bytes.")
		if took := time.Since(started); took > time.Second {
			t.Errorf("%s: refused after %s", name, took)
		}
	}
}

// A review's name is for people: no member of a task takes it, and the first message has no place for it.
func TestCustom_TheReviewNameIsNotSent(t *testing.T) {
	for _, place := range []string{"task", "review", "scope"} {
		p := fake.New(calls(submitCall(plainSummary)))
		h := newHarness(t, harnessOptions{providers: map[string]provider.Provider{"fake": p}})
		body := h.reviewBody(func(b map[string]any) {
			b["task"] = wireReview(func(task, review, scope map[string]any) {
				review["type"], review["instructions"] = "custom", growthRequest
				map[string]map[string]any{"task": task, "review": review, "scope": scope}[place]["name"] = "SENTINEL-NAME"
			})
		})
		requireSilentRefusal(t, "a name in the "+place, h, p, body, "The request body is not one JSON object.")
	}
}

func TestCatalogue_ListsCustom(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	client, err := agentclient.New(h.server.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	catalogue, err := client.Catalogue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"cost", "security", "reliability", "capacity", "custom"}; !equalStrings(catalogue.ReviewTypes, want) || !catalogue.SupportsReview("custom") {
		t.Errorf("review types = %v, want %v", catalogue.ReviewTypes, want)
	}
}

func TestLogs_NeverContainTheRequest(t *testing.T) {
	const request = "SENTINEL-REQUEST: which services cost most?\nSENTINEL-SECOND line, über all tags."
	down := &provider.Error{Kind: "unavailable", Retryable: true, Message: "cannot read SENTINEL-REQUEST", Status: 503}
	for name, p := range map[string]provider.Provider{
		"a result": fake.New(
			calls(provider.ToolCall{ID: "c1", Name: "SENTINEL-REQUEST", Arguments: map[string]any{}}, costCall("c2")),
			calls(submitCall("SENTINEL-REQUEST"+strings.Repeat(" and more", 20), map[string]any{"ref": "e1", "note": request})),
		),
		"no result":     fake.New(fake.Text(request, smallUsage), fake.Text(request, smallUsage)),
		"failing calls": fake.New(fake.Fail(down), fake.Fail(down)),
		"a panic":       panickingProvider{},
	} {
		h := newHarness(t, harnessOptions{providers: map[string]provider.Provider{"fake": p}})
		resp := h.post(t, "/v1/runs", h.token, h.customBody(request))
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status %d: %s", name, resp.StatusCode, raw)
		}
		h.server.Close() // waits for the run's last log line
		log := h.log.String()
		if strings.Contains(log, "SENTINEL") {
			t.Errorf("%s: the log holds the request:\n%s", name, log)
		}
		if name == "a panic" {
			continue
		}
		for _, want := range []string{`"msg":"review finished"`, `"review_type":"custom"`, fmt.Sprintf(`"request_bytes":%d`, len(request))} {
			if !strings.Contains(log, want) {
				t.Errorf("%s: the log lacks %s:\n%s", name, want, log)
			}
		}
	}
	// A built-in review has none.
	h := newHarness(t, harnessOptions{})
	resp := h.post(t, "/v1/runs", h.token, h.reviewBody(nil))
	_, _ = io.Copy(io.Discard, resp.Body)
	h.server.Close()
	if log := h.log.String(); !strings.Contains(log, `"review_type":"cost"`) || strings.Contains(log, "request_bytes") {
		t.Errorf("a built-in review logs\n%s", log)
	}
}
