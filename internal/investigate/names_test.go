package investigate

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/agent"
	"github.com/rcss-labs/harbormaster/internal/agent/provider"
	"github.com/rcss-labs/harbormaster/internal/agent/provider/fake"
	"github.com/rcss-labs/harbormaster/internal/agentresult"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/notify"
	"github.com/rcss-labs/harbormaster/internal/secretshape"
)

type toolBodies map[string]string

func (b toolBodies) Run(_ context.Context, _, name string, _ map[string]any) (string, bool) {
	return b[name], false
}

// agentScript answers a run of the fixture's fake agent with what the agent service's own loop makes of it when the
// model reads the service and submits summary.
func agentScript(t *testing.T, key model.ServiceKey, summary string) func(c *call) {
	return func(c *call) {
		raw, err := json.Marshal(c.body.Task)
		if err != nil {
			t.Error(err)
		}
		var task agent.Task
		if err := json.Unmarshal(raw, &task); err != nil {
			t.Error(err)
		}
		tokens := provider.Usage{InputTokens: 100, OutputTokens: 20}
		loop := &agent.Loop{
			Provider: fake.New(fake.ToolCalls(tokens, provider.ToolCall{ID: "c1", Name: "get_service", Arguments: map[string]any{"service_id": key.ID()}}),
				fake.ToolCalls(tokens, provider.ToolCall{ID: "c2", Name: "submit_result", Arguments: map[string]any{"summary": summary, "evidence": []any{map[string]any{"ref": "e1", "note": "It is down."}}}})),
			Tools:  toolBodies{"get_service": `{"name":"` + key.Service + `","running":0,"cluster":{"name":"` + key.Cluster + `"}}`},
			Limits: agent.Limits{MaxSteps: 10, MaxRunTokens: 1_000_000, Timeout: time.Minute},
		}
		if err := loop.Run(context.Background(), agent.RunInput{RunID: "run-1", Role: c.body.Role, Bearer: "run-token", Task: &task, MaxCostMicroUSD: 100_000_000,
			Model: provider.Model{ID: "fake-model", InputMicroUSDPerMTok: 3_000_000, OutputMicroUSDPerMTok: 15_000_000, ContextTokens: 1_000_000}},
			func(ev agent.Event) {
				if ev.Result != nil {
					c.send(usage(4200), resultEvent(*ev.Result), doneEvent)
				}
			}); err != nil {
			t.Error(err)
		}
	}
}

// The other half of the twenty runs that were all replaced before.
func TestNamedSummary_TenInvestigationsThroughTheWholePath(t *testing.T) {
	f := newFixture(t)
	kept := 0
	for i := range 10 {
		key := model.ServiceKey{ClusterKey: checkoutKey.ClusterKey, Service: fmt.Sprintf("Shop-%c%02dF3A9B2C1D4E5-Xy7KpQ2mNvR", 'A'+i, i)}
		if secretshape.ScrubText(key.Service) != secretshape.Redacted {
			t.Fatalf("test setup: %q is not hidden", key.Service)
		}
		f.services(key)
		summary := []string{
			"The service " + key.Service + " cannot start its tasks, so nothing serves traffic.",
			"In payments-prod, " + key.Service + " is the least available: check its task definition.",
			"Tasks of \"" + key.Service + "\" stop after a minute (" + key.Service + ").",
		}[i%3]
		f.agent.set(agentScript(t, key, summary))
		v := f.runTo(f.request(key.Service), "done")
		m := notify.Message{Event: notify.EventDiagnosis, BaseURL: "https://hm.example.com", Diagnosis: &notify.Diagnosis{InvestigationID: v.ID, Summary: v.Summary, NameIDs: v.SummaryNameIDs}}
		hook, err := notify.WebhookBody(m)
		if err != nil {
			t.Fatal(err)
		}
		var sent map[string]any
		if err := json.Unmarshal(hook, &sent); err != nil {
			t.Fatal(err)
		}
		ev, err := f.st.ListInvestigationEvidence(context.Background(), v.ID)
		if err != nil || len(ev) != 1 || ev[0].Excerpt != `name: "`+key.Service+`"` {
			t.Errorf("evidence = %+v, %v", ev, err)
		}
		if v.Summary == summary && !v.SummaryReplaced && sent["summary"] == summary && slices.Equal(v.SummaryNameIDs, []string{key.ID()}) {
			kept++
		} else {
			t.Errorf("%q was stored as %q with %q and sent as %s", summary, v.Summary, v.SummaryNameIDs, hook)
		}
		f.clock.advance(time.Second)
	}
	if kept != 10 {
		t.Errorf("%d of 10 summaries kept", kept)
	}
}

// The task's ids are Harbormaster's own copy: an answer names another service in vain.
func TestNamedSummary_OfAServiceOutsideTheTaskIsReplaced(t *testing.T) {
	f := newFixture(t)
	other := model.ServiceKey{ClusterKey: checkoutKey.ClusterKey, Service: "CheckoutStack-WorkerServiceA71B04D9-pL8sVw3TzKc5"}
	f.agent.set(func(c *call) {
		c.send(usage(4200), resultEvent(agentresult.Result{Summary: "It is " + other.Service + "."}), doneEvent)
	})
	v := f.runTo(f.request("checkout-api"), "done")
	if v.Summary != agentresult.ReplacementSummary || !v.SummaryReplaced || len(v.SummaryNameIDs) != 0 {
		t.Errorf("stored %q with %q", v.Summary, v.SummaryNameIDs)
	}
}
