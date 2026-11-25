package api

import (
	"time"

	"github.com/rcss-labs/harbormaster/internal/store"
)

// ReviewRunDTO is one scheduled review run without its evidence. Summary is empty unless the state is done.
// ReviewID is null once the review itself is deleted.
type ReviewRunDTO struct {
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
}

// ReviewRunDetailDTO adds the evidence filtered for this reader. EvidenceHidden counts the items the reader may not
// see; nothing else about them is sent.
type ReviewRunDetailDTO struct {
	ReviewRunDTO
	Evidence       []InvestigationEvidenceDTO `json:"evidence"`
	EvidenceHidden int                        `json:"evidence_hidden"`
}

// ReviewRunsDTO is one page of the runs a reader may read. NextCursor is empty on the last page.
type ReviewRunsDTO struct {
	Items      []ReviewRunDTO `json:"items"`
	NextCursor string         `json:"next_cursor"`
}

func reviewRunDTO(v store.ReviewRun) ReviewRunDTO {
	out := ReviewRunDTO{
		ID: v.ID, ReviewID: v.ReviewID, Name: v.ReviewName, Type: v.ReviewType, Manual: v.Manual, State: v.State, Reason: v.Reason,
		PeriodLabel: v.PeriodLabel, ScopeLabel: v.ScopeLabel, ServicesTotal: v.ServicesTotal, ScheduledFor: v.ScheduledFor,
		RequestedAt: v.RequestedAt, StartedAt: v.StartedAt, FinishedAt: v.FinishedAt,
	}
	if v.State == store.InvestigationDone {
		out.Summary, out.SummaryReplaced = v.Summary, v.SummaryReplaced
	}
	return out
}
