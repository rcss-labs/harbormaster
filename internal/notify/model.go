// Package notify decides which alerts go to which notification routes and sends them. This file holds the shared
// vocabulary; matching, quiet hours and message rendering are pure functions in their own files.
package notify

import (
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// Destinations a route can send to.
const (
	DestSlack     = "slack"
	DestTelegram  = "telegram"
	DestPagerDuty = "pagerduty"
	DestWebhook   = "webhook"
	DestEmail     = "email"
)

// KindInfo names a routable alert kind for the UI.
type KindInfo struct{ Kind, Label string }

// Kinds are the alert kinds a route can choose from, in display order. rollout_in_progress is info and is never
// notified.
var Kinds = []KindInfo{
	{"service_failing", "Service failing"},
	{"service_degraded", "Service degraded"},
	{"restart_loop", "Restart loop"},
	{"unhealthy_task", "Unhealthy tasks or targets"},
	{"deployment_failed", "Deployment failed"},
	{"cannot_place", "Cannot place tasks"},
	{"account_unreachable", "Account unreachable"},
}

// KnownKind reports whether kind is routable.
func KnownKind(kind string) bool {
	for _, k := range Kinds {
		if k.Kind == kind {
			return true
		}
	}
	return false
}

// SeverityName maps an alert severity to the name the API and messages use. Info alerts are never notified.
func SeverityName(s model.AlertSeverity) (string, bool) {
	switch s {
	case model.AlertDanger:
		return "critical", true
	case model.AlertWarning:
		return "warning", true
	}
	return "", false
}

// Secret is what a route seals: every credential the destination needs. Unused fields stay empty.
type Secret struct {
	URL           string `json:"url,omitempty"`
	RoutingKey    string `json:"routing_key,omitempty"`
	SigningSecret string `json:"signing_secret,omitempty"`
	BotToken      string `json:"bot_token,omitempty"`
}

// Kinds of a group incident: an aggregated notification for a cluster or account entry.
const (
	KindClusterFailing = "cluster_failing"
	KindAccountFailing = "account_failing"
)

// Subject is what a message is about: one alert, or (Scope "cluster" or "account") a group of failing services.
type Subject struct {
	Key         model.ServiceKey
	Kind        string
	Severity    string // "critical" | "warning"
	Title, Text string
	At          time.Time
	AccountName string
	// Scope is "" for an alert, "cluster" or "account" for a group. The fields below are for groups only.
	Scope                     string
	RouteID                   int64
	Threshold, Failing, Total int
	Members                   []store.IncidentMember
}

// Event says why a message is sent.
type Event string

const (
	EventOpened   Event = "opened"
	EventRepeat   Event = "repeat"
	EventUpdate   Event = "update"
	EventResolved Event = "resolved"
	EventTest     Event = "test"
	// EventDiagnosis carries the AI-written diagnosis of an alert, sent after the alert itself.
	EventDiagnosis Event = "diagnosis"
	// EventReview carries the AI-written message of one finished review run.
	EventReview Event = "review"
)

// Diagnosis is the model-written summary of one investigation. Its text is untrusted: every renderer cleans it
// with CleanSummary at send time.
type Diagnosis struct {
	InvestigationID int64
	Summary         string
	// NameIDs are the stored ids whose names the summary may use.
	NameIDs []string
}

// Review is the model-written summary of one review run. Its summary is untrusted: every renderer cleans it with
// CleanReviewSummary at send time. Type is one of cost, security, reliability, capacity; any other value is refused.
type Review struct {
	RunID                            int64
	Name, Type, PeriodLabel, Summary string
	// NameIDs are the stored ids whose names the summary may use.
	NameIDs []string
}

// Message is one notification to render and send.
type Message struct {
	Event      Event
	Subject    Subject
	DeliveryID int64
	BaseURL    string
	RouteName  string
	// Diagnosis is set for EventDiagnosis only.
	Diagnosis *Diagnosis
	// Review is set for EventReview only.
	Review *Review
	// IncidentID is the notification incident the message belongs to; Investigate is the route's switch. Together
	// they point a PagerDuty Opened event at the incident's investigation page.
	IncidentID  int64
	Investigate bool
}
