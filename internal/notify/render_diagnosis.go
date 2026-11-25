package notify

import (
	"errors"
	"strconv"
	"strings"

	"github.com/rcss-labs/harbormaster/internal/agentresult"
)

var errNoDiagnosis = errors.New("a diagnosis message needs its diagnosis")

// investigationLinkText is the link label of a diagnosis message.
const investigationLinkText = "Open the investigation"

// CleanSummary is the last check before a model-written summary leaves Harbormaster: the stored text is checked
// again (cap and scrubber) and every link that is not baseURL's own is removed. It runs at send time, so a text
// stored before a rule was tightened is still held to the current rule.
func CleanSummary(summary, baseURL string) string {
	clean, _ := agentresult.RecheckSummary(summary)
	return agentresult.StripForeignURLs(clean, baseURL)
}

// isDiagnosis reports whether m is a diagnosis message.
func (m Message) isDiagnosis() bool { return m.Event == EventDiagnosis }

// checkDiagnosis refuses a diagnosis message that carries no diagnosis, so nothing half-built is sent.
func (m Message) checkDiagnosis() error {
	if m.isDiagnosis() && m.Diagnosis == nil {
		return errNoDiagnosis
	}
	return nil
}

// summary is the cleaned diagnosis text, or "" when there is none.
func (m Message) summary() string {
	if m.Diagnosis == nil {
		return ""
	}
	return CleanSummary(m.Diagnosis.Summary, m.BaseURL)
}

// bodyText is the text under the headline: the alert's own, or the cleaned summary of a diagnosis.
func (m Message) bodyText() string {
	if m.isDiagnosis() {
		return m.summary()
	}
	return m.Subject.Text
}

// linkText is the label of the message's link.
func (m Message) linkText() string {
	if m.isDiagnosis() {
		return investigationLinkText
	}
	return "Open in Harbormaster"
}

// pagerDutyLink is the link of a PagerDuty event. The Opened event of a route that investigates points at the
// incident's investigation page; the investigation has no id yet when that event is sent.
func (m Message) pagerDutyLink() string {
	if m.Event == EventOpened && m.Investigate && m.IncidentID > 0 {
		return strings.TrimRight(m.BaseURL, "/") + "/investigations/incident/" + strconv.FormatInt(m.IncidentID, 10)
	}
	return m.Link()
}
