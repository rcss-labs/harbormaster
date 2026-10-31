package model

import (
	"encoding/json"
	"time"
)

// Target is one load balancer target. State is one of healthy, unhealthy,
// initial, draining, unused or unavailable.
type Target struct {
	ID          string
	Port        int
	AZ          string
	State       string
	Reason      string
	Description string
}

// TargetHealth is the health of one target group attached to a service.
type TargetHealth struct {
	Key             ServiceKey
	TargetGroupARN  string
	LoadBalancerARN string
	Healthy         int
	Unhealthy       int
	Targets         []Target
	Raw             json.RawMessage
	PolledAt        time.Time
}

// ScalingPolicy is an Application Auto Scaling policy. Type is TargetTrackingScaling or StepScaling.
type ScalingPolicy struct {
	Name             string
	Type             string
	Metric           string
	TargetValue      float64
	ScaleOutCooldown int
	ScaleInCooldown  int
	Raw              json.RawMessage
}

// ScheduledAction is an Application Auto Scaling scheduled action.
type ScheduledAction struct {
	Name        string
	Schedule    string
	Timezone    string
	MinCapacity *int
	MaxCapacity *int
	Raw         json.RawMessage
}

// ScalingActivity is one scaling event for a service.
type ScalingActivity struct {
	ID            string
	Description   string
	Cause         string
	StatusCode    string
	StatusMessage string
	StartedAt     time.Time
	EndedAt       *time.Time
}

// Scaling is the Application Auto Scaling state of a service.
type Scaling struct {
	Key        ServiceKey
	HasTarget  bool
	Min, Max   int
	Policies   []ScalingPolicy
	Scheduled  []ScheduledAction
	Activities []ScalingActivity
	PolledAt   time.Time
}

// AlertSeverity ranks an alert.
type AlertSeverity string

const (
	AlertDanger  AlertSeverity = "danger"
	AlertWarning AlertSeverity = "warning"
	AlertInfo    AlertSeverity = "info"
)

// Alert is one observed problem. Key.Service is empty for account-level alerts such as account_unreachable.
type Alert struct {
	Key      ServiceKey
	Kind     string
	Severity AlertSeverity
	Title    string
	Text     string
	At       time.Time
	Tab      string
}
