package agentrun

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/store"
)

// fakeUsageStore keeps the last figures written; write, when set, decides what a write does.
type fakeUsageStore struct {
	mu     sync.Mutex
	writes []Usage
	write  func(ctx context.Context) error
}

func (f *fakeUsageStore) RecordAgentRunUsage(ctx context.Context, _ uuid.UUID, in, out, cost int64, _ time.Time) error {
	if f.write != nil {
		if err := f.write(ctx); err != nil {
			return err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = append(f.writes, Usage{in, out, cost})
	return nil
}

func hang(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }

func newMeter(st UsageStore, draining <-chan struct{}) *Meter {
	return &Meter{Store: st, RunID: uuid.New(), Now: time.Now, Draining: draining}
}

func TestMeter_KeepsTheLastFiguresAndFlushesWhatWasNotStored(t *testing.T) {
	st := &fakeUsageStore{}
	m := newMeter(st, nil)
	if _, ok := m.Last(); ok || m.Flush(t.Context()) != nil || len(st.writes) != 0 {
		t.Fatal("a meter that saw nothing wrote something")
	}
	m.See(Usage{5, 5, 1000})
	if err := m.Record(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := m.Flush(t.Context()); err != nil || len(st.writes) != 1 {
		t.Fatalf("stored figures were written again: %v %v", st.writes, err)
	}
	m.See(Usage{4, 4, 500}) // a correction may lower the figures
	if err := m.Flush(t.Context()); err != nil || len(st.writes) != 2 || st.writes[1] != (Usage{4, 4, 500}) {
		t.Fatalf("writes = %v, %v", st.writes, err)
	}
	if last, ok := m.Last(); !ok || last != (Usage{4, 4, 500}) {
		t.Errorf("last = %v", last)
	}
	m.See(Usage{-1, -2, -3})
	if last, _ := m.Last(); last != (Usage{}) {
		t.Errorf("negative figures were kept: %v", last)
	}
}

func TestMeter_AFailedOrPanickingWriteIsFlushedLater(t *testing.T) {
	for name, bad := range map[string]func(context.Context) error{
		"fails":  func(context.Context) error { return errors.New("no") },
		"panics": func(context.Context) error { panic("boom") },
	} {
		st := &fakeUsageStore{write: bad}
		m := newMeter(st, nil)
		m.See(Usage{1, 2, 777})
		if err := m.Record(t.Context()); err == nil {
			t.Errorf("%s: Record returned nil", name)
		}
		if err := m.Flush(t.Context()); err == nil {
			t.Errorf("%s: Flush returned nil", name)
		}
		st.write = nil
		if err := m.Flush(t.Context()); err != nil || len(st.writes) != 1 || st.writes[0].CostMicroUSD != 777 {
			t.Errorf("%s: writes = %v, %v", name, st.writes, err)
		}
	}
}

// The run was swept or settled by someone else: Record says so, and there is nothing left to flush.
func TestMeter_SentinelIsReportedByRecordAndDoneForFlush(t *testing.T) {
	st := &fakeUsageStore{write: func(context.Context) error { return store.ErrAgentRunNotRunning }}
	m := newMeter(st, nil)
	m.See(Usage{1, 1, 1})
	if err := m.Record(t.Context()); !errors.Is(err, store.ErrAgentRunNotRunning) {
		t.Errorf("Record = %v", err)
	}
	if err := m.Flush(t.Context()); err != nil {
		t.Errorf("Flush after the sentinel = %v", err)
	}
	m.See(Usage{2, 2, 2})
	if err := m.Flush(t.Context()); err != nil {
		t.Errorf("Flush meeting the sentinel = %v", err)
	}
}

func TestMeter_AHangingWriteIsBoundedAndDrainShortensItInFlight(t *testing.T) {
	stubborn := make(chan struct{})
	for name, write := range map[string]func(context.Context) error{
		"honours its context": hang,
		"ignores its context": func(context.Context) error { <-stubborn; return nil },
	} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				stubborn = make(chan struct{})
				defer close(stubborn)
				start := time.Now()
				m := newMeter(&fakeUsageStore{write: write}, nil)
				m.See(Usage{1, 1, 1})
				if err := m.Record(t.Context()); err == nil || time.Since(start) < 10*time.Second || time.Since(start) > 11*time.Second {
					t.Errorf("ordinary: %v after %v, want an error after 10s", err, time.Since(start))
				}

				draining := make(chan struct{})
				m = newMeter(&fakeUsageStore{write: write}, draining)
				m.See(Usage{1, 1, 1})
				start = time.Now()
				time.AfterFunc(3*time.Second, func() { close(draining) })
				if err := m.Record(t.Context()); err == nil || time.Since(start) > 4200*time.Millisecond || time.Since(start) < 4*time.Second {
					t.Errorf("drain in flight: %v after %v, want an error a second after the drain", err, time.Since(start))
				}
				start = time.Now()
				if err := m.Record(t.Context()); err == nil || time.Since(start) > 1200*time.Millisecond {
					t.Errorf("already draining: %v after %v", err, time.Since(start))
				}
			})
		})
	}
}

// Flush takes at most half of the time its caller has, so a write that hangs leaves time to settle the run.
func TestMeter_FlushLeavesHalfTheTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := newMeter(&fakeUsageStore{write: hang}, nil)
		m.See(Usage{1, 1, 1})
		ctx, cancel := context.WithTimeout(t.Context(), 6*time.Second)
		defer cancel()
		start := time.Now()
		if err := m.Flush(ctx); err == nil || time.Since(start) != 3*time.Second {
			t.Errorf("Flush = %v after %v, want an error after 3s", err, time.Since(start))
		}
	})
}
