package offhours

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/applicationautoscaling"
	astypes "github.com/aws/aws-sdk-go-v2/service/applicationautoscaling/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/smithy-go"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// Applier makes the AWS changes of one off-hours service: Application Auto Scaling (range and scheduled actions)
// and ECS (desired count on release). The reconciler and the API call it inside a transaction that holds the
// enrolment lock.
//
// It only ever creates, changes or deletes scheduled actions whose name starts with ActionPrefix.
type Applier struct {
	Store   *store.Store
	Clients awsx.Clients
	Now     func() time.Time
	Log     *slog.Logger
}

// Live is what AWS holds for a service. Actions holds only the actions Harbormaster owns.
type Live struct {
	HasTarget         bool
	Min, Max, Desired int
	Actions           []model.ScheduledAction
}

func (a *Applier) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func scalingResourceID(k model.ServiceKey) string { return "service/" + k.Cluster + "/" + k.Service }

func targetOf(k model.ServiceKey) awsx.Target {
	return awsx.Target{AccountID: k.AccountID, Region: k.Region}
}

// mapAWS turns an access-denied answer into ErrAccessDenied. Every other error, throttles included, is returned
// unchanged so awsx.IsThrottle still sees it.
func mapAWS(err error) error {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "AccessDenied", "AccessDeniedException":
			return fmt.Errorf("%w: %v", ErrAccessDenied, err)
		}
	}
	return err
}

func isNotFound(err error) bool {
	var notFound *astypes.ObjectNotFoundException
	return errors.As(err, &notFound)
}

// ReadLive reads the scalable target, the owned scheduled actions and the desired count of one service.
func (a *Applier) ReadLive(ctx context.Context, k model.ServiceKey) (Live, error) {
	scaling, err := a.Clients.AutoScaling(ctx, targetOf(k))
	if err != nil {
		return Live{}, err
	}
	ecsAPI, err := a.Clients.ECS(ctx, targetOf(k))
	if err != nil {
		return Live{}, err
	}
	resourceID := scalingResourceID(k)
	var live Live

	targets, err := scaling.DescribeScalableTargets(ctx, &applicationautoscaling.DescribeScalableTargetsInput{
		ServiceNamespace: astypes.ServiceNamespaceEcs, ScalableDimension: astypes.ScalableDimensionECSServiceDesiredCount,
		ResourceIds: []string{resourceID}})
	if err != nil {
		return Live{}, mapAWS(fmt.Errorf("describe scalable target of %s: %w", k.Service, err))
	}
	for _, target := range targets.ScalableTargets {
		if aws.ToString(target.ResourceId) == resourceID {
			live.HasTarget = true
			live.Min, live.Max = int(aws.ToInt32(target.MinCapacity)), int(aws.ToInt32(target.MaxCapacity))
		}
	}

	if live.HasTarget {
		in := &applicationautoscaling.DescribeScheduledActionsInput{
			ServiceNamespace: astypes.ServiceNamespaceEcs, ScalableDimension: astypes.ScalableDimensionECSServiceDesiredCount,
			ResourceId: aws.String(resourceID)}
		for {
			out, err := scaling.DescribeScheduledActions(ctx, in)
			if err != nil {
				return Live{}, mapAWS(fmt.Errorf("describe scheduled actions of %s: %w", k.Service, err))
			}
			for _, action := range out.ScheduledActions {
				if IsOwned(aws.ToString(action.ScheduledActionName)) {
					live.Actions = append(live.Actions, liveAction(action))
				}
			}
			if out.NextToken == nil {
				break
			}
			in.NextToken = out.NextToken
		}
	}

	services, err := ecsAPI.DescribeServices(ctx, &ecs.DescribeServicesInput{
		Cluster: aws.String(k.Cluster), Services: []string{k.Service}})
	if err != nil {
		var noCluster *ecstypes.ClusterNotFoundException
		var noService *ecstypes.ServiceNotFoundException
		if errors.As(err, &noCluster) || errors.As(err, &noService) {
			return Live{}, fmt.Errorf("%w: %s/%s", ErrServiceGone, k.Cluster, k.Service)
		}
		return Live{}, mapAWS(fmt.Errorf("describe service %s: %w", k.Service, err))
	}
	if len(services.Services) == 0 || aws.ToString(services.Services[0].Status) == "INACTIVE" {
		return Live{}, fmt.Errorf("%w: %s/%s", ErrServiceGone, k.Cluster, k.Service)
	}
	live.Desired = int(services.Services[0].DesiredCount)
	return live, nil
}

func liveAction(action astypes.ScheduledAction) model.ScheduledAction {
	out := model.ScheduledAction{
		Name: aws.ToString(action.ScheduledActionName), Schedule: aws.ToString(action.Schedule), Timezone: aws.ToString(action.Timezone)}
	if capacity := action.ScalableTargetAction; capacity != nil {
		if capacity.MinCapacity != nil {
			v := int(*capacity.MinCapacity)
			out.MinCapacity = &v
		}
		if capacity.MaxCapacity != nil {
			v := int(*capacity.MaxCapacity)
			out.MaxCapacity = &v
		}
	}
	return out
}

// scheduleClock parses the schedule's grid and zone. The zone must load: the "at" action of a manual wake is
// written in it, and a silent UTC fallback would fire hours off.
func scheduleClock(sc store.OffHoursSchedule) (Week, *time.Location, error) {
	week, err := ParseWeek(sc.Awake)
	if err != nil {
		return Week{}, nil, err
	}
	loc, err := time.LoadLocation(sc.Timezone)
	if err != nil {
		return Week{}, nil, fmt.Errorf("off-hours schedule %q has an invalid time zone %q: %w", sc.Name, sc.Timezone, err)
	}
	return week, loc, nil
}

// isWakeEdge reports whether t is exactly the start of an hour at which the grid goes from asleep to awake.
func isWakeEdge(w Week, loc *time.Location, t time.Time) bool {
	local := t.In(loc)
	if local.Minute() != 0 || local.Second() != 0 || local.Nanosecond() != 0 {
		return false
	}
	idx := gridIndex(mondayFirst(local.Weekday()), local.Hour())
	return w[idx] && !w[(idx+len(w)-1)%len(w)]
}

// wokenAction is the one-shot action that ends a manual wake window. A window that ends at a wake edge gets none:
// a 0/0 "at" action and the wake action would fire at the same moment, AWS does not promise an order, and the
// service could stay asleep through the awake hours.
func wokenAction(w Week, loc *time.Location, tz string, until time.Time) (Action, bool) {
	if isWakeEdge(w, loc, until) {
		return Action{}, false
	}
	return WokenAction(until, tz), true
}

func (a *Applier) registerRange(ctx context.Context, k model.ServiceKey, min, max int) error {
	api, err := a.Clients.AutoScaling(ctx, targetOf(k))
	if err != nil {
		return err
	}
	_, err = api.RegisterScalableTarget(ctx, &applicationautoscaling.RegisterScalableTargetInput{
		ServiceNamespace: astypes.ServiceNamespaceEcs, ScalableDimension: astypes.ScalableDimensionECSServiceDesiredCount,
		ResourceId: aws.String(scalingResourceID(k)), MinCapacity: aws.Int32(int32(min)), MaxCapacity: aws.Int32(int32(max))})
	if err != nil {
		return mapAWS(fmt.Errorf("set the range of %s to %d-%d: %w", k.Service, min, max, err))
	}
	return nil
}

func (a *Applier) putAction(ctx context.Context, k model.ServiceKey, action Action) error {
	if !IsOwned(action.Name) {
		return fmt.Errorf("refusing to write scheduled action %q: not owned by Harbormaster", action.Name)
	}
	api, err := a.Clients.AutoScaling(ctx, targetOf(k))
	if err != nil {
		return err
	}
	_, err = api.PutScheduledAction(ctx, &applicationautoscaling.PutScheduledActionInput{
		ServiceNamespace: astypes.ServiceNamespaceEcs, ScalableDimension: astypes.ScalableDimensionECSServiceDesiredCount,
		ResourceId: aws.String(scalingResourceID(k)), ScheduledActionName: aws.String(action.Name),
		Schedule: aws.String(action.Schedule), Timezone: aws.String(action.Timezone),
		ScalableTargetAction: &astypes.ScalableTargetAction{
			MinCapacity: aws.Int32(int32(action.Capacity.Min)), MaxCapacity: aws.Int32(int32(action.Capacity.Max))}})
	if err != nil {
		return mapAWS(fmt.Errorf("put scheduled action %s of %s: %w", action.Name, k.Service, err))
	}
	return nil
}

// deleteAction removes an owned scheduled action. One that is already gone is fine.
func (a *Applier) deleteAction(ctx context.Context, k model.ServiceKey, name string) error {
	if !IsOwned(name) {
		return fmt.Errorf("refusing to delete scheduled action %q: not owned by Harbormaster", name)
	}
	api, err := a.Clients.AutoScaling(ctx, targetOf(k))
	if err != nil {
		return err
	}
	_, err = api.DeleteScheduledAction(ctx, &applicationautoscaling.DeleteScheduledActionInput{
		ServiceNamespace: astypes.ServiceNamespaceEcs, ScalableDimension: astypes.ScalableDimensionECSServiceDesiredCount,
		ResourceId: aws.String(scalingResourceID(k)), ScheduledActionName: aws.String(name)})
	if err != nil && !isNotFound(err) {
		return mapAWS(fmt.Errorf("delete scheduled action %s of %s: %w", name, k.Service, err))
	}
	return nil
}

func sameAction(live model.ScheduledAction, want Action) bool {
	return live.Schedule == want.Schedule && live.Timezone == want.Timezone &&
		live.MinCapacity != nil && *live.MinCapacity == want.Capacity.Min &&
		live.MaxCapacity != nil && *live.MaxCapacity == want.Capacity.Max
}

// SyncActions makes the owned scheduled actions match the schedule: it puts missing or changed ones and deletes
// owned ones that are no longer wanted. An unexpired manual wake keeps its "at" action. Actions of other owners
// are never listed in live and never touched.
func (a *Applier) SyncActions(ctx context.Context, e store.OffHoursEnrolment, sc store.OffHoursSchedule, live Live) error {
	week, loc, err := scheduleClock(sc)
	if err != nil {
		return err
	}
	wanted := DesiredActions(week, sc.Timezone, Capacity{Min: e.SavedMin, Max: e.SavedMax})
	// Wake actions go first: if a later put fails, a lone sleep action would put the service to sleep with
	// nothing to wake it.
	sort.SliceStable(wanted, func(i, j int) bool {
		return strings.HasPrefix(wanted[i].Name, wakeActionPrefix) && !strings.HasPrefix(wanted[j].Name, wakeActionPrefix)
	})
	if e.WokenUntil != nil && a.now().Before(*e.WokenUntil) {
		// A grid edit can make the hour the window ends awake; the at() sleep would then fire inside awake hours.
		if StateAt(week, loc, *e.WokenUntil, nil).Kind != Awake {
			if action, ok := wokenAction(week, loc, sc.Timezone, *e.WokenUntil); ok {
				wanted = append(wanted, action)
			}
		}
	}

	liveByName := map[string]model.ScheduledAction{}
	for _, action := range live.Actions {
		liveByName[action.Name] = action
	}
	wantedNames := map[string]bool{}
	for _, action := range wanted {
		wantedNames[action.Name] = true
		if current, ok := liveByName[action.Name]; ok && sameAction(current, action) {
			continue
		}
		if err := a.putAction(ctx, e.Key, action); err != nil {
			return err
		}
	}
	for _, action := range live.Actions {
		if IsOwned(action.Name) && !wantedNames[action.Name] {
			if err := a.deleteAction(ctx, e.Key, action.Name); err != nil {
				return err
			}
		}
	}
	return nil
}

func scheduleIDDetail(id *int64) any {
	if id == nil {
		return nil
	}
	return *id
}

func (a *Applier) record(ctx context.Context, tx store.Tx, e store.OffHoursEnrolment, actor *int64, action, text string) error {
	return tx.AppendOperation(ctx, store.Operation{
		AccountID: e.Key.AccountID, Region: e.Key.Region, Cluster: e.Key.Cluster, Service: &e.Key.Service,
		ActorUserID: actor, Source: "harbormaster", Text: text,
		Details: map[string]any{"op": "offhours", "action": action, "schedule_id": scheduleIDDetail(e.ScheduleID)},
	})
}

// stamp records last_applied_at = now in the row before the AWS call, so the reconciler gives Application Auto
// Scaling its grace period to converge (D3).
func (a *Applier) stamp(ctx context.Context, tx store.Tx, e *store.OffHoursEnrolment) error {
	now := a.now()
	e.LastAppliedAt = &now
	return a.Store.UpsertOffHoursEnrolment(ctx, tx, *e)
}

func (a *Applier) username(ctx context.Context, userID int64) (string, error) {
	user, err := a.Store.GetUser(ctx, userID)
	if err != nil {
		return "", fmt.Errorf("look up user %d: %w", userID, err)
	}
	return user.Username, nil
}

// recoverSleepingRange handles a target at 0/0. If Harbormaster's own wake action is there, the service is
// asleep from an enrolment whose row was lost, and the wake action carries the range to restore. The target was
// not necessarily registered by Harbormaster, so the caller records registered_target=false. Without a wake
// action the saved range is unknowable.
func (a *Applier) recoverSleepingRange(k model.ServiceKey, live Live) (min, max int, err error) {
	for _, action := range live.Actions {
		if strings.HasPrefix(action.Name, wakeActionPrefix) && action.MaxCapacity != nil && *action.MaxCapacity > 0 {
			min := 0
			if action.MinCapacity != nil {
				min = *action.MinCapacity
			}
			a.logger().Warn("off-hours: recovering the saved range of a sleeping service from its wake action; whether Harbormaster registered the target is unknown",
				"service", k.Service, "cluster", k.Cluster, "min", min, "max", *action.MaxCapacity)
			return min, *action.MaxCapacity, nil
		}
	}
	if len(live.Actions) > 0 {
		return 0, 0, ErrNeedsAttention
	}
	return 0, 0, ErrZeroCapacity
}

func (a *Applier) logger() *slog.Logger {
	if a.Log != nil {
		return a.Log
	}
	return slog.Default()
}

// Enrol saves the service's capacity, registers a scalable target when it has none, writes the scheduled actions
// and stores the enrolment. A service with nothing to restore returns ErrZeroCapacity before any write.
func (a *Applier) Enrol(ctx context.Context, tx store.Tx, sc store.OffHoursSchedule, m store.OffHoursMatch, live Live) (store.OffHoursEnrolment, error) {
	if _, _, err := scheduleClock(sc); err != nil {
		return store.OffHoursEnrolment{}, err
	}
	scheduleID := sc.ID
	e := store.OffHoursEnrolment{Key: m.Key, ScheduleID: &scheduleID, State: "active"}
	switch {
	case live.HasTarget && live.Max == 0:
		min, max, err := a.recoverSleepingRange(e.Key, live)
		if err != nil {
			return store.OffHoursEnrolment{}, err
		}
		e.SavedMin, e.SavedMax = min, max
	case live.HasTarget:
		e.SavedMin, e.SavedMax = live.Min, live.Max
	default:
		if live.Desired == 0 {
			return store.OffHoursEnrolment{}, ErrZeroCapacity
		}
		desired := live.Desired
		e.SavedMin, e.SavedMax, e.SavedDesired, e.RegisteredTarget = desired, desired, &desired, true
	}
	if e.SavedMin == 0 {
		return store.OffHoursEnrolment{}, ErrMinZero
	}
	// Remember what runs now: a service enrolled in sleep hours is put to sleep at once, and the page shows "N -> 0".
	e.AwakeTasks = live.Desired
	if e.AwakeTasks == 0 {
		// Recovered from a sleeping target: nothing runs now, so the saved minimum is the best figure for "N -> 0".
		e.AwakeTasks = e.SavedMin
	}

	if err := a.stamp(ctx, tx, &e); err != nil {
		return store.OffHoursEnrolment{}, err
	}
	if e.RegisteredTarget {
		if err := a.registerRange(ctx, e.Key, e.SavedMin, e.SavedMax); err != nil {
			return store.OffHoursEnrolment{}, err
		}
	}
	if err := a.SyncActions(ctx, e, sc, live); err != nil {
		return store.OffHoursEnrolment{}, err
	}
	text := fmt.Sprintf("Joined off-hours schedule %q (saved min %d, max %d).", sc.Name, e.SavedMin, e.SavedMax)
	return e, a.record(ctx, tx, e, nil, "enrol", text)
}

func (a *Applier) setRange(ctx context.Context, tx store.Tx, e *store.OffHoursEnrolment, min, max int, actor *int64, action, text string) error {
	if err := a.stamp(ctx, tx, e); err != nil {
		return err
	}
	if err := a.registerRange(ctx, e.Key, min, max); err != nil {
		return err
	}
	return a.record(ctx, tx, *e, actor, action, text)
}

// Sleep sets the range to 0/0. scheduleName is the schedule named in the operation text; actor is nil for the
// reconciler.
func (a *Applier) Sleep(ctx context.Context, tx store.Tx, e *store.OffHoursEnrolment, actor *int64, scheduleName string) error {
	return a.setRange(ctx, tx, e, 0, 0, actor, "sleep", fmt.Sprintf("Put to sleep by off-hours schedule %q.", scheduleName))
}

// Wake restores the saved range.
func (a *Applier) Wake(ctx context.Context, tx store.Tx, e *store.OffHoursEnrolment, actor *int64, scheduleName string) error {
	return a.setRange(ctx, tx, e, e.SavedMin, e.SavedMax, actor, "wake", fmt.Sprintf("Woken by off-hours schedule %q.", scheduleName))
}

// WakeFor wakes a sleeping service for WakeDuration, or until the next wake edge if that comes sooner, and
// returns when the window ends. The "at" action that puts it back to sleep is written first, so a failure
// leaves the service asleep rather than awake with no way back.
func (a *Applier) WakeFor(ctx context.Context, tx store.Tx, e *store.OffHoursEnrolment, sc store.OffHoursSchedule, actor int64) (time.Time, error) {
	week, loc, err := scheduleClock(sc)
	if err != nil {
		return time.Time{}, err
	}
	now := a.now()
	if StateAt(week, loc, now, e.WokenUntil).Kind != Asleep {
		return time.Time{}, &ConflictError{Message: e.Key.Service + " is not asleep."}
	}
	username, err := a.username(ctx, actor)
	if err != nil {
		return time.Time{}, err
	}
	until := WakeUntil(week, loc, now)
	e.WokenUntil, e.WokenBy = &until, &actor
	if err := a.stamp(ctx, tx, e); err != nil {
		return time.Time{}, err
	}

	if action, ok := wokenAction(week, loc, sc.Timezone, until); ok {
		if err := a.putAction(ctx, e.Key, action); err != nil {
			return time.Time{}, err
		}
	}
	text := fmt.Sprintf("Woken for 2 hours until %s by %s.", until.In(loc).Format("15:04 MST"), username)
	if err := a.setRange(ctx, tx, e, e.SavedMin, e.SavedMax, &actor, "wake_for", text); err != nil {
		return time.Time{}, err
	}
	return until, nil
}

// PutBackToSleep ends a manual wake early.
func (a *Applier) PutBackToSleep(ctx context.Context, tx store.Tx, e *store.OffHoursEnrolment, sc store.OffHoursSchedule, actor int64) error {
	week, loc, err := scheduleClock(sc)
	if err != nil {
		return err
	}
	if StateAt(week, loc, a.now(), e.WokenUntil).Kind != Woken {
		return &ConflictError{Message: e.Key.Service + " is not woken."}
	}
	username, err := a.username(ctx, actor)
	if err != nil {
		return err
	}
	if err := a.deleteAction(ctx, e.Key, WokenActionName); err != nil {
		return err
	}
	e.WokenUntil, e.WokenBy = nil, nil
	return a.setRange(ctx, tx, e, 0, 0, &actor, "sleep_again", fmt.Sprintf("Put back to sleep by %s.", username))
}

// Release takes the service out of off-hours and restores what Enrol saved. A target Harbormaster registered is
// deregistered (which drops its actions) and the desired count is restored through ECS; otherwise the owned actions
// are deleted and the saved range is registered again. scheduleName is the schedule named in the operation text.
// The row is deleted in tx, so it is not stamped first.
func (a *Applier) Release(ctx context.Context, tx store.Tx, e store.OffHoursEnrolment, live Live, scheduleName string) error {
	var text string
	if e.RegisteredTarget {
		api, err := a.Clients.AutoScaling(ctx, targetOf(e.Key))
		if err != nil {
			return err
		}
		_, err = api.DeregisterScalableTarget(ctx, &applicationautoscaling.DeregisterScalableTargetInput{
			ServiceNamespace: astypes.ServiceNamespaceEcs, ScalableDimension: astypes.ScalableDimensionECSServiceDesiredCount,
			ResourceId: aws.String(scalingResourceID(e.Key))})
		if err != nil && !isNotFound(err) {
			return mapAWS(fmt.Errorf("deregister the scalable target of %s: %w", e.Key.Service, err))
		}
		// An own target has min = max = the desired count, so the saved min is the fallback.
		desired := e.SavedMin
		if e.SavedDesired != nil {
			desired = *e.SavedDesired
		}
		if desired > 0 {
			ecsAPI, err := a.Clients.ECS(ctx, targetOf(e.Key))
			if err != nil {
				return err
			}
			if _, err := ecsAPI.UpdateService(ctx, &ecs.UpdateServiceInput{
				Cluster: aws.String(e.Key.Cluster), Service: aws.String(e.Key.Service),
				DesiredCount: aws.Int32(int32(desired))}); err != nil {
				return mapAWS(fmt.Errorf("restore the desired count of %s: %w", e.Key.Service, err))
			}
			text = fmt.Sprintf("Left off-hours schedule %q: restored desired count %d.", scheduleName, desired)
		}
	} else {
		// The caller's live may be stale; list the owned actions again so none is left behind.
		current, err := a.ReadLive(ctx, e.Key)
		if err != nil {
			return err
		}
		for _, action := range current.Actions {
			if err := a.deleteAction(ctx, e.Key, action.Name); err != nil {
				return err
			}
		}
		if err := a.registerRange(ctx, e.Key, e.SavedMin, e.SavedMax); err != nil {
			return err
		}
		text = fmt.Sprintf("Left off-hours schedule %q: restored min %d, max %d.", scheduleName, e.SavedMin, e.SavedMax)
	}
	if text == "" {
		text = fmt.Sprintf("Left off-hours schedule %q.", scheduleName)
	}
	if err := a.Store.DeleteOffHoursEnrolment(ctx, tx, e.Key); err != nil {
		return err
	}
	return a.record(ctx, tx, e, nil, "release", text)
}
