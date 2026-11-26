package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/agent/provider"
	"github.com/rcss-labs/harbormaster/internal/agent/provider/fake"
	"github.com/rcss-labs/harbormaster/internal/agentclient"
	"github.com/rcss-labs/harbormaster/internal/agentresult"
)

// wireTask is the task member as Harbormaster's client sends it.
func wireTask(edit func(task, subject, alert map[string]any)) map[string]any {
	subject := map[string]any{"kind": "service", "service_ids": []string{checkoutID}, "cluster_id": prodCluster.ID(), "account_id": "123456789012", "region": "eu-west-1"}
	alert := map[string]any{"kind": "service_down", "title": "checkout-api has no running tasks", "text": "0 of 3 tasks are running.", "opened_at": "2026-11-25T09:30:00Z"}
	task := map[string]any{"kind": "investigate", "subject": subject, "alert": alert}
	if edit != nil {
		edit(task, subject, alert)
	}
	return task
}

func (h *harness) investigateBody(mutate func(map[string]any)) []byte {
	return h.runBody(func(b map[string]any) {
		b["role"] = "investigator"
		b["messages"] = nil
		b["page"] = map[string]string{"path": "", "service_id": "", "cluster_id": ""}
		b["task"] = wireTask(nil)
		if mutate != nil {
			mutate(b)
		}
	})
}

func requireRefused(t *testing.T, name string, h *harness, p *fake.Provider, body []byte) {
	t.Helper()
	resp := h.post(t, "/v1/runs", h.token, body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("%s: status %d, want 400", name, resp.StatusCode)
		_, _ = io.Copy(io.Discard, resp.Body)
	} else if code := errorCode(t, resp); code != "invalid_request" {
		t.Errorf("%s: code %q", name, code)
	}
	if n := len(p.Requests()); n != 0 {
		t.Errorf("%s: %d provider calls", name, n)
	}
}

func TestRuns_InvestigatorNeedsATaskAndNoMessages(t *testing.T) {
	ids := func(n int) []string {
		list := make([]string, n)
		for i := range list {
			list[i] = checkoutID
		}
		return list
	}
	edit := func(f func(task, subject, alert map[string]any)) func(map[string]any) {
		return func(b map[string]any) { b["task"] = wireTask(f) }
	}
	long := strings.Repeat("x", 2001)
	refused := map[string]func(map[string]any){
		"no task":              func(b map[string]any) { delete(b, "task") },
		"null task":            func(b map[string]any) { b["task"] = nil },
		"messages":             func(b map[string]any) { b["messages"] = []map[string]string{{"role": "user", "text": "hi"}} },
		"a page":               func(b map[string]any) { b["page"] = map[string]string{"path": "/services"} },
		"a page's service":     func(b map[string]any) { b["page"] = map[string]string{"service_id": checkoutID} },
		"a page's cluster":     func(b map[string]any) { b["page"] = map[string]string{"cluster_id": prodCluster.ID()} },
		"other kind":           edit(func(task, _, _ map[string]any) { task["kind"] = "summarise" }),
		"no kind":              edit(func(task, _, _ map[string]any) { delete(task, "kind") }),
		"unknown task member":  edit(func(task, _, _ map[string]any) { task["system"] = "x" }),
		"unknown subject kind": edit(func(_, subject, _ map[string]any) { subject["kind"] = "planet" }),
		"null service ids":     edit(func(_, subject, _ map[string]any) { subject["service_ids"] = nil }),
		"no service ids":       edit(func(_, subject, _ map[string]any) { subject["service_ids"] = []string{} }),
		"21 service ids":       edit(func(_, subject, _ map[string]any) { subject["service_ids"] = ids(21) }),
		"an empty service id":  edit(func(_, subject, _ map[string]any) { subject["service_ids"] = []string{checkoutID, ""} }),
		"a long service id":    edit(func(_, subject, _ map[string]any) { subject["service_ids"] = []string{strings.Repeat("x", 513)} }),
		"a long subject kind":  edit(func(_, subject, _ map[string]any) { subject["kind"] = long }),
		"a long cluster id":    edit(func(_, subject, _ map[string]any) { subject["cluster_id"] = long }),
		"a long account id":    edit(func(_, subject, _ map[string]any) { subject["account_id"] = long }),
		"a long region":        edit(func(_, subject, _ map[string]any) { subject["region"] = long }),
		"a long alert kind":    edit(func(_, _, alert map[string]any) { alert["kind"] = long }),
		"a long alert title":   edit(func(_, _, alert map[string]any) { alert["title"] = long }),
		"a long alert text":    edit(func(_, _, alert map[string]any) { alert["text"] = long }),
		"a bad opened_at":      edit(func(_, _, alert map[string]any) { alert["opened_at"] = "yesterday" }),
	}
	for name, mutate := range refused {
		p := fake.New(calls(submitCall(plainSummary)))
		h := newHarness(t, harnessOptions{providers: map[string]provider.Provider{"fake": p}})
		requireRefused(t, name, h, p, h.investigateBody(mutate))
	}
	accepted := map[string]func(map[string]any){
		"as the client sends it": nil,
		"empty messages":         func(b map[string]any) { b["messages"] = []any{} },
		"no messages member":     func(b map[string]any) { delete(b, "messages") },
		"no page member":         func(b map[string]any) { delete(b, "page") },
		"20 ids, long strings": edit(func(_, subject, alert map[string]any) {
			subject["kind"], subject["service_ids"] = "cluster", ids(20)
			alert["text"] = long[:2000]
		}),
		"a 512-byte id and an account": edit(func(_, subject, _ map[string]any) {
			subject["kind"], subject["service_ids"] = "account", []string{strings.Repeat("x", 512)}
		}),
	}
	for name, mutate := range accepted {
		p := fake.New(calls(submitCall(plainSummary)))
		h := newHarness(t, harnessOptions{providers: map[string]provider.Provider{"fake": p}})
		resp := h.post(t, "/v1/runs", h.token, h.investigateBody(mutate))
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: status %d", name, resp.StatusCode)
			continue
		}
		if last := lastEvent(t, readEvents(t, resp)); last.Type != "done" || len(p.Requests()) != 1 {
			t.Errorf("%s: last event %+v after %d provider calls", name, last, len(p.Requests()))
		}
	}
}

func TestRuns_ChatRefusesATask(t *testing.T) {
	p := fake.New(fake.Text("ok", smallUsage))
	h := newHarness(t, harnessOptions{providers: map[string]provider.Provider{"fake": p}})
	requireRefused(t, "chat with a task", h, p, h.runBody(func(b map[string]any) { b["task"] = wireTask(nil) }))
	requireRefused(t, "chat without messages", h, p, h.runBody(func(b map[string]any) { b["messages"] = nil }))
}

func TestRuns_UnknownRoleIs400BeforeAnyProviderCall(t *testing.T) {
	for _, role := range []string{"", "test", "Investigator", "admin"} {
		p := fake.New(calls(submitCall(plainSummary)))
		h := newHarness(t, harnessOptions{providers: map[string]provider.Provider{"fake": p}})
		requireRefused(t, "role "+role, h, p, h.investigateBody(func(b map[string]any) { b["role"] = role }))
		requireRefused(t, "role "+role+" with messages", h, p, h.runBody(func(b map[string]any) { b["role"] = role }))
	}
}

// estate answers the demo investigator's reads.
func estate(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/timeline"):
		_, _ = io.WriteString(w, `{"range":"1h","entries":[{"kind":"task_stopped"}]}`)
	case strings.HasSuffix(r.URL.Path, "/services/"+checkoutID):
		_, _ = io.WriteString(w, serviceBody)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestRuns_ResultEventOnTheWire(t *testing.T) {
	demo := fake.Demo()
	h := newHarness(t, harnessOptions{providers: map[string]provider.Provider{"fake": demo}, upstreamAPI: estate})
	resp := h.post(t, "/v1/runs", h.token, h.investigateBody(nil))
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatal(err)
		}
		if ev["type"] == "result" {
			result = ev
			continue
		}
		for _, key := range []string{"summary", "summary_replaced", "evidence", "evidence_dropped"} {
			if _, ok := ev[key]; ok {
				t.Errorf("a %v event carries %s", ev["type"], key)
			}
		}
	}
	if len(result) != 5 || result["summary_replaced"] != false || result["evidence_dropped"] != float64(0) || result["summary"] == "" {
		t.Fatalf("result event = %v\nstream: %s", result, raw)
	}
	items, _ := result["evidence"].([]any)
	if len(items) != 1 {
		t.Fatalf("evidence = %v", result["evidence"])
	}
	item, _ := items[0].(map[string]any)
	if len(item) != 5 || item["tool"] != "get_service" || item["service_id"] != checkoutID || item["action"] != "view" || item["note"] == "" || item["excerpt"] != serviceUnits[0] {
		t.Errorf("item = %v", item)
	}

	// Harbormaster's own client reads the same run.
	client, err := agentclient.New(h.server.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	request := agentclient.RunRequest{
		RunID: uuid.New(), Role: "investigator", Provider: "fake", Model: "fake-1", MaxCostMicroUSD: 1_000_000,
		Task: &agentclient.Task{
			Kind:    "investigate",
			Subject: agentclient.TaskSubject{Kind: "service", ServiceIDs: []string{checkoutID}},
			Alert:   agentclient.TaskAlert{Kind: "service_down", Title: "down", OpenedAt: investigation.Alert.OpenedAt},
		},
	}
	var got *agentresult.Result
	if err := client.Run(context.Background(), h.token, request, func(ev agentclient.Event) {
		if ev.Type == "result" {
			got = ev.Result
		}
	}); err != nil {
		t.Fatal(err)
	}
	if got == nil || len(got.Evidence) != 1 || got.Evidence[0].ServiceID != checkoutID || got.SummaryReplaced {
		t.Errorf("the client read %+v", got)
	}
	// The same provider still answers a chat.
	if last := lastEvent(t, readEvents(t, h.post(t, "/v1/runs", h.token, h.runBody(nil)))); last.Type != "done" {
		t.Errorf("chat ended with %+v", last)
	}
}

func TestLogs_NeverContainSummaryNoteExcerptOrTask(t *testing.T) {
	p := fake.New(
		calls(serviceCall("c1", "get_service")),
		calls(submitCall("SENTINEL-BAD", map[string]any{"ref": "e1", "note": "SENTINEL-NOTE", "excerpt": "SENTINEL-FORGED"})),
		calls(submitCall("SENTINEL-SUMMARY", map[string]any{"ref": "e1", "note": "SENTINEL-NOTE"}, map[string]any{"ref": "SENTINEL-REF", "note": "SENTINEL-NOTE"})),
	)
	h := newHarness(t, harnessOptions{
		providers: map[string]provider.Provider{"fake": p},
		upstreamAPI: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"SENTINEL-MEMBER":"SENTINEL-EXCERPT","running":0}`)
		},
	})
	resp := h.post(t, "/v1/runs", h.token, h.investigateBody(func(b map[string]any) {
		b["task"] = wireTask(func(_, subject, alert map[string]any) {
			subject["region"], alert["kind"], alert["title"], alert["text"] = "SENTINEL-REGION", "SENTINEL-KIND", "SENTINEL-TITLE", "SENTINEL-TEXT"
		})
	}))
	raw, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(raw), "SENTINEL-SUMMARY") || !strings.Contains(string(raw), "SENTINEL-EXCERPT") {
		t.Fatalf("the run had no result to leak: %s", raw)
	}
	h.server.Close() // waits for the run's last log line
	log := h.log.String()
	for _, secret := range []string{"SENTINEL", checkoutID} {
		if strings.Contains(log, secret) {
			t.Errorf("the log holds %q:\n%s", secret, log)
		}
	}
	for _, want := range []string{`"units":2`, `"evidence":1`, `"evidence_dropped":1`, `"summary_replaced":false`} {
		if !strings.Contains(log, want) {
			t.Errorf("the log lacks %s:\n%s", want, log)
		}
	}
}
