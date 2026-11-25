package notify

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Link is where the message points: the cluster page or the account's overview for a group, the service page for an
// alert, or the accounts page for an account-level alert.
func (m Message) Link() string {
	base := strings.TrimRight(m.BaseURL, "/")
	if m.isDiagnosis() && m.Diagnosis != nil {
		return base + "/investigations/" + strconv.FormatInt(m.Diagnosis.InvestigationID, 10)
	}
	if m.isReview() && m.Review != nil {
		return base + "/reviews/runs/" + strconv.FormatInt(m.Review.RunID, 10)
	}
	switch m.Subject.Scope {
	case "cluster":
		return base + "/clusters/" + m.Subject.Key.ClusterKey.ID()
	case "account":
		link := base + "/?account=" + url.QueryEscape(m.Subject.Key.AccountID)
		if m.Subject.Key.Region != "" {
			link += "&region=" + url.QueryEscape(m.Subject.Key.Region)
		}
		return link
	}
	if m.Subject.Key.Service == "" {
		return base + "/settings/accounts"
	}
	return base + "/services/" + m.Subject.Key.ID()
}

// DedupKey identifies one alert, or one route's group, across opened, repeat, update and resolved events. A group
// key carries the route: two routes on one cluster are two incidents, and one resolving must not close the other.
func DedupKey(s Subject) string {
	k := s.Key
	if s.Scope != "" {
		return fmt.Sprintf("hm/route-%d/%s/%s/%s/%s", s.RouteID, s.Scope, k.AccountID, k.Region, k.Cluster)
	}
	return fmt.Sprintf("hm/%s/%s/%s/%s/%s", k.AccountID, k.Region, k.Cluster, k.Service, s.Kind)
}

func (m Message) accountName() string {
	if m.Subject.AccountName != "" {
		return m.Subject.AccountName
	}
	return m.Subject.Key.AccountID
}

// where is the cluster and account part shared by the human-readable renderings.
func (m Message) where() string {
	k := m.Subject.Key
	place := m.accountName()
	if k.Region != "" {
		place += " / " + k.Region
	}
	if k.Cluster == "" {
		return place
	}
	return k.Cluster + " · " + place
}

// plainHeadline is the title line without markup.
func (m Message) plainHeadline() string {
	switch m.Event {
	case EventResolved:
		return "Resolved: " + m.Subject.Title
	case EventUpdate:
		return "Update: " + m.Subject.Title
	case EventDiagnosis:
		return "Diagnosis: " + m.Subject.Title
	case EventReview:
		if m.Review != nil {
			return m.reviewTitle()
		}
	case EventTest:
		return "Test message from Harbormaster"
	}
	return m.Subject.Title
}

// updatePrefix marks an update to an incident that is already open.
func (m Message) updatePrefix() string {
	if m.Event == EventUpdate {
		return "Update: "
	}
	return ""
}

// slackEscape applies Slack's escaping so alert text cannot inject links, mentions or formatting entities.
var slackEscape = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// SlackBody renders an incoming-webhook payload: a section (title, then text) and a context line.
func SlackBody(m Message) ([]byte, error) {
	if err := m.checkMessage(); err != nil {
		return nil, err
	}
	if m.isAI() {
		return slackAIBody(m)
	}
	var title string
	switch {
	case m.Event == EventTest:
		title = "Test message from Harbormaster"
	case m.Event == EventResolved:
		title = ":white_check_mark: Resolved: " + slackEscape.Replace(m.Subject.Title)
	case m.Subject.Severity == "critical":
		title = ":red_circle: *" + slackEscape.Replace(m.updatePrefix()+m.Subject.Title) + "*"
	default:
		title = ":large_orange_circle: *" + slackEscape.Replace(m.updatePrefix()+m.Subject.Title) + "*"
	}
	section := title
	if m.Subject.Text != "" {
		section += "\n" + slackEscape.Replace(m.Subject.Text)
	}
	context := slackEscape.Replace(m.where()) + " · <" + m.Link() + "|Open in Harbormaster>"
	return json.Marshal(map[string]any{
		"text": m.plainHeadline(),
		"blocks": []any{
			map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": section}},
			map[string]any{"type": "context", "elements": []any{map[string]any{"type": "mrkdwn", "text": context}}},
		},
	})
}

const pagerDutySummaryMax = 1024

// PagerDutyBody renders an Events API v2 event. Trigger and resolve share the dedup key so a resolve closes the
// incident the trigger opened.
func PagerDutyBody(m Message, routingKey string) ([]byte, error) {
	if m.isAI() {
		return nil, errors.New("PagerDuty does not receive AI-written messages")
	}
	s := m.Subject
	action, dedup := "trigger", DedupKey(s)
	if m.Event == EventResolved {
		action = "resolve"
	}
	if m.Event == EventTest {
		dedup = fmt.Sprintf("hm/test/%d", m.DeliveryID)
	}
	summary := []rune(m.plainHeadline())
	if len(summary) > pagerDutySummaryMax {
		summary = summary[:pagerDutySummaryMax]
	}
	source := s.Key.Cluster + "/" + s.Key.Service
	switch {
	case s.Scope == "cluster":
		source = s.Key.Cluster
	case s.Key.Cluster == "":
		source = s.Key.AccountID + "/" + s.Key.Region
	}
	details := map[string]any{"text": s.Text, "account_id": s.Key.AccountID, "region": s.Key.Region}
	if s.Scope != "" {
		names := make([]string, len(s.Members))
		for i, member := range s.Members {
			names[i] = member.Service
		}
		details["failing_services"], details["failing"], details["total"], details["threshold"] = names, s.Failing, s.Total, s.Threshold
	}
	severity := s.Severity
	if severity != "critical" {
		severity = "warning"
	}
	return json.Marshal(map[string]any{
		"routing_key":  routingKey,
		"event_action": action,
		"dedup_key":    dedup,
		"payload": map[string]any{
			"summary":        string(summary),
			"source":         source,
			"severity":       severity,
			"timestamp":      s.At.UTC().Format(time.RFC3339),
			"component":      s.Key.Service,
			"group":          s.Key.Cluster,
			"class":          s.Kind,
			"custom_details": details,
		},
		"links":  []any{map[string]any{"href": m.pagerDutyLink(), "text": "Open in Harbormaster"}},
		"client": "Harbormaster",
	})
}

// WebhookBody renders the flat JSON object posted to a webhook.
func WebhookBody(m Message) ([]byte, error) {
	if err := m.checkMessage(); err != nil {
		return nil, err
	}
	s := m.Subject
	event := map[Event]string{EventOpened: "alert.raised", EventRepeat: "alert.repeated", EventUpdate: "alert.updated",
		EventResolved: "alert.resolved", EventTest: "test", EventDiagnosis: "diagnosis", EventReview: "review"}[m.Event]
	scope := s.Scope
	switch {
	case scope != "":
	case s.Key.Service != "":
		scope = "service"
	default:
		scope = "account" // an account-level alert (the account is unreachable)
	}
	body := map[string]any{
		"event": event, "delivery_id": m.DeliveryID, "scope": scope, "kind": s.Kind, "severity": s.Severity,
		"title": s.Title, "text": s.Text, "service": s.Key.Service, "cluster": s.Key.Cluster,
		"account_id": s.Key.AccountID, "region": s.Key.Region, "at": s.At.UTC().Format(time.RFC3339), "url": m.Link(),
	}
	if m.isDiagnosis() {
		body["summary"], body["investigation_id"], body["ai_generated"] = m.summary(), m.Diagnosis.InvestigationID, true
	}
	if m.isReview() {
		body["scope"], body["title"] = "review", m.plainHeadline()
		body["summary"], body["ai_generated"] = m.summary(), true
		body["review_run_id"], body["review_name"], body["review_type"], body["period"] =
			m.Review.RunID, m.reviewName(), m.Review.Type, m.Review.PeriodLabel
	}
	if s.Scope != "" {
		services := make([]map[string]any, len(s.Members))
		for i, member := range s.Members {
			services[i] = map[string]any{"service": member.Service, "cluster": member.Cluster, "kind": member.Kind, "title": member.Title}
		}
		body["threshold"], body["failing"], body["total"], body["services"] = s.Threshold, s.Failing, s.Total, services
	}
	return json.Marshal(body)
}

// Sign returns the X-Harbormaster-Signature value for body.
func Sign(body []byte, signingSecret string) string {
	mac := hmac.New(sha256.New, []byte(signingSecret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// headerValue removes line breaks so a value cannot start another header.
func headerValue(v string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(v)
}

// emailSubject is the plain subject line.
func emailSubject(m Message) string {
	return headerValue("[Harbormaster] " + m.plainHeadline())
}

// crlf gives free text the line breaks SMTP expects.
func crlf(text string) string {
	return strings.NewReplacer("\r\n", "\r\n", "\r", "\r\n", "\n", "\r\n").Replace(text)
}

// emailText is the body: title, text, where it happened and the link.
func emailText(m Message) string {
	var b strings.Builder
	if m.isReview() {
		b.WriteString(diagnosisNotice + "\r\n\r\n" + headerValue(m.Review.PeriodLabel) + "\r\n\r\n" + crlf(m.summary()) + "\r\n\r\n" + m.Link() + "\r\n")
		return b.String()
	}
	if m.isDiagnosis() {
		b.WriteString(diagnosisNotice + "\r\n\r\n" + crlf(m.summary()) + "\r\n\r\n" + m.Link() + "\r\n")
		return b.String()
	}
	b.WriteString(m.plainHeadline() + "\r\n\r\n")
	if m.Subject.Text != "" {
		b.WriteString(m.Subject.Text + "\r\n\r\n")
	}
	k := m.Subject.Key
	if k.Cluster != "" {
		b.WriteString("Cluster: " + k.Cluster + "\r\n")
	}
	b.WriteString("Account: " + m.accountName() + " (" + k.AccountID + ")\r\n")
	if k.Region != "" {
		b.WriteString("Region: " + k.Region + "\r\n")
	}
	b.WriteString("\r\n")
	b.WriteString("Open in Harbormaster: " + m.Link() + "\r\n")
	return b.String()
}

// foldedSubject Q-encodes subject as a run of encoded words of at most 75 characters, folded onto continuation
// lines, so no header line passes the 998 octets SMTP allows however long the subject is. A short ASCII subject is
// written as it is, unless it holds "=?": a recipient would decode that as an encoded word of its own.
func foldedSubject(subject string) string {
	const maxPlain, maxWordBytes = 900, 21 // 21 bytes encode to at most 63 characters, plus 12 for the word's frame
	if len(subject) <= maxPlain && !strings.Contains(subject, "=?") && mime.QEncoding.Encode("utf-8", subject) == subject {
		return subject
	}
	var words []string
	start := 0
	for i := range subject {
		if i-start > maxWordBytes-utf8.UTFMax {
			words = append(words, qWord(subject[start:i]))
			start = i
		}
	}
	words = append(words, qWord(subject[start:]))
	return strings.Join(words, "\r\n ")
}

// qWord is one RFC 2047 Q encoded word. Every word is encoded, even an all-ASCII one, because white space between
// two encoded words is dropped on decoding and white space between a plain word and an encoded one is not.
func qWord(s string) string {
	var b strings.Builder
	b.WriteString("=?utf-8?q?")
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == ' ':
			b.WriteByte('_')
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "=%02X", c)
		}
	}
	b.WriteString("?=")
	return b.String()
}

// Email renders a complete RFC 5322 message (text/plain, UTF-8) and the plain subject. Every header value has its
// line breaks stripped, and a non-ASCII subject is Q-encoded.
func Email(m Message, from string, to []string) (subject string, body []byte) {
	subject = emailSubject(m)
	cleanTo := make([]string, len(to))
	for i, addr := range to {
		cleanTo[i] = headerValue(addr)
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "From: %s\r\n", headerValue(from))
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(cleanTo, ", "))
	encodedSubject := mime.QEncoding.Encode("utf-8", subject)
	if m.isReview() {
		encodedSubject = foldedSubject(subject)
	}
	fmt.Fprintf(&b, "Subject: %s\r\n", encodedSubject)
	fmt.Fprintf(&b, "Date: %s\r\n", m.Subject.At.UTC().Format(time.RFC1123Z))
	fmt.Fprintf(&b, "Message-ID: <hm-%d-%d@harbormaster.invalid>\r\n", m.DeliveryID, m.Subject.At.Unix())
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	if m.isAI() {
		// Model text can hold lines of any length; quoted-printable keeps every line short and safe.
		b.WriteString("Content-Transfer-Encoding: quoted-printable\r\n\r\n")
		encoder := quotedprintable.NewWriter(&b)
		_, _ = encoder.Write([]byte(emailText(m)))
		_ = encoder.Close()
		return subject, b.Bytes()
	}
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
	b.WriteString(emailText(m))
	return subject, b.Bytes()
}

// Preview is what the editor shows for a destination. Title and Body serve Slack, PagerDuty, Telegram and email;
// JSON serves the webhook.
type Preview struct{ Destination, Title, Body, JSON string }

// RenderPreview renders m the way the destination's sender would, so the preview cannot drift from what is sent.
func RenderPreview(destination string, m Message) (Preview, error) {
	if err := m.checkMessage(); err != nil {
		return Preview{}, err
	}
	p := Preview{Destination: destination}
	switch destination {
	case DestSlack:
		if _, err := SlackBody(m); err != nil {
			return Preview{}, err
		}
		p.Title = m.plainHeadline()
		text := m.Subject.Text
		if m.isAI() {
			text = diagnosisNotice + "\n" + m.summary()
		}
		p.Body = strings.TrimSpace(text + "\n" + m.where())
	case DestPagerDuty:
		if _, err := PagerDutyBody(m, "preview"); err != nil {
			return Preview{}, err
		}
		p.Title = m.plainHeadline()
		p.Body = strings.TrimSpace(m.Subject.Text + "\n" + m.where())
	case DestTelegram:
		return telegramPreview(m), nil
	case DestWebhook:
		body, err := WebhookBody(m)
		if err != nil {
			return Preview{}, err
		}
		var indented bytes.Buffer
		if err := json.Indent(&indented, body, "", "  "); err != nil {
			return Preview{}, err
		}
		p.JSON = indented.String()
	case DestEmail:
		p.Title = emailSubject(m)
		p.Body = emailText(m)
	default:
		return Preview{}, fmt.Errorf("unknown destination %q", destination)
	}
	return p, nil
}
