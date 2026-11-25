package notify

import (
	"context"
	"encoding/json"
	"mime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/rcss-labs/harbormaster/internal/agentresult"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const ownReview = "https://hm.example.com/reviews/runs/31"

func reviewMessage(summary string) Message {
	return Message{
		Event: EventReview, DeliveryID: 42, BaseURL: "https://hm.example.com", RouteName: "Prod",
		Subject: Subject{Kind: "review", Severity: "info", At: time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)},
		Review:  &Review{RunID: 31, Name: "Monthly spend", Type: "cost", PeriodLabel: "November 2026", Summary: summary},
	}
}

func slackReview(t *testing.T, m Message) slackPayload {
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

func TestReview_Slack_LayoutEscapesAndDisablesUnfurl(t *testing.T) {
	summary := "pods <!channel> & <@U123> :tada: &gt; @here #general `x` *bold* crashed"
	got := slackReview(t, reviewMessage(summary))
	if got.UnfurlLinks == nil || *got.UnfurlLinks || got.UnfurlMedia == nil || *got.UnfurlMedia {
		t.Error("unfurl_links and unfurl_media must both be false")
	}
	head, sum, context := got.Blocks[0], got.Blocks[1], got.Blocks[2].Elements[0]
	if head.Type != "section" || head.Text.Type != "mrkdwn" || head.Text.Verbatim == nil || !*head.Text.Verbatim {
		t.Errorf("headline must be a mrkdwn section with verbatim true: %+v", head.Text)
	}
	if want := "*Cost review: Monthly spend*\nNovember 2026\n" + aiNotice; head.Text.Text != want {
		t.Errorf("headline = %q, want %q", head.Text.Text, want)
	}
	if got.Text != "Cost review: Monthly spend" {
		t.Errorf("fallback text = %q", got.Text)
	}
	if sum.Type != "section" || sum.Text.Type != "plain_text" || sum.Text.Text != summary || sum.Text.Emoji == nil || *sum.Text.Emoji {
		t.Errorf("the summary must be a plain_text section with emoji false: %+v", sum.Text)
	}
	if context.Text != "<"+ownReview+"|Open the review>" || context.Verbatim == nil || !*context.Verbatim {
		t.Errorf("context = %+v", context)
	}
}

func TestReview_Slack_NameIsEscapedAndCapped(t *testing.T) {
	for name, review := range map[string]string{
		"here":    "<!here> *spend* <https://evil.example|click>",
		"entity":  "&gt; not a quote",
		"long":    strings.Repeat("n", 6000),
		"astral":  strings.Repeat("😀", 6000),
		"amps":    strings.Repeat("&", 1200),
		"mention": "@here #general <@U1>",
	} {
		m := reviewMessage("ok")
		m.Review.Name = review
		got := slackReview(t, m)
		head := got.Blocks[0].Text.Text
		if !strings.HasPrefix(head, "*Cost review: ") || !strings.HasSuffix(head, "*\nNovember 2026\n"+aiNotice) {
			t.Errorf("%s: headline = %.80q", name, head)
		}
		if n := utf8.RuneCountInString(head); n > 400 {
			t.Errorf("%s: headline has %d characters, want it capped", name, n)
		}
		if strings.Contains(head, "<") || strings.Contains(got.Text, "<!") || strings.Contains(got.Text, "<@") || strings.Contains(got.Text, "<h") {
			t.Errorf("%s: a raw control sequence in %q / %q", name, head, got.Text)
		}
		for _, part := range strings.Split(head+got.Text, "&")[1:] {
			if !strings.HasPrefix(part, "amp;") && !strings.HasPrefix(part, "lt;") && !strings.HasPrefix(part, "gt;") {
				t.Errorf("%s: a partial entity near %q", name, part[:min(len(part), 10)])
				break
			}
		}
	}
	m := reviewMessage("ok")
	m.Review.Name = "<!here> *x*"
	if head := slackReview(t, m).Blocks[0].Text.Text; !strings.Contains(head, "&lt;!here&gt;") {
		t.Errorf("the name must be escaped: %q", head)
	}
}

func TestReview_Telegram_EscapesFitsAndIsPreformatted(t *testing.T) {
	summary := strings.Repeat("😀", agentresult.SummaryCap)
	m := reviewMessage(summary)
	m.Review.Name = "<b>spend</b> & @oncall"
	got := TelegramText(m)
	if n := utf16Units(got); n > telegramMaxUnits {
		t.Errorf("%d UTF-16 units, limit %d", n, telegramMaxUnits)
	}
	if !utf8.ValidString(got) || strings.Count(got, "<pre>") != 1 || strings.Count(got, "</pre>") != 1 {
		t.Fatalf("the summary must sit in exactly one pre element: %.200q", got)
	}
	start, end := strings.Index(got, "<pre>"), strings.LastIndex(got, "</pre>")
	if !strings.Contains(got[:start], aiNotice) || !strings.Contains(got[:start], "November 2026") || !strings.Contains(got[:start], "Cost review: &lt;b&gt;spend&lt;/b&gt; &amp; @oncall") {
		t.Errorf("head = %q", got[:start])
	}
	if !strings.HasSuffix(got, `<a href="`+ownReview+`">Open the review</a>`) || end < start {
		t.Errorf("tail = %q", got[end:])
	}
	if strings.Contains(got[start:end], "…") || !strings.Contains(got[start:end], summary) {
		t.Error("a summary that fits must be kept whole")
	}
	// Escaping makes every "<" four units, so 1200 of them overrun the budget and must be cut.
	escaped := TelegramText(reviewMessage(strings.Repeat("<", agentresult.SummaryCap)))
	if n := utf16Units(escaped); n > telegramMaxUnits || !strings.Contains(escaped, "…</pre>") {
		t.Errorf("an escaped summary must be cut with an ellipsis inside %d units, got %d", telegramMaxUnits, n)
	}
	hostile := reviewMessage("page @oncall_lead /stop evil.com #x </pre><a href=\"https://evil.example\">go</a>")
	text := TelegramText(hostile)
	s, e := strings.Index(text, "<pre>"), strings.LastIndex(text, "</pre>")
	inside, outside := text[s:e], text[:s]+text[e:]
	for _, trigger := range []string{"@oncall_lead", "/stop", "evil.com", "#x"} {
		if !strings.Contains(inside, trigger) || strings.Contains(outside, trigger) {
			t.Errorf("%q must be inside the pre element only: %q", trigger, text)
		}
	}
	body, err := TelegramBody(m, "-100123", 0)
	if err != nil || !strings.Contains(string(body), `"is_disabled":true`) {
		t.Errorf("the preview must be disabled: %s, %v", body, err)
	}
}

func TestReview_Email_SubjectAndLinesStayShort(t *testing.T) {
	m := reviewMessage("The spend is flat.\nNothing to do.")
	m.Review.Name = "spend\r\nBcc: evil@example.com"
	subject, raw := Email(m, "hm@example.com", []string{"ops@example.com"})
	if want := "[Harbormaster] Cost review: spend Bcc: evil@example.com"; subject != want {
		t.Errorf("subject = %q, want %q", subject, want)
	}
	head, body := decodedEmailBody(t, raw)
	if head.Get("Bcc") != "" || head.Get("Content-Transfer-Encoding") != "quoted-printable" {
		t.Errorf("headers = %v", head)
	}
	want := aiNotice + "\r\n\r\nNovember 2026\r\n\r\nThe spend is flat.\r\nNothing to do.\r\n\r\n" + ownReview + "\r\n"
	if body != want {
		t.Errorf("body = %q, want %q", body, want)
	}
	for name, summary := range map[string]string{
		"ascii": strings.Repeat("abcdefghij", 120), "cjk": strings.Repeat("故障", 600),
		"long word": strings.Repeat("a=b", 400), "lines": strings.Repeat("line one\nline two\r\n", 60),
	} {
		_, raw := Email(reviewMessage(summary), "hm@example.com", []string{"ops@example.com"})
		_, wire, _ := strings.Cut(string(raw), "\r\n\r\n")
		for _, line := range strings.Split(wire, "\r\n") {
			if len(line) > 998 {
				t.Errorf("%s: a %d-octet line", name, len(line))
				break
			}
		}
		_, body := decodedEmailBody(t, raw)
		if want := aiNotice + "\r\n\r\nNovember 2026\r\n\r\n" + crlf(CleanReviewSummary(summary, "https://hm.example.com")) + "\r\n\r\n" + ownReview + "\r\n"; body != want {
			t.Errorf("%s: the body does not decode to the text:\n got %.120q\nwant %.120q", name, body, want)
		}
	}
}

func TestReview_Webhook_EventAndFields(t *testing.T) {
	body, err := WebhookBody(reviewMessage("Spend is flat."))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{
		"event": "review", "ai_generated": true, "summary": "Spend is flat.", "review_run_id": float64(31),
		"review_name": "Monthly spend", "review_type": "cost", "period": "November 2026", "url": ownReview, "delivery_id": float64(42),
	} {
		if got[key] != want {
			t.Errorf("%s = %v, want %v: %s", key, got[key], want, body)
		}
	}
	if _, ok := got["investigation_id"]; ok {
		t.Error("a review has no investigation id")
	}
}

func TestReview_PagerDutyIsRefused(t *testing.T) {
	body, err := PagerDutyBody(reviewMessage("x"), "fake-routing-key")
	if err == nil || body != nil {
		t.Errorf("PagerDutyBody = %q, %v; want an error and no body", body, err)
	}
	if _, err := RenderPreview(DestPagerDuty, reviewMessage("x")); err == nil {
		t.Error("the preview of a PagerDuty review must be refused too")
	}
}

func TestReview_NoReviewOrNoIDIsRefusedByEverySender(t *testing.T) {
	nilReview := reviewMessage("x")
	nilReview.Review = nil
	zeroID, negativeID := reviewMessage("x"), reviewMessage("x")
	zeroID.Review.RunID, negativeID.Review.RunID = 0, -3
	for name, m := range map[string]Message{"nil": nilReview, "zero id": zeroID, "negative id": negativeID} {
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
		for name, m := range map[string]Message{"nil": nilReview, "zero id": zeroID} {
			if res := h.senders.Send(context.Background(), r.route, r.secret, m); res.Delivered {
				t.Errorf("%s/%s: delivered %+v", dest, name, res)
			}
		}
	}
	if n := len(h.recorded()); n != 0 || mails != 0 {
		t.Errorf("%d requests and %d mails left for a review that does not exist", n, mails)
	}
}

func TestReview_UnknownTypeIsRefused(t *testing.T) {
	for _, typ := range []string{"", "Cost", "billing", "cost\n", "<!here>"} {
		m := reviewMessage("x")
		m.Review.Type = typ
		if _, err := SlackBody(m); err == nil {
			t.Errorf("%q: SlackBody built a message", typ)
		}
		if _, err := WebhookBody(m); err == nil {
			t.Errorf("%q: WebhookBody built a message", typ)
		}
		if _, err := TelegramBody(m, "-100123", 0); err == nil {
			t.Errorf("%q: TelegramBody built a message", typ)
		}
		if _, err := RenderPreview(DestEmail, m); err == nil {
			t.Errorf("%q: the email preview was rendered", typ)
		}
	}
	for typ, label := range map[string]string{"cost": "Cost", "security": "Security findings", "reliability": "Reliability", "capacity": "Capacity"} {
		m := reviewMessage("x")
		m.Review.Type = typ
		body, err := WebhookBody(m)
		if err != nil || !strings.Contains(string(body), `"review_type":"`+typ+`"`) {
			t.Errorf("%s: %s, %v", typ, body, err)
		}
		if got := m.plainHeadline(); got != label+" review: Monthly spend" {
			t.Errorf("%s: headline = %q", typ, got)
		}
	}
}

func TestReview_ForeignURLsRemovedOwnURLKept(t *testing.T) {
	ownService := "https://hm.example.com/services/x"
	m := reviewMessage("See https://evil.example/x and " + ownReview + " and " + ownService + " for details.")
	slack, _ := SlackBody(m)
	hook, _ := WebhookBody(m)
	_, mail := Email(m, "hm@example.com", []string{"ops@example.com"})
	_, mailBody := decodedEmailBody(t, mail)
	for name, out := range map[string]string{"slack": string(slack), "telegram": TelegramText(m), "email": mailBody, "webhook": string(hook)} {
		if strings.Contains(out, "evil.example") || !strings.Contains(out, agentresult.URLPlaceholder) {
			t.Errorf("%s: foreign URL survived or no placeholder: %s", name, out)
		}
		if !strings.Contains(out, ownReview) || !strings.Contains(out, ownService) {
			t.Errorf("%s: own URL gone from %s", name, out)
		}
	}
}

func TestCleanReviewSummary_ScrubsBeforeLinksAreRemoved(t *testing.T) {
	for _, in := range []string{
		"db.example.com:5432/password: Hunter2Secret9x",
		"//x-api-key: Zx81kq02mmLw93hhTT",
		"http://x/Bearer abcdefghijklmnop1234567890ABCDEF",
		"//-----BEGIN RSA PRIVATE KEY----- MIIEow",
		"the key is " + "AKIA" + "IOSFODNN7EXAMPLE",
	} {
		if got := CleanReviewSummary(in, "https://hm.example.com"); got != agentresult.ReplacementReviewSummary {
			t.Errorf("CleanReviewSummary(%q) = %q, want the review sentence", in, got)
		}
	}
	if got := CleanReviewSummary("Plain text.", "https://hm.example.com"); got != "Plain text." {
		t.Errorf("a clean summary changed: %q", got)
	}
}

func TestCleanReviewSummary_ReplacementIsTheReviewSentence(t *testing.T) {
	const base = "https://hm.example.com"
	over := strings.Repeat("x", agentresult.SummaryCap+1)
	grows := strings.Repeat("http://a ", 100) // fits once, but not once every link is replaced
	for name, in := range map[string]string{"over the cap": over, "grows past the cap": grows} {
		if got := CleanReviewSummary(in, base); got != agentresult.ReplacementReviewSummary {
			t.Errorf("%s: got %.60q, want the review sentence", name, got)
		}
		if got := CleanSummary(in, base); got != agentresult.ReplacementSummary {
			t.Errorf("%s: the diagnosis must keep its own sentence, got %.60q", name, got)
		}
	}
	if got := reviewMessage("").summary(); got != agentresult.ReplacementReviewSummary {
		t.Errorf("an empty summary renders as %q", got)
	}
	slack, _ := SlackBody(reviewMessage(over))
	if !strings.Contains(string(slack), agentresult.ReplacementReviewSummary) {
		t.Errorf("slack: %s", slack)
	}
	// Idempotent and capped, as the diagnosis cleaner is.
	for _, in := range []string{"", "plain", grows, over, agentresult.ReplacementReviewSummary, "[link removed] http://a " + strings.Repeat("[link removed] ", 78)} {
		once := CleanReviewSummary(in, base)
		if utf8.RuneCountInString(once) > agentresult.SummaryCap || CleanReviewSummary(once, base) != once {
			t.Errorf("not capped and idempotent for %.60q: %.80q", in, once)
		}
	}
}

func TestCleanReviewSummary_LinearOn200KB(t *testing.T) {
	inputs := map[string]string{
		"links": strings.Repeat("http://a.b/", 20000), "dots": strings.Repeat("a.", 100000), "brackets": strings.Repeat("](", 100000),
		"spaces": strings.Repeat(" x", 100000), "slashes": strings.Repeat("// ", 70000), "long token": strings.Repeat("a", 200000),
	}
	start := time.Now()
	for name, in := range inputs {
		CleanReviewSummary(in, "https://hm.example.com")
		if elapsed := time.Since(start); elapsed > 20*time.Second {
			t.Fatalf("%s: %v for 200 KB inputs, want linear time", name, elapsed)
		}
	}
}

func TestReview_LinkPointsAtTheRun(t *testing.T) {
	m := reviewMessage("x")
	if got := m.Link(); got != ownReview {
		t.Errorf("Link = %q", got)
	}
	m.BaseURL = "https://hm.example.com/"
	if got := m.Link(); got != ownReview {
		t.Errorf("Link with a trailing slash = %q", got)
	}
}

func TestReview_PreviewFollowsTheSenders(t *testing.T) {
	m := reviewMessage("Spend is flat.")
	slack, err := RenderPreview(DestSlack, m)
	if err != nil || slack.Title != "Cost review: Monthly spend" || !strings.Contains(slack.Body, aiNotice) || !strings.Contains(slack.Body, "Spend is flat.") {
		t.Errorf("slack preview = %+v, %v", slack, err)
	}
	em, err := RenderPreview(DestEmail, m)
	if err != nil || em.Title != "[Harbormaster] Cost review: Monthly spend" || !strings.Contains(em.Body, ownReview) {
		t.Errorf("email preview = %+v, %v", em, err)
	}
	if hook, err := RenderPreview(DestWebhook, m); err != nil || !strings.Contains(hook.JSON, `"event": "review"`) {
		t.Errorf("webhook preview = %+v, %v", hook, err)
	}
}

func webhookReviewName(t *testing.T, m Message) string {
	t.Helper()
	body, err := WebhookBody(m)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	name, _ := got["review_name"].(string)
	return name
}

func TestReview_NameIsOneVisibleLine(t *testing.T) {
	forged := "x*\nChecked by a person. *Safe"
	m := reviewMessage("ok")
	m.Review.Name = forged
	const flat = "x* Checked by a person. *Safe"
	head := slackReview(t, m).Blocks[0].Text.Text
	if want := "*Cost review: " + flat + "*\nNovember 2026\n" + aiNotice; head != want {
		t.Errorf("slack headline = %q, want %q", head, want)
	}
	text := TelegramText(m)
	if lead, _, _ := strings.Cut(text, "<pre>"); lead != "<b>Cost review: "+flat+"</b>\nNovember 2026\n"+aiNotice+"\n" {
		t.Errorf("telegram lead = %q", lead)
	}
	if got := webhookReviewName(t, m); got != flat {
		t.Errorf("webhook review_name = %q, want %q", got, flat)
	}
	subject, _ := Email(m, "hm@example.com", []string{"ops@example.com"})
	if subject != "[Harbormaster] Cost review: "+flat {
		t.Errorf("subject = %q", subject)
	}
	if p, err := RenderPreview(DestSlack, m); err != nil || p.Title != "Cost review: "+flat {
		t.Errorf("preview title = %q, %v", p.Title, err)
	}

	for name, r := range map[string]string{
		"vt": "\v", "ff": "\f", "esc": "\x1b", "nel": "\u0085", "ls": "\u2028", "ps": "\u2029", "cr": "\r", "tab": "\t",
		"bidi override": "\u202e", "bidi isolate": "\u2066", "lrm": "\u200e", "zwsp": "\u200b", "bom": "\ufeff",
		"soft hyphen": "\u00ad", "cgj": "\u034f", "hangul filler": "\u3164", "variation selector": "\ufe0f", "nul": "\x00", "del": "\x7f",
	} {
		m := reviewMessage("ok")
		m.Review.Name = "a" + r + "b"
		got := webhookReviewName(t, m)
		if got != "ab" && got != "a b" {
			t.Errorf("%s: review_name = %q, want the rune dropped or turned into a space", name, got)
		}
		head := slackReview(t, m).Blocks[0].Text.Text
		if strings.Count(head, "\n") != 2 || strings.ContainsAny(head, "\v\f\x1b\u0085\u2028\u2029\u202e\u2066\u200e\u200b\ufeff\u00ad\u034f\u3164\ufe0f\x00\x7f\r\t") {
			t.Errorf("%s: slack headline = %q", name, head)
		}
		if lead, _, _ := strings.Cut(TelegramText(m), "<pre>"); strings.Count(lead, "\n") != 3 || strings.ContainsAny(lead, "\v\f\x1b\u0085\u2028\u2029\u202e\u2066\u200e\u200b\ufeff\u00ad\u034f\u3164\ufe0f\x00\x7f\r\t") {
			t.Errorf("%s: telegram lead = %q", name, lead)
		}
	}
	m.Review.Name = "  spaced \u202e out  "
	if got := webhookReviewName(t, m); got != "spaced out" {
		t.Errorf("review_name = %q, want trimmed and collapsed", got)
	}
}

func TestReview_NameEmptyAfterCleaningIsRefused(t *testing.T) {
	for _, name := range []string{"", "   ", "\u202e\n\x1b", "\u200b\u200e", "\r\n\t"} {
		m := reviewMessage("x")
		m.Review.Name = name
		if _, err := SlackBody(m); err == nil {
			t.Errorf("%q: SlackBody built a message", name)
		}
		if _, err := WebhookBody(m); err == nil {
			t.Errorf("%q: WebhookBody built a message", name)
		}
		if _, err := TelegramBody(m, "-100123", 0); err == nil {
			t.Errorf("%q: TelegramBody built a message", name)
		}
		if _, err := RenderPreview(DestEmail, m); err == nil {
			t.Errorf("%q: the email preview was rendered", name)
		}
	}
}

func TestReview_EmailSubjectFoldsEveryHeaderLine(t *testing.T) {
	for label, name := range map[string]string{
		"emoji": strings.Repeat("😀", 80), "cjk": strings.Repeat("故", 80), "ascii": strings.Repeat("n", 80), "huge": strings.Repeat("😀", 2000),
	} {
		m := reviewMessage("x")
		m.Review.Name = name
		subject, raw := Email(m, "hm@example.com", []string{"ops@example.com"})
		headEnd := strings.Index(string(raw), "\r\n\r\n")
		for _, line := range strings.Split(string(raw)[:headEnd], "\r\n") {
			if len(line) > 998 {
				t.Errorf("%s: a %d-octet header line", label, len(line))
			}
		}
		head, _ := decodedEmailBody(t, raw)
		decoded, err := new(mime.WordDecoder).DecodeHeader(head.Get("Subject"))
		if err != nil || decoded != subject {
			t.Errorf("%s: the folded Subject decodes to %.60q (%v), want %.60q", label, decoded, err, subject)
		}
	}
}
