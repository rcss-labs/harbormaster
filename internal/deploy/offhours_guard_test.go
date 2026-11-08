package deploy_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/deploy"
	"github.com/rcss-labs/harbormaster/internal/offhours"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// enrolPayments puts payments-api on a schedule named "always" whose grid is all awake or all asleep, and makes the
// service use the real off-hours guard.
func enrolPayments(t *testing.T, h *harness, awakeAllWeek, registeredTarget bool) {
	enrolPaymentsInState(t, h, awakeAllWeek, registeredTarget, "active")
}

func enrolPaymentsInState(t *testing.T, h *harness, awakeAllWeek, registeredTarget bool, state string) {
	t.Helper()
	grid := strings.Repeat("0", 168)
	if awakeAllWeek {
		grid = strings.Repeat("1", 168)
	}
	sc, err := h.store.CreateOffHoursSchedule(t.Context(), store.OffHoursSchedule{
		Name: "always", Enabled: true, TagKey: "env", TagValue: "prod", Timezone: "UTC", Awake: grid}, h.sam.UserID)
	if err != nil {
		t.Fatal(err)
	}
	err = h.store.InTx(t.Context(), func(tx store.Tx) error {
		return h.store.UpsertOffHoursEnrolment(t.Context(), tx, store.OffHoursEnrolment{
			Key: paymentsKey, ScheduleID: &sc.ID, SavedMin: 1, SavedMax: 4, RegisteredTarget: registeredTarget, State: state})
	})
	if err != nil {
		t.Fatal(err)
	}
	h.svc.Guard = &offhours.Guard{Store: h.store, Now: h.svc.Now}
}

func wantOffHoursConflict(t *testing.T, err error, reason string) {
	t.Helper()
	var conflict *offhours.ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("err = %v, want *offhours.ConflictError", err)
	}
	if want := `payments-api is on the off-hours schedule "always". ` + reason; conflict.Message != want {
		t.Fatalf("message = %q, want %q", conflict.Message, want)
	}
}

func TestChangeSet_RefusesOffHoursActionEdit(t *testing.T) {
	h := newHarness(t)
	enrolPayments(t, h, true, false)
	before := writeCalls(h)
	for name, scaling := range map[string]deploy.ScalingChange{
		"put": {PutScheduledActions: []deploy.ScheduledActionChange{
			{Name: "harbormaster-offhours-sleep-20", Schedule: "rate(1 hour)", Timezone: "UTC", MinCapacity: ptr(0)}}},
		"delete": {DeleteScheduledActions: []string{"harbormaster-offhours-wake-07"}},
	} {
		cs := baseChangeSet(t, h, paymentsKey)
		cs.Scaling = &scaling
		_, err := h.svc.ApplyChangeSet(t.Context(), h.sam, paymentsKey, cs)
		t.Run(name, func(t *testing.T) {
			wantOffHoursConflict(t, err, "Scheduled actions named harbormaster-offhours-… belong to the schedule.")
		})
	}
	assertNoNewWrites(t, h, before)
}

func TestChangeSet_RefusesRemoveTargetAndRangeWhileAsleep(t *testing.T) {
	h := newHarness(t)
	enrolPayments(t, h, false, false)
	before := writeCalls(h)
	cs := baseChangeSet(t, h, paymentsKey)
	cs.Scaling = &deploy.ScalingChange{RemoveTarget: true}
	_, err := h.svc.ApplyChangeSet(t.Context(), h.sam, paymentsKey, cs)
	wantOffHoursConflict(t, err, "Its task count is held by the schedule.")

	cs = baseChangeSet(t, h, paymentsKey)
	cs.Scaling = &deploy.ScalingChange{Target: &deploy.ScalingTargetChange{Min: 1, Max: 5}}
	_, err = h.svc.ApplyChangeSet(t.Context(), h.sam, paymentsKey, cs)
	wantOffHoursConflict(t, err, "It is asleep: wake it first.")
	assertNoNewWrites(t, h, before)
}

func TestSetDesiredCount_RefusedForEnrolledService(t *testing.T) {
	h := newHarness(t)
	enrolPayments(t, h, false, false)
	before := writeCalls(h)
	_, err := h.svc.SetDesiredCount(t.Context(), h.sam, paymentsKey, 5)
	wantOffHoursConflict(t, err, "It is asleep: wake it first.")
	assertNoNewWrites(t, h, before)
}

func TestChangeSet_RangeEditAllowedWhileAwake(t *testing.T) {
	h := newHarness(t)
	enrolPayments(t, h, true, false)
	cs := baseChangeSet(t, h, paymentsKey)
	cs.Scaling = &deploy.ScalingChange{Target: &deploy.ScalingTargetChange{Min: 2, Max: 6}}
	if _, err := h.svc.ApplyChangeSet(t.Context(), h.sam, paymentsKey, cs); err != nil {
		t.Fatalf("range edit while awake: %v", err)
	}
}

func TestChangeSet_RefusesServiceDesiredCountWhenBlocked(t *testing.T) {
	h := newHarness(t)
	enrolPaymentsInState(t, h, true, false, "blocked")
	before := writeCalls(h)
	cs := baseChangeSet(t, h, paymentsKey)
	cs.Service = &deploy.ServiceChange{DesiredCount: ptr(5)}
	_, err := h.svc.ApplyChangeSet(t.Context(), h.sam, paymentsKey, cs)
	wantOffHoursConflict(t, err, "Its task count is held by the schedule.")
	assertNoNewWrites(t, h, before)
}
