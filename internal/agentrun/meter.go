package agentrun

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/store"
)

// usageLimit bounds one usage write. The shutdown shortens it even when it is already waiting.
var usageLimit = Limit{Normal: 10 * time.Second, Draining: time.Second}

// UsageStore is where a run's cumulative figures are written.
type UsageStore interface {
	RecordAgentRunUsage(ctx context.Context, id uuid.UUID, inputTokens, outputTokens, costMicroUSD int64, now time.Time) error
}

// Meter holds the figures of one run: those last seen, which a failed write does not lose, and those last stored.
type Meter struct {
	Store    UsageStore
	RunID    uuid.UUID
	Now      func() time.Time
	Draining <-chan struct{}

	mu     sync.Mutex // a write that was left behind may still report what it stored
	seen   *Usage
	stored *Usage
}

// See remembers cumulative figures. Negative ones count as zero.
func (m *Meter) See(u Usage) {
	u = Usage{max(u.InputTokens, 0), max(u.OutputTokens, 0), max(u.CostMicroUSD, 0)}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seen = &u
}

// Last is the figures last seen, and whether there are any.
func (m *Meter) Last() (Usage, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.seen == nil {
		return Usage{}, false
	}
	return *m.seen, true
}

// Record stores the figures last seen. Any error means the run can no longer be billed reliably and must stop;
// store.ErrAgentRunNotRunning means that someone else has settled it. It is safe on a context that has ended.
func (m *Meter) Record(ctx context.Context) error {
	return m.write(context.WithoutCancel(ctx), usageLimit)
}

// Flush stores the figures last seen if they are not stored yet, in at most half the time ctx has left, so that the
// run can be settled even when this write hangs. A run that someone else settled has nothing left to flush.
func (m *Meter) Flush(ctx context.Context) error {
	m.mu.Lock()
	upToDate := m.seen == nil || (m.stored != nil && *m.stored == *m.seen)
	m.mu.Unlock()
	if upToDate {
		return nil
	}
	limit := usageLimit
	if deadline, ok := ctx.Deadline(); ok {
		limit.Normal = min(limit.Normal, time.Until(deadline)/2)
	}
	if err := m.write(ctx, limit); !errors.Is(err, store.ErrAgentRunNotRunning) {
		return err
	}
	return nil
}

func (m *Meter) write(ctx context.Context, limit Limit) error {
	u, ok := m.Last()
	if !ok {
		return nil
	}
	_, err := Within(ctx, limit, m.Draining, func(ctx context.Context) (struct{}, error) {
		err := m.Store.RecordAgentRunUsage(ctx, m.RunID, u.InputTokens, u.OutputTokens, u.CostMicroUSD, m.Now())
		if err == nil || errors.Is(err, store.ErrAgentRunNotRunning) { // the store has kept what it will keep
			m.mu.Lock()
			m.stored = &u
			m.mu.Unlock()
		}
		return struct{}{}, err
	})
	return err
}
