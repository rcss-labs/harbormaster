package notify

import (
	"encoding/json"
	"html"
	"regexp"
	"strings"
	"unicode/utf16"
)

const (
	telegramMaxUnits = 4096 // UTF-16 code units; Telegram counts after parsing entities, so counting the source is conservative
	telegramMaxTitle = 256  // runes; keeps room for the alert text
)

var telegramEscape = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
var telegramAttrEscape = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")

func utf16Units(s string) int {
	n := 0
	for _, r := range s {
		n += len(utf16.Encode([]rune{r}))
	}
	return n
}

// cutEscaped escapes s and cuts it, on a rune boundary and never inside an entity, so the escaped result fits
// budget UTF-16 code units, appending an ellipsis when something was cut.
func cutEscaped(s string, budget int) string {
	if budget <= 0 {
		return ""
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		piece := telegramEscape.Replace(string(r))
		units := utf16Units(piece)
		if used+units > budget {
			// Leave room for the ellipsis (one unit).
			return trimToFit(b.String(), budget-1) + "…"
		}
		b.WriteString(piece)
		used += units
	}
	return b.String()
}

// trimToFit drops trailing whole characters (or whole entities) until s fits the budget.
func trimToFit(s string, budget int) string {
	for utf16Units(s) > budget && s != "" {
		if i := strings.LastIndex(s, "&"); i >= 0 && strings.HasSuffix(s, ";") && i > len(s)-7 {
			s = s[:i]
			continue
		}
		r := []rune(s)
		s = string(r[:len(r)-1])
	}
	return s
}

func cutRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

// TelegramText renders the HTML message: a bold title line, the alert text, where it happened and one link. Every
// dynamic string is escaped for & < >; the link is also escaped for ". The alert text is cut, with an ellipsis, so
// the whole message fits Telegram's limit.
func TelegramText(m Message) string {
	if m.isDiagnosis() {
		return telegramDiagnosisText(m)
	}
	var title string
	switch {
	case m.Event == EventTest:
		title = "Test message from Harbormaster"
	case m.Event == EventResolved:
		title = "✅ Resolved: " + m.Subject.Title
	case m.Subject.Severity == "critical":
		title = "🔴 " + m.updatePrefix() + m.Subject.Title
	default:
		title = "🟠 " + m.updatePrefix() + m.Subject.Title
	}
	head := "<b>" + telegramEscape.Replace(cutRunes(title, telegramMaxTitle)) + "</b>"
	tail := "\n" + telegramEscape.Replace(cutRunes(m.where(), telegramMaxTitle)) +
		"\n" + `<a href="` + telegramAttrEscape.Replace(m.Link()) + `">Open in Harbormaster</a>`
	if m.Subject.Text == "" {
		return head + tail
	}
	// "\n" before the text counts one unit.
	budget := telegramMaxUnits - utf16Units(head) - utf16Units(tail) - 1
	return head + "\n" + cutEscaped(m.Subject.Text, budget) + tail
}

// TelegramBody renders the sendMessage JSON. The chat id is sent as the string given (a number, negative for
// groups and channels, or @channelname); the topic id is sent only when set.
func TelegramBody(m Message, chatID string, threadID int) ([]byte, error) {
	if err := m.checkDiagnosis(); err != nil {
		return nil, err
	}
	body := map[string]any{
		"chat_id":              chatID,
		"text":                 TelegramText(m),
		"parse_mode":           "HTML",
		"link_preview_options": map[string]any{"is_disabled": true},
	}
	if threadID > 0 {
		body["message_thread_id"] = threadID
	}
	return json.Marshal(body)
}

var htmlTag = regexp.MustCompile(`<[^>]*>`)

// telegramPreview shows what the reader will see: the rendered text with tags stripped and entities unescaped.
func telegramPreview(m Message) Preview {
	plain := html.UnescapeString(htmlTag.ReplaceAllString(TelegramText(m), ""))
	title, body, _ := strings.Cut(plain, "\n")
	return Preview{Destination: DestTelegram, Title: title, Body: strings.TrimSpace(body)}
}
