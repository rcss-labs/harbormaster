package notify

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/rcss-labs/harbormaster/internal/agentresult"
)

var (
	errNoDiagnosis = errors.New("a diagnosis message needs its diagnosis")
	errNoReview    = errors.New("a review message needs its review, a run to point at and a known type")
)

// reviewTypes maps the fixed label of each review type to its id. A message carries the label, so only these four
// strings can reach a headline.
var reviewTypes = map[string]string{"Cost": "cost", "Security findings": "security", "Reliability": "reliability", "Capacity": "capacity"}

const (
	// diagnosisNotice is the code-owned line every diagnosis carries, ahead of the model's text.
	diagnosisNotice = "Written by AI. Check it before acting."
	// investigationLinkText is the link label of a diagnosis message.
	investigationLinkText = "Open the investigation"
	// reviewLinkText is the link label of a review message.
	reviewLinkText = "Open the review"

	slackMaxTitle   = 256  // characters of the escaped title in a headline
	slackMaxSection = 3000 // characters Slack accepts in one section's text
)

// CleanSummary is the last check before a model-written summary leaves Harbormaster. The stored text is checked
// (cap and scrubber) on the raw text first, because removing a link can hide the key name that tells the scrubber
// a value is a secret; then every link that is not baseURL's own is removed; then the result is checked again,
// because the placeholder is longer than a short link and could push it past the cap. A failure at either check
// replaces the whole text. The result is a fixed point, CleanSummary(CleanSummary(x)) == CleanSummary(x), and at
// most 1200 runes. It runs at send time, so a text stored before a rule was tightened is still held to the current
// rule. Callers pass the stored summary through unchanged.
func CleanSummary(summary, baseURL string) string {
	return cleanSummaryAs(summary, baseURL, agentresult.ReplacementSummary)
}

// CleanReviewSummary is CleanSummary with the review sentence as the replacement, under the same order of checks.
func CleanReviewSummary(summary, baseURL string) string {
	return cleanSummaryAs(summary, baseURL, agentresult.ReplacementReviewSummary)
}

func cleanSummaryAs(summary, baseURL, replacement string) string {
	raw, replaced := agentresult.RecheckSummaryAs(summary, replacement)
	if replaced {
		return raw
	}
	clean, _ := agentresult.RecheckSummaryAs(agentresult.StripForeignURLs(raw, baseURL), replacement)
	return clean
}

// isDiagnosis reports whether m is a diagnosis message.
func (m Message) isDiagnosis() bool { return m.Event == EventDiagnosis }

// isReview reports whether m is a review message.
func (m Message) isReview() bool { return m.Event == EventReview }

// isAI reports whether m carries model-written text, which every destination renders as plain text behind the notice.
func (m Message) isAI() bool { return m.isDiagnosis() || m.isReview() }

// checkMessage refuses an AI message that has nothing to point at (no diagnosis or review, a record id of 0 or
// less, a review of an unknown type), so nothing half-built is sent.
func (m Message) checkMessage() error {
	switch {
	case m.isDiagnosis() && (m.Diagnosis == nil || m.Diagnosis.InvestigationID <= 0):
		return errNoDiagnosis
	case m.isReview():
		if _, known := reviewTypes[m.reviewLabel()]; m.Review == nil || m.Review.RunID <= 0 || !known {
			return errNoReview
		}
	}
	return nil
}

func (m Message) reviewLabel() string {
	if m.Review == nil {
		return ""
	}
	return m.Review.TypeLabel
}

// summary is the cleaned model text, never empty (a Slack text object may not be).
func (m Message) summary() string {
	switch {
	case m.isDiagnosis() && m.Diagnosis != nil:
		if clean := CleanSummary(m.Diagnosis.Summary, m.BaseURL); clean != "" {
			return clean
		}
		return agentresult.ReplacementSummary
	case m.isReview() && m.Review != nil:
		if clean := CleanReviewSummary(m.Review.Summary, m.BaseURL); clean != "" {
			return clean
		}
		return agentresult.ReplacementReviewSummary
	}
	return ""
}

// reviewTitle is the headline of a review: the fixed type label and the name, which is admin text.
func (m Message) reviewTitle() string { return m.reviewLabel() + " review: " + m.Review.Name }

// linkText is the label of the link of an AI message.
func (m Message) linkText() string {
	if m.isReview() {
		return reviewLinkText
	}
	return investigationLinkText
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

// slackAIBody renders a diagnosis or a review for Slack. The model's text goes in a plain_text section, where Slack
// interprets nothing; the headline, whose title is alert or admin text, is verbatim mrkdwn with the title escaped
// and capped.
func slackAIBody(m Message) ([]byte, error) {
	var title, second, where string
	if m.isReview() {
		title = slackCutEscaped(m.reviewTitle(), slackMaxTitle)
		second = slackCutEscaped(m.Review.PeriodLabel, slackMaxTitle)
		where = "<" + m.Link() + "|" + reviewLinkText + ">"
	} else {
		title = "Diagnosis: " + slackCutEscaped(m.Subject.Title, slackMaxTitle)
		where = slackEscape.Replace(m.where()) + " · <" + m.Link() + "|" + investigationLinkText + ">"
	}
	headline := "*" + title + "*\n" + diagnosisNotice
	if second != "" {
		headline = "*" + title + "*\n" + second + "\n" + diagnosisNotice
	}
	return json.Marshal(map[string]any{
		"text":         title,
		"unfurl_links": false,
		"unfurl_media": false,
		"blocks": []any{
			map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "verbatim": true, "text": headline}},
			map[string]any{"type": "section", "text": map[string]any{"type": "plain_text", "emoji": false, "text": slackPlainText(m.summary())}},
			map[string]any{"type": "context", "elements": []any{map[string]any{"type": "mrkdwn", "verbatim": true, "text": where}}},
		},
	})
}

// telegramAIText renders a diagnosis or a review for Telegram. The summary sits in a pre element, where Telegram
// makes no mention, command, hashtag or link of what it holds; the tags count against the budget.
func telegramAIText(m Message) string {
	second := m.where()
	if m.isReview() {
		second = m.Review.PeriodLabel
	}
	head := "<b>" + telegramEscape.Replace(cutRunes(m.plainHeadline(), telegramMaxTitle)) + "</b>"
	tail := "\n" + `<a href="` + telegramAttrEscape.Replace(m.Link()) + `">` + m.linkText() + `</a>`
	lead := "\n" + telegramEscape.Replace(cutRunes(second, telegramMaxTitle)) + "\n" + diagnosisNotice + "\n<pre>"
	budget := telegramMaxUnits - utf16Units(head) - utf16Units(lead) - utf16Units("</pre>") - utf16Units(tail)
	return head + lead + cutEscaped(m.summary(), budget) + "</pre>" + tail
}
