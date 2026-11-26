package agentrun

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
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

	tokenDeleted atomic.Bool
}

func (f *fakeRunStore) SettleAgentRun(_ context.Context, _ uuid.UUID, state string, usage *store.AgentUsage, tools []string, _ time.Time) error {
	f.calls, f.usage = append(f.calls, "settle "+state), usage
	return f.settleErr
}
func (f *fakeRunStore) DeleteRunToken(context.Context, uuid.UUID) error {
	f.tokenDeleted.Store(true)
	return nil
}
func (f *fakeRunStore) AuditAgentRun(_ context.Context, _ uuid.UUID, e store.AuditEntry) error {
	f.entry = e
	return nil
}

func newOps(st *fakeRunStore) *StoreOps {
	run := store.AgentRun{ID: uuid.New(), Role: "chat", Provider: "fake", Model: "m"}
	m := &Meter{Store: st, RunID: run.ID, Now: time.Now}
	m.See(Usage{1, 2, 900})
	return &StoreOps{Store: st, Run: run, Meter: m, State: "done", Outcome: "done", Username: "sam", IP: "203.0.113.9", Tools: []string{"unknown_tool"}, Now: time.Now}
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
		d["saved"] != false || !slices.Equal(d["tools"].([]string), []string{"unknown_tool"}) || st.entry.ActorUsername != "sam" || st.entry.IP != "203.0.113.9" {
		t.Errorf("entry = %+v", st.entry)
	}
	if id, err := ops.Save(t.Context()); id != 7 || err != nil {
		t.Errorf("Save = %d, %v", id, err)
	}
}

// Details join the audit row; a key the row already has keeps its value, and nil changes nothing.
func TestStoreOps_AuditDetailsDoNotOverrideTheRow(t *testing.T) {
	plain := &fakeRunStore{}
	if err := newOps(plain).Audit(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	st := &fakeRunStore{}
	ops := newOps(st)
	ops.Details = map[string]any{"investigation_id": 7, "outcome": "forged", "run_id": "forged"}
	if err := ops.Audit(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	d := st.entry.Details
	if d["investigation_id"] != 7 || d["outcome"] != "done" || d["run_id"] != ops.Run.ID.String() {
		t.Errorf("details = %v", d)
	}
	delete(d, "investigation_id")
	if len(d) != len(plain.entry.Details) {
		t.Errorf("details = %v, want the keys of %v", d, plain.entry.Details)
	}
}

// A run with no meter settles at what its row has and does not panic.
func TestStoreOps_NoMeterSettlesWithNoFigures(t *testing.T) {
	st := &fakeRunStore{}
	ops := newOps(st)
	ops.Meter = nil
	if err := ops.Settle(t.Context()); err != nil || st.usage != nil || !slices.Equal(st.calls, []string{"settle done"}) {
		t.Errorf("Settle = %v, calls %v, figures %+v", err, st.calls, st.usage)
	}
}
