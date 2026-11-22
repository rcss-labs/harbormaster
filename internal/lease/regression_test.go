package lease

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
	"github.com/rcss-labs/harbormaster/internal/telemetry"
)

// A blocked claim holds its lease back for two ticks, so a lone claimant retries after claim deadline + 2 ticks + a tick,
// not longer (the backoff was four ticks before).
func TestManager_BlockedClaimBacksOffTwoTicks(t *testing.T) {
	m := &Manager{Timing: testTiming(), Log: discardLogger()}
	k := sliceKeyOf("acct00")
	slow := 0
	m.noteClaim(k, 2*slowClaim, false, &slow)

	if !m.backedOff(k) {
		t.Fatal("a blocked claim did not back its lease off")
	}
	ticks := float64(time.Until(m.claimBackoff[k])) / float64(m.Timing.Tick)
	if ticks < 1.5 || ticks > 2.05 {
		t.Fatalf("backoff = %.2f ticks, want 2", ticks)
	}
	// A claim that was fast or succeeded leaves no backoff.
	other := sliceKeyOf("acct01")
	m.noteClaim(other, 2*slowClaim, true, &slow)
	m.noteClaim(other, time.Millisecond, false, &slow)
	if m.backedOff(other) {
		t.Fatal("a fast or successful claim backed off")
	}
}

// stopAll gives a lease back only when it is still ours: one a renewal found taken has nothing to release, and counting
// a release for it would double-report the loss.
func TestManager_StopAllReleasesOnlyLeasesStillHeld(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	reg := telemetry.New()
	m := &Manager{Store: st, ID: uuid.New(), Timing: testTiming(), Telemetry: reg, Log: discardLogger(), held: map[store.LeaseKey]*holding{}}

	add := func(name string, lost bool) {
		k := sliceKeyOf(name)
		ensureAccounts(st, awsx.Target{AccountID: name, Region: "r"})
		if err := st.EnsureSliceLease(ctx, awsx.Target{AccountID: name, Region: "r"}); err != nil {
			t.Fatal(err)
		}
		l, ok, err := st.ClaimLease(ctx, k, m.ID, time.Minute)
		if err != nil || !ok {
			t.Fatalf("claim %s: %v %v", name, ok, err)
		}
		done := make(chan struct{})
		close(done)
		m.held[k] = &holding{lease: l, cancel: func() {}, done: done, lost: lost}
	}
	add("held", false)
	add("lost", true)

	m.stopAll(ctx, "test")

	text := metricsText(t, reg)
	if !hasLine(text, `harbormaster_lease_events_total{kind="slice",event="released"} 1`) {
		t.Fatalf("want exactly one released event (the held lease), got:\n%s", text)
	}
	if len(m.held) != 0 {
		t.Fatalf("stopAll left %d holders", len(m.held))
	}
}
