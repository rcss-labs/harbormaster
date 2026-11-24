package fake_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/agent/provider"
	"github.com/rcss-labs/harbormaster/internal/agent/provider/fake"
)

func TestFake_PlaysScriptInOrderAndStreamsText(t *testing.T) {
	call := provider.ToolCall{ID: "c1", Name: "get_overview", Arguments: map[string]any{}}
	p := fake.New(
		fake.ToolCalls(provider.Usage{InputTokens: 10, OutputTokens: 2}, call),
		fake.Text("Two services are down. Both are in staging.", provider.Usage{InputTokens: 20, OutputTokens: 9}),
	)
	var _ provider.Provider = p

	r1, err := p.Generate(context.Background(), provider.Request{Model: "m"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r1.StopReason != "tool_use" || len(r1.ToolCalls) != 1 || r1.ToolCalls[0].Name != "get_overview" || r1.Usage.InputTokens != 10 {
		t.Fatalf("step 1 = %+v", r1)
	}

	var deltas []string
	r2, err := p.Generate(context.Background(), provider.Request{Model: "m"}, func(d string) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatal(err)
	}
	if r2.StopReason != "end" || r2.Text != "Two services are down. Both are in staging." || r2.Usage.OutputTokens != 9 {
		t.Fatalf("step 2 = %+v", r2)
	}
	if len(deltas) < 2 || strings.Join(deltas, "") != r2.Text {
		t.Fatalf("deltas %q must be several pieces that join to the text", deltas)
	}

	if _, err := p.Generate(context.Background(), provider.Request{}, nil); err == nil {
		t.Fatal("expected an error once the script is exhausted")
	}
}

func TestFake_ReturnsProviderErrorForAStep(t *testing.T) {
	want := &provider.Error{Kind: "rate_limited", Retryable: true, RetryAfter: time.Second, Message: "slow down"}
	p := fake.New(fake.Fail(want))
	_, err := p.Generate(context.Background(), provider.Request{}, nil)
	var got *provider.Error
	if !errors.As(err, &got) || got.Kind != "rate_limited" || !got.Retryable || got.RetryAfter != time.Second {
		t.Fatalf("err = %v", err)
	}
}

func TestFake_BlockStepWaitsForContextCancel(t *testing.T) {
	p := fake.New(fake.Block())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := p.Generate(ctx, provider.Request{}, nil)
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("returned before cancel: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("did not return after cancel")
	}
}

func TestFake_RecordsRequests(t *testing.T) {
	p := fake.New(fake.Text("a", provider.Usage{}), fake.Fail(&provider.Error{Kind: "auth"}))
	msgs := []provider.Message{{Role: provider.RoleUser, Text: "hi"}}
	_, _ = p.Generate(context.Background(), provider.Request{Model: "m1", System: "s", Messages: msgs}, nil)
	msgs[0].Text = "mutated after the call"
	_, _ = p.Generate(context.Background(), provider.Request{Model: "m2"}, nil)

	got := p.Requests()
	if len(got) != 2 || got[0].Model != "m1" || got[1].Model != "m2" {
		t.Fatalf("requests = %+v", got)
	}
	if got[0].Messages[0].Text != "hi" {
		t.Fatalf("recorded request aliases the caller's slice: %q", got[0].Messages[0].Text)
	}
}

func TestDemo_CallsOverviewThenAnswersInTwoSentences(t *testing.T) {
	p := fake.Demo()
	for run := 0; run < 2; run++ { // stateless across runs
		r1, err := p.Generate(context.Background(), provider.Request{Messages: []provider.Message{{Role: provider.RoleUser, Text: "hi"}}}, nil)
		if err != nil || len(r1.ToolCalls) != 1 || r1.ToolCalls[0].Name != "get_overview" {
			t.Fatalf("run %d first = %+v, %v", run, r1, err)
		}
		r2, err := p.Generate(context.Background(), provider.Request{Messages: []provider.Message{
			{Role: provider.RoleUser, Text: "hi"},
			{Role: provider.RoleAssistant, ToolCalls: r1.ToolCalls},
			{Role: provider.RoleTool, ToolCallID: r1.ToolCalls[0].ID, ToolName: "get_overview", Text: "{}"},
		}}, nil)
		if err != nil || len(r2.ToolCalls) != 0 || r2.StopReason != "end" || strings.Count(r2.Text, ". ") != 1 || !strings.HasSuffix(r2.Text, ".") {
			t.Fatalf("run %d second = %+v, %v", run, r2, err)
		}
	}
}

func TestFake_RawReturnsTheResponseAndStreamsItsText(t *testing.T) {
	want := provider.Response{
		Text:       "checking now",
		ToolCalls:  []provider.ToolCall{{ID: "c1", Name: "list_services", Arguments: map[string]any{"env": "prod"}}},
		Usage:      provider.Usage{InputTokens: 5, OutputTokens: 3},
		StopReason: "max_tokens",
		Opaque:     []byte(`{"sig":"x"}`),
	}
	p := fake.New(fake.Raw(want))
	var streamed strings.Builder
	got, err := p.Generate(context.Background(), provider.Request{}, func(d string) { streamed.WriteString(d) })
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != want.Text || got.StopReason != "max_tokens" || len(got.ToolCalls) != 1 || got.ToolCalls[0].Name != "list_services" ||
		string(got.Opaque) != `{"sig":"x"}` || got.Usage != want.Usage {
		t.Fatalf("got %+v", got)
	}
	if streamed.String() != "checking now" {
		t.Fatalf("streamed %q", streamed.String())
	}
}

func TestFake_RecordedRequestsAreDeepCopies(t *testing.T) {
	p := fake.New(fake.Text("a", provider.Usage{}))
	args := map[string]any{"nested": map[string]any{"k": "v"}}
	schema := map[string]any{"type": "object"}
	opaque := []byte(`{"a":1}`)
	req := provider.Request{
		Messages: []provider.Message{{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "c", Arguments: args}}, Opaque: opaque}},
		Tools:    []provider.ToolDef{{Name: "t", InputSchema: schema}},
	}
	_, _ = p.Generate(context.Background(), req, nil)
	args["nested"].(map[string]any)["k"] = "changed"
	req.Messages[0].ToolCalls[0].Name = "changed"
	schema["type"] = "changed"
	opaque[2] = 'Z'

	got := p.Requests()[0]
	if got.Messages[0].ToolCalls[0].Arguments["nested"].(map[string]any)["k"] != "v" || got.Messages[0].ToolCalls[0].Name != "" ||
		got.Tools[0].InputSchema["type"] != "object" || string(got.Messages[0].Opaque) != `{"a":1}` {
		t.Fatalf("recorded request was mutated through the caller's data: %+v", got)
	}
}
