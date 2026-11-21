package notify

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"
)

func utf16Len(s string) int { return len(utf16.Encode([]rune(s))) }

func TestTelegramText_EscapesEveryDynamicString(t *testing.T) {
	m := testMessage(EventOpened)
	m.Subject.Title = `a<b>&"c`
	m.Subject.Text = `<a href="https://evil">x</a> & <b>`
	m.Subject.Key.Cluster = "x<y"
	m.Subject.AccountName = "acct<&>"
	got := TelegramText(m)
	if n := strings.Count(got, "<a "); n != 1 {
		t.Errorf("%d anchors, want exactly the Harbormaster link:\n%s", n, got)
	}
	if strings.Count(got, "<b>") != 1 || strings.Count(got, "</b>") != 1 {
		t.Errorf("want exactly one bold pair:\n%s", got)
	}
	for _, want := range []string{`a&lt;b&gt;&amp;"c`, `&lt;a href="https://evil"&gt;x&lt;/a&gt; &amp; &lt;b&gt;`, "x&lt;y", "acct&lt;&amp;&gt;", "🔴"} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
	if !strings.Contains(got, `<a href="`+m.Link()+`">Open in Harbormaster</a>`) {
		t.Errorf("link missing:\n%s", got)
	}
}

func TestTelegramText_TitlesPerEvent(t *testing.T) {
	warn := testMessage(EventOpened)
	warn.Subject.Severity = "warning"
	for name, tc := range map[string]struct {
		m    Message
		want string
	}{
		"warning":  {warn, "<b>🟠 api: service failing</b>"},
		"resolved": {testMessage(EventResolved), "<b>✅ Resolved: api: service failing</b>"},
		"test":     {testMessage(EventTest), "<b>Test message from Harbormaster</b>"},
	} {
		if got := TelegramText(tc.m); !strings.HasPrefix(got, tc.want) {
			t.Errorf("%s: text = %q, want prefix %q", name, got, tc.want)
		}
	}
}

func TestTelegramText_FitsTheLengthLimit(t *testing.T) {
	for name, text := range map[string]string{
		"ampersands": strings.Repeat("&", 5000),
		"astral":     strings.Repeat("😀", 5000),
		"long ascii": strings.Repeat("a", 5000),
	} {
		m := testMessage(EventOpened)
		m.Subject.Text = text
		got := TelegramText(m)
		if n := utf16Len(got); n > 4096 {
			t.Errorf("%s: %d UTF-16 code units, want at most 4096", name, n)
		}
		if !utf8.ValidString(got) {
			t.Errorf("%s: invalid UTF-8", name)
		}
		if strings.Count(got, "<b>") != 1 || strings.Count(got, "</b>") != 1 {
			t.Errorf("%s: bold tag not closed", name)
		}
		if !strings.HasSuffix(got, `<a href="`+m.Link()+`">Open in Harbormaster</a>`) {
			t.Errorf("%s: the link was cut", name)
		}
		if !strings.Contains(got, "…") {
			t.Errorf("%s: a cut text must end with an ellipsis", name)
		}
		// A cut never leaves half an entity: every & in the output starts a complete entity.
		for _, part := range strings.Split(got, "&")[1:] {
			if !strings.HasPrefix(part, "amp;") && !strings.HasPrefix(part, "lt;") && !strings.HasPrefix(part, "gt;") && !strings.HasPrefix(part, "quot;") {
				t.Errorf("%s: a partial entity near %q", name, part[:min(len(part), 10)])
				break
			}
		}
	}
	// A long title is bounded too, so the limit holds even when everything is long.
	m := testMessage(EventOpened)
	m.Subject.Title, m.Subject.Text = strings.Repeat("t", 6000), strings.Repeat("x", 6000)
	if n := utf16Len(TelegramText(m)); n > 4096 {
		t.Errorf("long title: %d code units", n)
	}
}

func TestTelegramBody_ChatIDAndThread(t *testing.T) {
	decode := func(chat string, thread int) map[string]any {
		body, err := TelegramBody(testMessage(EventOpened), chat, thread)
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	numeric := decode("-1001234567890", 0)
	if numeric["chat_id"] != "-1001234567890" {
		t.Errorf("chat_id = %#v, want the string as given", numeric["chat_id"])
	}
	if _, present := numeric["message_thread_id"]; present {
		t.Error("message_thread_id must be omitted without a topic")
	}
	if numeric["parse_mode"] != "HTML" || numeric["link_preview_options"].(map[string]any)["is_disabled"] != true {
		t.Errorf("body = %v", numeric)
	}
	if numeric["text"] != TelegramText(testMessage(EventOpened)) {
		t.Error("the body must carry TelegramText")
	}
	threaded := decode("@ops_alerts", 7)
	if threaded["chat_id"] != "@ops_alerts" || threaded["message_thread_id"] != float64(7) {
		t.Errorf("threaded body = %v", threaded)
	}
}

func TestRenderPreview_TelegramIsPlainText(t *testing.T) {
	m := testMessage(EventOpened)
	m.Subject.Text = "5 < 6 & 7 > 3 tasks stopped"
	p, err := RenderPreview(DestTelegram, m)
	if err != nil {
		t.Fatal(err)
	}
	if p.Title != "🔴 api: service failing" {
		t.Errorf("title = %q", p.Title)
	}
	if strings.Contains(p.Title, "<b>") || strings.Contains(p.Body, "&lt;") || strings.Contains(p.Body, "&amp;") || strings.Contains(p.Body, "<a href") {
		t.Errorf("preview = %+v, want tags stripped and entities unescaped", p)
	}
	if !strings.Contains(p.Body, "5 < 6 & 7 > 3 tasks stopped") || !strings.Contains(p.Body, "Open in Harbormaster") {
		t.Errorf("body = %q", p.Body)
	}
}
