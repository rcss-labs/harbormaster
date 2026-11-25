package investigate

import (
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Whatever goes wrong while the job works out what a finished run means, the run is settled once and the
// investigation ends: it is not left running for a second paid run.
func TestJob_AMappingThatPanicsEndsTheInvestigationFailed(t *testing.T) {
	f := newFixture(t)
	id := f.request("checkout-api")
	var armed, fired atomic.Bool
	f.agent.set(func(c *call) {
		armed.Store(true)
		c.w.WriteHeader(http.StatusTooManyRequests) // the mapping of a busy agent reads the clock
	})
	f.runner.Now = func() time.Time {
		if armed.Load() {
			pcs := make([]uintptr, 16)
			frames := runtime.CallersFrames(pcs[:runtime.Callers(2, pcs)])
			for fr, more := frames.Next(); ; fr, more = frames.Next() {
				if strings.HasSuffix(fr.Function, "(*job).busy") && fired.CompareAndSwap(false, true) {
					panic("PANIC-SENTINEL")
				}
				if !more {
					break
				}
			}
		}
		return f.clock.now()
	}
	f.want(f.runTo(id, "failed"), "failed", "error")
	f.settled("failed 0")
	if f.agent.count() != 1 || !fired.Load() {
		t.Errorf("%d agent calls, mapping panicked: %v; want 1 and true", f.agent.count(), fired.Load())
	}
	if strings.Contains(f.logs.String(), "PANIC-SENTINEL") {
		t.Error("the panic's value was logged")
	}
}

// The driver's own log lines name the investigation.
func TestJob_TheDriversLogLinesCarryTheInvestigationID(t *testing.T) {
	f := newFixture(t)
	id := f.request("checkout-api")
	f.agent.catalogueStatus = http.StatusServiceUnavailable
	stop := f.start(f.runner)
	f.wait("the catalogue to be asked for", func() bool { return strings.Contains(f.logs.String(), "agentjob: agent catalogue unavailable") })
	stop()
	for _, line := range strings.Split(strings.TrimSpace(f.logs.String()), "\n") {
		if strings.Contains(line, "agentjob: agent catalogue unavailable") && !strings.Contains(line, fmt.Sprintf(`"investigation_id":%d`, id)) {
			t.Errorf("a line of the driver has no investigation id: %s", line)
		}
	}
	f.agent.mu.Lock()
	f.agent.catalogueStatus = http.StatusOK
	f.agent.mu.Unlock()
	f.clock.advance(2 * time.Minute)
	f.runTo(id, "done")
	for _, line := range strings.Split(strings.TrimSpace(f.logs.String()), "\n") {
		if strings.Contains(line, `"msg":"agent run finished"`) && !strings.Contains(line, fmt.Sprintf(`"investigation_id":%d`, id)) {
			t.Errorf("a line of the driver's run has no investigation id: %s", line)
		}
	}
}
