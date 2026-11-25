package api

import (
	"encoding/json"
	"time"
)

// ReviewScheduleDTO is when a review runs. Weekday is set for weekly reviews and MonthDay for monthly ones, null
// otherwise; AtMinute counts local minutes after midnight in Timezone.
type ReviewScheduleDTO struct {
	Cadence  string `json:"cadence"`
	AtMinute int    `json:"at_minute"`
	Weekday  *int   `json:"weekday"`
	MonthDay *int   `json:"month_day"`
	Timezone string `json:"timezone"`
}

// ReviewRouteDTO is a route a review is sent to.
type ReviewRouteDTO struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Destination string `json:"destination"`
	Enabled     bool   `json:"enabled"`
}

// ReviewLastRunDTO is the latest run of a review.
type ReviewLastRunDTO struct {
	ID         int64      `json:"id"`
	State      string     `json:"state"`
	Reason     string     `json:"reason"`
	FinishedAt *time.Time `json:"finished_at"`
}

// ReviewDTO is a review definition as the editor shows it. Instructions is empty for a built-in type. Scope entries are the selector DTO of routes, with names
// and missing, and never carry a threshold. NextRunAt is null when the review is off, when reviews are off or when
// the schedule does not validate. WorstCaseMonthUSD is null without a reviewer model.
type ReviewDTO struct {
	ID                int64                     `json:"id"`
	Name              string                    `json:"name"`
	Type              string                    `json:"type"`
	Enabled           bool                      `json:"enabled"`
	Schedule          ReviewScheduleDTO         `json:"schedule"`
	Scope             []NotificationSelectorDTO `json:"scope"`
	Routes            []ReviewRouteDTO          `json:"routes"`
	Version           int                       `json:"version"`
	NextRunAt         *time.Time                `json:"next_run_at"`
	LastRun           *ReviewLastRunDTO         `json:"last_run"`
	WorstCaseMonthUSD *string                   `json:"worst_case_month_usd"`
	Instructions      string                    `json:"instructions"`
}

// ReviewScopeInput is one scope entry of a request: an account (region optional), a cluster or a Harbormaster tag.
// A threshold, or any member of another kind, is refused by the strict decoder.
type ReviewScopeInput struct {
	Kind      string `json:"kind"`
	AccountID string `json:"account_id"`
	Region    string `json:"region"`
	ClusterID string `json:"cluster_id"`
	TagID     int64  `json:"tag_id"`
}

// ReviewInput is the body of a create, an update and a preview. Version is read on update only. Instructions is the
// text of a custom review; a built-in type takes none.
type ReviewInput struct {
	Name         string             `json:"name"`
	Type         string             `json:"type"`
	Enabled      bool               `json:"enabled"`
	Schedule     ReviewScheduleDTO  `json:"schedule"`
	Scope        []ReviewScopeInput `json:"scope"`
	RouteIDs     []int64            `json:"route_ids"`
	Version      *int               `json:"version"`
	Instructions string             `json:"instructions"`
}

// ReviewPreviewDTO is what a draft would cover. Both money and time members are null when they cannot be known.
// InstructionsProblem is empty or the code a save of the draft's text would answer; the text itself is never sent back.
type ReviewPreviewDTO struct {
	ServicesTotal       int        `json:"services_total"`
	ServicesUnseen      int        `json:"services_unseen"`
	NextRunAt           *time.Time `json:"next_run_at"`
	WorstCaseMonthUSD   *string    `json:"worst_case_month_usd"`
	InstructionsProblem string     `json:"instructions_problem"`
}

// ReviewRunInput is the optional body of a run now. Deliver false asks for a test run: stored and never sent.
// Anything but true, false or a missing member is refused.
type ReviewRunInput struct {
	Deliver json.RawMessage `json:"deliver"`
}

// ReviewRunStartedDTO is the body of a run now: the run is recorded and waits for the job.
type ReviewRunStartedDTO struct {
	RunID int64 `json:"run_id"`
}

// AgentReviewRunDTO is one row of the admin list of review runs. Summary is empty unless the run is done. CostUSD
// covers both attempts and is empty when unknown. Instructions is the run's own copy of the text, empty when it has
// none; this type is for admins only, the read routes have their own without it.
type AgentReviewRunDTO struct {
	ID              int64      `json:"id"`
	ReviewID        *int64     `json:"review_id"`
	Name            string     `json:"name"`
	Type            string     `json:"type"`
	Manual          bool       `json:"manual"`
	State           string     `json:"state"`
	Reason          string     `json:"reason"`
	PeriodLabel     string     `json:"period_label"`
	ScopeLabel      string     `json:"scope_label"`
	ServicesTotal   int        `json:"services_total"`
	ScheduledFor    time.Time  `json:"scheduled_for"`
	RequestedAt     time.Time  `json:"requested_at"`
	StartedAt       *time.Time `json:"started_at"`
	FinishedAt      *time.Time `json:"finished_at"`
	Summary         string     `json:"summary"`
	SummaryReplaced bool       `json:"summary_replaced"`
	Provider        string     `json:"provider"`
	Model           string     `json:"model"`
	Attempts        int        `json:"attempts"`
	CostUSD         string     `json:"cost_usd"`
	Test            bool       `json:"test"`
	Instructions    string     `json:"instructions"`
}

// AgentReviewRunsDTO is the body of GET /api/agent/reviews/runs. NextCursor is empty on the last page.
type AgentReviewRunsDTO struct {
	Items      []AgentReviewRunDTO `json:"items"`
	NextCursor string              `json:"next_cursor"`
}

// AgentReviewTypeDTO is a review type the form offers.
type AgentReviewTypeDTO struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// AgentReviewsDTO is the reviews block of the agent settings; read only, set by the operator's configuration.
// SupportedTypes are the types the agent offers now (empty while it cannot be asked); WorstCaseRunUSD is what one
// run reserves at the reviewer model (null without a model, or while the model is not known). CustomEnabled is
// HM_AGENT_CUSTOM_REVIEWS; Types holds the custom type only while it is on.
type AgentReviewsDTO struct {
	Enabled             bool                 `json:"enabled"`
	PerDay              int                  `json:"per_day"`
	MaxLateMinutes      int                  `json:"max_late_minutes"`
	MaxReviews          int                  `json:"max_reviews"`
	Types               []AgentReviewTypeDTO `json:"types"`
	SupportedTypes      []string             `json:"supported_types"`
	WorstCaseRunUSD     *string              `json:"worst_case_run_usd"`
	CustomEnabled       bool                 `json:"custom_enabled"`
	MaxInstructionChars int                  `json:"max_instruction_chars"`
}
