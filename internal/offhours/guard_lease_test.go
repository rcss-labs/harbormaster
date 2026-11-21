package offhours_test

import (
	"context"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/store"
)

// A coordinator that lost its lease changes nothing in AWS or the database, whether it would enrol a service (which
// puts it to sleep) or release one.
func TestReconciler_GuardBlocksChangesAfterLeaseLoss(t *testing.T) {
	h := newReconcilerHarness(t) // Saturday 23:00: a new enrolment goes straight to sleep
	h.rec.Guard = func(context.Context) error { return store.ErrLeaseLost }

	h.cycle(t)

	h.noEnrolment(t)
	h.requireRange(t, 2, 6)
	h.requireActions(t, "nightly-batch")
	if ops := h.operations(t); len(ops) != 0 {
		t.Fatalf("operations = %+v, want none", ops)
	}

	// The lease holder does it.
	h.rec.Guard = func(context.Context) error { return nil }
	h.cycle(t)
	h.requireRange(t, 0, 0)
}

func TestReconciler_GuardBlocksReleaseAfterLeaseLoss(t *testing.T) {
	h := newReconcilerHarness(t)
	h.at(9, 10, 0)
	h.cycle(t) // enrolled and awake
	h.requireActions(t, "nightly-batch", "harbormaster-offhours-wake-07", "harbormaster-offhours-sleep-20")

	h.tagService(t, paymentsKey, `[{"Key":"env","Value":"dev"}]`, "ACTIVE") // no longer selected: would be released
	h.rec.Guard = func(context.Context) error { return store.ErrLeaseLost }
	h.cycle(t)

	if e := h.enrolment(t); e.State != "active" {
		t.Fatalf("enrolment = %+v, want it untouched", e)
	}
	h.requireActions(t, "nightly-batch", "harbormaster-offhours-wake-07", "harbormaster-offhours-sleep-20")

	h.rec.Guard = nil
	h.cycle(t)
	h.noEnrolment(t)
	h.requireActions(t, "nightly-batch")
}
