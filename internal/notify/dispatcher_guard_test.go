package notify

import (
	"context"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// A coordinator that lost its lease while a send was about to happen must not send: the delivery goes back to the queue
// with no attempt counted, and whoever holds the lease sends it.
func TestDispatcher_GuardBlocksSendAfterLeaseLoss(t *testing.T) {
	e := newEnv(t)
	e.route("one")
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.d.Guard = func(context.Context) error { return store.ErrLeaseLost }

	e.cycle()

	if got := e.sender.records(); len(got) != 0 {
		t.Fatalf("sent %d messages without the lease: %+v", len(got), got)
	}
	rows := e.deliveries()
	if len(rows) != 1 {
		t.Fatalf("deliveries = %+v, want the one planned", rows)
	}
	if d := rows[0]; d.Status == "delivered" || d.Attempts != 0 || d.ClaimedUntil != nil && d.ClaimedUntil.After(e.clock) {
		t.Fatalf("delivery = %+v, want unsent, no attempt counted and not claimed", d)
	}

	// The new holder (a dispatcher whose guard passes) sends it.
	e.d.Guard = func(context.Context) error { return nil }
	e.cycle()
	if got := e.sender.count("one", EventOpened); got != 1 {
		t.Fatalf("opened sent %d times after the guard passed, want once", got)
	}
}

// A guard that cannot reach the database also stops the send: fail closed.
func TestDispatcher_GuardErrorAlsoBlocksSend(t *testing.T) {
	e := newEnv(t)
	e.route("one")
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.d.Guard = func(context.Context) error { return context.DeadlineExceeded }
	e.cycle()
	if got := e.sender.records(); len(got) != 0 {
		t.Fatalf("sent %d messages while the guard could not confirm the lease", len(got))
	}
}
