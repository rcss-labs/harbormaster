package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/cost"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/offhours"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const (
	offHoursZeroCapacityReason = "Not managed: already at 0 tasks."
	offHoursScheduleOffReason  = "Not managed: the schedule is turned off."
	hoursPerWeek               = 168
	maxOverlapNames            = 10
)

// wakeActionPrefix is the name prefix of Harbormaster's own wake actions (see offhours.DesiredActions).
const wakeActionPrefix = offhours.ActionPrefix + "wake-"

// offHoursBlockedReason is the D6 sentence for a service whose account refuses auto scaling writes.
func offHoursBlockedReason(accountName string) string {
	return "Can't apply: Harbormaster's role in " + accountName + " can't change auto scaling. The access module is probably deployed with read_only = true."
}

// offHoursState is what a service shows: one of the DTO states, when it ends, and why.
type offHoursState struct {
	State  string
	Until  *time.Time
	Reason string
}

// enrolledState is the state of a service the schedule has enrolled: blocked, or the grid with any manual wake.
func enrolledState(e store.OffHoursEnrolment, week offhours.Week, loc *time.Location, now time.Time, accountName string) offHoursState {
	if e.State == "blocked" {
		return offHoursState{State: "blocked", Reason: offHoursBlockedReason(accountName)}
	}
	st := offhours.StateAt(week, loc, now, e.WokenUntil)
	return offHoursState{State: string(st.Kind), Until: st.Until}
}

// unenrolledState is the state of a matched service that is not enrolled yet. It reads the polled scaling snapshot
// and never calls AWS: the same refusals Applier.Enrol would make are shown up front.
func (s *Server) unenrolledState(ctx context.Context, key model.ServiceKey, desired int) (offHoursState, error) {
	zero := offHoursState{State: "not_managed", Reason: offHoursZeroCapacityReason}
	scaling, err := s.Store.ScalingForService(ctx, key)
	if errors.Is(err, store.ErrNotFound) {
		if desired == 0 {
			return zero, nil
		}
		return offHoursState{State: "pending"}, nil
	}
	if err != nil {
		return offHoursState{}, err
	}
	switch {
	case !scaling.HasTarget:
		if desired == 0 {
			return zero, nil
		}
	case scaling.Max == 0:
		var owned, wake *model.ScheduledAction
		for i := range scaling.Scheduled {
			action := &scaling.Scheduled[i]
			if !offhours.IsOwned(action.Name) {
				continue
			}
			owned = action
			if wake == nil && strings.HasPrefix(action.Name, wakeActionPrefix) && action.MaxCapacity != nil && *action.MaxCapacity > 0 {
				wake = action
			}
		}
		switch {
		case owned == nil:
			return zero, nil
		case wake == nil:
			return offHoursState{State: "pending", Reason: offhours.ReasonNeedsAttention}, nil
		case wake.MinCapacity == nil || *wake.MinCapacity == 0:
			return offHoursState{State: "not_managed", Reason: offhours.ReasonMinZero}, nil
		}
	case scaling.Min == 0:
		return offHoursState{State: "not_managed", Reason: offhours.ReasonMinZero}, nil
	}
	return offHoursState{State: "pending"}, nil
}

// alwaysOnMonthly prices tasks running all month, or nil when the service has no Fargate price (EC2, external,
// unknown size, a region or architecture without a rate).
func alwaysOnMonthly(m store.OffHoursMatch, tasks int) *float64 {
	class := cost.Classify(m.CapacityProvider, m.LaunchType, m.CPU, m.Memory)
	if class != cost.OnDemand && class != cost.Spot {
		return nil
	}
	rate, ok := cost.RateFor(m.Key.Region, m.Architecture)
	if !ok {
		return nil
	}
	cpu, cpuErr := strconv.Atoi(m.CPU)
	memory, memoryErr := strconv.Atoi(m.Memory)
	if cpuErr != nil || memoryErr != nil {
		return nil
	}
	monthly := float64(tasks) * rate.Monthly(cpu, memory)
	return &monthly
}

// savingOf is the part of an always-on cost that sleeping saves.
func savingOf(alwaysOn float64, awakeHours int) float64 {
	return alwaysOn * float64(hoursPerWeek-awakeHours) / hoursPerWeek
}

var (
	prodValuePattern   = regexp.MustCompile(`(?i)^(prod|production|prd|live)$`)
	prodClusterPattern = regexp.MustCompile(`(?i)(^|[-_])prod(uction)?($|[-_])`)
)

// looksLikeProduction lists why a service looks like production (D12): a warning only, it never blocks a save.
func looksLikeProduction(m store.OffHoursMatch) []string {
	reasons := []string{}
	keys := make([]string, 0, len(m.AWSTags))
	for key := range m.AWSTags {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if prodValuePattern.MatchString(m.AWSTags[key]) {
			reasons = append(reasons, "AWS tag "+key+"="+m.AWSTags[key])
		}
	}
	if strings.Contains(strings.ToLower(m.EffectiveTagName), "prod") {
		reasons = append(reasons, `Harbormaster tag "`+m.EffectiveTagName+`"`)
	}
	if prodClusterPattern.MatchString(m.Key.Cluster) {
		reasons = append(reasons, `Cluster name "`+m.Key.Cluster+`"`)
	}
	return reasons
}

// offHoursBuilder holds what the schedule views need once per request: schedules, enrolments, account and tag names
// and the services each schedule matches.
type offHoursBuilder struct {
	s            *Server
	ctx          context.Context
	now          time.Time
	subject      authz.Subject
	accountNames map[string]string
	tags         map[authz.TagID]TagDTO
	users        map[int64]string
	schedules    []store.OffHoursSchedule
	enrolments   map[model.ServiceKey]store.OffHoursEnrolment
	matches      map[int64][]store.OffHoursMatch
	// matchers lists, per service, the ids of the enabled schedules that match it, lowest first.
	matchers map[model.ServiceKey][]int64
}

func (s *Server) newOffHoursBuilder(r *http.Request) (*offHoursBuilder, error) {
	ctx := r.Context()
	b := &offHoursBuilder{s: s, ctx: ctx, now: s.now(), subject: auth.SubjectFrom(ctx),
		matches: map[int64][]store.OffHoursMatch{}, matchers: map[model.ServiceKey][]int64{}}
	var err error
	if b.accountNames, err = s.accountNames(ctx); err != nil {
		return nil, err
	}
	if b.tags, err = s.tagDTOs(ctx); err != nil {
		return nil, err
	}
	users, err := s.Store.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	b.users = make(map[int64]string, len(users))
	for _, u := range users {
		b.users[u.ID] = u.Username
	}
	if b.schedules, err = s.Store.ListOffHoursSchedules(ctx); err != nil {
		return nil, err
	}
	sort.Slice(b.schedules, func(i, j int) bool { return b.schedules[i].ID < b.schedules[j].ID })
	enrolments, err := s.Store.ListOffHoursEnrolments(ctx)
	if err != nil {
		return nil, err
	}
	b.enrolments = make(map[model.ServiceKey]store.OffHoursEnrolment, len(enrolments))
	for _, e := range enrolments {
		b.enrolments[e.Key] = e
	}
	for _, sc := range b.schedules {
		if !sc.Enabled {
			continue
		}
		matched, err := b.matchesOf(sc)
		if err != nil {
			return nil, err
		}
		for _, m := range matched {
			b.matchers[m.Key] = append(b.matchers[m.Key], sc.ID)
		}
	}
	return b, nil
}

func (b *offHoursBuilder) matchesOf(sc store.OffHoursSchedule) ([]store.OffHoursMatch, error) {
	if matched, ok := b.matches[sc.ID]; ok {
		return matched, nil
	}
	matched, err := b.s.Store.OffHoursMatches(b.ctx, sc.TagKey, sc.TagValue)
	if err != nil {
		return nil, err
	}
	b.matches[sc.ID] = matched
	return matched, nil
}

func (b *offHoursBuilder) scheduleByID(id int64) (store.OffHoursSchedule, bool) {
	for _, sc := range b.schedules {
		if sc.ID == id {
			return sc, true
		}
	}
	return store.OffHoursSchedule{}, false
}

func (b *offHoursBuilder) accountName(accountID string) string {
	if name := b.accountNames[accountID]; name != "" {
		return name
	}
	return accountID
}

// kept is the schedule a service stays on when several enabled ones match it (D2): the one it is enrolled in, else
// the lowest id.
func (b *offHoursBuilder) kept(key model.ServiceKey) int64 {
	ids := b.matchers[key]
	if e, ok := b.enrolments[key]; ok && e.ScheduleID != nil {
		for _, id := range ids {
			if id == *e.ScheduleID {
				return id
			}
		}
	}
	if len(ids) == 0 {
		return 0
	}
	return ids[0]
}

// alsoMatches is the D2 sentence for a service that more than one enabled schedule matches; "" when only one does.
func (b *offHoursBuilder) alsoMatches(sc store.OffHoursSchedule, key model.ServiceKey) string {
	var other string
	for _, id := range b.matchers[key] {
		if id != sc.ID {
			if o, ok := b.scheduleByID(id); ok {
				other = o.Name
				break
			}
		}
	}
	if other == "" {
		return ""
	}
	kept, _ := b.scheduleByID(b.kept(key))
	return fmt.Sprintf("Also matches %q. Stays on %q.", other, kept.Name)
}

func (b *offHoursBuilder) baseService(m store.OffHoursMatch, st offHoursState, awakeTasks int) OffHoursServiceDTO {
	tag, ok := b.tags[m.EffectiveTagID]
	if !ok {
		tag = TagDTO{ID: int64(m.EffectiveTagID), Name: m.EffectiveTagName}
	}
	dto := OffHoursServiceDTO{
		ServiceID: m.Key.ID(), Service: m.Key.Service, Cluster: m.Key.Cluster, AccountName: b.accountName(m.Key.AccountID),
		Region: m.Key.Region, Tag: tag, AwakeTasks: awakeTasks, CurrentTasks: m.Desired,
		State: st.State, Until: st.Until, Reason: st.Reason,
		LooksLikeProduction: looksLikeProduction(m), CanOperate: authz.Can(b.subject, authz.Operate, m.EffectiveTagID),
	}
	if st.State != "not_managed" {
		dto.MonthlyAlwaysOn = alwaysOnMonthly(m, awakeTasks)
	}
	return dto
}

// serviceDTOs lists the services sc matches with their states.
func (b *offHoursBuilder) serviceDTOs(sc store.OffHoursSchedule) ([]OffHoursServiceDTO, error) {
	week, err := offhours.ParseWeek(sc.Awake)
	if err != nil {
		return nil, err
	}
	loc, err := time.LoadLocation(sc.Timezone)
	if err != nil {
		return nil, err
	}
	matched, err := b.matchesOf(sc)
	if err != nil {
		return nil, err
	}
	out := make([]OffHoursServiceDTO, 0, len(matched))
	for _, m := range matched {
		e, enrolled := b.enrolments[m.Key]
		enrolledHere := enrolled && e.ScheduleID != nil && *e.ScheduleID == sc.ID
		also := b.alsoMatches(sc, m.Key)
		var st offHoursState
		awakeTasks := m.Desired
		switch {
		case enrolledHere:
			st = enrolledState(e, week, loc, b.now, b.accountName(m.Key.AccountID))
			awakeTasks = e.AwakeTasks
			if st.Reason == "" {
				st.Reason = also
			}
		case !sc.Enabled:
			st = offHoursState{State: "not_managed", Reason: offHoursScheduleOffReason}
		case also != "" && b.kept(m.Key) != sc.ID:
			st = offHoursState{State: "conflict", Reason: also}
		default:
			if st, err = b.s.unenrolledState(b.ctx, m.Key, m.Desired); err != nil {
				return nil, err
			}
			if st.Reason == "" {
				st.Reason = also
			}
		}
		out = append(out, b.baseService(m, st, awakeTasks))
	}
	return out, nil
}

// totalSaving sums the saving of the priced rows that a schedule really manages; nil when none is priced.
func totalSaving(services []OffHoursServiceDTO, awakeHours int) (saving *float64, unpriced int) {
	var sum float64
	var priced bool
	for _, svc := range services {
		if svc.State == "not_managed" || svc.State == "conflict" {
			continue
		}
		if svc.MonthlyAlwaysOn == nil {
			unpriced++
			continue
		}
		sum += savingOf(*svc.MonthlyAlwaysOn, awakeHours)
		priced = true
	}
	if !priced {
		return nil, unpriced
	}
	return &sum, unpriced
}

// scheduleDTO builds a schedule view; withServices adds the rows (detail) instead of an empty array (list).
func (b *offHoursBuilder) scheduleDTO(sc store.OffHoursSchedule, withServices bool) (OffHoursScheduleDTO, error) {
	week, err := offhours.ParseWeek(sc.Awake)
	if err != nil {
		return OffHoursScheduleDTO{}, err
	}
	services, err := b.serviceDTOs(sc)
	if err != nil {
		return OffHoursScheduleDTO{}, err
	}
	dto := OffHoursScheduleDTO{
		ID: sc.ID, Name: sc.Name, Enabled: sc.Enabled, TagKey: sc.TagKey, TagValue: sc.TagValue, Timezone: sc.Timezone,
		Awake: week.Rows(), AwakeHoursPerWeek: week.AwakeHours(), ServiceCount: len(services),
		Version: sc.Version, UpdatedAt: sc.UpdatedAt, Services: []OffHoursServiceDTO{},
	}
	if sc.UpdatedBy != nil {
		dto.UpdatedBy = b.users[*sc.UpdatedBy]
	}
	for _, svc := range services {
		switch svc.State {
		case "asleep":
			dto.AsleepCount++
		case "blocked":
			dto.BlockedCount++
		}
	}
	saving, unpriced := totalSaving(services, dto.AwakeHoursPerWeek)
	dto.UnpricedServices = unpriced
	if sc.Enabled {
		dto.MonthlySaving = saving
	}
	if withServices {
		dto.Services = services
	}
	return dto, nil
}

// overlapNames renders matches as cluster/service, up to maxOverlapNames, then "and N more".
func overlapNames(keys []model.ServiceKey) string {
	names := make([]string, len(keys))
	for i, key := range keys {
		names[i] = key.Cluster + "/" + key.Service
	}
	if len(names) <= maxOverlapNames {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(names[:maxOverlapNames], ", "), len(names)-maxOverlapNames)
}

// overlap is another schedule's share of a set of matched services.
type overlap struct {
	Schedule store.OffHoursSchedule
	Keys     []model.ServiceKey
}

// overlaps lists the other schedules (enabled or not, D2) that match any of the given services.
func (b *offHoursBuilder) overlaps(selfID int64, matched []store.OffHoursMatch) ([]overlap, error) {
	mine := make(map[model.ServiceKey]bool, len(matched))
	for _, m := range matched {
		mine[m.Key] = true
	}
	var out []overlap
	for _, other := range b.schedules {
		if other.ID == selfID {
			continue
		}
		theirs, err := b.matchesOf(other)
		if err != nil {
			return nil, err
		}
		var shared []model.ServiceKey
		for _, m := range theirs {
			if mine[m.Key] {
				shared = append(shared, m.Key)
			}
		}
		if len(shared) > 0 {
			out = append(out, overlap{Schedule: other, Keys: shared})
		}
	}
	return out, nil
}

func offHoursConflict(message string) *APIError {
	return &APIError{Status: http.StatusConflict, Code: "conflict", Message: message}
}

// overlapError is the 409 for the first overlap, or nil.
func overlapError(overlaps []overlap) error {
	if len(overlaps) == 0 {
		return nil
	}
	first := overlaps[0]
	return offHoursConflict(fmt.Sprintf("Schedule %q already matches %d of these services: %s.",
		first.Schedule.Name, len(first.Keys), overlapNames(first.Keys)))
}

// uniqueError is the 409 for a name or selector another schedule already uses, or nil.
func (b *offHoursBuilder) uniqueError(selfID int64, name, tagKey, tagValue string) error {
	for _, other := range b.schedules {
		if other.ID == selfID {
			continue
		}
		if other.TagKey == tagKey && other.TagValue == tagValue {
			return offHoursConflict(fmt.Sprintf("A schedule for %s=%s already exists: %q.", tagKey, tagValue, other.Name))
		}
		if other.Name == name {
			return offHoursConflict(fmt.Sprintf("A schedule called %q already exists.", name))
		}
	}
	return nil
}

// asleepUnderStoredSchedule reports whether an enrolled service is asleep right now under the schedule saved in
// the database, so a preview of an edit counts only the services the save would put to sleep.
func (b *offHoursBuilder) asleepUnderStoredSchedule(e store.OffHoursEnrolment) bool {
	if e.State == "blocked" || e.ScheduleID == nil {
		return false
	}
	for _, sc := range b.schedules {
		if sc.ID != *e.ScheduleID {
			continue
		}
		week, err := offhours.ParseWeek(sc.Awake)
		loc, locErr := time.LoadLocation(sc.Timezone)
		if err != nil || locErr != nil {
			return false
		}
		return offhours.StateAt(week, loc, b.now, e.WokenUntil).Kind == offhours.Asleep
	}
	return false
}
