package offhours

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// The reasons shown when another capacity write would undo or fight an off-hours schedule.
const (
	reasonAsleep  = "It is asleep: wake it first."
	reasonHeld    = "Its task count is held by the schedule."
	reasonActions = "Scheduled actions named " + ActionPrefix + "… belong to the schedule."
)

// Guard refuses capacity writes that would undo or fight an off-hours schedule. Both methods return nil for a
// service that is not enrolled. It reads the database only, so it is cheap to call before any AWS read.
type Guard struct {
	Store *store.Store
	Now   func() time.Time
}

// DesiredCount refuses a desired-count change while the service is asleep or blocked, or while Harbormaster's own
// scalable target (min = max) holds the count.
func (g *Guard) DesiredCount(ctx context.Context, k model.ServiceKey) error {
	enrolment, schedule, enrolled, err := g.load(ctx, k)
	if err != nil || !enrolled {
		return err
	}
	asleep, err := g.asleep(enrolment, schedule)
	if err != nil {
		return err
	}
	switch {
	case asleep:
		return conflict(k, schedule, reasonAsleep)
	case enrolment.State == "blocked" || enrolment.RegisteredTarget:
		return conflict(k, schedule, reasonHeld)
	}
	return nil
}

// Scaling refuses a change set's scaling part that puts or deletes an owned scheduled action, removes the scalable
// target, or sets its range while the service is asleep. Range edits while awake are allowed: the reconciler adopts
// them as the new awake range.
func (g *Guard) Scaling(ctx context.Context, k model.ServiceKey, setsRange, removesTarget bool, actionNames []string) error {
	enrolment, schedule, enrolled, err := g.load(ctx, k)
	if err != nil || !enrolled {
		return err
	}
	for _, name := range actionNames {
		if IsOwned(name) {
			return conflict(k, schedule, reasonActions)
		}
	}
	if removesTarget {
		return conflict(k, schedule, reasonHeld)
	}
	if setsRange {
		asleep, err := g.asleep(enrolment, schedule)
		if err != nil {
			return err
		}
		if asleep {
			return conflict(k, schedule, reasonAsleep)
		}
	}
	return nil
}

// load returns the enrolment and its schedule; enrolled is false when the service is not on a schedule.
func (g *Guard) load(ctx context.Context, k model.ServiceKey) (store.OffHoursEnrolment, store.OffHoursSchedule, bool, error) {
	enrolment, err := g.Store.GetOffHoursEnrolment(ctx, k)
	if errors.Is(err, store.ErrNotFound) {
		return store.OffHoursEnrolment{}, store.OffHoursSchedule{}, false, nil
	}
	if err != nil {
		return store.OffHoursEnrolment{}, store.OffHoursSchedule{}, false, err
	}
	if enrolment.ScheduleID == nil { // the schedule was deleted; the reconciler releases the service
		return enrolment, store.OffHoursSchedule{}, false, nil
	}
	schedule, err := g.Store.GetOffHoursSchedule(ctx, *enrolment.ScheduleID)
	if errors.Is(err, store.ErrNotFound) {
		return enrolment, store.OffHoursSchedule{}, false, nil
	}
	if err != nil {
		return enrolment, store.OffHoursSchedule{}, false, err
	}
	return enrolment, schedule, true, nil
}

func (g *Guard) asleep(enrolment store.OffHoursEnrolment, schedule store.OffHoursSchedule) (bool, error) {
	week, err := ParseWeek(schedule.Awake)
	if err != nil {
		return false, fmt.Errorf("off-hours schedule %q: %w", schedule.Name, err)
	}
	loc, err := time.LoadLocation(schedule.Timezone)
	if err != nil {
		return false, fmt.Errorf("off-hours schedule %q: %w", schedule.Name, err)
	}
	return StateAt(week, loc, g.Now(), enrolment.WokenUntil).Kind == Asleep, nil
}

func conflict(k model.ServiceKey, schedule store.OffHoursSchedule, reason string) error {
	return &ConflictError{Message: fmt.Sprintf("%s is on the off-hours schedule %q. %s", k.Service, schedule.Name, reason)}
}
