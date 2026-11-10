package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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
	defaultOffHoursTimeout = 20 * time.Second
	offHoursRecordTimeout  = 5 * time.Second
)

const staleScheduleMessage = "This schedule changed since you opened it. Reload to see the latest."

// kickOffHours wakes the reconciler in this process; a nil KickOffHours is a no-op.
func (s *Server) kickOffHours() {
	if s.KickOffHours != nil {
		s.KickOffHours()
	}
}

func scheduleIDFrom(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		return 0, errNotFound
	}
	return id, nil
}

func (s *Server) listOffHoursSchedules(w http.ResponseWriter, r *http.Request) {
	b, err := s.newOffHoursBuilder(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	list := OffHoursListDTO{Items: []OffHoursScheduleDTO{}, PricesAsOf: cost.PricesAsOf, Now: b.now}
	var total float64
	var priced bool
	sorted := append([]store.OffHoursSchedule(nil), b.schedules...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	for _, sc := range sorted {
		dto, err := b.scheduleDTO(sc, false)
		if err != nil {
			s.fail(w, err)
			return
		}
		if sc.Enabled && dto.MonthlySaving != nil {
			total += *dto.MonthlySaving
			priced = true
		}
		list.Items = append(list.Items, dto)
	}
	if priced {
		list.MonthlySavingTotal = &total
	}
	writeJSON(w, http.StatusOK, list)
}

// offHoursScheduleDetail is the detail view of one schedule, read fresh from the database.
func (s *Server) offHoursScheduleDetail(r *http.Request, id int64) (OffHoursScheduleDTO, error) {
	b, err := s.newOffHoursBuilder(r)
	if err != nil {
		return OffHoursScheduleDTO{}, err
	}
	sc, ok := b.scheduleByID(id)
	if !ok {
		return OffHoursScheduleDTO{}, store.ErrNotFound
	}
	return b.scheduleDTO(sc, true)
}

func (s *Server) getOffHoursSchedule(w http.ResponseWriter, r *http.Request) {
	id, err := scheduleIDFrom(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	dto, err := s.offHoursScheduleDetail(r, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

const (
	maxOffHoursTags     = 50
	maxOffHoursExcluded = 1000
)

// selectorsFromInput validates the tags of a request. Harbormaster tags must exist. With allowEmpty (preview) an
// empty list is not an error.
func (s *Server) selectorsFromInput(ctx context.Context, in []OffHoursSelectorInput, allowEmpty bool) ([]store.OffHoursSelector, error) {
	if len(in) == 0 && !allowEmpty {
		return nil, invalid("Pick at least one tag.")
	}
	if len(in) > maxOffHoursTags {
		return nil, invalid(fmt.Sprintf("A schedule can select by at most %d tags.", maxOffHoursTags))
	}
	var existing map[authz.TagID]TagDTO
	seen := map[store.OffHoursSelector]bool{}
	out := make([]store.OffHoursSelector, 0, len(in))
	for _, tag := range in {
		var sel store.OffHoursSelector
		switch tag.Kind {
		case store.OffHoursSelectorAWS:
			if err := offhours.ValidateAWSTag(tag.Key, tag.Value); err != nil {
				return nil, invalid(err.Error())
			}
			sel = store.OffHoursSelector{Kind: tag.Kind, Key: tag.Key, Value: tag.Value}
		case store.OffHoursSelectorHarbormaster:
			if existing == nil {
				var err error
				if existing, err = s.tagDTOs(ctx); err != nil {
					return nil, err
				}
			}
			if _, ok := existing[authz.TagID(tag.TagID)]; !ok {
				return nil, invalid("That Harbormaster tag no longer exists.")
			}
			sel = store.OffHoursSelector{Kind: tag.Kind, TagID: authz.TagID(tag.TagID)}
		default:
			return nil, invalid(`The tag kind must be "aws" or "harbormaster".`)
		}
		if seen[sel] {
			return nil, invalid("The same tag is listed twice.")
		}
		seen[sel] = true
		out = append(out, sel)
	}
	return out, nil
}

// exclusionsFromInput parses the excluded service ids, dropping repeats.
func exclusionsFromInput(ids []string) ([]model.ServiceKey, error) {
	if len(ids) > maxOffHoursExcluded {
		return nil, invalid(fmt.Sprintf("A schedule can exclude at most %d services.", maxOffHoursExcluded))
	}
	seen := make(map[model.ServiceKey]bool, len(ids))
	out := make([]model.ServiceKey, 0, len(ids))
	for _, id := range ids {
		key, err := model.ParseServiceID(id)
		if err != nil {
			return nil, invalid("One of the excluded services is not a valid service.")
		}
		if !seen[key] {
			seen[key] = true
			out = append(out, key)
		}
	}
	return out, nil
}

// scheduleFromInput validates a body (D1: tag names are used exactly, only the schedule name is trimmed).
func (s *Server) scheduleFromInput(ctx context.Context, in OffHoursScheduleInput) (store.OffHoursSchedule, error) {
	name := strings.TrimSpace(in.Name)
	week := offhours.WeekFromRows(in.Awake)
	if _, err := offhours.Validate(name, in.Timezone, week); err != nil {
		return store.OffHoursSchedule{}, invalid(err.Error())
	}
	selectors, err := s.selectorsFromInput(ctx, in.Tags, false)
	if err != nil {
		return store.OffHoursSchedule{}, err
	}
	exclusions, err := exclusionsFromInput(in.ExcludedServiceIDs)
	if err != nil {
		return store.OffHoursSchedule{}, err
	}
	return store.OffHoursSchedule{Name: name, Enabled: in.Enabled, Selectors: selectors, Exclusions: exclusions,
		Timezone: in.Timezone, Awake: week.String()}, nil
}

// scheduleAuditView is the audit snapshot of a schedule; tagNames resolves its Harbormaster tags.
func scheduleAuditView(sc store.OffHoursSchedule, tagNames map[authz.TagID]string) map[string]any {
	tags := make([]string, len(sc.Selectors))
	for i, sel := range sc.Selectors {
		if sel.Kind == store.OffHoursSelectorHarbormaster {
			tags[i] = "Harbormaster tag " + tagNames[sel.TagID]
		} else {
			tags[i] = sel.Key + "=" + sel.Value
		}
	}
	excluded := make([]string, len(sc.Exclusions))
	for i, key := range sc.Exclusions {
		excluded[i] = key.Cluster + "/" + key.Service
	}
	return map[string]any{"name": sc.Name, "enabled": sc.Enabled, "tags": tags, "excluded": excluded,
		"timezone": sc.Timezone, "awake": sc.Awake, "version": sc.Version}
}

// auditView renders sc for the audit log with the current Harbormaster tag names.
func (s *Server) auditView(ctx context.Context, sc store.OffHoursSchedule) map[string]any {
	names := map[authz.TagID]string{}
	if tags, err := s.tagDTOs(ctx); err == nil {
		for id, tag := range tags {
			names[id] = tag.Name
		}
	}
	return scheduleAuditView(sc, names)
}

// checkScheduleFree runs the name and overlap rules for a schedule that would be saved as sc. The overlap is computed
// on the services left after the exclusions.
func (s *Server) checkScheduleFree(b *offHoursBuilder, selfID int64, sc store.OffHoursSchedule) error {
	if err := b.uniqueError(selfID, sc.Name); err != nil {
		return err
	}
	matched, err := s.Store.OffHoursMatches(b.ctx, sc.Selectors)
	if err != nil {
		return err
	}
	matched, _ = store.SplitExcluded(matched, sc.Exclusions)
	overlaps, err := b.overlaps(selfID, matched)
	if err != nil {
		return err
	}
	return overlapError(overlaps)
}

func (s *Server) createOffHoursSchedule(w http.ResponseWriter, r *http.Request) {
	var in OffHoursScheduleInput
	if err := decodeStrictJSON(w, r, &in); err != nil {
		s.fail(w, err)
		return
	}
	sc, err := s.scheduleFromInput(r.Context(), in)
	if err != nil {
		s.fail(w, err)
		return
	}
	b, err := s.newOffHoursBuilder(r)
	if err == nil {
		err = s.checkScheduleFree(b, 0, sc)
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	actor, _ := auth.UserFrom(r.Context())
	created, err := s.Store.CreateOffHoursSchedule(r.Context(), sc, actor.ID)
	if errors.Is(err, store.ErrConflict) {
		s.fail(w, offHoursConflict("A schedule with that name already exists."))
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	s.auditAdmin(r, "offhours_schedule_create", "offhours:"+strconv.FormatInt(created.ID, 10),
		"Created off-hours schedule "+strconv.Quote(created.Name)+".", map[string]any{"before": nil, "after": s.auditView(r.Context(), created)})
	s.kickOffHours()
	dto, err := s.offHoursScheduleDetail(r, created.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, dto)
}

func (s *Server) updateOffHoursSchedule(w http.ResponseWriter, r *http.Request) {
	id, err := scheduleIDFrom(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	var in OffHoursScheduleInput
	if err := decodeStrictJSON(w, r, &in); err != nil {
		s.fail(w, err)
		return
	}
	ctx := r.Context()
	before, err := s.Store.GetOffHoursSchedule(ctx, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if in.Version == nil {
		s.fail(w, invalid("version is required."))
		return
	}
	if *in.Version != before.Version {
		s.fail(w, offHoursConflict(staleScheduleMessage))
		return
	}
	sc, err := s.scheduleFromInput(r.Context(), in)
	if err != nil {
		s.fail(w, err)
		return
	}
	sc.ID, sc.Version = id, before.Version
	b, err := s.newOffHoursBuilder(r)
	if err == nil {
		err = s.checkScheduleFree(b, id, sc)
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	actor, _ := auth.UserFrom(ctx)
	updated, err := s.Store.UpdateOffHoursSchedule(ctx, sc, actor.ID)
	if errors.Is(err, store.ErrConflict) {
		// Either someone saved first, or a duplicate slipped in between the check and the write.
		if current, getErr := s.Store.GetOffHoursSchedule(ctx, id); getErr == nil && current.Version != before.Version {
			s.fail(w, offHoursConflict(staleScheduleMessage))
		} else {
			s.fail(w, offHoursConflict("A schedule with that name already exists."))
		}
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	s.auditAdmin(r, "offhours_schedule_update", "offhours:"+strconv.FormatInt(id, 10),
		"Updated off-hours schedule "+strconv.Quote(updated.Name)+".",
		map[string]any{"before": s.auditView(ctx, before), "after": s.auditView(ctx, updated)})
	s.kickOffHours()
	dto, err := s.offHoursScheduleDetail(r, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

func (s *Server) deleteOffHoursSchedule(w http.ResponseWriter, r *http.Request) {
	id, err := scheduleIDFrom(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	before, err := s.Store.GetOffHoursSchedule(r.Context(), id)
	if err == nil {
		err = s.Store.DeleteOffHoursSchedule(r.Context(), id)
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	s.auditAdmin(r, "offhours_schedule_delete", "offhours:"+strconv.FormatInt(id, 10),
		"Deleted off-hours schedule "+strconv.Quote(before.Name)+".", map[string]any{"before": s.auditView(r.Context(), before), "after": nil})
	s.kickOffHours()
	w.WriteHeader(http.StatusNoContent)
}

// previewOffHours shows what a schedule would match and do, without writing. The editor sends it while the grid is
// still being painted, so an empty grid or name is not an error here; the save validates them.
func (s *Server) previewOffHours(w http.ResponseWriter, r *http.Request) {
	var req OffHoursPreviewRequest
	if err := decodeStrictJSON(w, r, &req); err != nil {
		s.fail(w, err)
		return
	}
	loc, err := time.LoadLocation(req.Timezone)
	if err != nil || req.Timezone == "" || req.Timezone == "Local" {
		s.fail(w, invalid("Pick an IANA timezone such as Europe/Berlin."))
		return
	}
	b, err := s.newOffHoursBuilder(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	week := offhours.WeekFromRows(req.Awake)
	preview := OffHoursPreviewDTO{Services: []OffHoursServiceDTO{}, Excluded: []OffHoursServiceDTO{}, Overlaps: []OverlapDTO{},
		AwakeHoursPerWeek: week.AwakeHours()}
	selectors, err := s.selectorsFromInput(r.Context(), req.Tags, true)
	if err != nil {
		s.fail(w, err)
		return
	}
	exclusions, err := exclusionsFromInput(req.ExcludedServiceIDs)
	if err != nil {
		s.fail(w, err)
		return
	}
	if len(selectors) == 0 {
		writeJSON(w, http.StatusOK, preview)
		return
	}
	tagMatches, err := s.Store.OffHoursMatches(r.Context(), selectors)
	if err != nil {
		s.fail(w, err)
		return
	}
	matched, removed := store.SplitExcluded(tagMatches, exclusions)
	for _, m := range removed {
		preview.Excluded = append(preview.Excluded, b.baseService(m, offHoursState{State: "not_managed", Reason: offHoursExcludedReason}, m.Desired))
	}
	for _, m := range matched {
		var st offHoursState
		awakeTasks := m.Desired
		alreadyAsleep := false
		if e, ok := b.enrolments[m.Key]; ok && e.ScheduleID != nil {
			st = enrolledState(e, week, loc, b.now, b.accountName(m.Key.AccountID))
			awakeTasks = e.AwakeTasks
			alreadyAsleep = b.asleepUnderStoredSchedule(e)
		} else {
			if st, err = s.unenrolledState(r.Context(), m.Key, m.Desired); err != nil {
				s.fail(w, err)
				return
			}
			if st.State == "pending" && st.Reason == "" {
				grid := offhours.StateAt(week, loc, b.now, nil)
				st = offHoursState{State: string(grid.Kind), Until: grid.Until}
			}
		}
		svc := b.baseService(m, st, awakeTasks)
		if svc.State == "asleep" && !alreadyAsleep {
			preview.SleepNow++
		}
		preview.Services = append(preview.Services, svc)
	}
	saving, _ := totalSaving(preview.Services, preview.AwakeHoursPerWeek)
	preview.MonthlySaving = saving
	var selfID int64
	if req.ScheduleID != nil {
		selfID = *req.ScheduleID
	}
	overlaps, err := b.overlaps(selfID, matched)
	if err != nil {
		s.fail(w, err)
		return
	}
	for _, o := range overlaps {
		names := make([]string, len(o.Keys))
		for i, key := range o.Keys {
			names[i] = key.Cluster + "/" + key.Service
		}
		preview.Overlaps = append(preview.Overlaps, OverlapDTO{ScheduleID: o.Schedule.ID, ScheduleName: o.Schedule.Name, Services: names})
	}
	writeJSON(w, http.StatusOK, preview)
}

func (s *Server) listOffHoursTags(w http.ResponseWriter, r *http.Request) {
	values, err := s.Store.OffHoursTagValues(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	items := []OffHoursTagOptionDTO{}
	for _, v := range values {
		if strings.HasPrefix(v.Key, "aws:") { // AWS reserves the prefix; a schedule cannot select on it
			continue
		}
		items = append(items, OffHoursTagOptionDTO{Key: v.Key, Value: v.Value, Services: v.Services})
	}
	counts, err := s.Store.OffHoursHarbormasterTags(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	harbormaster := make([]OffHoursHarbormasterTagDTO, 0, len(counts))
	for _, tag := range counts {
		harbormaster = append(harbormaster, OffHoursHarbormasterTagDTO{ID: int64(tag.ID), Name: tag.Name, Color: tag.Color, Services: tag.Services})
	}
	writeJSON(w, http.StatusOK, OffHoursTagsDTO{Items: items, Harbormaster: harbormaster})
}

func (s *Server) getServiceOffHours(w http.ResponseWriter, r *http.Request) {
	key, sc, row, ok := s.visibleService(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	out := ServiceOffHoursDTO{CanOperate: authz.Can(sc.Subject, authz.Operate, row.EffectiveTagID)}
	schedules, err := s.Store.ListOffHoursSchedules(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	sort.Slice(schedules, func(i, j int) bool { return schedules[i].ID < schedules[j].ID })
	enrolment, enrolErr := s.Store.GetOffHoursEnrolment(ctx, key)
	if enrolErr != nil && !errors.Is(enrolErr, store.ErrNotFound) {
		s.fail(w, enrolErr)
		return
	}
	// The store decides what a schedule matches (active services only), the same as the schedule views.
	var matching []store.OffHoursSchedule
	for _, candidate := range schedules {
		if !candidate.Enabled {
			continue
		}
		tagMatches, err := s.Store.OffHoursMatches(ctx, candidate.Selectors)
		if err != nil {
			s.fail(w, err)
			return
		}
		matched, _ := store.SplitExcluded(tagMatches, candidate.Exclusions)
		for _, m := range matched {
			if m.Key == key {
				matching = append(matching, candidate)
				break
			}
		}
	}
	// The service stays on the schedule it is enrolled in, else the lowest id (D2).
	var chosen *store.OffHoursSchedule
	if enrolErr == nil && enrolment.ScheduleID != nil {
		for i := range schedules {
			if schedules[i].ID == *enrolment.ScheduleID {
				chosen = &schedules[i]
			}
		}
	}
	if chosen == nil && len(matching) > 0 {
		chosen = &matching[0]
	}
	if chosen == nil {
		writeJSON(w, http.StatusOK, out)
		return
	}
	var also string
	for _, candidate := range matching {
		if candidate.ID != chosen.ID {
			also = "Also matches " + strconv.Quote(candidate.Name) + ". Stays on " + strconv.Quote(chosen.Name) + "."
			break
		}
	}
	out.ScheduleName, out.Timezone = chosen.Name, chosen.Timezone
	enrolled := enrolErr == nil && enrolment.ScheduleID != nil && *enrolment.ScheduleID == chosen.ID
	var st offHoursState
	if enrolled {
		week, parseErr := offhours.ParseWeek(chosen.Awake)
		loc, locErr := time.LoadLocation(chosen.Timezone)
		if parseErr != nil || locErr != nil {
			s.fail(w, errors.Join(parseErr, locErr))
			return
		}
		names, namesErr := s.accountNames(ctx)
		if namesErr != nil {
			s.fail(w, namesErr)
			return
		}
		accountName := names[key.AccountID]
		if accountName == "" {
			accountName = key.AccountID
		}
		st = enrolledState(enrolment, week, loc, s.now(), accountName)
	} else {
		var stateErr error
		if st, stateErr = s.unenrolledState(ctx, key, row.Desired); stateErr != nil {
			s.fail(w, stateErr)
			return
		}
	}
	if st.Reason == "" {
		st.Reason = also
	}
	out.Enrolled, out.State, out.Until, out.Reason = enrolled, st.State, st.Until, st.Reason
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) wakeServiceOffHours(w http.ResponseWriter, r *http.Request) {
	s.changeServiceOffHours(w, r, true)
}

func (s *Server) sleepServiceOffHours(w http.ResponseWriter, r *http.Request) {
	s.changeServiceOffHours(w, r, false)
}

// changeServiceOffHours wakes or puts back to sleep an enrolled service (D7). It runs in the handler, holding the
// enrolment lock in a transaction. The Applier stamps the row before it writes to AWS, so the transaction must
// commit even when a later write fails or the client leaves: it runs on a context detached from the request, and
// only the work inside it has a deadline (OffHoursTimeout, 20 s by default). The reconciler then converges AWS.
func (s *Server) changeServiceOffHours(w http.ResponseWriter, r *http.Request, wake bool) {
	key, _, _, ok := s.requireAction(w, r, authz.Operate)
	if !ok {
		return
	}
	if s.OffHours == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Off-hours is not available on this server.")
		return
	}
	actor, _ := auth.UserFrom(r.Context())
	base := context.WithoutCancel(r.Context())
	timeout := s.OffHoursTimeout
	if timeout <= 0 {
		timeout = defaultOffHoursTimeout
	}

	var (
		opErr    error
		blocked  bool
		until    time.Time
		schedule store.OffHoursSchedule
	)
	txErr := s.Store.InTx(base, func(tx store.Tx) error {
		ctx, cancel := context.WithTimeout(base, timeout)
		defer cancel()
		e, err := s.Store.LockOffHoursEnrolment(ctx, tx, key)
		if err != nil {
			return err
		}
		if e.ScheduleID == nil { // leaving: the reconciler is about to restore it
			return store.ErrNotFound
		}
		if e.State == "blocked" {
			blocked = true
			return nil
		}
		if schedule, err = s.Store.GetOffHoursSchedule(ctx, *e.ScheduleID); err != nil {
			return err
		}
		if wake {
			until, opErr = s.OffHours.WakeFor(ctx, tx, &e, schedule, actor.ID)
		} else {
			opErr = s.OffHours.PutBackToSleep(ctx, tx, &e, schedule, actor.ID)
		}
		if errors.Is(opErr, offhours.ErrAccessDenied) {
			// Like the reconciler: blocked, checked now, no error text, so the hourly retry backoff applies.
			now := s.now()
			e.State, e.BlockedReason, e.LastError, e.LastCheckedAt = "blocked", "access_denied", "", &now
			if wake {
				e.WokenUntil, e.WokenBy = nil, nil // the wake never reached AWS
			}
			recordCtx, cancelRecord := context.WithTimeout(base, offHoursRecordTimeout)
			defer cancelRecord()
			return s.Store.UpsertOffHoursEnrolment(recordCtx, tx, e)
		}
		return nil // commit what was written even when opErr is set
	})

	switch {
	case errors.Is(txErr, store.ErrNotFound):
		s.fail(w, &APIError{Status: http.StatusNotFound, Code: "not_found", Message: key.Service + " is not on an off-hours schedule."})
		return
	case errors.Is(txErr, context.DeadlineExceeded):
		// The reconciler may hold the row lock for a while; nothing was written.
		s.fail(w, offHoursConflict("This service is being updated. Try again in a moment."))
		return
	case txErr != nil:
		s.fail(w, txErr)
		return
	}
	var conflict *offhours.ConflictError
	switch {
	case blocked, errors.Is(opErr, offhours.ErrAccessDenied):
		names, err := s.accountNames(r.Context())
		if err != nil {
			s.fail(w, err)
			return
		}
		name := names[key.AccountID]
		if name == "" {
			name = key.AccountID
		}
		s.fail(w, offHoursConflict(offHoursBlockedReason(name)))
		return
	case errors.As(opErr, &conflict):
		s.fail(w, offHoursConflict(conflict.Message))
		return
	case opErr != nil:
		s.fail(w, opErr)
		return
	}

	target := "service:" + key.Cluster + "/" + key.Service
	if wake {
		s.auditAdmin(r, "offhours_wake", target, "Woke "+key.Service+" for 2 hours.",
			map[string]any{"schedule": schedule.Name, "woken_until": until})
		writeJSON(w, http.StatusOK, WakeResponse{WokenUntil: until})
		return
	}
	s.auditAdmin(r, "offhours_sleep", target, "Put "+key.Service+" back to sleep.", map[string]any{"schedule": schedule.Name})
	writeJSON(w, http.StatusOK, struct{}{})
}
