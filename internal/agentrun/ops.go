package agentrun

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/store"
)

// RunStore is what ending a run needs of the store.
type RunStore interface {
	UsageStore
	SettleAgentRun(ctx context.Context, id uuid.UUID, state string, usage *store.AgentUsage, tools []string, now time.Time) error
	DeleteRunToken(ctx context.Context, runID uuid.UUID) error
	AuditAgentRun(ctx context.Context, runID uuid.UUID, e store.AuditEntry) error
}

// StoreOps are the Operations of a run kept in the store. Its fields do not change once a Finisher has it.
type StoreOps struct {
	Store        RunStore
	Run          store.AgentRun
	Meter        *Meter
	State        string // done or failed
	Outcome      string // the audit row's word for it
	Tools        []string
	Username, IP string
	// Details are added to the audit row; a key the row already has keeps its value. Nil adds nothing.
	Details       map[string]any
	Now           func() time.Time
	SaveAnswer    func(ctx context.Context) (int64, error) // nil when the run has no answer to save
	SlotToRelease func()                                   // nil when the run holds no slot
}

func (o *StoreOps) DeleteToken(ctx context.Context) error {
	return o.Store.DeleteRunToken(ctx, o.Run.ID)
}

// Settle finishes the run. One that was swept meanwhile is not an error: the sweep kept the figures and the tools.
func (o *StoreOps) Settle(ctx context.Context) error {
	var usage *store.AgentUsage
	if o.Meter != nil { // a run ended by someone who never metered it keeps the figures its row has
		if u, ok := o.Meter.Last(); ok {
			usage = (*store.AgentUsage)(&u)
		}
	}
	err := o.Store.SettleAgentRun(ctx, o.Run.ID, o.State, usage, o.Tools, o.Now())
	if errors.Is(err, store.ErrAgentRunNotRunning) {
		return nil
	}
	return err
}

func (o *StoreOps) Save(ctx context.Context) (int64, error) {
	if o.SaveAnswer == nil {
		return 0, nil
	}
	return o.SaveAnswer(ctx)
}

func (o *StoreOps) ReleaseSlot() {
	if o.SlotToRelease != nil {
		o.SlotToRelease()
	}
}

// Audit writes the audit row; the store takes its figures from the run row.
func (o *StoreOps) Audit(ctx context.Context, saved bool) error {
	run := o.Run
	run.Tools = o.Tools
	entry := store.AgentRunAudit(run, o.Username, o.Outcome)
	entry.IP = o.IP
	if o.SaveAnswer != nil {
		entry.Details["saved"] = saved
	}
	for k, v := range o.Details {
		if _, taken := entry.Details[k]; !taken {
			entry.Details[k] = v
		}
	}
	return o.Store.AuditAgentRun(ctx, run.ID, entry)
}
