package agentrun

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
)

// usageLimit bounds one usage write. The shutdown shortens it even when it is already waiting.
var usageLimit = Limit{Normal: 10 * time.Second, Draining: time.Second}

// UsageStore is where a run's cumulative figures are written.
type UsageStore interface {
	RecordAgentRunUsage(ctx context.Context, id uuid.UUID, inputTokens, outputTokens, costMicroUSD int64, now time.Time) error
}

// Meter holds the figures of one run as last seen. A write that fails does not lose them: they are stored again with
// the settlement.
type Meter struct {
	Store    UsageStore
	RunID    uuid.UUID
	Now      func() time.Time
	Draining <-chan struct{}

	mu   sync.Mutex // the owner of the run writes them, the step that settles it reads them
	seen *Usage
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
	u, ok := m.Last()
	if !ok {
		return nil
	}
	_, err := Within(context.WithoutCancel(ctx), usageLimit, m.Draining, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, m.Store.RecordAgentRunUsage(ctx, m.RunID, u.InputTokens, u.OutputTokens, u.CostMicroUSD, m.Now())
	})
	return err
}
