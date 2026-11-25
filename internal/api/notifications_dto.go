package api

import "time"

// NotificationSelectorDTO is one scope entry of a route. Kind is account, cluster, service, aws or harbormaster.
// A request reads kind plus account_id and region (account), cluster_id (cluster), service_id (service), key and
// value (aws) or tag_id (harbormaster); the names and missing are output only. Every field is always present.
type NotificationSelectorDTO struct {
	Kind        string `json:"kind"`
	AccountID   string `json:"account_id"`
	AccountName string `json:"account_name"`
	Region      string `json:"region"`
	ClusterID   string `json:"cluster_id"`
	Cluster     string `json:"cluster"`
	ServiceID   string `json:"service_id"`
	Service     string `json:"service"`
	Key         string `json:"key"`
	Value       string `json:"value"`
	TagID       int64  `json:"tag_id"`
	TagName     string `json:"tag_name"`
	// Threshold is the number of failing services that makes a cluster or account entry notify (1 to 500); null for
	// service and tag entries, which notify per alert.
	Threshold *int `json:"threshold"`
	// Missing: the account is not connected (or lacks the region), or the cluster or service is not in the snapshot.
	// The selector is kept and matches nothing until the target returns or the entry is removed.
	Missing bool `json:"missing"`
}

// NotificationRouteDTO is a route as the editor shows it. The destination's secret is never present: SecretSet says
// one is stored, Signed that a signing secret is, and SecretUnreadable that the stored one no longer opens.
type NotificationRouteDTO struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Enabled     bool     `json:"enabled"`
	Destination string   `json:"destination"`
	Target      string   `json:"target"` // display only: the label, the host of the stored URL, the chat or the recipients
	Label       string   `json:"label"`
	Recipients  []string `json:"recipients"`
	// PagerDutyRegion is "us" or "eu" for PagerDuty and empty otherwise.
	PagerDutyRegion  string                    `json:"pagerduty_region"`
	ChatID           string                    `json:"chat_id"`
	ThreadID         *int                      `json:"thread_id"`
	SecretSet        bool                      `json:"secret_set"`
	Signed           bool                      `json:"signed"`
	SecretUnreadable bool                      `json:"secret_unreadable"`
	MinSeverity      string                    `json:"min_severity"`
	Kinds            []string                  `json:"kinds"`
	RepeatSeconds    int                       `json:"repeat_seconds"`
	QuietEnabled     bool                      `json:"quiet_enabled"`
	QuietTimezone    string                    `json:"quiet_timezone"`
	NotifyResolved   bool                      `json:"notify_resolved"`
	Scope            []NotificationSelectorDTO `json:"scope"` // empty means everything
	Fallback         bool                      `json:"fallback"`
	// Investigate: a diagnosis from the AI agent follows the alert on this route.
	Investigate bool `json:"investigate"`
	// MatchesNothing: the route has scope entries and every one is missing.
	MatchesNothing bool      `json:"matches_nothing"`
	Failing        bool      `json:"failing"` // its last three deliveries all failed
	Version        int       `json:"version"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// NotificationKindDTO is a routable alert kind.
type NotificationKindDTO struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
}

// NotificationRoutesDTO is GET /api/notifications/routes.
type NotificationRoutesDTO struct {
	Items            []NotificationRouteDTO `json:"items"`
	EmailAvailable   bool                   `json:"email_available"`
	SecretsAvailable bool                   `json:"secrets_available"`
	Kinds            []NotificationKindDTO  `json:"kinds"`
	// SettleSeconds is how long service_failing and service_degraded must stay open before they notify or count.
	SettleSeconds int `json:"settle_seconds"`
}

// NotificationRouteInput is the body of create, update and preview. A secret that is nil keeps the stored one on
// update; an empty signing_secret removes it. Version is required on update.
type NotificationRouteInput struct {
	Name            string                    `json:"name"`
	Enabled         bool                      `json:"enabled"`
	Destination     string                    `json:"destination"`
	Label           string                    `json:"label"`
	Recipients      []string                  `json:"recipients"`
	PagerDutyRegion string                    `json:"pagerduty_region"`
	ChatID          string                    `json:"chat_id"`
	ThreadID        *int                      `json:"thread_id"`
	URL             *string                   `json:"url"`
	RoutingKey      *string                   `json:"routing_key"`
	SigningSecret   *string                   `json:"signing_secret"`
	BotToken        *string                   `json:"bot_token"`
	MinSeverity     string                    `json:"min_severity"`
	Kinds           []string                  `json:"kinds"`
	RepeatSeconds   int                       `json:"repeat_seconds"`
	QuietEnabled    bool                      `json:"quiet_enabled"`
	QuietTimezone   string                    `json:"quiet_timezone"`
	NotifyResolved  bool                      `json:"notify_resolved"`
	Scope           []NotificationSelectorDTO `json:"scope"`
	Fallback        bool                      `json:"fallback"`
	// Investigate is optional: a request that leaves it out (an older browser bundle) keeps the stored value.
	Investigate *bool `json:"investigate,omitempty"`
	Version     *int  `json:"version,omitempty"`
}

// NotificationPreviewRequest is POST /api/notifications/preview. RouteID names the route being edited, so a
// catch-all draft does not count that route as covering an alert.
type NotificationPreviewRequest struct {
	NotificationRouteInput
	RouteID *int64 `json:"route_id,omitempty"`
}

// NotificationMatchDTO is one open alert a draft route would take.
type NotificationMatchDTO struct {
	Service  string `json:"service"`
	Cluster  string `json:"cluster"`
	Kind     string `json:"kind"`
	Severity string `json:"severity"`
	Title    string `json:"title"`
}

// NotificationGroupPreviewDTO is one cluster or account entry of the draft: open when its threshold is met, else
// listed with the services that are failing.
type NotificationGroupPreviewDTO struct {
	Kind        string   `json:"kind"` // cluster | account
	Label       string   `json:"label"`
	ClusterID   string   `json:"cluster_id"`
	AccountID   string   `json:"account_id"`
	Region      string   `json:"region"`
	Threshold   int      `json:"threshold"`
	Failing     int      `json:"failing"`
	Total       int      `json:"total"`
	Open        bool     `json:"open"`
	Unreachable bool     `json:"unreachable"`
	Services    []string `json:"services"` // failing service names, at most 10
}

// NotificationPreviewDTO is the server-rendered message and the open alerts the draft would take. Sample is true
// when no open alert matched and the built-in sample was rendered.
type NotificationPreviewDTO struct {
	Matches     []NotificationMatchDTO `json:"matches"` // at most 20
	MatchCount  int                    `json:"match_count"`
	Sample      bool                   `json:"sample"`
	Destination string                 `json:"destination"`
	Title       string                 `json:"title"`
	Body        string                 `json:"body"`
	JSON        string                 `json:"json"`
	// Groups lists the draft's cluster and account entries: open ones first, then those below their threshold.
	Groups []NotificationGroupPreviewDTO `json:"groups"`
	// NotificationCount is what the route would send right now: per-alert matches plus open groups.
	NotificationCount int `json:"notification_count"`
	// Pending counts alerts still inside the settle time that would otherwise match or count.
	Pending int `json:"pending"`
	// Agent is set when the draft has investigate on and the agent is on: how many of the route's services the agent
	// identity cannot View now.
	Agent *NotificationPreviewAgentDTO `json:"agent"`
}

// NotificationPreviewAgentDTO counts the route's services the agent identity can and cannot see.
type NotificationPreviewAgentDTO struct {
	ServicesTotal  int `json:"services_total"`
	ServicesUnseen int `json:"services_unseen"`
}

// NotificationDeliveryDTO is one row of the delivery log.
type NotificationDeliveryDTO struct {
	ID          int64      `json:"id"`
	At          time.Time  `json:"at"`
	FinishedAt  *time.Time `json:"finished_at"`
	Event       string     `json:"event"` // opened | repeat | resolved | test
	Alert       string     `json:"alert"` // the alert title, or "Test message"
	Service     string     `json:"service"`
	Cluster     string     `json:"cluster"`
	ServiceID   string     `json:"service_id"`
	RouteID     *int64     `json:"route_id"`
	Route       string     `json:"route"`
	Destination string     `json:"destination"`
	Target      string     `json:"target"`
	Status      string     `json:"status"` // pending | held | delivered | failed | cancelled
	Note        string     `json:"note"`
	HeldUntil   *time.Time `json:"held_until"`
	Attempts    int        `json:"attempts"`
	CanRetry    bool       `json:"can_retry"`
	// Scope is service, cluster or account; empty for a test message. ClusterID, AccountID and Region link a row to
	// its cluster or account (an aggregated row has no service).
	Scope     string `json:"scope"`
	ClusterID string `json:"cluster_id"`
	AccountID string `json:"account_id"`
	Region    string `json:"region"`
}

// NotificationTargetAccountDTO is a connected account the scope picker can offer.
type NotificationTargetAccountDTO struct {
	AccountID string   `json:"account_id"`
	Name      string   `json:"name"`
	Regions   []string `json:"regions"`
}

// NotificationTargetClusterDTO is a cluster the scope picker can offer.
type NotificationTargetClusterDTO struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	AccountID   string `json:"account_id"`
	AccountName string `json:"account_name"`
	Region      string `json:"region"`
}

// NotificationTargetServiceDTO is an ACTIVE service the scope picker can offer.
type NotificationTargetServiceDTO struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Cluster     string `json:"cluster"`
	ClusterID   string `json:"cluster_id"`
	AccountID   string `json:"account_id"`
	AccountName string `json:"account_name"`
	Region      string `json:"region"`
}

// NotificationTargetsDTO is GET /api/notifications/targets: everything the scope picker offers except tags.
type NotificationTargetsDTO struct {
	Accounts []NotificationTargetAccountDTO `json:"accounts"`
	Clusters []NotificationTargetClusterDTO `json:"clusters"`
	Services []NotificationTargetServiceDTO `json:"services"`
}
