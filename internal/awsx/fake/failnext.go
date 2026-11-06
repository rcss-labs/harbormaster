package fake

import (
	"os"
	"strings"
	"sync"

	"github.com/aws/smithy-go"
)

// failureQueue holds errors injected with FailNext, per operation name, and the errors that
// FailAlways returns on every call.
type failureQueue struct {
	mu      sync.Mutex
	pending map[string][]error
	always  map[string]error
}

// FailNext makes the next call of the named operation (for example
// "RegisterTaskDefinition") return err. Calls queue: each FailNext covers one call.
func (f *Fake) FailNext(method string, err error) {
	f.failures.mu.Lock()
	defer f.failures.mu.Unlock()
	if f.failures.pending == nil {
		f.failures.pending = map[string][]error{}
	}
	f.failures.pending[method] = append(f.failures.pending[method], err)
}

// FailAlways makes every call of the named operation return err; a nil err stops the failure.
// Queued FailNext errors are served first.
func (f *Fake) FailAlways(method string, err error) {
	f.failures.mu.Lock()
	defer f.failures.mu.Unlock()
	if err == nil {
		delete(f.failures.always, method)
		return
	}
	if f.failures.always == nil {
		f.failures.always = map[string]error{}
	}
	f.failures.always[method] = err
}

// takeFailure pops the next injected error for op, then falls back to a FailAlways error, or nil.
func (f *Fake) takeFailure(op string) error {
	f.failures.mu.Lock()
	defer f.failures.mu.Unlock()
	queue := f.failures.pending[op]
	if len(queue) == 0 {
		return f.failures.always[op]
	}
	f.failures.pending[op] = queue[1:]
	return queue[0]
}

// failEnv makes operations fail on every call (plan Decision 17): a comma list of
// scheduled_actions, scaling_tier (both fail DescribeScheduledActions, so the scaling poll tier
// fails too) and target_health (DescribeTargetHealth). Unset means no injection.
const failEnv = "HM_FAKE_FAIL"

var failEnvOps = map[string]string{
	"scheduled_actions": "DescribeScheduledActions",
	"scaling_tier":      "DescribeScheduledActions",
	"target_health":     "DescribeTargetHealth",
}

// failFromEnv registers a FailAlways error for each operation named in HM_FAKE_FAIL.
// Unknown names are ignored.
func (f *Fake) failFromEnv() {
	for _, name := range strings.Split(os.Getenv(failEnv), ",") {
		if op, ok := failEnvOps[strings.TrimSpace(name)]; ok {
			f.FailAlways(op, &smithy.GenericAPIError{
				Code: "UnsupportedOperation", Message: op + " is not supported (injected by " + failEnv + ")", Fault: smithy.FaultClient})
		}
	}
}
