package offhours_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/offhours"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

const (
	testAccountID = "482109375512"
	testRegion    = "us-east-1"
)

var paymentsKey = model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: testAccountID, Region: testRegion, Cluster: "core-services"}, Service: "payments-api"}

type applierHarness struct {
	applier  *offhours.Applier
	fake     *fake.Fake
	store    *store.Store
	schedule store.OffHoursSchedule
	userID   int64
	now      time.Time
	berlin   *time.Location
}

// newApplierHarness builds an Applier over the fake estate with payments-api holding a 2/6 scalable target, a
// foreign "nightly-batch" scheduled action and a Mon-Fri 07-20 Europe/Berlin schedule. The clock starts at
// Saturday 2026-11-07 23:00 Berlin.
func newApplierHarness(t *testing.T) *applierHarness {
	t.Helper()
	st := storetest.Open(t)
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	h := &applierHarness{store: st, berlin: berlin, now: time.Date(2026, 11, 7, 23, 0, 0, 0, berlin)}
	if h.now.Weekday() != time.Saturday {
		t.Fatalf("test clock is a %v, want Saturday", h.now.Weekday())
	}
	h.fake = fake.New(fake.MockupEstate(func() time.Time { return h.now }))
	h.fake.Estate.Mu.Lock()
	h.fake.Estate.ScalingConfig[paymentsKey] = &fake.ScalingSeed{Min: 2, Max: 6, Scheduled: []fake.ScheduledSeed{
		{Name: "nightly-batch", Schedule: "cron(0 3 * * ? *)", Min: aws.Int32(0), Max: aws.Int32(6)}}}
	h.fake.Estate.Mu.Unlock()
	h.setDesired(t, 3)

	ctx := context.Background()
	user, err := st.CreateUser(ctx, "sam", "hash", true, false)
	if err != nil {
		t.Fatal(err)
	}
	h.userID = user.ID
	awake := []byte(strings.Repeat("0", 168))
	for day := 0; day < 5; day++ {
		for hour := 7; hour < 20; hour++ {
			awake[day*24+hour] = '1'
		}
	}
	h.schedule, err = st.CreateOffHoursSchedule(ctx, store.OffHoursSchedule{
		Name: "staging nights", Enabled: true, TagKey: "env", TagValue: "staging", Timezone: "Europe/Berlin", Awake: string(awake)}, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	h.applier = &offhours.Applier{Store: st, Clients: h.fake, Now: func() time.Time { return h.now }}
	return h
}

func (h *applierHarness) setDesired(t *testing.T, n int32) {
	t.Helper()
	h.fake.Estate.Mu.Lock()
	defer h.fake.Estate.Mu.Unlock()
	for i := range h.fake.Estate.Services {
		s := &h.fake.Estate.Services[i]
		if s.ClusterName == paymentsKey.Cluster && aws.ToString(s.Service.ServiceName) == paymentsKey.Service {
			s.Service.DesiredCount = n
			return
		}
	}
	t.Fatal("payments-api not in the fake estate")
}

func (h *applierHarness) removeTarget() {
	h.fake.Estate.Mu.Lock()
	defer h.fake.Estate.Mu.Unlock()
	delete(h.fake.Estate.ScalingConfig, paymentsKey)
}

func (h *applierHarness) desired(t *testing.T) int {
	t.Helper()
	live, err := h.applier.ReadLive(t.Context(), paymentsKey)
	if err != nil {
		t.Fatal(err)
	}
	return live.Desired
}

// allActions lists every scheduled action of payments-api, owned or not, straight from the fake.
func (h *applierHarness) allActions() map[string]fake.ScheduledSeed {
	h.fake.Estate.Mu.Lock()
	defer h.fake.Estate.Mu.Unlock()
	out := map[string]fake.ScheduledSeed{}
	if cfg := h.fake.Estate.ScalingConfig[paymentsKey]; cfg != nil {
		for _, a := range cfg.Scheduled {
			out[a.Name] = a
		}
	}
	return out
}

func (h *applierHarness) target() (min, max int, ok bool) {
	h.fake.Estate.Mu.Lock()
	defer h.fake.Estate.Mu.Unlock()
	cfg := h.fake.Estate.ScalingConfig[paymentsKey]
	if cfg == nil {
		return 0, 0, false
	}
	return cfg.Min, cfg.Max, true
}

func (h *applierHarness) inTx(t *testing.T, fn func(tx store.Tx) error) error {
	t.Helper()
	return h.store.InTx(t.Context(), fn)
}

func (h *applierHarness) enrol(t *testing.T) store.OffHoursEnrolment {
	t.Helper()
	live, err := h.applier.ReadLive(t.Context(), paymentsKey)
	if err != nil {
		t.Fatal(err)
	}
	var e store.OffHoursEnrolment
	err = h.inTx(t, func(tx store.Tx) error {
		var err error
		e, err = h.applier.Enrol(t.Context(), tx, h.schedule, store.OffHoursMatch{Key: paymentsKey, Desired: live.Desired}, live)
		return err
	})
	if err != nil {
		t.Fatalf("enrol: %v", err)
	}
	return e
}

func (h *applierHarness) sleep(t *testing.T, e *store.OffHoursEnrolment) {
	t.Helper()
	if err := h.inTx(t, func(tx store.Tx) error { return h.applier.Sleep(t.Context(), tx, e, nil, h.schedule.Name) }); err != nil {
		t.Fatalf("sleep: %v", err)
	}
}

func (h *applierHarness) wakeFor(t *testing.T, e *store.OffHoursEnrolment) (time.Time, error) {
	t.Helper()
	var until time.Time
	err := h.inTx(t, func(tx store.Tx) error {
		var err error
		until, err = h.applier.WakeFor(t.Context(), tx, e, h.schedule, h.userID)
		return err
	})
	return until, err
}

func (h *applierHarness) putBack(t *testing.T, e *store.OffHoursEnrolment) error {
	t.Helper()
	return h.inTx(t, func(tx store.Tx) error {
		return h.applier.PutBackToSleep(t.Context(), tx, e, h.schedule, h.userID)
	})
}

func (h *applierHarness) release(t *testing.T, e store.OffHoursEnrolment) {
	t.Helper()
	live, err := h.applier.ReadLive(t.Context(), paymentsKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.inTx(t, func(tx store.Tx) error {
		return h.applier.Release(t.Context(), tx, e, live, h.schedule.Name)
	}); err != nil {
		t.Fatalf("release: %v", err)
	}
}

// operations returns the service's operations oldest first.
func (h *applierHarness) operations(t *testing.T) []store.OperationRow {
	t.Helper()
	ops, err := h.store.ListOperations(t.Context(), paymentsKey, false, nil, 50)
	if err != nil {
		t.Fatal(err)
	}
	for i, j := 0, len(ops)-1; i < j; i, j = i+1, j-1 {
		ops[i], ops[j] = ops[j], ops[i]
	}
	return ops
}

func (h *applierHarness) lastOperationText(t *testing.T) string {
	t.Helper()
	ops := h.operations(t)
	if len(ops) == 0 {
		t.Fatal("no operations")
	}
	return ops[len(ops)-1].Text
}

func TestApplier_EnrolExistingTargetSavesRange(t *testing.T) {
	h := newApplierHarness(t)
	e := h.enrol(t)
	if e.SavedMin != 2 || e.SavedMax != 6 || e.RegisteredTarget || e.SavedDesired != nil {
		t.Fatalf("enrolment = %+v, want saved 2/6, own target false, no saved desired", e)
	}
	if e.ScheduleID == nil || *e.ScheduleID != h.schedule.ID || e.State != "active" {
		t.Fatalf("enrolment = %+v, want the schedule and state active", e)
	}
	actions := h.allActions()
	if len(actions) != 3 {
		t.Fatalf("actions = %v, want nightly-batch plus the two owned ones", actions)
	}
	wake := actions["harbormaster-offhours-wake-07"]
	if wake.Min == nil || *wake.Min != 2 || wake.Max == nil || *wake.Max != 6 {
		t.Fatalf("wake action = %+v, want 2/6", wake)
	}
	sleep := actions["harbormaster-offhours-sleep-20"]
	if sleep.Min == nil || *sleep.Min != 0 || sleep.Max == nil || *sleep.Max != 0 {
		t.Fatalf("sleep action = %+v, want 0/0", sleep)
	}
	if got := actions["nightly-batch"]; got.Schedule != "cron(0 3 * * ? *)" {
		t.Fatalf("foreign action changed: %+v", got)
	}
	if min, max, _ := h.target(); min != 2 || max != 6 {
		t.Fatalf("range = %d/%d, enrolling must not change it", min, max)
	}
	stored, err := h.store.GetOffHoursEnrolment(t.Context(), paymentsKey)
	if err != nil || stored.LastAppliedAt == nil || !stored.LastAppliedAt.Equal(h.now) {
		t.Fatalf("stored = %+v err=%v, want last_applied_at = now", stored, err)
	}
	if got := h.lastOperationText(t); got != `Joined off-hours schedule "staging nights" (saved min 2, max 6).` {
		t.Fatalf("operation = %q", got)
	}
}

func TestApplier_EnrolWithoutTargetRegistersOne(t *testing.T) {
	h := newApplierHarness(t)
	h.removeTarget()
	h.setDesired(t, 2)

	e := h.enrol(t)
	if e.SavedMin != 2 || e.SavedMax != 2 || !e.RegisteredTarget || e.SavedDesired == nil || *e.SavedDesired != 2 {
		t.Fatalf("enrolment = %+v, want saved 2/2, desired 2, own target", e)
	}
	if min, max, ok := h.target(); !ok || min != 2 || max != 2 {
		t.Fatalf("target = %d/%d ok=%v, want 2/2", min, max, ok)
	}
}

func TestApplier_EnrolSkipsZeroCapacity(t *testing.T) {
	h := newApplierHarness(t)
	h.removeTarget()
	h.setDesired(t, 0)

	live, err := h.applier.ReadLive(t.Context(), paymentsKey)
	if err != nil {
		t.Fatal(err)
	}
	err = h.inTx(t, func(tx store.Tx) error {
		_, err := h.applier.Enrol(t.Context(), tx, h.schedule, store.OffHoursMatch{Key: paymentsKey}, live)
		return err
	})
	if !errors.Is(err, offhours.ErrZeroCapacity) {
		t.Fatalf("Enrol = %v, want ErrZeroCapacity", err)
	}
	if _, _, ok := h.target(); ok {
		t.Fatal("a scalable target was registered for a service with no capacity")
	}
	if _, err := h.store.GetOffHoursEnrolment(t.Context(), paymentsKey); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("enrolment stored: %v", err)
	}
}

func TestApplier_WakeForSetsAtActionAndRestores(t *testing.T) {
	h := newApplierHarness(t)
	e := h.enrol(t)
	h.sleep(t, &e)
	if min, max, _ := h.target(); min != 0 || max != 0 {
		t.Fatalf("asleep range = %d/%d, want 0/0", min, max)
	}

	until, err := h.wakeFor(t, &e)
	if err != nil {
		t.Fatal(err)
	}
	if want := h.now.Add(2 * time.Hour); !until.Equal(want) {
		t.Fatalf("until = %v, want %v", until, want)
	}
	if min, max, _ := h.target(); min != 2 || max != 6 {
		t.Fatalf("woken range = %d/%d, want the saved 2/6", min, max)
	}
	woken, ok := h.allActions()["harbormaster-offhours-woken-until"]
	if !ok || woken.Schedule != "at(2026-11-08T01:00:00)" || woken.Timezone != "Europe/Berlin" ||
		woken.Min == nil || *woken.Min != 0 || woken.Max == nil || *woken.Max != 0 {
		t.Fatalf("woken action = %+v ok=%v, want at(2026-11-08T01:00:00) Europe/Berlin 0/0", woken, ok)
	}
	if e.WokenUntil == nil || !e.WokenUntil.Equal(until) || e.WokenBy == nil || *e.WokenBy != h.userID {
		t.Fatalf("enrolment = %+v, want woken_until and woken_by set", e)
	}
	if got := h.lastOperationText(t); got != "Woken for 2 hours until 01:00 CET by sam." {
		t.Fatalf("operation = %q", got)
	}
}

func TestApplier_WakeForRefusesWhenNotAsleep(t *testing.T) {
	h := newApplierHarness(t)
	e := h.enrol(t)
	h.sleep(t, &e)
	if _, err := h.wakeFor(t, &e); err != nil {
		t.Fatal(err)
	}
	_, err := h.wakeFor(t, &e)
	var conflict *offhours.ConflictError
	if !errors.As(err, &conflict) || conflict.Message != "payments-api is not asleep." {
		t.Fatalf("second WakeFor = %v, want ConflictError %q", err, "payments-api is not asleep.")
	}
}

func TestApplier_WakeNearMorningWritesNoAtAction(t *testing.T) {
	h := newApplierHarness(t)
	e := h.enrol(t)
	h.now = time.Date(2026, 11, 9, 6, 0, 0, 0, h.berlin) // Monday 06:00, the grid wakes at 07:00
	h.sleep(t, &e)

	until, err := h.wakeFor(t, &e)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 11, 9, 7, 0, 0, 0, h.berlin); !until.Equal(want) {
		t.Fatalf("until = %v, want the wake edge %v", until, want)
	}
	if e.WokenUntil == nil || !e.WokenUntil.Equal(until) {
		t.Fatalf("woken_until = %v, want %v", e.WokenUntil, until)
	}
	for name, a := range h.allActions() {
		if name == "harbormaster-offhours-woken-until" || strings.HasPrefix(a.Schedule, "at(") {
			t.Fatalf("action %s %q exists; a window ending at the wake edge must write no at() action", name, a.Schedule)
		}
	}
	if min, max, _ := h.target(); min != 2 || max != 6 {
		t.Fatalf("range = %d/%d, want woken 2/6", min, max)
	}
}

func TestApplier_PutBackToSleepRemovesAtAction(t *testing.T) {
	h := newApplierHarness(t)
	e := h.enrol(t)
	h.sleep(t, &e)
	if _, err := h.wakeFor(t, &e); err != nil {
		t.Fatal(err)
	}
	h.now = h.now.Add(30 * time.Minute)
	if err := h.putBack(t, &e); err != nil {
		t.Fatalf("put back to sleep: %v", err)
	}
	if _, ok := h.allActions()["harbormaster-offhours-woken-until"]; ok {
		t.Fatal("the woken action survived")
	}
	if min, max, _ := h.target(); min != 0 || max != 0 {
		t.Fatalf("range = %d/%d, want 0/0", min, max)
	}
	if e.WokenUntil != nil || e.WokenBy != nil {
		t.Fatalf("enrolment = %+v, want the woken fields cleared", e)
	}
	if got := h.lastOperationText(t); got != "Put back to sleep by sam." {
		t.Fatalf("operation = %q", got)
	}
	var conflict *offhours.ConflictError
	if err := h.putBack(t, &e); !errors.As(err, &conflict) {
		t.Fatalf("PutBackToSleep of a sleeping service = %v, want ConflictError", err)
	}
}

func TestApplier_ReleaseOwnTargetRestoresDesired(t *testing.T) {
	h := newApplierHarness(t)
	h.removeTarget()
	h.setDesired(t, 2)
	e := h.enrol(t)
	h.sleep(t, &e)
	if got := h.desired(t); got != 0 {
		t.Fatalf("desired while asleep = %d, want 0", got)
	}

	h.release(t, e)
	if _, _, ok := h.target(); ok {
		t.Fatal("the scalable target Harbormaster registered is still there")
	}
	if got := h.desired(t); got != 2 {
		t.Fatalf("desired = %d, want the saved 2", got)
	}
	if _, err := h.store.GetOffHoursEnrolment(t.Context(), paymentsKey); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("enrolment still stored: %v", err)
	}
	if got := h.lastOperationText(t); got != `Left off-hours schedule "staging nights": restored desired count 2.` {
		t.Fatalf("operation = %q", got)
	}
}

func TestApplier_ReleaseLeavesForeignActions(t *testing.T) {
	h := newApplierHarness(t)
	e := h.enrol(t)
	h.sleep(t, &e)
	h.release(t, e)

	actions := h.allActions()
	if len(actions) != 1 || actions["nightly-batch"].Name == "" {
		t.Fatalf("actions = %v, want only nightly-batch", actions)
	}
	if min, max, _ := h.target(); min != 2 || max != 6 {
		t.Fatalf("range = %d/%d, want the saved 2/6", min, max)
	}
	if got := h.lastOperationText(t); got != `Left off-hours schedule "staging nights": restored min 2, max 6.` {
		t.Fatalf("operation = %q", got)
	}
}

func TestApplier_AccessDeniedIsTyped(t *testing.T) {
	h := newApplierHarness(t)
	e := h.enrol(t)
	h.fake.DenyWrites(testAccountID)
	err := h.inTx(t, func(tx store.Tx) error { return h.applier.Sleep(t.Context(), tx, &e, nil, h.schedule.Name) })
	if !errors.Is(err, offhours.ErrAccessDenied) {
		t.Fatalf("Sleep = %v, want ErrAccessDenied", err)
	}
}

func TestApplier_WritesOperationForEachChange(t *testing.T) {
	h := newApplierHarness(t)
	e := h.enrol(t)
	h.sleep(t, &e)
	if _, err := h.wakeFor(t, &e); err != nil {
		t.Fatal(err)
	}
	if err := h.putBack(t, &e); err != nil {
		t.Fatal(err)
	}
	if err := h.inTx(t, func(tx store.Tx) error { return h.applier.Wake(t.Context(), tx, &e, nil, h.schedule.Name) }); err != nil {
		t.Fatal(err)
	}
	h.release(t, e)

	want := []string{"enrol", "sleep", "wake_for", "sleep_again", "wake", "release"}
	ops := h.operations(t)
	if len(ops) != len(want) {
		t.Fatalf("%d operations, want %d: %+v", len(ops), len(want), ops)
	}
	for i, op := range ops {
		if op.Source != "harbormaster" || op.Details["op"] != "offhours" || op.Details["action"] != want[i] ||
			op.Details["schedule_id"] != float64(h.schedule.ID) {
			t.Fatalf("operation %d = %+v, want action %s of schedule %d", i, op, want[i], h.schedule.ID)
		}
	}
	if ops[2].Actor != "sam" || ops[1].Actor != "" {
		t.Fatalf("actors = %q (sleep), %q (wake_for), want the reconciler and sam", ops[1].Actor, ops[2].Actor)
	}
}
