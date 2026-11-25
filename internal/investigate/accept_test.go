package investigate

import (
	"encoding/base64"
	"math"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/rcss-labs/harbormaster/internal/agentresult"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
)

var (
	checkoutKey = model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: "482109375512", Region: "us-east-1", Cluster: "payments-prod"}, Service: "checkout-api"}
	ledgerKey   = model.ServiceKey{ClusterKey: checkoutKey.ClusterKey, Service: "ledger-writer"}

	fakeSecret = "AKIA" + "IOSFODNN7EXAMPLE"
)

func readAll(model.ServiceKey, authz.Action) bool { return true }

func viewItem(excerpt string) agentresult.Evidence {
	return agentresult.Evidence{Tool: "get_service", ServiceID: checkoutKey.ID(), Action: "view", Note: "The service has no running tasks.", Excerpt: excerpt}
}

func logsItem(excerpt string) agentresult.Evidence {
	return agentresult.Evidence{Tool: "get_service_logs", ServiceID: checkoutKey.ID(), Action: "logs", Note: "The start fails.", Excerpt: excerpt}
}

func resultWith(items ...agentresult.Evidence) agentresult.Result {
	return agentresult.Result{Summary: "The service stopped after its last deploy.", Evidence: items}
}

func TestAccept_DerivesTheActionFromTheTool(t *testing.T) {
	lying := logsItem("connection refused")
	lying.Action = "view" // what Harbormaster would show to a reader without Logs
	upper := viewItem("desired 2, running 0")
	upper.Action = "VIEW"
	got := Accept(resultWith(lying, upper, logsItem("connection refused"), viewItem("desired 2, running 0")), readAll)
	if len(got.Evidence) != 2 || got.EvidenceDropped != 2 {
		t.Fatalf("kept %d, dropped %d; want 2 and 2: %+v", len(got.Evidence), got.EvidenceDropped, got.Evidence)
	}
	if got.Evidence[0].Action != "logs" || got.Evidence[1].Action != "view" {
		t.Errorf("actions = %q, %q", got.Evidence[0].Action, got.Evidence[1].Action)
	}
	e := got.Evidence[0]
	if e.AccountID != checkoutKey.AccountID || e.Region != checkoutKey.Region || e.ClusterName != checkoutKey.Cluster || e.ServiceName != checkoutKey.Service || e.Tool != "get_service_logs" {
		t.Errorf("evidence = %+v", e)
	}
}

func TestAccept_DropsUnknownToolsAndUncitableTools(t *testing.T) {
	var items []agentresult.Evidence
	for _, tool := range []string{"", "delete_service", "GET_SERVICE", "get_overview", "whoami", "submit_result"} {
		for _, action := range []string{"view", "logs", ""} {
			item := viewItem("x")
			item.Tool, item.Action = tool, action
			items = append(items, item)
		}
	}
	got := Accept(resultWith(items...), readAll)
	if len(got.Evidence) != 0 || got.EvidenceDropped != len(items) {
		t.Errorf("kept %d, dropped %d, want 0 and %d", len(got.Evidence), got.EvidenceDropped, len(items))
	}
}

func TestAccept_DropsUnparsableServiceIDs(t *testing.T) {
	encode := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	bad := []string{
		"", "not an id!", checkoutKey.ClusterKey.ID(), "482109375512/us-east-1/payments-prod/checkout-api",
		encode("482109375512|us-east-1|payments-prod|"),
		encode("482109375512|us-east-1|payments-prod|check\x00out"),
		encode("482109375512|us-east-1|payments-prod|check\xffout"),
		encode("482109375512|us-east-1|payments-prod|" + strings.Repeat("a", 256)),
	}
	var items []agentresult.Evidence
	for _, id := range bad {
		item := viewItem("x")
		item.ServiceID = id
		items = append(items, item)
	}
	got := Accept(resultWith(items...), readAll)
	if len(got.Evidence) != 0 || got.EvidenceDropped != len(bad) {
		t.Fatalf("kept %d, dropped %d, want 0 and %d: %+v", len(got.Evidence), got.EvidenceDropped, len(bad), got.Evidence)
	}
	// A spelling of the id that decodes to a service is stored as that service, never as it was spelled.
	spelled := viewItem("x")
	spelled.ServiceID = checkoutKey.ID()[:8] + "\n" + checkoutKey.ID()[8:]
	got = Accept(resultWith(spelled), readAll)
	if len(got.Evidence) != 1 || got.Evidence[0].ServiceName != checkoutKey.Service || got.Evidence[0].ClusterName != checkoutKey.Cluster {
		t.Errorf("evidence = %+v, want the canonical service", got.Evidence)
	}
}

func TestAccept_DropsWhatTheIdentityCannotRead(t *testing.T) {
	asked := map[model.ServiceKey][]authz.Action{}
	viewOnly := func(key model.ServiceKey, action authz.Action) bool {
		asked[key] = append(asked[key], action)
		return key == checkoutKey && action == authz.View
	}
	other := viewItem("x")
	other.ServiceID = ledgerKey.ID()
	got := Accept(resultWith(logsItem("secret plan"), viewItem("desired 2"), other), viewOnly)
	if len(got.Evidence) != 1 || got.Evidence[0].Action != "view" || got.Evidence[0].ServiceName != checkoutKey.Service || got.EvidenceDropped != 2 {
		t.Fatalf("got %+v", got)
	}
	if len(asked[checkoutKey]) != 2 || asked[checkoutKey][0] != authz.Logs || len(asked[ledgerKey]) != 1 {
		t.Errorf("asked %v", asked)
	}
}

func TestAccept_CapsAtTwelve(t *testing.T) {
	var items []agentresult.Evidence
	for range 15 {
		items = append(items, viewItem("desired 2, running 0"))
	}
	got := Accept(resultWith(items...), readAll)
	if len(got.Evidence) != 12 || got.EvidenceDropped != 3 {
		t.Errorf("kept %d, dropped %d, want 12 and 3", len(got.Evidence), got.EvidenceDropped)
	}
}

func TestAccept_RechecksSummaryNoteAndExcerpt(t *testing.T) {
	for name, summary := range map[string]string{
		"a secret": "It uses " + fakeSecret + " to sign.", "too long": strings.Repeat("a ", 601), "empty": "  ",
		"hidden characters": "It failed\u202e.", "not UTF-8": "It failed\xff.",
	} {
		got := Accept(agentresult.Result{Summary: summary}, readAll)
		if got.Summary != agentresult.ReplacementSummary || !got.SummaryReplaced {
			t.Errorf("%s: summary = %q, replaced %v", name, got.Summary, got.SummaryReplaced)
		}
	}
	got := Accept(agentresult.Result{Summary: "Fine.", SummaryReplaced: true}, readAll)
	if got.Summary != "Fine." || !got.SummaryReplaced {
		t.Errorf("replaced by the agent: %+v", got)
	}

	item := logsItem("login with " + fakeSecret + " refused\x00here " + strings.Repeat("é", 2000))
	item.Note = "Key " + fakeSecret
	long := viewItem("ok")
	long.Note = strings.Repeat("b", 301)
	lines := viewItem("ok")
	lines.Note = "two\nlines"
	got = Accept(resultWith(item, long, lines, viewItem(" \x00\n"), viewItem("")), readAll)
	if len(got.Evidence) != 3 || got.EvidenceDropped != 2 {
		t.Fatalf("kept %d, dropped %d, want 3 and 2 (an empty excerpt shows nothing)", len(got.Evidence), got.EvidenceDropped)
	}
	e := got.Evidence[0]
	if strings.Contains(e.Excerpt, fakeSecret) || strings.ContainsRune(e.Excerpt, 0) || len(e.Excerpt) > agentresult.ExcerptCap || !utf8.ValidString(e.Excerpt) {
		t.Errorf("excerpt of %d bytes: %.80q", len(e.Excerpt), e.Excerpt)
	}
	if e.Note != agentresult.ReplacementNote || got.Evidence[1].Note != agentresult.ReplacementNote || got.Evidence[2].Note != "two lines" {
		t.Errorf("notes = %q, %q, %q", e.Note, got.Evidence[1].Note, got.Evidence[2].Note)
	}
}

func TestAccept_CountsDropped(t *testing.T) {
	bad := viewItem("x")
	bad.Tool = "whoami"
	for name, c := range map[string]struct{ said, want int }{
		"added to the agent's own": {3, 5}, "a negative count is none": {-7, 2}, "a huge count fits the column": {math.MaxInt, 1_000_000},
	} {
		res := resultWith(bad, viewItem("ok"), bad)
		res.EvidenceDropped = c.said
		if got := Accept(res, readAll); got.EvidenceDropped != c.want || len(got.Evidence) != 1 {
			t.Errorf("%s: dropped = %d, kept %d; want %d and 1", name, got.EvidenceDropped, len(got.Evidence), c.want)
		}
	}
}

// The agent's answer is untrusted text: reading a large one must not take quadratic time.
func TestAccept_LargeInputIsLinear(t *testing.T) {
	const size = 256 << 10
	for name, text := range map[string]string{
		"pairs": strings.Repeat("a=", size/2), "quotes": strings.Repeat(`\"`, size/2), "one run": strings.Repeat("A", size),
		"ids": strings.Repeat(checkoutKey.ID()+"/", size/len(checkoutKey.ID())), "hidden": strings.Repeat("\u200b", size/3), "invalid": strings.Repeat("\xff", size),
	} {
		res := agentresult.Result{Summary: text}
		for range 4 {
			item := logsItem(text)
			item.Note = text
			res.Evidence = append(res.Evidence, item)
		}
		start := time.Now()
		got := Accept(res, readAll)
		if took := time.Since(start); took > 20*time.Second {
			t.Errorf("%s: took %v", name, took)
		}
		if !got.SummaryReplaced || len(got.Evidence) != 4 {
			t.Errorf("%s: %d items, replaced %v", name, len(got.Evidence), got.SummaryReplaced)
		}
	}
}
