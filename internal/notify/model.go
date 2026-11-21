// Package notify decides which alerts go to which notification routes and sends them. This file holds the shared
// vocabulary; matching, quiet hours and message rendering are pure functions in their own files.
package notify

import (
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
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

// Subject is what a message is about.
type Subject struct {
	Key         model.ServiceKey
	Kind        string
	Severity    string // "critical" | "warning"
	Title, Text string
	At          time.Time
	AccountName string
}

// Event says why a message is sent.
type Event string

const (
	EventOpened   Event = "opened"
	EventRepeat   Event = "repeat"
	EventResolved Event = "resolved"
	EventTest     Event = "test"
)

// Message is one notification to render and send.
type Message struct {
	Event      Event
	Subject    Subject
	DeliveryID int64
	BaseURL    string
	RouteName  string
}
