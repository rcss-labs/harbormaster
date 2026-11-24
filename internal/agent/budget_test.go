package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/agent/provider"
	"github.com/rcss-labs/harbormaster/internal/agent/provider/fake"
)

// scriptedStep is one reply of scriptedProvider: a response and an error
// together, with text streamed first, which the fake provider cannot do.
type scriptedStep struct {
	stream string
	resp   provider.Response
	err    error
}

type scriptedProvider struct {
	mu    sync.Mutex
	steps []scriptedStep
	reqs  []provider.Request
}

func (p *scriptedProvider) Name() string { return "scripted" }

func (p *scriptedProvider) Requests() []provider.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]provider.Request(nil), p.reqs...)
}

func (p *scriptedProvider) Generate(_ context.Context, req provider.Request, onText func(string)) (provider.Response, error) {
	p.mu.Lock()
	p.reqs = append(p.reqs, req)
	var step scriptedStep
	if len(p.reqs) <= len(p.steps) {
		step = p.steps[len(p.reqs)-1]
	}
	p.mu.Unlock()
	if step.stream != "" {
		onText(step.stream)
	}
	return step.resp, step.err
}

// twoCalls is a run of one tool turn and one answer, with the first turn's
// usage given.
func twoCalls(first provider.Usage) *fake.Provider {
	return fake.New(
		fake.ToolCalls(first, overviewCall("c1")),
		fake.Text("ok", provider.Usage{InputTokens: 10, OutputTokens: 1}),
	)
}

func ceilingInput(ceiling int64) RunInput {
	in := baseInput()
	in.MaxCostMicroUSD = ceiling
	return in
}

func TestLoop_OpaqueStateCountsInTheEstimate(t *testing.T) {
	secondEstimate := func(opaque int) int64 {
		p := fake.New(
			fake.Raw(provider.Response{
				ToolCalls: []provider.ToolCall{overviewCall("c1")}, Usage: provider.Usage{InputTokens: 10, OutputTokens: 1},
				StopReason: "tool_use", Opaque: json.RawMessage(`"` + strings.Repeat("o", opaque) + `"`),
			}),
			fake.Text("ok", provider.Usage{InputTokens: 10, OutputTokens: 1}),
		)
		events, err := run(t, newLoop(p, &recordingRunner{}), baseInput())
		if err != nil {
			t.Fatal(err)
		}
		return usageEvents(events)[2].Usage.InputTokens
	}
	if small, large := secondEstimate(10), secondEstimate(30_000); large-small < 9_000 {
		t.Errorf("30,000 bytes of opaque state raised the estimate by %d tokens, want about 10,000", large-small)
	}
}

func TestLoop_MaxTokensReplyIsReported(t *testing.T) {
	const text = "a long answer that was cut"
	p := fake.New(fake.Raw(provider.Response{Text: text, StopReason: "max_tokens", Usage: provider.Usage{InputTokens: 10, OutputTokens: 8192}}))
	events, err := run(t, newLoop(p, &recordingRunner{}), baseInput())
	if err != nil {
		t.Fatal(err)
	}
	requireLimitReached(t, events)
	if joinedText(events) != text {
		t.Errorf("text = %q", joinedText(events))
	}
}

func TestLoop_ZeroUsageIsReplacedPerField(t *testing.T) {
	p := fake.New(fake.Raw(provider.Response{Text: "ok", StopReason: "end", Usage: provider.Usage{InputTokens: 100}}))
	events, err := run(t, newLoop(p, &recordingRunner{}), baseInput())
	if err != nil {
		t.Fatal(err)
	}
	usage := usageEvents(events)
	if got := usage[1].Usage; got.InputTokens != 100 || got.OutputTokens != usage[0].Usage.OutputTokens {
		t.Errorf("final usage = %+v, want the reported input and the provisional output %d", got, usage[0].Usage.OutputTokens)
	}
}

func TestLoop_FailedCallWithUsageKeepsTheLargerFigure(t *testing.T) {
	// The vendor reports more than we provisioned and the retry then no longer
	// fits the ceiling: the run ends with the provider's error, not a limit.
	p := &scriptedProvider{steps: []scriptedStep{{
		resp: provider.Response{Usage: provider.Usage{InputTokens: 5_000_000}},
		err:  rateLimited(),
	}}}
	events, err := run(t, newLoop(p, &recordingRunner{}, func(l *Limits) { l.MaxRunTokens = 100_000_000 }), ceilingInput(10_000_000))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(p.Requests()); n != 1 {
		t.Errorf("%d requests, want 1: the retry does not fit the ceiling", n)
	}
	last := events[len(events)-1]
	if last.Type != "error" || last.Code != "rate_limited" {
		t.Errorf("last event = %+v, want the provider's error", last)
	}
}

func TestLoop_NeverRetriesOnceTextWasStreamed(t *testing.T) {
	p := &scriptedProvider{steps: []scriptedStep{
		{stream: "Two clusters", err: rateLimited()},
		{resp: provider.Response{Text: "again", Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}, StopReason: "end"}},
	}}
	events, err := run(t, newLoop(p, &recordingRunner{}), baseInput())
	if err != nil {
		t.Fatal(err)
	}
	if n := len(p.Requests()); n != 1 {
		t.Errorf("%d requests, want 1: a retry would repeat text the user already saw", n)
	}
	if last := events[len(events)-1]; last.Type != "error" || last.Code != "rate_limited" {
		t.Errorf("last event = %+v", last)
	}
}

func TestLoop_RetryWaitIsCappedAtTwentySeconds(t *testing.T) {
	var waits []time.Duration
	p := fake.New(
		fake.Fail(&provider.Error{Kind: "rate_limited", Retryable: true, RetryAfter: time.Hour, Message: "Slow down."}),
		fake.Text("ok", provider.Usage{InputTokens: 1, OutputTokens: 1}),
	)
	l := newLoop(p, &recordingRunner{}, func(l *Limits) { l.Timeout = 2 * time.Hour })
	l.newTimer = func(d time.Duration) *time.Timer {
		waits = append(waits, d)
		return time.NewTimer(0)
	}
	if _, err := run(t, l, baseInput()); err != nil {
		t.Fatal(err)
	}
	if len(waits) != 1 || waits[0] != 20*time.Second {
		t.Errorf("waits = %v, want one wait of 20s", waits)
	}
}

func TestLoop_NoRetryWhenTheWaitOutlastsTheTimeout(t *testing.T) {
	p := fake.New(
		fake.Fail(&provider.Error{Kind: "rate_limited", Retryable: true, RetryAfter: 2 * time.Second, Message: "Slow down."}),
		fake.Text("never", provider.Usage{}),
	)
	l := newLoop(p, &recordingRunner{}, func(l *Limits) { l.Timeout = 500 * time.Millisecond })
	events, err := run(t, l, baseInput())
	if err != nil {
		t.Fatal(err)
	}
	if n := len(p.Requests()); n != 1 {
		t.Errorf("%d requests, want 1", n)
	}
	if last := events[len(events)-1]; last.Type != "error" || last.Code != "rate_limited" {
		t.Errorf("last event = %+v", last)
	}
}

func TestLoop_CancelDuringTheRetryWaitStopsPromptly(t *testing.T) {
	p := fake.New(
		fake.Fail(&provider.Error{Kind: "rate_limited", Retryable: true, RetryAfter: 10 * time.Second, Message: "Slow down."}),
		fake.Text("never", provider.Usage{}),
	)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	events, err := runWithContext(t, ctx, newLoop(p, &recordingRunner{}, func(l *Limits) { l.Timeout = time.Hour }), baseInput())
	if err != context.Canceled || time.Since(start) > 2*time.Second {
		t.Fatalf("err = %v after %v", err, time.Since(start))
	}
	if n := len(p.Requests()); n != 1 {
		t.Errorf("%d requests, want 1", n)
	}
	for _, e := range events {
		if e.Type == "done" || e.Type == "error" {
			t.Errorf("a cancelled run emitted %s", e.Type)
		}
	}
}

func TestLoop_FailedCallsReportedUsageCountsInTheNextProvisionalFigures(t *testing.T) {
	reported := provider.Usage{InputTokens: 50_000, OutputTokens: 20_000}
	p := &scriptedProvider{steps: []scriptedStep{
		{resp: provider.Response{Usage: reported}, err: rateLimited()},
		{resp: provider.Response{Text: "ok", Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}, StopReason: "end"}},
	}}
	events, err := run(t, newLoop(p, &recordingRunner{}, func(l *Limits) { l.MaxRunTokens = 100_000_000 }), ceilingInput(1_000_000_000))
	if err != nil {
		t.Fatal(err)
	}
	first, second := usageEvents(events)[0], usageEvents(events)[1]
	if got := second.Usage.InputTokens - first.Usage.InputTokens; got < reported.InputTokens {
		t.Errorf("input tokens grew by %d across the failed call, want at least the reported %d", got, reported.InputTokens)
	}
	if got := second.Usage.OutputTokens - first.Usage.OutputTokens; got < reported.OutputTokens {
		t.Errorf("output tokens grew by %d across the failed call, want at least the reported %d", got, reported.OutputTokens)
	}
}
