package agentrun

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// fakeOps records what ran; behave decides how the named step goes.
type fakeOps struct {
	mu     sync.Mutex
	order  []string // a step's name when it ended well
	starts map[string]time.Time
	calls  map[string]int
	saved  []bool // the saved flags Audit was called with
	behave map[string]func(ctx context.Context, call int) error
}

func newFakeOps() *fakeOps {
	return &fakeOps{starts: map[string]time.Time{}, calls: map[string]int{}, behave: map[string]func(context.Context, int) error{}}
}

func (f *fakeOps) step(ctx context.Context, name string) error {
	f.mu.Lock()
	f.calls[name]++
	call := f.calls[name]
	if call == 1 {
		f.starts[name] = time.Now()
	}
	fn := f.behave[name]
	f.mu.Unlock()
	if fn != nil {
		if err := fn(ctx, call); err != nil {
			return err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.order = append(f.order, name)
	return nil
}

func (f *fakeOps) DeleteToken(ctx context.Context) error   { return f.step(ctx, "token") }
func (f *fakeOps) Settle(ctx context.Context) error        { return f.step(ctx, "settle") }
func (f *fakeOps) Save(ctx context.Context) (int64, error) { return 42, f.step(ctx, "save") }
func (f *fakeOps) ReleaseSlot()                            { _ = f.step(context.Background(), "slot") }
func (f *fakeOps) Audit(ctx context.Context, saved bool) error {
	f.mu.Lock()
	f.saved = append(f.saved, saved)
	f.mu.Unlock()
	return f.step(ctx, "audit")
}

var stepNames = []string{"token", "settle", "save", "slot", "audit"}

func (f *fakeOps) ended() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.order)
}

// Each step in turn fails, hangs or panics, on an ordinary finish, one that starts while draining, and one that the
// drain catches in the middle: every other step still runs, and the finish ends in time.
func TestFinisher_EveryStepEveryWay(t *testing.T) {
	ways := map[string]func(release chan struct{}) func(context.Context, int) error{
		"ok": func(chan struct{}) func(context.Context, int) error { return nil },
		"error": func(chan struct{}) func(context.Context, int) error {
			return func(context.Context, int) error { return errors.New("no") }
		},
		"panic": func(chan struct{}) func(context.Context, int) error {
			return func(context.Context, int) error { panic("boom") }
		},
		"hang": func(chan struct{}) func(context.Context, int) error {
			return func(ctx context.Context, _ int) error { return hang(ctx) }
		},
		"ignore": func(r chan struct{}) func(context.Context, int) error {
			return func(context.Context, int) error { <-r; return errors.New("late") }
		},
	}
	limits := map[string]time.Duration{"normal": 35 * time.Second, "draining": 6 * time.Second, "drained midway": 4*time.Second + 6*time.Second}
	for _, bad := range stepNames {
		for way, build := range ways {
			if bad == "slot" && way == "hang" {
				continue // the slot takes no context; "ignore" covers it
			}
			for mode, limit := range limits {
				t.Run(fmt.Sprintf("%s/%s/%s", bad, way, mode), func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						release := make(chan struct{})
						defer close(release)
						draining := make(chan struct{})
						switch mode {
						case "draining":
							close(draining)
						case "drained midway":
							time.AfterFunc(4*time.Second, func() { close(draining) })
						}
						ops := newFakeOps()
						ops.behave[bad] = build(release)
						var logged []string
						f := Finisher{Ops: ops, Draining: draining, Log: func(step string, err error) { logged = append(logged, step) }}
						start := time.Now()
						res := f.Finish(t.Context())
						if took := time.Since(start); took > limit {
							t.Errorf("took %v, over %v", took, limit)
						}
						if mode == "drained midway" {
							time.Sleep(10 * time.Second) // let the drain timer fire inside the bubble
						}
						got := ops.ended()
						for _, name := range stepNames {
							if (name != bad || way == "ok") && !slices.Contains(got, name) {
								t.Errorf("%s did not run: %v", name, got)
							}
						}
						if saveBad := bad == "save" && way != "ok"; (res.SaveErr != nil) != saveBad || (res.MessageID == 42) == saveBad {
							t.Errorf("save result = %d, %v", res.MessageID, res.SaveErr)
						}
						if want := !(bad == "save" && way != "ok"); len(ops.saved) != 1 || ops.saved[0] != want {
							t.Errorf("audit was told saved=%v, want %v once", ops.saved, want)
						}
						if bad != "slot" && way != "ok" && len(logged) == 0 {
							t.Error("the failed step was not logged")
						}
						if way == "ok" && len(logged) != 0 {
							t.Errorf("logged %v with nothing wrong", logged)
						}
					})
				})
			}
		}
	}
}

func TestFinisher_OrderAndParallel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ops := newFakeOps()
		slow := func(d time.Duration) func(context.Context, int) error {
			return func(context.Context, int) error { time.Sleep(d); return nil }
		}
		ops.behave["token"], ops.behave["settle"], ops.behave["save"] = slow(3*time.Second), slow(2*time.Second), slow(9*time.Second)
		start := time.Now()
		res := (&Finisher{Ops: ops, Log: func(step string, err error) { t.Errorf("%s: %v", step, err) }}).Finish(t.Context())
		if got := ops.ended(); !slices.Equal(got, []string{"settle", "token", "save", "slot", "audit"}) {
			t.Errorf("order = %v", got)
		}
		for _, name := range []string{"token", "settle", "save"} {
			if !ops.starts[name].Equal(start) {
				t.Errorf("%s started %v in, want at once", name, ops.starts[name].Sub(start))
			}
		}
		if took := time.Since(start); took != 9*time.Second || res.SaveErr != nil || res.MessageID != 42 {
			t.Errorf("took %v, result %+v", took, res)
		}
		if ops.calls["settle"] != 1 {
			t.Errorf("%d settle calls", ops.calls["settle"])
		}
	})
}

// A settle that failed is tried once more before the audit row, which needs a settled run.
func TestFinisher_SettleIsRetriedOnceBeforeTheAudit(t *testing.T) {
	ops := newFakeOps()
	ops.behave["settle"] = func(_ context.Context, call int) error {
		if call == 1 {
			return errors.New("no")
		}
		return nil
	}
	(&Finisher{Ops: ops, Log: func(string, error) {}}).Finish(t.Context())
	if ops.calls["settle"] != 2 || !slices.Equal(ops.ended()[len(ops.ended())-3:], []string{"slot", "settle", "audit"}) {
		t.Errorf("settle calls %d, order %v", ops.calls["settle"], ops.ended())
	}
}

// A caller whose request is gone does not take the finish with it.
func TestFinisher_RunsOnADeadContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	ops := newFakeOps()
	for _, name := range []string{"token", "settle", "save", "audit"} {
		ops.behave[name] = func(ctx context.Context, _ int) error { return ctx.Err() }
	}
	(&Finisher{Ops: ops, Log: func(step string, err error) { t.Errorf("%s: %v", step, err) }}).Finish(ctx)
	if got := ops.ended(); len(got) != 5 {
		t.Errorf("ran %v", got)
	}
}
