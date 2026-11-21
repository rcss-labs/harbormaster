package notify

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/mail"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

func testMessage(event Event) Message {
	return Message{
		Event: event, DeliveryID: 42, BaseURL: "https://hm.example.com", RouteName: "Prod",
		Subject: Subject{
			Key: svc("111111111111", "us-east-1", "core", "api"), Kind: "service_failing", Severity: "critical",
			Title: "api: service failing", Text: "0 of 3 tasks are running.", At: time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC),
			AccountName: "prod-account",
		},
	}
}

func TestMessage_Link(t *testing.T) {
	m := testMessage(EventOpened)
	if got, want := m.Link(), "https://hm.example.com/services/"+m.Subject.Key.ID(); got != want {
		t.Errorf("service link = %q, want %q", got, want)
	}
	m.Subject.Key = model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: "111111111111", Region: "us-east-1"}}
	if got, want := m.Link(), "https://hm.example.com/settings/accounts"; got != want {
		t.Errorf("account link = %q, want %q", got, want)
	}
	m.BaseURL = "https://hm.example.com/"
	if strings.Contains(strings.TrimPrefix(m.Link(), "https://"), "//") {
		t.Errorf("link %q has a doubled slash", m.Link())
	}
}

func TestDedupKey_StableAndDistinctPerKind(t *testing.T) {
	a := testMessage(EventOpened).Subject
	if got, want := DedupKey(a), "hm/111111111111/us-east-1/core/api/service_failing"; got != want {
		t.Errorf("DedupKey = %q, want %q", got, want)
	}
	if DedupKey(a) != DedupKey(a) {
		t.Error("DedupKey is not stable")
	}
	b := a
	b.Kind = "restart_loop"
	if DedupKey(a) == DedupKey(b) {
		t.Error("two kinds on one service share a dedup key")
	}
}

func TestSlackBody_EscapesAlertText(t *testing.T) {
	m := testMessage(EventOpened)
	m.Subject.Text = "<!channel> <https://evil|x> & more"
	m.Subject.Title = "api <b>"
	body, err := SlackBody(m)
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	for _, bad := range []string{"<!channel>", "<https://evil|x>"} {
		if strings.Contains(s, bad) {
			t.Errorf("body contains the unescaped %q: %s", bad, s)
		}
	}
	// JSON encoding writes < as \u003c; decode before looking for the escapes.
	var decoded struct {
		Text   string `json:"text"`
		Blocks []struct {
			Type string `json:"type"`
			Text struct {
				Text string `json:"text"`
			} `json:"text"`
			Elements []struct {
				Text string `json:"text"`
			} `json:"elements"`
		} `json:"blocks"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	section := decoded.Blocks[0].Text.Text
	if !strings.Contains(section, "&lt;!channel&gt; &lt;https://evil|x&gt; &amp; more") {
		t.Errorf("section text = %q, want Slack-escaped alert text", section)
	}
	if !strings.Contains(section, ":red_circle: *api &lt;b&gt;*") {
		t.Errorf("section text = %q, want the red circle and escaped title", section)
	}
	context := decoded.Blocks[1].Elements[0].Text
	if !strings.Contains(context, "core") || !strings.Contains(context, "prod-account / us-east-1") || !strings.Contains(context, "|Open in Harbormaster>") {
		t.Errorf("context = %q", context)
	}
	if decoded.Text == "" {
		t.Error("the fallback text is empty")
	}
}

func TestSlackBody_TitlesPerEvent(t *testing.T) {
	warn := testMessage(EventOpened)
	warn.Subject.Severity = "warning"
	resolved := testMessage(EventResolved)
	test := testMessage(EventTest)
	for name, tc := range map[string]struct {
		m    Message
		want string
	}{
		"warning":  {warn, ":large_orange_circle: *api: service failing*"},
		"resolved": {resolved, ":white_check_mark: Resolved: api: service failing"},
		"test":     {test, "Test message from Harbormaster"},
	} {
		body, err := SlackBody(tc.m)
		if err != nil {
			t.Fatal(err)
		}
		var decoded struct {
			Blocks []struct {
				Text struct {
					Text string `json:"text"`
				} `json:"text"`
			} `json:"blocks"`
		}
		_ = json.Unmarshal(body, &decoded)
		if !strings.HasPrefix(decoded.Blocks[0].Text.Text, tc.want) {
			t.Errorf("%s: section = %q, want prefix %q", name, decoded.Blocks[0].Text.Text, tc.want)
		}
	}
}

func TestPagerDutyBody_TriggerAndResolveShareTheDedupKey(t *testing.T) {
	decode := func(m Message) map[string]any {
		body, err := PagerDutyBody(m, "routingkey")
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	opened, repeat, resolved, test := decode(testMessage(EventOpened)), decode(testMessage(EventRepeat)), decode(testMessage(EventResolved)), decode(testMessage(EventTest))
	if opened["event_action"] != "trigger" || repeat["event_action"] != "trigger" || test["event_action"] != "trigger" || resolved["event_action"] != "resolve" {
		t.Errorf("event actions = %v %v %v %v", opened["event_action"], repeat["event_action"], test["event_action"], resolved["event_action"])
	}
	if opened["dedup_key"] != resolved["dedup_key"] || opened["dedup_key"] != "hm/111111111111/us-east-1/core/api/service_failing" {
		t.Errorf("dedup keys: opened %v resolved %v", opened["dedup_key"], resolved["dedup_key"])
	}
	if test["dedup_key"] != "hm/test/42" {
		t.Errorf("test dedup key = %v, want hm/test/42", test["dedup_key"])
	}
	if opened["routing_key"] != "routingkey" || opened["client"] != "Harbormaster" {
		t.Errorf("routing_key/client = %v / %v", opened["routing_key"], opened["client"])
	}
	payload := opened["payload"].(map[string]any)
	if payload["severity"] != "critical" || payload["component"] != "api" || payload["group"] != "core" || payload["class"] != "service_failing" || payload["source"] != "core/api" {
		t.Errorf("payload = %v", payload)
	}
	if opened["links"].([]any)[0].(map[string]any)["text"] != "Open in Harbormaster" {
		t.Errorf("links = %v", opened["links"])
	}
}

func TestPagerDutyBody_TruncatesSummary(t *testing.T) {
	m := testMessage(EventOpened)
	m.Subject.Title = strings.Repeat("é", 2000)
	body, err := PagerDutyBody(m, "k")
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Payload struct {
			Summary string `json:"summary"`
		} `json:"payload"`
	}
	_ = json.Unmarshal(body, &out)
	if n := len([]rune(out.Payload.Summary)); n > 1024 || n == 0 {
		t.Errorf("summary has %d characters, want 1..1024", n)
	}
}

func TestWebhookBody_ShapeAndEventNames(t *testing.T) {
	for event, want := range map[Event]string{EventOpened: "alert.raised", EventRepeat: "alert.repeated", EventResolved: "alert.resolved", EventTest: "test", EventUpdate: "alert.updated"} {
		body, err := WebhookBody(testMessage(event))
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatal(err)
		}
		if out["event"] != want {
			t.Errorf("event %s = %v, want %s", event, out["event"], want)
		}
	}
	body, _ := WebhookBody(testMessage(EventOpened))
	var out map[string]any
	_ = json.Unmarshal(body, &out)
	for key, want := range map[string]any{
		"delivery_id": float64(42), "kind": "service_failing", "severity": "critical", "title": "api: service failing",
		"text": "0 of 3 tasks are running.", "service": "api", "cluster": "core", "account_id": "111111111111", "region": "us-east-1",
		"at": "2026-11-21T12:00:00Z", "url": testMessage(EventOpened).Link(),
	} {
		if out[key] != want {
			t.Errorf("%s = %v, want %v", key, out[key], want)
		}
	}
	if out["scope"] != "service" {
		t.Errorf("scope = %v, want service", out["scope"])
	}
	if len(out) != 13 {
		t.Errorf("payload has %d fields, want the 13 documented: %v", len(out), out)
	}
}

func TestSign_IsHMACSHA256OfTheBody(t *testing.T) {
	body := []byte(`{"a":1}`)
	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write(body)
	if got, want := Sign(body, "s3cret"), "sha256="+hex.EncodeToString(mac.Sum(nil)); got != want {
		t.Errorf("Sign = %q, want %q", got, want)
	}
}

func TestEmail_HeadersCannotBeInjected(t *testing.T) {
	m := testMessage(EventOpened)
	m.Subject.Title = "api failing\r\nBcc: x@y.example"
	subject, raw := Email(m, "Harbormaster <hm@example.com>", []string{"ops@example.com", "dev@example.com"})
	if strings.ContainsAny(subject, "\r\n") {
		t.Errorf("subject %q contains a line break", subject)
	}
	parsed, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if got := parsed.Header["Subject"]; len(got) != 1 {
		t.Errorf("Subject headers = %v, want exactly one", got)
	}
	if _, present := parsed.Header["Bcc"]; present {
		t.Error("an injected Bcc header was created")
	}
	if got := parsed.Header.Get("To"); !strings.Contains(got, "ops@example.com") || !strings.Contains(got, "dev@example.com") {
		t.Errorf("To = %q", got)
	}
	if ct := parsed.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") || !strings.Contains(ct, "utf-8") {
		t.Errorf("Content-Type = %q", ct)
	}
	body := new(bytes.Buffer)
	body.ReadFrom(parsed.Body)
	for _, want := range []string{"0 of 3 tasks are running.", "core", "111111111111", "us-east-1", m.Link()} {
		if !strings.Contains(body.String(), want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
}

func TestEmail_SubjectPrefixesAndNonASCII(t *testing.T) {
	m := testMessage(EventResolved)
	m.Subject.Title = "api: tâches arrêtées"
	subject, raw := Email(m, "hm@example.com", []string{"ops@example.com"})
	if !strings.Contains(subject, "Resolved: ") || !strings.Contains(subject, "[Harbormaster]") {
		t.Errorf("subject = %q", subject)
	}
	if !strings.Contains(string(raw), "=?utf-8?q?") {
		t.Errorf("a non-ASCII subject must be Q-encoded:\n%s", raw)
	}
}

func TestRenderPreview_UsesTheSenderBodies(t *testing.T) {
	m := testMessage(EventOpened)
	preview, err := RenderPreview(DestWebhook, m)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := WebhookBody(m)
	var indented bytes.Buffer
	if err := json.Indent(&indented, body, "", "  "); err != nil {
		t.Fatal(err)
	}
	if preview.JSON != indented.String() || preview.Destination != DestWebhook {
		t.Errorf("webhook preview = %+v, want the indented WebhookBody", preview)
	}
	for _, dest := range []string{DestSlack, DestPagerDuty, DestEmail} {
		p, err := RenderPreview(dest, m)
		if err != nil || p.Title == "" || p.Body == "" {
			t.Errorf("%s preview = %+v err=%v, want a title and body", dest, p, err)
		}
	}
	if _, err := RenderPreview("pager", m); err == nil {
		t.Error("an unknown destination must be an error")
	}
}

func groupMessage(event Event) Message {
	cluster := model.ClusterKey{AccountID: "111111111111", Region: "us-east-1", Cluster: "core"}
	return Message{
		Event: event, DeliveryID: 9, BaseURL: "https://hm.example.com", RouteName: "Half of core",
		Subject: Subject{
			Key: model.ServiceKey{ClusterKey: cluster}, Kind: KindClusterFailing, Severity: "critical", Scope: "cluster", RouteID: 7,
			Title: "core: 3 of 5 services failing", Text: "api is failing\nweb is failing\nand 1 more\nNotifies when 2 or more services are failing.",
			At: time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC), AccountName: "prod-account", Threshold: 2, Failing: 3, Total: 5,
			Members: []store.IncidentMember{
				{Cluster: "core", Service: "api", Kind: "service_failing", Title: "api is failing", Critical: true},
				{Cluster: "core", Service: "web", Kind: "restart_loop", Title: "web is failing"},
			},
		},
	}
}

func TestMessage_LinkForGroups(t *testing.T) {
	m := groupMessage(EventOpened)
	if got, want := m.Link(), "https://hm.example.com/clusters/"+m.Subject.Key.ClusterKey.ID(); got != want {
		t.Errorf("cluster link = %q, want %q", got, want)
	}
	m.Subject.Scope = "account"
	m.Subject.Key = model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: "111111111111"}}
	if got, want := m.Link(), "https://hm.example.com/?account=111111111111"; got != want {
		t.Errorf("account link = %q, want %q", got, want)
	}
	m.Subject.Key.Region = "eu-west-1"
	if got, want := m.Link(), "https://hm.example.com/?account=111111111111&region=eu-west-1"; got != want {
		t.Errorf("account x region link = %q, want %q", got, want)
	}
}

func TestDedupKey_GroupIsPerRoute(t *testing.T) {
	a := groupMessage(EventOpened).Subject
	if got, want := DedupKey(a), "hm/route-7/cluster/111111111111/us-east-1/core"; got != want {
		t.Errorf("DedupKey = %q, want %q", got, want)
	}
	b := a
	b.RouteID = 8
	if DedupKey(a) == DedupKey(b) {
		t.Error("two routes on one cluster share a dedup key: one would resolve the other's incident")
	}
	if DedupKey(groupMessage(EventOpened).Subject) != DedupKey(groupMessage(EventResolved).Subject) {
		t.Error("opened and resolved of one group must share the key")
	}
}

func TestWebhookBody_GroupShapeAndUpdatedEvent(t *testing.T) {
	body, err := WebhookBody(groupMessage(EventUpdate))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if out["event"] != "alert.updated" || out["scope"] != "cluster" || out["service"] != "" || out["cluster"] != "core" ||
		out["threshold"] != float64(2) || out["failing"] != float64(3) || out["total"] != float64(5) {
		t.Errorf("group body = %v", out)
	}
	services, _ := out["services"].([]any)
	if len(services) != 2 || services[0].(map[string]any)["service"] != "api" || services[0].(map[string]any)["title"] != "api is failing" {
		t.Errorf("services = %v", out["services"])
	}
	// An alert-level body carries no group fields; an account-level alert under an empty scope is scope account.
	alert, _ := WebhookBody(testMessage(EventOpened))
	var plain map[string]any
	_ = json.Unmarshal(alert, &plain)
	for _, group := range []string{"threshold", "failing", "total", "services"} {
		if _, present := plain[group]; present {
			t.Errorf("an alert body carries %q", group)
		}
	}
	accountLevel := testMessage(EventOpened)
	accountLevel.Subject.Key = model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: "111111111111", Region: "us-east-1"}}
	accountBody, _ := WebhookBody(accountLevel)
	var acct map[string]any
	_ = json.Unmarshal(accountBody, &acct)
	if acct["scope"] != "account" {
		t.Errorf("account-level alert scope = %v, want account", acct["scope"])
	}
}

func TestPagerDutyBody_UpdateIsATriggerWithTheSameKey(t *testing.T) {
	decode := func(m Message) map[string]any {
		body, err := PagerDutyBody(m, "key")
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		_ = json.Unmarshal(body, &out)
		return out
	}
	opened, update, resolved := decode(groupMessage(EventOpened)), decode(groupMessage(EventUpdate)), decode(groupMessage(EventResolved))
	if update["event_action"] != "trigger" || resolved["event_action"] != "resolve" || update["dedup_key"] != opened["dedup_key"] || update["dedup_key"] != resolved["dedup_key"] {
		t.Errorf("update %v opened %v resolved %v", update, opened["dedup_key"], resolved["event_action"])
	}
	payload := update["payload"].(map[string]any)
	if payload["source"] != "core" || payload["component"] != "" || !strings.HasPrefix(payload["summary"].(string), "Update: ") {
		t.Errorf("payload = %v", payload)
	}
	details := payload["custom_details"].(map[string]any)
	names, _ := details["failing_services"].([]any)
	if details["failing"] != float64(3) || details["total"] != float64(5) || details["threshold"] != float64(2) || len(names) != 2 || names[0] != "api" {
		t.Errorf("custom_details = %v", details)
	}
}

func TestSlackBody_UpdateHeadline(t *testing.T) {
	body, err := SlackBody(groupMessage(EventUpdate))
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Blocks []struct {
			Text struct {
				Text string `json:"text"`
			} `json:"text"`
		} `json:"blocks"`
	}
	_ = json.Unmarshal(body, &decoded)
	if got := decoded.Blocks[0].Text.Text; !strings.HasPrefix(got, ":red_circle: *Update: core: 3 of 5 services failing*") {
		t.Errorf("section = %q", got)
	}
	if subject, _ := Email(groupMessage(EventUpdate), "hm@example.com", []string{"a@example.com"}); !strings.Contains(subject, "Update: core: 3 of 5") {
		t.Errorf("email subject = %q", subject)
	}
}
