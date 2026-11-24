// Package agentrun is the life of one paid agent run, shared by everything that starts one: what its events mean
// (Turn), what it cost (Meter), how it is ended (Finisher), and the repair of runs whose end never happened
// (Reconciler). It knows nothing of HTTP or browsers.
package agentrun

import (
	"context"
	"fmt"
	"runtime/debug"
	"time"
)

// Limit is how long something may take: Normal from its start, and no more than Draining once the server has begun
// to shut down, also when that happens while it is running.
type Limit struct{ Normal, Draining time.Duration }

// abandonGrace is how long a call is given to notice that its context has ended before it is left behind.
const abandonGrace = 100 * time.Millisecond

// PanicError is a panic that was turned into an error. It names the type only: a panic's value could carry text
// that must not be logged.
type PanicError struct {
	Type  string
	Stack string
}

func (p *PanicError) Error() string { return "panic: " + p.Type }

// NewPanicError wraps what recover returned.
func NewPanicError(rec any) *PanicError {
	return &PanicError{Type: fmt.Sprintf("%T", rec), Stack: string(debug.Stack())}
}

// Within calls fn with a context that ends at limit and returns what it returned, or an error soon after that
// context ends. A panic in fn becomes a PanicError. A call that ignores its context is left behind, so fn must hand
// everything back through its result and share nothing with the caller.
func Within[T any](ctx context.Context, limit Limit, draining <-chan struct{}, fn func(ctx context.Context) (T, error)) (T, error) {
	ctx, cancel := context.WithTimeout(ctx, limit.Normal)
	defer cancel()
	type result struct {
		value T
		err   error
	}
	results := make(chan result, 1)
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				results <- result{err: NewPanicError(rec)}
			}
		}()
		value, err := fn(ctx)
		results <- result{value, err}
	}()
	var drained <-chan time.Time
	for {
		select {
		case r := <-results:
			return r.value, r.err
		case <-draining:
			draining = nil
			timer := time.NewTimer(limit.Draining)
			defer timer.Stop()
			drained = timer.C
		case <-drained:
			cancel()
		case <-ctx.Done():
			select {
			case r := <-results:
				return r.value, r.err
			case <-time.After(abandonGrace):
				var none T
				return none, fmt.Errorf("left behind: %w", ctx.Err())
			}
		}
	}
}
