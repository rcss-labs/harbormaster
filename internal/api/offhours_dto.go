package api

import "time"

// OffHoursScheduleInput is the body of create, update and preview. Version is required on update.
type OffHoursScheduleInput struct {
	Name     string      `json:"name"`
	Enabled  bool        `json:"enabled"`
	TagKey   string      `json:"tag_key"`
	TagValue string      `json:"tag_value"`
	Timezone string      `json:"timezone"`
	Awake    [7][24]bool `json:"awake"`
	Version  *int        `json:"version,omitempty"`
}

// OffHoursPreviewRequest is the body of POST /api/offhours/preview. ScheduleID names the schedule being edited, so
// it does not overlap itself.
type OffHoursPreviewRequest struct {
	OffHoursScheduleInput
	ScheduleID *int64 `json:"schedule_id,omitempty"`
}

// OffHoursListDTO is the body of GET /api/offhours/schedules.
type OffHoursListDTO struct {
	Items              []OffHoursScheduleDTO `json:"items"`
	MonthlySavingTotal *float64              `json:"monthly_saving_total"`
	PricesAsOf         string                `json:"prices_as_of"`
	Now                time.Time             `json:"now"`
}

// OffHoursScheduleDTO is a schedule. The list carries counts and an empty services array; the detail carries the rows.
type OffHoursScheduleDTO struct {
	ID                int64                `json:"id"`
	Name              string               `json:"name"`
	Enabled           bool                 `json:"enabled"`
	TagKey            string               `json:"tag_key"`
	TagValue          string               `json:"tag_value"`
	Timezone          string               `json:"timezone"`
	Awake             [7][24]bool          `json:"awake"`
	AwakeHoursPerWeek int                  `json:"awake_hours_per_week"`
	MonthlySaving     *float64             `json:"monthly_saving"`
	UnpricedServices  int                  `json:"unpriced_services"`
	ServiceCount      int                  `json:"service_count"`
	AsleepCount       int                  `json:"asleep_count"`
	BlockedCount      int                  `json:"blocked_count"`
	Version           int                  `json:"version"`
	UpdatedAt         time.Time            `json:"updated_at"`
	UpdatedBy         string               `json:"updated_by"`
	Services          []OffHoursServiceDTO `json:"services"`
}

// OffHoursServiceDTO is one service a schedule matches. State is asleep, awake, woken, blocked, pending,
// not_managed or conflict; Reason carries the sentence for blocked, not_managed, conflict and needs-attention rows.
type OffHoursServiceDTO struct {
	ServiceID           string     `json:"service_id"`
	Service             string     `json:"service"`
	Cluster             string     `json:"cluster"`
	AccountName         string     `json:"account_name"`
	Region              string     `json:"region"`
	Tag                 TagDTO     `json:"tag"`
	AwakeTasks          int        `json:"awake_tasks"`
	CurrentTasks        int        `json:"current_tasks"`
	State               string     `json:"state"`
	Until               *time.Time `json:"until"`
	Reason              string     `json:"reason"`
	MonthlyAlwaysOn     *float64   `json:"monthly_always_on"`
	LooksLikeProduction []string   `json:"looks_like_production"`
	CanOperate          bool       `json:"can_operate"`
}

// OffHoursPreviewDTO is the editor preview: nothing is written.
type OffHoursPreviewDTO struct {
	Services          []OffHoursServiceDTO `json:"services"`
	Overlaps          []OverlapDTO         `json:"overlaps"`
	SleepNow          int                  `json:"sleep_now"`
	MonthlySaving     *float64             `json:"monthly_saving"`
	AwakeHoursPerWeek int                  `json:"awake_hours_per_week"`
}

// OverlapDTO is another schedule that already matches some of the previewed services (cluster/service names).
type OverlapDTO struct {
	ScheduleID   int64    `json:"schedule_id"`
	ScheduleName string   `json:"schedule_name"`
	Services     []string `json:"services"`
}

// OffHoursTagOptionDTO is one AWS tag key/value seen on active services.
type OffHoursTagOptionDTO struct {
	Key      string `json:"key"`
	Value    string `json:"value"`
	Services int    `json:"services"`
}

// ServiceOffHoursDTO is GET /api/services/{id}/offhours. Enrolled is true once the reconciler has taken the service
// on; a service that a schedule matches but has not enrolled yet has Enrolled false with ScheduleName and State set.
// A service no schedule matches has everything empty.
type ServiceOffHoursDTO struct {
	Enrolled     bool       `json:"enrolled"`
	ScheduleName string     `json:"schedule_name"`
	Timezone     string     `json:"timezone"`
	State        string     `json:"state"`
	Until        *time.Time `json:"until"`
	Reason       string     `json:"reason"`
	CanOperate   bool       `json:"can_operate"`
}

// WakeResponse is the answer to a manual wake.
type WakeResponse struct {
	WokenUntil time.Time `json:"woken_until"`
}
