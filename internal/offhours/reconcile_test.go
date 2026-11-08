package offhours_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/applicationautoscaling"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/offhours"
	"github.com/rcss-labs/harbormaster/internal/store"
)

type reconcilerHarness struct {
	*applierHarness
	rec   *offhours.Reconciler
	polls int
}

func (h *applierHarness) reconciler() *offhours.Reconciler {
	return &offhours.Reconciler{Applier: h.applier, Store: h.store, Interval: time.Minute, Now: func() time.Time { return h.now }}
}

// newReconcilerHarness adds an enabled account and the polled payments-api row carrying env=staging.
func newReconcilerHarness(t *testing.T) *reconcilerHarness {
	t.Helper()
	h := &reconcilerHarness{applierHarness: newApplierHarness(t)}
	h.rec = h.reconciler()
	if _, err := h.store.CreateAccount(t.Context(), store.AWSAccount{Name: "homelab", AccountID: testAccountID,
		RoleARN: "arn:aws:iam::" + testAccountID + ":role/x", ExternalID: "e", Regions: []string{testRegion}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	h.tagService(t, paymentsKey, `[{"Key":"env","Value":"staging"}]`, "ACTIVE")
	// The schedule row is stamped with the real clock while the tests run on a simulated one; an old stamp keeps the
	// "schedule saved after the last check" retry out of the way unless a test saves the schedule itself.
	if _, err := h.store.Pool.Exec(t.Context(), `update offhours_schedules set updated_at = now() - interval '10 years'`); err != nil {
		t.Fatal(err)
	}
	return h
}

// tagService stores the polled service row. The desired count changes on every call because the store writes a
// service row only when a hashed field changed, and the hash ignores the raw tags.
func (h *reconcilerHarness) tagService(t *testing.T, key model.ServiceKey, tagsJSON, status string) {
	t.Helper()
	h.polls++
	_, err := h.store.UpsertServices(t.Context(), []model.Service{{Key: key, Status: status, Desired: 3 + h.polls, LaunchType: "FARGATE",
		Raw: []byte(`{"Tags":` + tagsJSON + `}`), PolledAt: h.now}})
	if err != nil {
		t.Fatal(err)
	}
}

// at moves the clock to a Berlin wall time. 2026-11-09 is a Monday; 2026-11-07 a Saturday.
func (h *reconcilerHarness) at(day, hour, minute int) {
	h.now = time.Date(2026, 11, day, hour, minute, 0, 0, h.berlin)
}

func (h *reconcilerHarness) cycle(t *testing.T) {
	t.Helper()
	if err := h.rec.Cycle(t.Context()); err != nil {
		t.Fatalf("cycle: %v", err)
	}
}

func (h *reconcilerHarness) enrolment(t *testing.T) store.OffHoursEnrolment {
	t.Helper()
	e, err := h.store.GetOffHoursEnrolment(t.Context(), paymentsKey)
	if err != nil {
		t.Fatalf("enrolment: %v", err)
	}
	return e
}

func (h *reconcilerHarness) noEnrolment(t *testing.T) {
	t.Helper()
	if _, err := h.store.GetOffHoursEnrolment(t.Context(), paymentsKey); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("enrolment = %v, want ErrNotFound", err)
	}
}

func (h *reconcilerHarness) setRange(min, max int) {
	h.fake.Estate.Mu.Lock()
	defer h.fake.Estate.Mu.Unlock()
	cfg := h.fake.Estate.ScalingConfig[paymentsKey]
	cfg.Min, cfg.Max = min, max
}

func (h *reconcilerHarness) removeAction(name string) {
	h.fake.Estate.Mu.Lock()
	defer h.fake.Estate.Mu.Unlock()
	cfg := h.fake.Estate.ScalingConfig[paymentsKey]
	kept := cfg.Scheduled[:0]
	for _, a := range cfg.Scheduled {
		if a.Name != name {
			kept = append(kept, a)
		}
	}
	cfg.Scheduled = kept
}

func (h *reconcilerHarness) requireRange(t *testing.T, wantMin, wantMax int) {
	t.Helper()
	if min, max, _ := h.target(); min != wantMin || max != wantMax {
		t.Fatalf("range = %d/%d, want %d/%d", min, max, wantMin, wantMax)
	}
}

func (h *reconcilerHarness) requireActions(t *testing.T, want ...string) {
	t.Helper()
	got := h.allActions()
	if len(got) != len(want) {
		t.Fatalf("actions = %v, want %v", got, want)
	}
	for _, name := range want {
		if _, ok := got[name]; !ok {
			t.Fatalf("actions = %v, want %v", got, want)
		}
	}
}

func (h *reconcilerHarness) saveSchedule(t *testing.T, edit func(*store.OffHoursSchedule)) {
	t.Helper()
	edit(&h.schedule)
	updated, err := h.store.UpdateOffHoursSchedule(t.Context(), h.schedule, h.userID)
	if err != nil {
		t.Fatal(err)
	}
	h.schedule = updated
}

func weekdayGrid(from, to int) string {
	grid := []byte(strings.Repeat("0", 168))
	for day := 0; day < 5; day++ {
		for hour := from; hour < to; hour++ {
			grid[day*24+hour] = '1'
		}
	}
	return string(grid)
}

func TestReconcile_EnrolsMatchesAndSleepsInSleepHours(t *testing.T) {
	h := newReconcilerHarness(t) // Saturday 23:00: asleep
	h.cycle(t)

	e := h.enrolment(t)
	if e.SavedMin != 2 || e.SavedMax != 6 || e.State != "active" || e.LastCheckedAt == nil {
		t.Fatalf("enrolment = %+v, want saved 2/6, active, checked", e)
	}
	// The Tasks column reads "N -> 0" for a sleeping service, so N must be what it ran before it was put to sleep.
	if e.AwakeTasks == 0 {
		t.Fatalf("awake tasks = %d, want the desired count the service had before it slept", e.AwakeTasks)
	}
	h.requireRange(t, 0, 0)
	h.requireActions(t, "nightly-batch", "harbormaster-offhours-wake-07", "harbormaster-offhours-sleep-20")
	var texts []string
	for _, op := range h.operations(t) {
		texts = append(texts, op.Text)
	}
	want := []string{`Joined off-hours schedule "staging nights" (saved min 2, max 6).`, `Put to sleep by off-hours schedule "staging nights".`}
	if strings.Join(texts, "|") != strings.Join(want, "|") {
		t.Fatalf("operations = %q, want %q", texts, want)
	}
}

func TestReconcile_EnrolsAwakeServiceWithoutTouchingTheRange(t *testing.T) {
	h := newReconcilerHarness(t)
	h.at(9, 10, 0)
	h.cycle(t)
	h.requireRange(t, 2, 6)
	h.requireActions(t, "nightly-batch", "harbormaster-offhours-wake-07", "harbormaster-offhours-sleep-20")
}

func TestReconcile_ScheduleEditRewritesActions(t *testing.T) {
	h := newReconcilerHarness(t)
	h.at(9, 10, 0)
	h.cycle(t)

	h.saveSchedule(t, func(sc *store.OffHoursSchedule) { sc.Awake = weekdayGrid(8, 18) })
	h.at(9, 10, 1)
	h.cycle(t)

	h.requireActions(t, "nightly-batch", "harbormaster-offhours-wake-08", "harbormaster-offhours-sleep-18")
}

func TestReconcile_TagRemovedReleases(t *testing.T) {
	h := newReconcilerHarness(t)
	h.at(9, 10, 0)
	h.cycle(t)

	h.tagService(t, paymentsKey, `[{"Key":"env","Value":"dev"}]`, "ACTIVE")
	h.cycle(t)

	h.noEnrolment(t)
	h.requireActions(t, "nightly-batch")
	h.requireRange(t, 2, 6)
}

func TestReconcile_DeleteRestoresCapacity(t *testing.T) {
	h := newReconcilerHarness(t) // asleep
	h.cycle(t)
	h.requireRange(t, 0, 0)

	if err := h.store.DeleteOffHoursSchedule(t.Context(), h.schedule.ID); err != nil {
		t.Fatal(err)
	}
	h.cycle(t)

	h.noEnrolment(t)
	h.requireRange(t, 2, 6)
	h.requireActions(t, "nightly-batch")
}

func TestReconcile_DisableDeregistersOwnTarget(t *testing.T) {
	h := newReconcilerHarness(t)
	h.removeTarget()
	h.setDesired(t, 2)
	h.cycle(t) // Saturday night: registers 2/2, then sleeps
	if e := h.enrolment(t); !e.RegisteredTarget {
		t.Fatalf("enrolment = %+v, want an own target", e)
	}

	h.saveSchedule(t, func(sc *store.OffHoursSchedule) { sc.Enabled = false })
	h.cycle(t)

	h.noEnrolment(t)
	if _, _, ok := h.target(); ok {
		t.Fatal("the target Harbormaster registered is still there")
	}
	if got := h.desired(t); got != 2 {
		t.Fatalf("desired = %d, want the original 2 back", got)
	}
}

func TestReconcile_AdoptsExternalCapacityWhileAwake(t *testing.T) {
	h := newReconcilerHarness(t)
	h.at(9, 10, 0)
	h.cycle(t)

	h.setRange(3, 8)
	h.at(9, 11, 0)
	h.cycle(t)

	if e := h.enrolment(t); e.SavedMin != 3 || e.SavedMax != 8 {
		t.Fatalf("saved = %d/%d, want 3/8", e.SavedMin, e.SavedMax)
	}
	h.requireRange(t, 3, 8)
	wake := h.allActions()["harbormaster-offhours-wake-07"]
	if wake.Min == nil || *wake.Min != 3 || wake.Max == nil || *wake.Max != 8 {
		t.Fatalf("wake action = %+v, want 3/8", wake)
	}
	if got := h.lastOperationText(t); got != "Saved capacity updated to min 3, max 8: changed outside Harbormaster." {
		t.Fatalf("operation = %q", got)
	}
}

func TestReconcile_TerraformApplyAtNightIsAdoptedThenPutBackToSleep(t *testing.T) {
	h := newReconcilerHarness(t)
	h.cycle(t) // asleep
	h.requireRange(t, 0, 0)

	h.setRange(3, 8) // a nightly terraform apply
	h.at(8, 0, 30)
	h.cycle(t)

	if e := h.enrolment(t); e.SavedMin != 3 || e.SavedMax != 8 {
		t.Fatalf("saved = %d/%d, want 3/8", e.SavedMin, e.SavedMax)
	}
	h.requireRange(t, 0, 0)
	if wake := h.allActions()["harbormaster-offhours-wake-07"]; wake.Max == nil || *wake.Max != 8 {
		t.Fatalf("wake action = %+v, want 3/8", wake)
	}
}

func TestReconcile_GraceWindowSkipsEnforcement(t *testing.T) {
	h := newReconcilerHarness(t)
	h.at(9, 10, 0)
	h.cycle(t)

	h.setRange(3, 8)
	h.at(10, 7, 2) // 2 minutes after Tuesday's wake edge
	h.cycle(t)
	if e := h.enrolment(t); e.SavedMin != 2 || e.SavedMax != 6 {
		t.Fatalf("saved = %d/%d inside the grace window, want 2/6 untouched", e.SavedMin, e.SavedMax)
	}

	h.at(10, 7, 10)
	h.cycle(t)
	if e := h.enrolment(t); e.SavedMin != 3 || e.SavedMax != 8 {
		t.Fatalf("saved = %d/%d after the grace window, want 3/8", e.SavedMin, e.SavedMax)
	}
}

// sleepAndWake enrols at Saturday 23:00 and wakes manually until Sunday 01:00.
func (h *reconcilerHarness) sleepAndWake(t *testing.T) time.Time {
	t.Helper()
	h.cycle(t)
	e := h.enrolment(t)
	until, err := h.wakeFor(t, &e)
	if err != nil {
		t.Fatal(err)
	}
	return until
}

func TestReconcile_NewLeaderHonoursWokenUntil(t *testing.T) {
	h := newReconcilerHarness(t)
	until := h.sleepAndWake(t)

	h.now = until.Add(-30 * time.Minute)
	h.rec = h.reconciler() // a fresh leader holds no state
	h.cycle(t)

	h.requireRange(t, 2, 6)
	if _, ok := h.allActions()["harbormaster-offhours-woken-until"]; !ok {
		t.Fatal("the woken-until action is gone")
	}
}

func TestReconcile_ExpiredWakeIsEnforcedWhenAtActionMissing(t *testing.T) {
	h := newReconcilerHarness(t)
	until := h.sleepAndWake(t)
	h.removeAction("harbormaster-offhours-woken-until") // the fake never runs at() actions, and one may have been lost

	h.now = until.Add(30 * time.Minute)
	h.cycle(t)

	h.requireRange(t, 0, 0)
	if e := h.enrolment(t); e.WokenUntil != nil || e.WokenBy != nil {
		t.Fatalf("enrolment = %+v, want the wake window cleared", e)
	}
}

func TestReconcile_ExpiredWakeDeletesItsAction(t *testing.T) {
	h := newReconcilerHarness(t)
	until := h.sleepAndWake(t)

	h.now = until.Add(30 * time.Minute)
	h.cycle(t)

	if _, ok := h.allActions()["harbormaster-offhours-woken-until"]; ok {
		t.Fatal("the expired woken-until action is still there")
	}
}

func TestReconcile_WokenEnrolmentAtZeroRangeIsWoken(t *testing.T) {
	// The at() put failed after woken_until was stamped: the row says woken, AWS says 0/0 and has no at() action.
	h := newReconcilerHarness(t)
	until := h.sleepAndWake(t)
	h.setRange(0, 0)
	h.removeAction("harbormaster-offhours-woken-until")

	h.now = until.Add(-30 * time.Minute)
	h.cycle(t)

	h.requireRange(t, 2, 6)
	if _, ok := h.allActions()["harbormaster-offhours-woken-until"]; !ok {
		t.Fatal("the at() action was not put again")
	}
}

func TestReconcile_WokenEnrolmentClampedToWakeEdgeGetsNoAtAction(t *testing.T) {
	h := newReconcilerHarness(t)
	h.cycle(t)
	h.at(9, 5, 30) // Monday, asleep; the wake edge at 07:00 comes before the two hours are up
	e := h.enrolment(t)
	until, err := h.wakeFor(t, &e)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 11, 9, 7, 0, 0, 0, h.berlin); !until.Equal(want) {
		t.Fatalf("until = %v, want %v", until, want)
	}
	h.setRange(0, 0)

	h.now = until.Add(-30 * time.Minute)
	h.cycle(t)

	h.requireRange(t, 2, 6)
	if _, ok := h.allActions()["harbormaster-offhours-woken-until"]; ok {
		t.Fatal("an at() action was put for a window that ends on the wake edge")
	}
}

func TestReconcile_ReadOnlyAccountIsBlockedAndRetriedHourly(t *testing.T) {
	h := newReconcilerHarness(t)
	h.fake.DenyWrites(testAccountID)
	h.cycle(t)

	e := h.enrolment(t)
	if e.State != "blocked" || e.BlockedReason != "access_denied" || e.LastCheckedAt == nil {
		t.Fatalf("enrolment = %+v, want blocked by access_denied", e)
	}
	checked := *e.LastCheckedAt

	h.now = h.now.Add(10 * time.Minute)
	h.cycle(t)
	if got := h.enrolment(t).LastCheckedAt; !got.Equal(checked) {
		t.Fatalf("last_checked_at moved to %v after 10 minutes, want a skip", got)
	}

	h.now = h.now.Add(55 * time.Minute)
	h.cycle(t)
	if got := h.enrolment(t); !got.LastCheckedAt.After(checked) || got.State != "blocked" {
		t.Fatalf("enrolment = %+v, want a retry after an hour that is blocked again", got)
	}
}

func TestReconcile_BlockedEnrolmentIsRetriedWhenTheScheduleChanges(t *testing.T) {
	h := newReconcilerHarness(t)
	h.fake.DenyWrites(testAccountID)
	h.cycle(t)
	checked := *h.enrolment(t).LastCheckedAt

	time.Sleep(10 * time.Millisecond)
	h.saveSchedule(t, func(sc *store.OffHoursSchedule) { sc.Awake = weekdayGrid(8, 18) })
	h.now = h.now.Add(time.Minute)
	h.cycle(t)

	if got := h.enrolment(t).LastCheckedAt; !got.After(checked) {
		t.Fatalf("last_checked_at = %v, want a retry after the schedule was saved", got)
	}
}

func TestReconcile_DoubleMatchStaysOnExistingSchedule(t *testing.T) {
	h := newReconcilerHarness(t)
	h.tagService(t, paymentsKey, `[{"Key":"env","Value":"staging"},{"Key":"tier","Value":"web"}]`, "ACTIVE")
	second, err := h.store.CreateOffHoursSchedule(t.Context(), store.OffHoursSchedule{Name: "web hours", Enabled: true,
		TagKey: "tier", TagValue: "web", Timezone: "Europe/Berlin", Awake: weekdayGrid(9, 17)}, h.userID)
	if err != nil {
		t.Fatal(err)
	}
	h.saveSchedule(t, func(sc *store.OffHoursSchedule) { sc.Enabled = false })
	h.at(9, 10, 0)
	h.cycle(t) // only the second schedule is enabled
	if e := h.enrolment(t); e.ScheduleID == nil || *e.ScheduleID != second.ID {
		t.Fatalf("enrolment = %+v, want the second schedule", e)
	}

	h.saveSchedule(t, func(sc *store.OffHoursSchedule) { sc.Enabled = true }) // the first has the lower id
	h.at(9, 10, 5)
	h.cycle(t)

	if e := h.enrolment(t); e.ScheduleID == nil || *e.ScheduleID != second.ID {
		t.Fatalf("enrolment = %+v, want it to stay on the second schedule", e)
	}
}

func TestReconcile_ForeignActionsUntouched(t *testing.T) {
	h := newReconcilerHarness(t)
	h.at(9, 10, 0)
	h.cycle(t)
	h.saveSchedule(t, func(sc *store.OffHoursSchedule) { sc.Awake = weekdayGrid(8, 18) })
	h.cycle(t)
	h.saveSchedule(t, func(sc *store.OffHoursSchedule) { sc.Enabled = false })
	h.cycle(t)

	h.requireActions(t, "nightly-batch")
	if got := h.allActions()["nightly-batch"]; got.Schedule != "cron(0 3 * * ? *)" || got.Min == nil || *got.Min != 0 || got.Max == nil || *got.Max != 6 {
		t.Fatalf("foreign action changed: %+v", got)
	}
}

func TestReconcile_PartialEnrolFailureKeepsTheRow(t *testing.T) {
	h := newReconcilerHarness(t)
	h.removeTarget()
	h.setDesired(t, 2)
	h.at(9, 10, 0)
	// Enrol's first AWS write registers the target; its second puts the first scheduled action.
	h.fake.FailNext("PutScheduledAction", errors.New("injected put failure"))
	h.cycle(t)

	e := h.enrolment(t)
	if !e.RegisteredTarget || !strings.Contains(e.LastError, "injected put failure") {
		t.Fatalf("enrolment = %+v, want registered_target and last_error set", e)
	}
	if _, _, ok := h.target(); !ok {
		t.Fatal("the target registered before the failure is gone")
	}

	h.cycle(t) // the next cycle finishes the job
	if e := h.enrolment(t); e.LastError != "" {
		t.Fatalf("last_error = %q, want it cleared", e.LastError)
	}
	h.requireActions(t, "harbormaster-offhours-wake-07", "harbormaster-offhours-sleep-20") // removeTarget dropped the foreign action
}

func TestReconcile_LastErrorIsTrimmed(t *testing.T) {
	h := newReconcilerHarness(t)
	h.at(9, 10, 0)
	h.fake.FailNext("PutScheduledAction", errors.New(strings.Repeat("x", 900)))
	h.cycle(t)
	if got := h.enrolment(t).LastError; len(got) != 500 {
		t.Fatalf("last_error has %d characters, want 500", len(got))
	}
}

func TestReconcile_ServicesThatCannotBeEnrolledGetNoRowAndNoWrite(t *testing.T) {
	t.Run("zero capacity", func(t *testing.T) {
		h := newReconcilerHarness(t)
		h.removeTarget()
		h.setDesired(t, 0)
		h.cycle(t)
		h.noEnrolment(t)
		if _, _, ok := h.target(); ok {
			t.Fatal("a target was registered")
		}
	})
	t.Run("minimum zero", func(t *testing.T) {
		h := newReconcilerHarness(t)
		h.setRange(0, 6)
		h.at(9, 10, 0)
		h.cycle(t)
		h.noEnrolment(t)
		h.requireActions(t, "nightly-batch")
	})
	t.Run("needs attention is logged once", func(t *testing.T) {
		h := newReconcilerHarness(t)
		h.setRange(0, 0)
		h.fake.Estate.Mu.Lock()
		h.fake.Estate.ScalingConfig[paymentsKey].Scheduled = append(h.fake.Estate.ScalingConfig[paymentsKey].Scheduled,
			fakeSleepSeed())
		h.fake.Estate.Mu.Unlock()
		h.cycle(t)
		h.cycle(t)
		h.noEnrolment(t)
		count := 0
		for _, op := range h.operations(t) {
			if op.Text == offhours.ReasonNeedsAttention {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("%d operation entries for needs attention, want 1", count)
		}
	})
}

func TestReconcile_ServiceGoneFromAWSDeletesTheRow(t *testing.T) {
	h := newReconcilerHarness(t)
	h.at(9, 10, 0)
	h.cycle(t)

	h.fake.Estate.Mu.Lock()
	for i := range h.fake.Estate.Services {
		s := &h.fake.Estate.Services[i]
		if s.ClusterName == paymentsKey.Cluster && aws.ToString(s.Service.ServiceName) == paymentsKey.Service {
			s.Service.Status = aws.String("INACTIVE")
		}
	}
	h.fake.Estate.Mu.Unlock()
	h.tagService(t, paymentsKey, `[{"Key":"env","Value":"staging"}]`, "INACTIVE")
	before := h.allActions()
	h.cycle(t)

	h.noEnrolment(t)
	if got := h.lastOperationText(t); got != "Left off-hours schedule: the service no longer exists." {
		t.Fatalf("operation = %q", got)
	}
	if len(h.allActions()) != len(before) {
		t.Fatal("the reconciler wrote to a service that no longer exists")
	}
}

func TestReconcile_DisabledAccountIsIgnored(t *testing.T) {
	h := newReconcilerHarness(t)
	if _, err := h.store.Pool.Exec(t.Context(), `update aws_accounts set enabled = false`); err != nil {
		t.Fatal(err)
	}
	h.cycle(t)
	h.noEnrolment(t)
}

func TestReconciler_RunCyclesNowAndOnKick(t *testing.T) {
	h := newReconcilerHarness(t)
	h.rec.Interval = time.Hour
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { h.rec.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	waitUntil(t, "the first cycle to enrol the service", func() bool {
		_, err := h.store.GetOffHoursEnrolment(ctx, paymentsKey)
		return err == nil
	})
	if _, err := h.store.Pool.Exec(ctx, `delete from offhours_enrolments`); err != nil {
		t.Fatal(err)
	}
	h.rec.Kick()
	waitUntil(t, "a kick to run a cycle", func() bool {
		_, err := h.store.GetOffHoursEnrolment(ctx, paymentsKey)
		return err == nil
	})
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// fakeSleepSeed is a Harbormaster sleep action with no wake action beside it.
func fakeSleepSeed() fake.ScheduledSeed {
	return fake.ScheduledSeed{Name: "harbormaster-offhours-sleep-20", Schedule: "cron(0 20 ? * MON-FRI *)", Timezone: "Europe/Berlin",
		Min: aws.Int32(0), Max: aws.Int32(0)}
}

// cancelAfterRegister cancels a context right after the first successful RegisterScalableTarget, as losing
// leadership in the middle of a service would.
type cancelAfterRegister struct {
	*fake.Fake
	cancel context.CancelFunc
}

type cancellingAutoScaling struct {
	awsx.AutoScalingAPI
	cancel context.CancelFunc
}

func (c cancelAfterRegister) AutoScaling(ctx context.Context, t awsx.Target) (awsx.AutoScalingAPI, error) {
	api, err := c.Fake.AutoScaling(ctx, t)
	return cancellingAutoScaling{AutoScalingAPI: api, cancel: c.cancel}, err
}

func (c cancellingAutoScaling) RegisterScalableTarget(ctx context.Context, in *applicationautoscaling.RegisterScalableTargetInput, opts ...func(*applicationautoscaling.Options)) (*applicationautoscaling.RegisterScalableTargetOutput, error) {
	out, err := c.AutoScalingAPI.RegisterScalableTarget(ctx, in, opts...)
	c.cancel()
	return out, err
}

func TestReconcile_CancelledLeaderContextStillKeepsTheRow(t *testing.T) {
	h := newReconcilerHarness(t)
	h.removeTarget()
	h.setDesired(t, 2)
	h.at(9, 10, 0)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	h.applier.Clients = cancelAfterRegister{Fake: h.fake, cancel: cancel}

	_ = h.rec.Cycle(ctx)

	if _, _, ok := h.target(); !ok {
		t.Fatal("the target was not registered")
	}
	if e := h.enrolment(t); !e.RegisteredTarget {
		t.Fatalf("enrolment = %+v, want registered_target kept after the context was cancelled", e)
	}
}

// stallAfterRegister blocks after the first successful RegisterScalableTarget until the call's context expires.
type stallAfterRegister struct{ *fake.Fake }

type stallingAutoScaling struct{ awsx.AutoScalingAPI }

func (c stallAfterRegister) AutoScaling(ctx context.Context, t awsx.Target) (awsx.AutoScalingAPI, error) {
	api, err := c.Fake.AutoScaling(ctx, t)
	return stallingAutoScaling{AutoScalingAPI: api}, err
}

func (c stallingAutoScaling) RegisterScalableTarget(ctx context.Context, in *applicationautoscaling.RegisterScalableTargetInput, opts ...func(*applicationautoscaling.Options)) (*applicationautoscaling.RegisterScalableTargetOutput, error) {
	out, err := c.AutoScalingAPI.RegisterScalableTarget(ctx, in, opts...)
	<-ctx.Done()
	return out, err
}

func TestReconcile_ServiceDeadlineExpiryStillKeepsTheRowAndRecordsTheError(t *testing.T) {
	h := newReconcilerHarness(t)
	h.removeTarget()
	h.setDesired(t, 2)
	h.at(9, 10, 0)
	h.applier.Clients = stallAfterRegister{Fake: h.fake}
	h.rec.ServiceTimeout = 100 * time.Millisecond

	_ = h.rec.Cycle(t.Context())

	e := h.enrolment(t)
	if !e.RegisteredTarget || e.LastError == "" {
		t.Fatalf("enrolment = %+v, want registered_target kept and last_error recorded", e)
	}
}

func TestReconcile_ConcurrentLeadersEnrolAServiceOnce(t *testing.T) {
	h := newReconcilerHarness(t)
	h.at(9, 10, 0)
	other := h.reconciler()
	done := make(chan struct{}, 2)
	for _, r := range []*offhours.Reconciler{h.rec, other} {
		go func() { _ = r.Cycle(t.Context()); done <- struct{}{} }()
	}
	<-done
	<-done

	joins := 0
	for _, op := range h.operations(t) {
		if strings.HasPrefix(op.Text, "Joined off-hours schedule") {
			joins++
		}
	}
	if joins != 1 {
		t.Fatalf("%d join entries, want 1", joins)
	}
}

func TestReconcile_MissingTargetIsReRegisteredAndLogged(t *testing.T) {
	h := newReconcilerHarness(t)
	h.at(9, 10, 0)
	h.cycle(t)
	h.removeTarget()
	h.at(9, 11, 0)
	h.cycle(t)

	h.requireRange(t, 2, 6)
	if e := h.enrolment(t); !e.RegisteredTarget {
		t.Fatalf("enrolment = %+v, want registered_target set", e)
	}
	found := false
	for _, op := range h.operations(t) {
		found = found || op.Text == "Scalable target was missing; re-registered with min 2, max 6."
	}
	if !found {
		t.Fatal("no operation for the re-registered target")
	}
	if got := h.lastOperationText(t); got != "Scalable target was missing; re-registered with min 2, max 6." {
		t.Fatalf("last operation = %q, want only the re-register entry for the repair", got)
	}
}

func TestReconcile_EnrolledServiceInDisabledAccountIsLeftAlone(t *testing.T) {
	h := newReconcilerHarness(t)
	h.at(9, 10, 0)
	h.cycle(t)
	if _, err := h.store.Pool.Exec(t.Context(), `update aws_accounts set enabled = false`); err != nil {
		t.Fatal(err)
	}
	h.tagService(t, paymentsKey, `[{"Key":"env","Value":"dev"}]`, "ACTIVE")
	before := h.fake.CallCounts()
	h.cycle(t)

	h.enrolment(t)
	h.requireActions(t, "nightly-batch", "harbormaster-offhours-wake-07", "harbormaster-offhours-sleep-20")
	for k, n := range h.fake.CallCounts() {
		if n != before[k] {
			t.Fatalf("AWS call %s ran %d times in a disabled account", k, n-before[k])
		}
	}
}
