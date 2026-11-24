package agentrun

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/store"
)

// fakeRunStore is a RunStore whose calls are recorded in order.
type fakeRunStore struct {
	fakeUsageStore
	calls     []string
	settleErr error
	usage     *store.AgentUsage
	entry     store.AuditEntry
}

func (f *fakeRunStore) SettleAgentRun(_ context.Context, _ uuid.UUID, state string, usage *store.AgentUsage, tools []string, _ time.Time) error {
	f.calls, f.usage = append(f.calls, "settle "+state), usage
	return f.settleErr
}
func (f *fakeRunStore) DeleteRunToken(context.Context, uuid.UUID) error { return nil }
func (f *fakeRunStore) AuditAgentRun(_ context.Context, _ uuid.UUID, e store.AuditEntry) error {
	f.entry = e
	return nil
}

func newOps(st *fakeRunStore) *StoreOps {
	run := store.AgentRun{ID: uuid.New(), Role: "chat", Provider: "fake", Model: "m"}
	m := &Meter{Store: st, RunID: run.ID, Now: time.Now}
	m.See(Usage{1, 2, 900})
	return &StoreOps{Store: st, Run: run, Meter: m, State: "done", Outcome: "done", Username: "sam", Tools: []string{"unknown_tool"}, Now: time.Now}
}

// The last figures go with the settlement; a run that reported none keeps what its row has.
func TestStoreOps_SettlePassesTheFiguresLastSeen(t *testing.T) {
	st := &fakeRunStore{}
	ops := newOps(st)
	if err := ops.Settle(t.Context()); err != nil || !slices.Equal(st.calls, []string{"settle done"}) ||
		st.usage == nil || *st.usage != (store.AgentUsage{InputTokens: 1, OutputTokens: 2, CostMicroUSD: 900}) {
		t.Errorf("settle: %v, calls %v, figures %+v", err, st.calls, st.usage)
	}
	ops.Meter = &Meter{}
	if err := ops.Settle(t.Context()); err != nil || st.usage != nil {
		t.Errorf("settle with nothing seen: %v, figures %+v", err, st.usage)
	}
}

func TestStoreOps_ASettleThatFailsIsReported(t *testing.T) {
	bad := errors.New("no")
	if err := newOps(&fakeRunStore{settleErr: bad}).Settle(t.Context()); !errors.Is(err, bad) {
		t.Errorf("Settle = %v", err)
	}
}

func TestStoreOps_SettledElsewhereIsNotAnError(t *testing.T) {
	st := &fakeRunStore{settleErr: store.ErrAgentRunNotRunning}
	if err := newOps(st).Settle(t.Context()); err != nil {
		t.Errorf("Settle = %v", err)
	}
}

func TestStoreOps_AuditEntry(t *testing.T) {
	st := &fakeRunStore{}
	ops := newOps(st)
	ops.SaveAnswer = func(context.Context) (int64, error) { return 7, nil }
	if err := ops.Audit(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	d := st.entry.Details
	if d["outcome"] != "done" ||
		d["saved"] != false || !slices.Equal(d["tools"].([]string), []string{"unknown_tool"}) || st.entry.ActorUsername != "sam" {
		t.Errorf("entry = %+v", st.entry)
	}
	if id, err := ops.Save(t.Context()); id != 7 || err != nil {
		t.Errorf("Save = %d, %v", id, err)
	}
}
