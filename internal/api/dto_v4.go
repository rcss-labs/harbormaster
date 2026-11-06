package api

import (
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
)

// PollerTierDTO is the poll status of one data type (tiers sharing a label are merged) of an account x region.
// Core tiers feed the item's failing/throttled/last_error; the others only appear here.
type PollerTierDTO struct {
	Tier       string     `json:"tier"`
	Label      string     `json:"label"`
	Core       bool       `json:"core"`
	Failing    bool       `json:"failing"`
	Throttled  bool       `json:"throttled"`
	LastOKAt   *time.Time `json:"last_ok_at"`
	LastError  string     `json:"last_error"`
	IntervalMs int64      `json:"interval_ms"`
}

// PartialErrorDTO names a part of a composite response that could not be read.
type PartialErrorDTO struct {
	Part    string `json:"part"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ServiceListCanDTO is what the caller may do with one service in the flat list.
type ServiceListCanDTO struct {
	Operate  bool `json:"operate"`
	Logs     bool `json:"logs"`
	Terminal bool `json:"terminal"`
}

// ServiceListItemDTO is one row of GET /api/services. Rollout is "in_progress", "failed", "completed" or null.
type ServiceListItemDTO struct {
	ID                       string            `json:"id"`
	Name                     string            `json:"name"`
	Cluster                  ClusterRef        `json:"cluster"`
	AccountID                string            `json:"account_id"`
	AccountName              string            `json:"account_name"`
	Region                   string            `json:"region"`
	Tag                      TagDTO            `json:"tag"`
	TagInherited             bool              `json:"tag_inherited"`
	Status                   model.Status      `json:"status"`
	Attention                string            `json:"attention"`
	Family                   string            `json:"family"`
	Revision                 int               `json:"revision"`
	ImageTag                 string            `json:"image_tag"`
	Running                  int               `json:"running"`
	Desired                  int               `json:"desired"`
	CPU                      string            `json:"cpu"`
	Memory                   string            `json:"memory"`
	LatestRegisteredRevision *int              `json:"latest_registered_revision"`
	LastChangeAt             *time.Time        `json:"last_change_at"`
	Rollout                  *string           `json:"rollout"`
	Can                      ServiceListCanDTO `json:"can"`
}

// ServiceListDTO is the body of GET /api/services.
type ServiceListDTO struct {
	Items     []ServiceListItemDTO `json:"items"`
	Total     int                  `json:"total"`
	Truncated bool                 `json:"truncated"`
	PolledAt  time.Time            `json:"polled_at"`
}

// AlertStepDTO is the server-built next action for an alert.
type AlertStepDTO struct {
	Label string `json:"label"`
	Href  string `json:"href"`
}

// AlertChangeDTO is the newest change that preceded an alert; never synthesised.
type AlertChangeDTO struct {
	At     time.Time `json:"at"`
	Text   string    `json:"text"`
	Source string    `json:"source"`
}

// ServiceNowMetricsDTO is one service's latest CPU and memory use (percent of reserved).
// Insights is the cluster's Container Insights mode ("enabled", "enhanced" or "disabled"); when it is
// "disabled" cpu and mem are null because nothing was measured, which is not the same as 0.
type ServiceNowMetricsDTO struct {
	Insights string   `json:"insights"`
	CPU      *float64 `json:"cpu"`
	Mem      *float64 `json:"mem"`
}

// ServicesMetricsDTO is the body of GET /api/services/metrics?id=&id=. Items holds only the ids the
// caller may see and whose account x region could be read; every target that failed is in Errors.
type ServicesMetricsDTO struct {
	Items    map[string]ServiceNowMetricsDTO `json:"items"`
	Errors   []TargetErrorDTO                `json:"errors"`
	Stale    *StaleDTO                       `json:"stale"`
	PolledAt time.Time                       `json:"polled_at"`
}
