package poller

import (
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

// A poll-now that reaches a loop whose focus scheduler has stopped must start no cycle: it would race the scheduler's
// Wait on the cycle WaitGroup and run on a cancelled context.
func TestFocusManager_PollNowAfterStopStartsNoCycle(t *testing.T) {
	r := newFocusRig(t, mockupEstate(), scaledFocusConfig(100*time.Millisecond, 30*time.Second), time.Hour)
	r.cancel()
	time.Sleep(300 * time.Millisecond) // runFocus passes its Wait
	before := r.focusCalls("DescribeServices")
	if got := r.loop.pollNow(paymentsObject(), time.Now()); got != "dropped" {
		t.Fatalf("poll-now after stop = %q, want dropped", got)
	}
	time.Sleep(200 * time.Millisecond)
	if got := r.focusCalls("DescribeServices"); got != before {
		t.Fatalf("a cycle ran after the stop: %d calls, had %d", got, before)
	}
	r.loop.focusMu.Lock()
	running := r.loop.onceRunning
	r.loop.focusMu.Unlock()
	if running != 0 {
		t.Fatalf("%d one-off cycles started after the stop", running)
	}
}

// Before Run or runFocus has set the loop's context there is nothing to run a one-off under; it must not fall back to
// context.Background().
func TestFocusManager_PollNowBeforeStartStartsNoCycle(t *testing.T) {
	st := storetest.Open(t)
	p := New(st, fake.New(mockupEstate()), focusConfig(), discardLogger())
	l := newLoop(p, loopTarget, testWriter(t, st, loopTarget))
	if got := l.pollNow(paymentsObject(), time.Now()); got != "dropped" {
		t.Fatalf("poll-now before the scheduler started = %q, want dropped", got)
	}
	l.focusMu.Lock()
	defer l.focusMu.Unlock()
	if l.onceRunning != 0 {
		t.Fatalf("%d one-off cycles started with no scheduler", l.onceRunning)
	}
}
