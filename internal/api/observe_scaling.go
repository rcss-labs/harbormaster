package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// ScalingDTO is the Application Auto Scaling state of a service. Slices are never null.
type ScalingDTO struct {
	HasTarget        bool                 `json:"has_target"`
	Min              int                  `json:"min"`
	Max              int                  `json:"max"`
	Policies         []ScalingPolicyDTO   `json:"policies"`
	ScheduledActions []ScheduledActionDTO `json:"scheduled_actions"`
	Activities       []ScalingActivityDTO `json:"activities"`
	PolledAt         *time.Time           `json:"polled_at"`
}

type ScalingPolicyDTO struct {
	Name             string  `json:"name"`
	Type             string  `json:"type"`
	Metric           string  `json:"metric"`
	TargetValue      float64 `json:"target_value"`
	ScaleOutCooldown int     `json:"scale_out_cooldown"`
	ScaleInCooldown  int     `json:"scale_in_cooldown"`
}

type ScheduledActionDTO struct {
	Name        string `json:"name"`
	Schedule    string `json:"schedule"`
	Timezone    string `json:"timezone"`
	MinCapacity *int   `json:"min_capacity"`
	MaxCapacity *int   `json:"max_capacity"`
}

type ScalingActivityDTO struct {
	ID            string     `json:"id"`
	Description   string     `json:"description"`
	Cause         string     `json:"cause"`
	StatusCode    string     `json:"status_code"`
	StatusMessage string     `json:"status_message"`
	StartedAt     time.Time  `json:"started_at"`
	EndedAt       *time.Time `json:"ended_at"`
}

// getScaling answers GET /api/services/{serviceId}/scaling. Invisible services are 404; a visible
// service that has never been polled is 200 with has_target false and empty lists.
func (s *Server) getScaling(w http.ResponseWriter, r *http.Request) {
	key, err := serviceKeyFrom(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	sc, err := s.scopeFrom(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	ctx := r.Context()
	if _, err := s.Store.GetServiceView(ctx, key, sc.Vis); err != nil {
		s.fail(w, err)
		return
	}
	scaling, err := s.Store.ScalingForService(ctx, key)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusOK, emptyScalingDTO())
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, scalingDTO(scaling))
}

func emptyScalingDTO() ScalingDTO {
	return ScalingDTO{Policies: []ScalingPolicyDTO{}, ScheduledActions: []ScheduledActionDTO{}, Activities: []ScalingActivityDTO{}}
}

func scalingDTO(scaling model.Scaling) ScalingDTO {
	out := ScalingDTO{
		HasTarget: scaling.HasTarget, Min: scaling.Min, Max: scaling.Max,
		Policies: make([]ScalingPolicyDTO, 0, len(scaling.Policies)), ScheduledActions: make([]ScheduledActionDTO, 0, len(scaling.Scheduled)),
		Activities: make([]ScalingActivityDTO, 0, len(scaling.Activities)),
	}
	if !scaling.PolledAt.IsZero() {
		polled := scaling.PolledAt
		out.PolledAt = &polled
	}
	for _, p := range scaling.Policies {
		out.Policies = append(out.Policies, ScalingPolicyDTO{
			Name: p.Name, Type: p.Type, Metric: p.Metric, TargetValue: p.TargetValue,
			ScaleOutCooldown: p.ScaleOutCooldown, ScaleInCooldown: p.ScaleInCooldown,
		})
	}
	for _, a := range scaling.Scheduled {
		out.ScheduledActions = append(out.ScheduledActions, ScheduledActionDTO{
			Name: a.Name, Schedule: a.Schedule, Timezone: a.Timezone, MinCapacity: a.MinCapacity, MaxCapacity: a.MaxCapacity,
		})
	}
	for _, a := range scaling.Activities {
		out.Activities = append(out.Activities, ScalingActivityDTO{
			ID: a.ID, Description: a.Description, Cause: a.Cause, StatusCode: a.StatusCode,
			StatusMessage: a.StatusMessage, StartedAt: a.StartedAt, EndedAt: a.EndedAt,
		})
	}
	return out
}
