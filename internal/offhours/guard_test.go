package offhours_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/offhours"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

type guardHarness struct {
	guard *offhours.Guard
	store *store.Store
	now   time.Time
	// disabled creates the schedule switched off.
	disabled bool
}

// newGuardHarness builds a Guard over a Mon-Fri 07-20 Europe/Berlin schedule named "staging nights". The clock is
// Monday 2026-11-09 10:00 Berlin (awake). Nothing is enrolled yet.
func newGuardHarness(t *testing.T) *guardHarness {
	t.Helper()
	st := storetest.Open(t)
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	h := &guardHarness{store: st, now: time.Date(2026, 11, 9, 10, 0, 0, 0, berlin)}
	h.guard = &offhours.Guard{Store: st, Now: func() time.Time { return h.now }}
	return h
}

// enrol creates the schedule on first use and enrols paymentsKey with the given state.
func (h *guardHarness) enrol(t *testing.T, state string, registeredTarget bool, wokenUntil *time.Time) {
	t.Helper()
	ctx := t.Context()
	user, err := h.store.CreateUser(ctx, "sam", "hash", true, false)
	if err != nil {
		t.Fatal(err)
	}
	awake := []byte(strings.Repeat("0", 168))
	for day := 0; day < 5; day++ {
		for hour := 7; hour < 20; hour++ {
			awake[day*24+hour] = '1'
		}
	}
	sc, err := h.store.CreateOffHoursSchedule(ctx, store.OffHoursSchedule{
		Name: "staging nights", Enabled: !h.disabled, Selectors: []store.OffHoursSelector{{Kind: "aws", Key: "env", Value: "staging"}}, Timezone: "Europe/Berlin", Awake: string(awake)}, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	err = h.store.InTx(ctx, func(tx store.Tx) error {
		return h.store.UpsertOffHoursEnrolment(ctx, tx, store.OffHoursEnrolment{
			Key: paymentsKey, ScheduleID: &sc.ID, SavedMin: 2, SavedMax: 6, RegisteredTarget: registeredTarget,
			State: state, WokenUntil: wokenUntil})
	})
	if err != nil {
		t.Fatal(err)
	}
}

func wantConflict(t *testing.T, err error, wantSuffix string) {
	t.Helper()
	var conflict *offhours.ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("err = %v, want *ConflictError", err)
	}
	want := `payments-api is on the off-hours schedule "staging nights". ` + wantSuffix
	if conflict.Message != want {
		t.Fatalf("message = %q, want %q", conflict.Message, want)
	}
}

func TestGuard_DesiredCountBlockedWhileAsleep(t *testing.T) {
	h := newGuardHarness(t)
	h.enrol(t, "active", false, nil)
	h.now = h.now.Add(12 * time.Hour) // 22:00, asleep
	wantConflict(t, h.guard.DesiredCount(t.Context(), paymentsKey), "It is asleep: wake it first.")
}

func TestGuard_DesiredCountBlockedForOwnTarget(t *testing.T) {
	h := newGuardHarness(t)
	h.enrol(t, "active", true, nil)
	wantConflict(t, h.guard.DesiredCount(t.Context(), paymentsKey), "Its task count is held by the schedule.")
}

func TestGuard_DesiredCountBlockedWhenBlocked(t *testing.T) {
	h := newGuardHarness(t)
	h.enrol(t, "blocked", false, nil)
	wantConflict(t, h.guard.DesiredCount(t.Context(), paymentsKey), "Its task count is held by the schedule.")
}

func TestGuard_DesiredCountAllowedWhenAwakeWithOwnRange(t *testing.T) {
	h := newGuardHarness(t)
	h.enrol(t, "active", false, nil)
	if err := h.guard.DesiredCount(t.Context(), paymentsKey); err != nil {
		t.Fatalf("awake with the service's own range: %v", err)
	}
}

func TestGuard_ScalingRefusesOwnedActionNames(t *testing.T) {
	h := newGuardHarness(t)
	h.enrol(t, "active", false, nil)
	wantConflict(t, h.guard.Scaling(t.Context(), paymentsKey, false, false, []string{"nightly-batch", "harbormaster-offhours-sleep-20"}),
		"Scheduled actions named harbormaster-offhours-… belong to the schedule.")
	if err := h.guard.Scaling(t.Context(), paymentsKey, false, false, []string{"nightly-batch"}); err != nil {
		t.Fatalf("foreign action: %v", err)
	}
}

func TestGuard_ScalingRefusesRemoveTarget(t *testing.T) {
	h := newGuardHarness(t)
	h.enrol(t, "active", false, nil)
	wantConflict(t, h.guard.Scaling(t.Context(), paymentsKey, false, true, nil), "Its task count is held by the schedule.")
}

func TestGuard_ScalingRangeOnlyRefusedWhileAsleep(t *testing.T) {
	h := newGuardHarness(t)
	h.enrol(t, "active", false, nil)
	if err := h.guard.Scaling(t.Context(), paymentsKey, true, false, nil); err != nil {
		t.Fatalf("range edit while awake: %v", err)
	}
	h.now = h.now.Add(12 * time.Hour)
	wantConflict(t, h.guard.Scaling(t.Context(), paymentsKey, true, false, nil), "It is asleep: wake it first.")
}

func TestGuard_NotEnrolledAllowsAll(t *testing.T) {
	h := newGuardHarness(t)
	ctx := t.Context()
	if err := h.guard.DesiredCount(ctx, paymentsKey); err != nil {
		t.Fatalf("DesiredCount: %v", err)
	}
	if err := h.guard.Scaling(ctx, paymentsKey, true, true, []string{"harbormaster-offhours-wake-07"}); err != nil {
		t.Fatalf("Scaling: %v", err)
	}
}

func TestGuard_BlockedAndAsleepReportsHeld(t *testing.T) {
	h := newGuardHarness(t)
	h.enrol(t, "blocked", false, nil)
	h.now = h.now.Add(12 * time.Hour) // asleep too, but a blocked service cannot be woken
	wantConflict(t, h.guard.DesiredCount(t.Context(), paymentsKey), "Its task count is held by the schedule.")
}

func TestGuard_RemoveTargetWithOwnedActionReportsHeld(t *testing.T) {
	h := newGuardHarness(t)
	h.enrol(t, "active", false, nil)
	wantConflict(t, h.guard.Scaling(t.Context(), paymentsKey, false, true, []string{"harbormaster-offhours-wake-07"}),
		"Its task count is held by the schedule.")
}

func TestGuard_DisabledScheduleIsNotEnrolled(t *testing.T) {
	h := newGuardHarness(t)
	h.disabled = true
	h.enrol(t, "active", true, nil)
	h.now = h.now.Add(12 * time.Hour)
	if err := h.guard.DesiredCount(t.Context(), paymentsKey); err != nil {
		t.Fatalf("DesiredCount: %v", err)
	}
	if err := h.guard.Scaling(t.Context(), paymentsKey, true, true, []string{"harbormaster-offhours-wake-07"}); err != nil {
		t.Fatalf("Scaling: %v", err)
	}
}
