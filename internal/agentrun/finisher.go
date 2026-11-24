package agentrun

import (
	"context"
	"sync"
	"time"
)

// The limits of the steps that end a run. Ordinarily a step may wait for a lock as long as the request would. While
// the server shuts down the whole of it (3 s for the three parallel steps, then 0.5, 0.75 and 0.75, and the grace
// after each) stays under 6 s, which leaves 2 s to send the last events inside the 8 s the shutdown can spare. What
// a step could not do in its time, the Reconciler does; an answer that could not be saved is lost.
var (
	parallelLimit    = Limit{Normal: 10 * time.Second, Draining: 3 * time.Second}
	slotLimit        = Limit{Normal: 2 * time.Second, Draining: 500 * time.Millisecond}
	settleRetryLimit = Limit{Normal: 10 * time.Second, Draining: 750 * time.Millisecond}
	auditLimit       = Limit{Normal: 10 * time.Second, Draining: 750 * time.Millisecond}
)

// Operations are the things the end of a run must do. Each may be called after another failed, hung or panicked.
type Operations interface {
	// DeleteToken deletes the run token.
	DeleteToken(ctx context.Context) error
	// Settle stores the last usage and settles the run, both or neither.
	Settle(ctx context.Context) error
	// Save saves the answer and returns its message id; it does nothing when there is nothing to save.
	Save(ctx context.Context) (messageID int64, err error)
	// ReleaseSlot lets the user start another run.
	ReleaseSlot()
	// Audit writes the run's audit row; saved is false when Save failed. It refuses while the run is not settled.
	Audit(ctx context.Context, saved bool) error
}

// Result is what the one who asked must be told.
type Result struct {
	MessageID int64
	SaveErr   error
}

// Finisher ends a run. The token, the usage and settlement, and the answer do not depend on each other and go in
// parallel, so none waits for another's lock; then the slot is given back and the audit row written. A step that
// fails, hangs or panics never keeps the others from running.
type Finisher struct {
	Ops      Operations
	Draining <-chan struct{}
	Log      func(step string, err error) // called on the goroutine that called Finish
}

// Finish runs every step and returns once each has ended or been given up. It works on a ctx that has ended.
func (f *Finisher) Finish(ctx context.Context) Result {
	ctx = context.WithoutCancel(ctx)
	var (
		res                 Result
		tokenErr, settleErr error
		parallel            sync.WaitGroup
	)
	parallel.Go(func() { tokenErr = f.step(ctx, parallelLimit, f.Ops.DeleteToken) })
	parallel.Go(func() { settleErr = f.step(ctx, parallelLimit, f.Ops.Settle) })
	parallel.Go(func() { res.MessageID, res.SaveErr = Within(ctx, parallelLimit, f.Draining, f.Ops.Save) })
	parallel.Wait()
	f.report("delete the run token", tokenErr)
	f.report("settle the run", settleErr)
	f.report("save the answer", res.SaveErr)
	if res.SaveErr != nil {
		res.MessageID = 0
	}

	f.report("release the slot", f.step(ctx, slotLimit, func(context.Context) error {
		f.Ops.ReleaseSlot()
		return nil
	}))
	if settleErr != nil {
		f.report("settle the run again", f.step(ctx, settleRetryLimit, f.Ops.Settle))
	}
	f.report("write the audit row", f.step(ctx, auditLimit, func(ctx context.Context) error {
		return f.Ops.Audit(ctx, res.SaveErr == nil)
	}))
	return res
}

func (f *Finisher) step(ctx context.Context, limit Limit, fn func(ctx context.Context) error) error {
	_, err := Within(ctx, limit, f.Draining, func(ctx context.Context) (struct{}, error) { return struct{}{}, fn(ctx) })
	return err
}

func (f *Finisher) report(step string, err error) {
	if err != nil {
		f.Log(step, err)
	}
}
