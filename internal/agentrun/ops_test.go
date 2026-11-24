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
	entry     store.AuditEntry
}

func (f *fakeRunStore) SettleAgentRun(_ context.Context, _ uuid.UUID, state string, tools []string, _ time.Time) error {
	f.calls = append(f.calls, "settle "+state)
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

// A run is settled only once its last usage is stored, so its cost is never final too early; the last try settles
// it whatever happened.
func TestStoreOps_SettleWaitsForTheUsageUntilTheLastTry(t *testing.T) {
	bad := errors.New("no")
	st := &fakeRunStore{}
	st.write = func(context.Context) error { return bad }
	ops := newOps(st)
	if err := ops.Settle(t.Context(), false); !errors.Is(err, bad) || len(st.calls) != 0 {
		t.Errorf("first try: %v, calls %v; want the write's error and no settle", err, st.calls)
	}
	if err := ops.Settle(t.Context(), true); !errors.Is(err, bad) || !slices.Equal(st.calls, []string{"settle done"}) {
		t.Errorf("last try: %v, calls %v", err, st.calls)
	}
	st.write = nil
	if err := ops.Settle(t.Context(), false); err != nil || len(st.writes) != 1 || st.writes[0].CostMicroUSD != 900 {
		t.Errorf("once the store answers: %v, writes %v", err, st.writes)
	}
}

func TestStoreOps_SettledElsewhereIsNotAnError(t *testing.T) {
	st := &fakeRunStore{settleErr: store.ErrAgentRunNotRunning}
	if err := newOps(st).Settle(t.Context(), false); err != nil {
		t.Errorf("Settle = %v", err)
	}
}

func TestStoreOps_AuditCarriesTheFiguresLastSeen(t *testing.T) {
	st := &fakeRunStore{}
	ops := newOps(st)
	ops.SaveAnswer = func(context.Context) (int64, error) { return 7, nil }
	if err := ops.Audit(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	d := st.entry.Details
	if d["cost_micro_usd"] != int64(900) || d["input_tokens"] != int64(1) || d["output_tokens"] != int64(2) || d["outcome"] != "done" ||
		d["saved"] != false || !slices.Equal(d["tools"].([]string), []string{"unknown_tool"}) || st.entry.ActorUsername != "sam" {
		t.Errorf("entry = %+v", st.entry)
	}
	if id, err := ops.Save(t.Context()); id != 7 || err != nil {
		t.Errorf("Save = %d, %v", id, err)
	}
}
