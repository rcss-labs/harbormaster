package notify

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/rcss-labs/harbormaster/internal/agentresult"
)

var errNoDiagnosis = errors.New("a diagnosis message needs its diagnosis")

const (
	// diagnosisNotice is the code-owned line every diagnosis carries, ahead of the model's text.
	diagnosisNotice = "Written by AI. Check it before acting."
	// investigationLinkText is the link label of a diagnosis message.
	investigationLinkText = "Open the investigation"

	slackMaxTitle   = 256  // characters of the escaped title in a headline
	slackMaxSection = 3000 // characters Slack accepts in one section's text
)

// CleanSummary is the last check before a model-written summary leaves Harbormaster: every link that is not
// baseURL's own is removed, then the stored text is checked again (cap and scrubber). The order matters: the check
// comes last so replacing links with a longer placeholder can never push the result past the cap, and the result is
// a fixed point, CleanSummary(CleanSummary(x)) == CleanSummary(x). It runs at send time, so a text stored before a
// rule was tightened is still held to the current rule. Callers pass the stored summary through unchanged.
func CleanSummary(summary, baseURL string) string {
	clean, _ := agentresult.RecheckSummary(agentresult.StripForeignURLs(summary, baseURL))
	return clean
}

// isDiagnosis reports whether m is a diagnosis message.
func (m Message) isDiagnosis() bool { return m.Event == EventDiagnosis }

// checkDiagnosis refuses a diagnosis message that has no diagnosis or no investigation to point at, so nothing
// half-built is sent.
func (m Message) checkDiagnosis() error {
	if m.isDiagnosis() && (m.Diagnosis == nil || m.Diagnosis.InvestigationID <= 0) {
		return errNoDiagnosis
	}
	return nil
}

// summary is the cleaned diagnosis text, never empty (a Slack text object may not be).
func (m Message) summary() string {
	if m.Diagnosis == nil {
		return ""
	}
	if clean := CleanSummary(m.Diagnosis.Summary, m.BaseURL); clean != "" {
		return clean
	}
	return agentresult.ReplacementSummary
}

// pagerDutyLink is the link of a PagerDuty event. The Opened event of a route that investigates points at the
// incident's investigation page; the investigation has no id yet when that event is sent.
func (m Message) pagerDutyLink() string {
	if m.Event == EventOpened && m.Investigate && m.IncidentID > 0 {
		return strings.TrimRight(m.BaseURL, "/") + "/investigations/incident/" + strconv.FormatInt(m.IncidentID, 10)
	}
	return m.Link()
}

// slackCutEscaped escapes s and cuts it to at most budget characters, never inside an entity, with an ellipsis.
func slackCutEscaped(s string, budget int) string {
	var pieces []string
	used := 0
	for _, r := range s {
		piece := slackEscape.Replace(string(r))
		n := len([]rune(piece))
		if used+n > budget {
			for used > budget-1 && len(pieces) > 0 {
				used -= len([]rune(pieces[len(pieces)-1]))
				pieces = pieces[:len(pieces)-1]
			}
			return strings.Join(pieces, "") + "…"
		}
		pieces = append(pieces, piece)
		used += n
	}
	return strings.Join(pieces, "")
}

// slackPlainText cuts text to what one Slack section holds, on a rune boundary.
func slackPlainText(text string) string {
	if runes := []rune(text); len(runes) > slackMaxSection {
		return string(runes[:slackMaxSection])
	}
	return text
}

// slackDiagnosisBody renders a diagnosis for Slack. The model's text goes in a plain_text section, where Slack
// interprets nothing; the headline, whose title is alert text, is verbatim mrkdwn with the title escaped and capped.
func slackDiagnosisBody(m Message) ([]byte, error) {
	title := "Diagnosis: " + slackCutEscaped(m.Subject.Title, slackMaxTitle)
	where := slackEscape.Replace(m.where()) + " · <" + m.Link() + "|" + investigationLinkText + ">"
	return json.Marshal(map[string]any{
		"text":         title,
		"unfurl_links": false,
		"unfurl_media": false,
		"blocks": []any{
			map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "verbatim": true, "text": "*" + title + "*\n" + diagnosisNotice}},
			map[string]any{"type": "section", "text": map[string]any{"type": "plain_text", "emoji": false, "text": slackPlainText(m.summary())}},
			map[string]any{"type": "context", "elements": []any{map[string]any{"type": "mrkdwn", "verbatim": true, "text": where}}},
		},
	})
}

// telegramDiagnosisText renders a diagnosis for Telegram. The summary sits in a pre element, where Telegram makes
// no mention, command, hashtag or link of what it holds; the tags count against the budget.
func telegramDiagnosisText(m Message) string {
	head := "<b>" + telegramEscape.Replace(cutRunes(m.plainHeadline(), telegramMaxTitle)) + "</b>"
	tail := "\n" + `<a href="` + telegramAttrEscape.Replace(m.Link()) + `">` + investigationLinkText + `</a>`
	lead := "\n" + telegramEscape.Replace(cutRunes(m.where(), telegramMaxTitle)) + "\n" + diagnosisNotice + "\n<pre>"
	budget := telegramMaxUnits - utf16Units(head) - utf16Units(lead) - utf16Units("</pre>") - utf16Units(tail)
	return head + lead + cutEscaped(m.summary(), budget) + "</pre>" + tail
}
