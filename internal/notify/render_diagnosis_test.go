package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math/rand"
	"net/mail"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"mime/quotedprintable"

	"github.com/rcss-labs/harbormaster/internal/agentresult"
	"github.com/rcss-labs/harbormaster/internal/secretshape"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const (
	aiNotice         = "Written by AI. Check it before acting."
	ownInvestigation = "https://hm.example.com/investigations/77"
)

func diagnosisMessage(summary string) Message {
	m := testMessage(EventDiagnosis)
	m.Diagnosis = &Diagnosis{InvestigationID: 77, Summary: summary}
	return m
}

type slackBlock struct {
	Type string `json:"type"`
	Text *struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Emoji    *bool  `json:"emoji"`
		Verbatim *bool  `json:"verbatim"`
	} `json:"text"`
	Elements []struct {
		Text     string `json:"text"`
		Verbatim *bool  `json:"verbatim"`
	} `json:"elements"`
}

type slackPayload struct {
	Text        string       `json:"text"`
	UnfurlLinks *bool        `json:"unfurl_links"`
	UnfurlMedia *bool        `json:"unfurl_media"`
	Blocks      []slackBlock `json:"blocks"`
}

func slackDiagnosis(t *testing.T, m Message) slackPayload {
	t.Helper()
	body, err := SlackBody(m)
	if err != nil {
		t.Fatal(err)
	}
	var got slackPayload
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Blocks) != 3 || got.Blocks[0].Text == nil || got.Blocks[1].Text == nil {
		t.Fatalf("want headline section, summary section, context: %s", body)
	}
	return got
}

func TestDiagnosis_Slack_EscapesAndDisablesUnfurl(t *testing.T) {
	summary := "pods <!channel> & <@U123> :tada: &gt; @here #general `x` crashed"
	m := diagnosisMessage(summary)
	m.Subject.Title = "api <!here> & down"
	got := slackDiagnosis(t, m)
	if got.UnfurlLinks == nil || *got.UnfurlLinks || got.UnfurlMedia == nil || *got.UnfurlMedia {
		t.Error("unfurl_links and unfurl_media must both be false")
	}
	head, sum, context := got.Blocks[0], got.Blocks[1], got.Blocks[2].Elements[0].Text
	if head.Type != "section" || head.Text.Type != "mrkdwn" || head.Text.Verbatim == nil || !*head.Text.Verbatim {
		t.Errorf("headline must be a mrkdwn section with verbatim true: %+v", head.Text)
	}
	if want := "*Diagnosis: api &lt;!here&gt; &amp; down*\n" + aiNotice; head.Text.Text != want {
		t.Errorf("headline = %q, want %q", head.Text.Text, want)
	}
	// The summary is plain text: nothing in it is interpreted, so nothing in it is escaped either.
	if sum.Type != "section" || sum.Text.Type != "plain_text" || sum.Text.Emoji == nil || *sum.Text.Emoji || sum.Text.Text != summary {
		t.Errorf("summary section = %+v %q", sum.Text, sum.Text.Text)
	}
	if v := got.Blocks[2].Elements[0].Verbatim; v == nil || !*v {
		t.Error("the context line must be verbatim")
	}
	if want := "<" + ownInvestigation + "|Open the investigation>"; !strings.Contains(context, want) {
		t.Errorf("context %q lacks %q", context, want)
	}
	if !strings.Contains(context, "core · prod-account / us-east-1") {
		t.Errorf("context %q lacks the where line", context)
	}
	if want := "Diagnosis: api &lt;!here&gt; &amp; down"; got.Text != want {
		t.Errorf("fallback text = %q, want %q", got.Text, want)
	}
}

func TestDiagnosis_Slack_HeadlineIsEscapedAndCapped(t *testing.T) {
	titles := map[string]string{
		"spoofed event": ":white_check_mark: *Resolved: all clear",
		"entity":        "&gt; not a quote",
		"mentions":      "@here #general <!channel>",
		"ampersands":    strings.Repeat("&", 1200),
		"slashes":       strings.Repeat("// ", 400),
		"long":          strings.Repeat("t", 6000),
		"astral":        strings.Repeat("😀", 6000),
	}
	for name, title := range titles {
		m := diagnosisMessage("ok")
		m.Subject.Title = title
		got := slackDiagnosis(t, m)
		head := got.Blocks[0].Text.Text
		if !strings.HasPrefix(head, "*Diagnosis: ") || !strings.HasSuffix(head, "*\n"+aiNotice) {
			t.Errorf("%s: headline = %.80q", name, head)
		}
		if n := utf8.RuneCountInString(head); n > 400 {
			t.Errorf("%s: headline has %d characters, want it capped", name, n)
		}
		if !utf8.ValidString(head) || !utf8.ValidString(got.Text) {
			t.Errorf("%s: invalid UTF-8", name)
		}
		if strings.Contains(head, "<!") || strings.Contains(got.Text, "<!") || strings.Contains(head, "<@") {
			t.Errorf("%s: a raw control sequence in %q", name, head)
		}
		// A cut never leaves half an entity.
		for _, part := range strings.Split(head+got.Text, "&")[1:] {
			if !strings.HasPrefix(part, "amp;") && !strings.HasPrefix(part, "lt;") && !strings.HasPrefix(part, "gt;") {
				t.Errorf("%s: a partial entity near %q", name, part[:min(len(part), 10)])
				break
			}
		}
		if got.Blocks[0].Text.Verbatim == nil || !*got.Blocks[0].Text.Verbatim {
			t.Errorf("%s: headline lost verbatim", name)
		}
	}
	m := diagnosisMessage("ok")
	m.Subject.Title = ":white_check_mark: *Resolved: x"
	if head := slackDiagnosis(t, m).Blocks[0].Text.Text; !strings.HasPrefix(head, "*Diagnosis: :white_check_mark:") {
		t.Errorf("a title must not get in front of the fixed prefix: %q", head)
	}
	m.Subject.Title = "&gt;"
	if head := slackDiagnosis(t, m).Blocks[0].Text.Text; !strings.Contains(head, "&amp;gt;") {
		t.Errorf("an entity in the title must be escaped: %q", head)
	}
}

func TestDiagnosis_Slack_SummarySectionIsCutOnARuneBoundary(t *testing.T) {
	long := strings.Repeat("😀", 5000)
	got := slackPlainText(long)
	if utf8.RuneCountInString(got) != 3000 || !utf8.ValidString(got) {
		t.Errorf("cut to %d runes, valid %v", utf8.RuneCountInString(got), utf8.ValidString(got))
	}
	if slackPlainText("short") != "short" {
		t.Error("a short text must stay as it is")
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
	if !strings.Contains(got, "<pre>"+strings.Repeat("😀", 1200)+"</pre>") {
		t.Error("a 1200-rune summary that fits was cut or not preformatted")
	}
	if !strings.HasSuffix(got, `<a href="`+ownInvestigation+`">Open the investigation</a>`) {
		t.Errorf("link missing at the end: %q", got[max(0, len(got)-120):])
	}

	// Escaping makes the text longer than the budget: it is cut on an entity boundary, the tags are counted, the
	// link survives.
	m = diagnosisMessage(strings.Repeat("&", 1200))
	m.Subject.Title = "a <b>"
	got = TelegramText(m)
	if n := utf16Len(got); n > 4096 {
		t.Errorf("escaped: %d UTF-16 units", n)
	}
	if !strings.Contains(got, "<b>Diagnosis: a &lt;b&gt;</b>") || !strings.Contains(got, "…</pre>") ||
		!strings.HasSuffix(got, `Open the investigation</a>`) {
		t.Errorf("escaped text = %.140q", got)
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

func TestDiagnosis_Telegram_SummaryIsPreformatted(t *testing.T) {
	summary := "page @oncall_lead, run /stop on evil.com #x </pre><a href=\"https://evil.example\">go</a>"
	got := TelegramText(diagnosisMessage(summary))
	start := strings.Index(got, "<pre>")
	end := strings.LastIndex(got, "</pre>")
	if start < 0 || end < start || strings.Count(got, "<pre>") != 1 || strings.Count(got, "</pre>") != 1 {
		t.Fatalf("the summary must sit in exactly one pre element: %q", got)
	}
	inside, outside := got[start:end], got[:start]+got[end:]
	for _, trigger := range []string{"@oncall_lead", "/stop", "evil.com", "#x"} {
		if !strings.Contains(inside, trigger) {
			t.Errorf("%q is missing from the summary", trigger)
		}
		if strings.Contains(outside, trigger) {
			t.Errorf("%q appears outside the pre element, where Telegram would link it: %q", trigger, outside)
		}
	}
	if !strings.Contains(inside, "&lt;/pre&gt;&lt;a ") {
		t.Errorf("a closing tag in the summary must be escaped: %q", inside)
	}
	if !strings.Contains(outside, aiNotice) || strings.Index(got, aiNotice) > start {
		t.Errorf("the notice must come before the summary: %q", got)
	}
}

func decodedEmailBody(t *testing.T, raw []byte) (header mail.Header, body string) {
	t.Helper()
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	reader := io.Reader(msg.Body)
	if msg.Header.Get("Content-Transfer-Encoding") == "quoted-printable" {
		reader = quotedprintable.NewReader(msg.Body)
	}
	decoded, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return msg.Header, string(decoded)
}

func TestDiagnosis_Email(t *testing.T) {
	m := diagnosisMessage("The task ran out of memory.\nRestart it.")
	m.Subject.Title = "api down\r\nBcc: evil@example.com"
	subject, raw := Email(m, "hm@example.com", []string{"ops@example.com"})
	if want := "Diagnosis: api down  Bcc: evil@example.com"; !strings.Contains(subject, want) || strings.ContainsAny(subject, "\r\n") {
		t.Errorf("subject = %q, want it to contain %q on one line", subject, want)
	}
	head, body := decodedEmailBody(t, raw)
	if head.Get("Bcc") != "" || head.Get("Content-Transfer-Encoding") != "quoted-printable" {
		t.Errorf("headers = %v", head)
	}
	want := aiNotice + "\r\n\r\nThe task ran out of memory.\r\nRestart it.\r\n\r\n" + ownInvestigation + "\r\n"
	if body != want {
		t.Errorf("body = %q, want %q", body, want)
	}
}

func TestDiagnosis_Email_LinesStayShort(t *testing.T) {
	for name, summary := range map[string]string{
		"ascii":     strings.Repeat("abcdefghij", 120),
		"cjk":       strings.Repeat("故障", 600),
		"long word": strings.Repeat("a=b", 400),
		"lines":     strings.Repeat("line one\nline two\r\n", 60),
		"spaces":    "trailing space \nand tab\t\nend ",
	} {
		_, raw := Email(diagnosisMessage(summary), "hm@example.com", []string{"ops@example.com"})
		_, wire, _ := strings.Cut(string(raw), "\r\n\r\n")
		for _, line := range strings.Split(wire, "\r\n") {
			if len(line) > 998 {
				t.Errorf("%s: a %d-octet line", name, len(line))
				break
			}
		}
		_, body := decodedEmailBody(t, raw)
		want := aiNotice + "\r\n\r\n" + crlf(CleanSummary(summary, "https://hm.example.com", secretshape.Names{})) + "\r\n\r\n" + ownInvestigation + "\r\n"
		if body != want {
			t.Errorf("%s: the body does not decode to the text:\n got %.120q\nwant %.120q", name, body, want)
		}
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
		got["url"] != ownInvestigation || got["ai_generated"] != true {
		t.Errorf("body = %s", body)
	}
	for _, key := range []string{"delivery_id", "scope", "kind", "severity", "title", "service", "cluster", "account_id", "region", "at"} {
		if _, ok := got[key]; !ok {
			t.Errorf("flat object lost %q: %s", key, body)
		}
	}
	other, _ := WebhookBody(testMessage(EventOpened))
	if bytes.Contains(other, []byte("ai_generated")) {
		t.Error("only a diagnosis says it is AI generated")
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
	ownService := "https://hm.example.com/services/x"
	summary := "See https://evil.example/x and " + ownInvestigation + " and " + ownService + " for details."
	m := diagnosisMessage(summary)
	slack, _ := SlackBody(m)
	hook, _ := WebhookBody(m)
	_, mail := Email(m, "hm@example.com", []string{"ops@example.com"})
	_, mailBody := decodedEmailBody(t, mail)
	for name, out := range map[string]string{
		"slack": string(slack), "telegram": TelegramText(m), "email": mailBody, "webhook": string(hook),
	} {
		if strings.Contains(out, "evil.example") {
			t.Errorf("%s: foreign URL survived: %s", name, out)
		}
		if !strings.Contains(out, agentresult.URLPlaceholder) {
			t.Errorf("%s: no placeholder in %s", name, out)
		}
		if !strings.Contains(out, ownInvestigation) || !strings.Contains(out, ownService) {
			t.Errorf("%s: own URL gone from %s", name, out)
		}
	}
}

func TestCleanSummary_OwnLinksAndForeignLinks(t *testing.T) {
	for _, tc := range []struct{ name, base, in, want string }{
		{"service page", "https://hm.example.com", "open https://hm.example.com/services/x now", "open https://hm.example.com/services/x now"},
		{"trailing slash base", "https://hm.example.com/", "https://hm.example.com/services/x", "https://hm.example.com/services/x"},
		{"base with a path", "https://hm.example.com/hm", "https://hm.example.com/hm/services/x", "https://hm.example.com/hm/services/x"},
		{"lookalike host", "https://hm.example.com", "https://hm.example.com.evil.io/x", agentresult.URLPlaceholder},
		{"foreign", "https://hm.example.com", "go to https://evil.example/x", "go to " + agentresult.URLPlaceholder},
		{"no base", "", "https://hm.example.com/services/x", agentresult.URLPlaceholder},
	} {
		if got := CleanSummary(tc.in, tc.base, secretshape.Names{}); got != tc.want {
			t.Errorf("%s: CleanSummary(%q, %q) = %q, want %q", tc.name, tc.in, tc.base, got, tc.want)
		}
	}
}

func TestCleanSummary_ScrubsBeforeLinksAreRemoved(t *testing.T) {
	for _, in := range []string{
		"db.example.com:5432/password: Hunter2Secret9x",
		"//x-api-key: Zx81kq02mmLw93hhTT",
		"http://x/Bearer abcdefghijklmnop1234567890ABCDEF",
		"//Authorization: Basic dXNlcjpwYXNzd29yZDEyMzQ1Ng==",
		"//-----BEGIN RSA PRIVATE KEY----- MIIEow",
	} {
		if got := CleanSummary(in, "https://hm.example.com", secretshape.Names{}); got != agentresult.ReplacementSummary {
			t.Errorf("CleanSummary(%q) = %q, want the replacement summary", in, got)
		}
	}
}

func TestDiagnosis_SummaryIsRecheckedAtSendTime(t *testing.T) {
	stored := "the key is " + "AKIA" + "IOSFODNN7EXAMPLE"
	m := diagnosisMessage(stored)
	slack, _ := SlackBody(m)
	hook, _ := WebhookBody(m)
	_, mail := Email(m, "hm@example.com", []string{"ops@example.com"})
	_, mailBody := decodedEmailBody(t, mail)
	for name, out := range map[string]string{
		"slack": string(slack), "telegram": TelegramText(m), "email": mailBody, "webhook": string(hook),
	} {
		if strings.Contains(out, "IOSFODNN7EXAMPLE") {
			t.Errorf("%s: token-shaped value left the building: %s", name, out)
		}
		if !strings.Contains(out, agentresult.ReplacementSummary) {
			t.Errorf("%s: no replacement summary in %s", name, out)
		}
	}
	if got := CleanSummary(stored, m.BaseURL, secretshape.Names{}); got != agentresult.ReplacementSummary {
		t.Errorf("CleanSummary = %q", got)
	}
	if got := CleanSummary("Plain text.", m.BaseURL, secretshape.Names{}); got != "Plain text." {
		t.Errorf("CleanSummary changed a clean summary: %q", got)
	}
}

func TestCleanSummary_CapSafeAndIdempotent(t *testing.T) {
	const base = "https://hm.example.com"
	fragments := []string{
		"http://a ", "https://hm.example.com/services/x ", "https://evil.example/p?q=1 ", "www.evil.io ", "a.b ", "[link removed]",
		" ", "\n", "word", "😀", "故", "[x](", "](", "mailto:a@b.c ", "AKIA" + "IOSFODNN7EXAMPLE", "x", "://", "//", "evil.com/x",
		"\u202e", "\u200b", "`", "<", "&",
	}
	check := func(label, in string) {
		once := CleanSummary(in, base, secretshape.Names{})
		if n := utf8.RuneCountInString(once); n > agentresult.SummaryCap {
			t.Errorf("%s: %d runes after cleaning, cap %d (input %.60q)", label, n, agentresult.SummaryCap, in)
		}
		if twice := CleanSummary(once, base, secretshape.Names{}); twice != once {
			t.Errorf("%s: not idempotent for %.60q:\n once %.80q\ntwice %.80q", label, in, once, twice)
		}
	}
	for name, in := range map[string]string{
		"empty":             "",
		"plain":             "The service ran out of memory.",
		"url growth":        strings.Repeat("http://a ", 100), // 900 runes in, 1500 once every link is replaced
		"at the cap":        strings.Repeat("x", agentresult.SummaryCap),
		"over the cap":      strings.Repeat("x", agentresult.SummaryCap+1),
		"placeholder":       strings.Repeat("[link removed] ", 80),
		"token":             "key " + "AKIA" + "IOSFODNN7EXAMPLE",
		"own and foreign":   "https://hm.example.com/services/x https://evil.example",
		"replacement again": agentresult.ReplacementSummary,
	} {
		check(name, in)
	}
	if got := CleanSummary(strings.Repeat("http://a ", 100), base, secretshape.Names{}); got != agentresult.ReplacementSummary {
		t.Errorf("a summary that grows past the cap once its links are replaced must be replaced whole, got %.60q", got)
	}
	rng := rand.New(rand.NewSource(9))
	for i := 0; i < 5000; i++ {
		var b strings.Builder
		for n := rng.Intn(60); n > 0; n-- {
			b.WriteString(fragments[rng.Intn(len(fragments))])
		}
		check("generated", b.String())
	}
}

func TestCleanSummary_AndCRLF_AreLinear(t *testing.T) {
	inputs := map[string]string{
		"links":      strings.Repeat("http://a.b/", 20000),
		"dots":       strings.Repeat("a.", 100000),
		"brackets":   strings.Repeat("](", 100000),
		"spaces":     strings.Repeat(" x", 100000),
		"returns":    strings.Repeat("\r", 200000),
		"mixed":      strings.Repeat("\r\n\r\n\r", 40000),
		"slashes":    strings.Repeat("// ", 70000),
		"long token": strings.Repeat("a", 200000),
	}
	start := time.Now()
	for name, in := range inputs {
		CleanSummary(in, "https://hm.example.com", secretshape.Names{})
		crlf(in)
		if elapsed := time.Since(start); elapsed > 20*time.Second {
			t.Fatalf("%s: %v for 200 KB inputs, want linear time", name, elapsed)
		}
	}
	if got := crlf(strings.Repeat("\r", 200000)); strings.Count(got, "\r\n") != 200000 || strings.Contains(strings.ReplaceAll(got, "\r\n", ""), "\r") {
		t.Error("crlf must give every bare CR one CRLF")
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

func TestDiagnosis_NoDiagnosisOrNoIDIsRefusedByEverySender(t *testing.T) {
	nilDiagnosis := testMessage(EventDiagnosis)
	zeroID, negativeID := diagnosisMessage("x"), diagnosisMessage("x")
	zeroID.Diagnosis.InvestigationID, negativeID.Diagnosis.InvestigationID = 0, -3
	for name, m := range map[string]Message{"nil": nilDiagnosis, "zero id": zeroID, "negative id": negativeID} {
		if _, err := SlackBody(m); err == nil {
			t.Errorf("%s: SlackBody built a message", name)
		}
		if _, err := WebhookBody(m); err == nil {
			t.Errorf("%s: WebhookBody built a message", name)
		}
		if _, err := TelegramBody(m, "-100123", 0); err == nil {
			t.Errorf("%s: TelegramBody built a message", name)
		}
		if _, err := PagerDutyBody(m, "fake-routing-key"); err == nil {
			t.Errorf("%s: PagerDutyBody built a message", name)
		}
		for _, dest := range []string{DestSlack, DestTelegram, DestWebhook, DestPagerDuty, DestEmail} {
			if _, err := RenderPreview(dest, m); err == nil {
				t.Errorf("%s: the %s preview was rendered", name, dest)
			}
		}
	}

	h := newHarness(t, nil)
	mails := 0
	h.senders.FakeMail = func(string, []string, []byte) error { mails++; return nil }
	routes := map[string]struct {
		route  store.NotificationRoute
		secret Secret
	}{
		DestSlack:     {store.NotificationRoute{Destination: DestSlack}, Secret{URL: "https://example.com/services/T0/B0/TOKEN"}},
		DestWebhook:   {webhookRoute(), Secret{URL: "https://example.com/hook"}},
		DestPagerDuty: {store.NotificationRoute{Destination: DestPagerDuty}, Secret{RoutingKey: strings.Repeat("a", 32)}},
		DestTelegram:  {telegramRoute(), Secret{BotToken: telegramToken}},
		DestEmail:     {store.NotificationRoute{Destination: DestEmail, Config: store.NotificationConfig{Recipients: []string{"ops@example.com"}}}, Secret{}},
	}
	for dest, r := range routes {
		for name, m := range map[string]Message{"nil": nilDiagnosis, "zero id": zeroID} {
			if res := h.senders.Send(context.Background(), r.route, r.secret, m); res.Delivered {
				t.Errorf("%s/%s: delivered %+v", dest, name, res)
			}
		}
	}
	if n := len(h.recorded()); n != 0 || mails != 0 {
		t.Errorf("%d requests and %d mails left for a diagnosis that does not exist", n, mails)
	}
}

func TestDiagnosis_PreviewFollowsTheSenders(t *testing.T) {
	m := diagnosisMessage("Out of memory.")
	slack, err := RenderPreview(DestSlack, m)
	if err != nil || slack.Title != "Diagnosis: api: service failing" || !strings.Contains(slack.Body, aiNotice) || !strings.Contains(slack.Body, "Out of memory.") {
		t.Errorf("slack preview = %+v, %v", slack, err)
	}
	tg, err := RenderPreview(DestTelegram, m)
	if err != nil || !strings.Contains(tg.Body, aiNotice) || !strings.Contains(tg.Body, "Out of memory.") {
		t.Errorf("telegram preview = %+v, %v", tg, err)
	}
	em, err := RenderPreview(DestEmail, m)
	if err != nil || !strings.Contains(em.Body, aiNotice) || !strings.Contains(em.Body, ownInvestigation) {
		t.Errorf("email preview = %+v, %v", em, err)
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
