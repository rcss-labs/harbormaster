package fake

import "sync"

// failureQueue holds errors injected with FailNext, per operation name.
type failureQueue struct {
	mu      sync.Mutex
	pending map[string][]error
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

// takeFailure pops the next injected error for op, or nil.
func (f *Fake) takeFailure(op string) error {
	f.failures.mu.Lock()
	defer f.failures.mu.Unlock()
	queue := f.failures.pending[op]
	if len(queue) == 0 {
		return nil
	}
	f.failures.pending[op] = queue[1:]
	return queue[0]
}
