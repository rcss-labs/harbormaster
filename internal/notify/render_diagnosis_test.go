package notify

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/rcss-labs/harbormaster/internal/agentresult"
)

func diagnosisMessage(summary string) Message {
	m := testMessage(EventDiagnosis)
	m.Diagnosis = &Diagnosis{InvestigationID: 77, Summary: summary}
	return m
}

const ownInvestigation = "https://hm.example.com/investigations/77"

func TestDiagnosis_Slack_EscapesAndDisablesUnfurl(t *testing.T) {
	m := diagnosisMessage("pods <!channel> & <@U123> crashed")
	m.Subject.Title = "api <!here> & down"
	body, err := SlackBody(m)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Text        string `json:"text"`
		UnfurlLinks *bool  `json:"unfurl_links"`
		UnfurlMedia *bool  `json:"unfurl_media"`
		Blocks      []struct {
			Text     struct{ Text string }   `json:"text"`
			Elements []struct{ Text string } `json:"elements"`
		} `json:"blocks"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.UnfurlLinks == nil || *got.UnfurlLinks || got.UnfurlMedia == nil || *got.UnfurlMedia {
		t.Errorf("unfurl_links and unfurl_media must both be false: %s", body)
	}
	if len(got.Blocks) != 2 {
		t.Fatalf("blocks = %d, want 2: %s", len(got.Blocks), body)
	}
	section, context := got.Blocks[0].Text.Text, got.Blocks[1].Elements[0].Text
	if !strings.HasPrefix(section, "*Diagnosis: api &lt;!here&gt; &amp; down*\n") {
		t.Errorf("headline = %q", section)
	}
	if !strings.Contains(section, "pods &lt;!channel&gt; &amp; &lt;@U123&gt; crashed") {
		t.Errorf("summary not escaped: %q", section)
	}
	if strings.Contains(section, "<!") || strings.Contains(section, "<@") {
		t.Errorf("raw Slack control sequence in %q", section)
	}
	if want := "<" + ownInvestigation + "|Open the investigation>"; !strings.Contains(context, want) {
		t.Errorf("context %q lacks %q", context, want)
	}
	if !strings.Contains(context, "core · prod-account / us-east-1") {
		t.Errorf("context %q lacks the where line", context)
	}
	if !strings.HasPrefix(got.Text, "Diagnosis: ") {
		t.Errorf("fallback text = %q", got.Text)
	}
}

func TestDiagnosis_Telegram_EscapesFitsAndDisablesPreview(t *testing.T) {
	m := diagnosisMessage(strings.Repeat("😀", 1200))
	m.Subject.Title = strings.Repeat("t", 6000)
	got := TelegramText(m)
	if n := utf16Len(got); n > 4096 {
		t.Errorf("%d UTF-16 units, want at most 4096", n)
	}
	if !utf8.ValidString(got) || !strings.HasPrefix(got, "<b>Diagnosis: ") {
		t.Errorf("text = %.80q", got)
	}
	if !strings.Contains(got, strings.Repeat("😀", 1200)) {
		t.Error("a 1200-rune summary that fits was cut")
	}
	if !strings.HasSuffix(got, `<a href="`+ownInvestigation+`">Open the investigation</a>`) {
		t.Errorf("link missing at the end: %q", got[max(0, len(got)-120):])
	}

	// Escaping makes the text longer than the budget: it is cut on an entity boundary and the link survives.
	m = diagnosisMessage(strings.Repeat("&", 1200))
	m.Subject.Title = "a <b>"
	got = TelegramText(m)
	if n := utf16Len(got); n > 4096 {
		t.Errorf("escaped: %d UTF-16 units", n)
	}
	if !strings.Contains(got, "<b>Diagnosis: a &lt;b&gt;</b>") || !strings.Contains(got, "…") ||
		!strings.HasSuffix(got, `Open the investigation</a>`) {
		t.Errorf("escaped text = %.120q", got)
	}
	for _, part := range strings.Split(got, "&")[1:] {
		if !strings.HasPrefix(part, "amp;") && !strings.HasPrefix(part, "lt;") && !strings.HasPrefix(part, "gt;") {
			t.Errorf("partial entity near %q", part[:min(len(part), 10)])
			break
		}
	}

	body, err := TelegramBody(m, "-100123", 0)
	if err != nil {
		t.Fatal(err)
	}
	var sent struct {
		Options struct {
			Disabled bool `json:"is_disabled"`
		} `json:"link_preview_options"`
		ParseMode string `json:"parse_mode"`
	}
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatal(err)
	}
	if !sent.Options.Disabled || sent.ParseMode != "HTML" {
		t.Errorf("preview must be disabled and parse mode HTML: %s", body)
	}
}

func TestDiagnosis_Email(t *testing.T) {
	m := diagnosisMessage("The task ran out of memory.\nRestart it.")
	m.Subject.Title = "api down\r\nBcc: evil@example.com"
	subject, raw := Email(m, "hm@example.com", []string{"ops@example.com"})
	if want := "Diagnosis: api down  Bcc: evil@example.com"; !strings.Contains(subject, want) || strings.ContainsAny(subject, "\r\n") {
		t.Errorf("subject = %q, want it to contain %q on one line", subject, want)
	}
	head, body, _ := strings.Cut(string(raw), "\r\n\r\n")
	if strings.Contains(head, "\r\nBcc:") {
		t.Errorf("header injection: %q", head)
	}
	if want := "The task ran out of memory.\r\nRestart it.\r\n\r\n" + ownInvestigation + "\r\n"; body != want {
		t.Errorf("body = %q, want %q", body, want)
	}
}

func TestDiagnosis_Webhook_EventAndFields(t *testing.T) {
	body, err := WebhookBody(diagnosisMessage("Out of memory."))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got["event"] != "diagnosis" || got["summary"] != "Out of memory." || got["investigation_id"] != float64(77) ||
		got["url"] != ownInvestigation {
		t.Errorf("body = %s", body)
	}
	for _, key := range []string{"delivery_id", "scope", "kind", "severity", "title", "service", "cluster", "account_id", "region", "at"} {
		if _, ok := got[key]; !ok {
			t.Errorf("flat object lost %q: %s", key, body)
		}
	}
	// Signature and headers are the existing path: the body is signed as any other.
	if Sign(body, "s") == "" {
		t.Error("unsignable")
	}
}

func TestDiagnosis_PagerDutyIsRefused(t *testing.T) {
	body, err := PagerDutyBody(diagnosisMessage("x"), "fake-routing-key")
	if err == nil || body != nil {
		t.Errorf("PagerDutyBody = %q, %v; want an error and no body", body, err)
	}
	if _, err := RenderPreview(DestPagerDuty, diagnosisMessage("x")); err == nil {
		t.Error("the preview of a PagerDuty diagnosis must be refused too")
	}
}

func TestDiagnosis_ForeignURLsRemovedOwnURLKept(t *testing.T) {
	summary := "See https://evil.example/x and " + ownInvestigation + " for details."
	m := diagnosisMessage(summary)
	slack, _ := SlackBody(m)
	hook, _ := WebhookBody(m)
	_, mail := Email(m, "hm@example.com", []string{"ops@example.com"})
	for name, out := range map[string]string{
		"slack": string(slack), "telegram": TelegramText(m), "email": string(mail), "webhook": string(hook),
	} {
		if strings.Contains(out, "evil.example") {
			t.Errorf("%s: foreign URL survived: %s", name, out)
		}
		if !strings.Contains(out, agentresult.URLPlaceholder) {
			t.Errorf("%s: no placeholder in %s", name, out)
		}
		if !strings.Contains(out, ownInvestigation) {
			t.Errorf("%s: own URL gone from %s", name, out)
		}
	}
}

func TestDiagnosis_SummaryIsRecheckedAtSendTime(t *testing.T) {
	stored := "the key is " + "AKIA" + "IOSFODNN7EXAMPLE"
	m := diagnosisMessage(stored)
	slack, _ := SlackBody(m)
	hook, _ := WebhookBody(m)
	_, mail := Email(m, "hm@example.com", []string{"ops@example.com"})
	for name, out := range map[string]string{
		"slack": string(slack), "telegram": TelegramText(m), "email": string(mail), "webhook": string(hook),
	} {
		if strings.Contains(out, "IOSFODNN7EXAMPLE") {
			t.Errorf("%s: token-shaped value left the building: %s", name, out)
		}
		if !strings.Contains(out, agentresult.ReplacementSummary) {
			t.Errorf("%s: no replacement summary in %s", name, out)
		}
	}
	if got := CleanSummary(stored, m.BaseURL); got != agentresult.ReplacementSummary {
		t.Errorf("CleanSummary = %q", got)
	}
	if got := CleanSummary("Plain text.", m.BaseURL); got != "Plain text." {
		t.Errorf("CleanSummary changed a clean summary: %q", got)
	}
}

func TestDiagnosis_LinkPointsAtTheInvestigation(t *testing.T) {
	m := diagnosisMessage("x")
	if got := m.Link(); got != ownInvestigation {
		t.Errorf("Link = %q", got)
	}
	m.BaseURL = "https://hm.example.com/"
	if got := m.Link(); got != ownInvestigation {
		t.Errorf("Link with a trailing slash = %q", got)
	}
	// A diagnosis without its record has no investigation to point at; it falls back to the service page.
	m.Diagnosis = nil
	if got, want := m.Link(), "https://hm.example.com/services/"+m.Subject.Key.ID(); got != want {
		t.Errorf("Link without a diagnosis = %q, want %q", got, want)
	}
}

func pagerDutyLink(t *testing.T, m Message) string {
	t.Helper()
	body, err := PagerDutyBody(m, "fake-routing-key")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Links []struct{ Href string } `json:"links"`
	}
	if err := json.Unmarshal(body, &got); err != nil || len(got.Links) != 1 {
		t.Fatalf("body = %s, err %v", body, err)
	}
	return got.Links[0].Href
}

func TestPagerDuty_OpenedLinkWhenInvestigating(t *testing.T) {
	m := testMessage(EventOpened)
	m.IncidentID, m.Investigate = 9, true
	if got, want := pagerDutyLink(t, m), "https://hm.example.com/investigations/incident/9"; got != want {
		t.Errorf("Opened link = %q, want %q", got, want)
	}
	// Every other message keeps its link: other events, switch off, no incident id, other destinations.
	service := "https://hm.example.com/services/" + m.Subject.Key.ID()
	for _, event := range []Event{EventRepeat, EventUpdate, EventResolved, EventTest} {
		other := testMessage(event)
		other.IncidentID, other.Investigate = 9, true
		if got := pagerDutyLink(t, other); got != service {
			t.Errorf("%s link = %q, want %q", event, got, service)
		}
	}
	off := testMessage(EventOpened)
	off.IncidentID = 9
	if got := pagerDutyLink(t, off); got != service {
		t.Errorf("switch off: link = %q", got)
	}
	noID := testMessage(EventOpened)
	noID.Investigate = true
	if got := pagerDutyLink(t, noID); got != service {
		t.Errorf("no incident id: link = %q", got)
	}
	if got := m.Link(); got != service {
		t.Errorf("Message.Link for Opened changed to %q", got)
	}
}
