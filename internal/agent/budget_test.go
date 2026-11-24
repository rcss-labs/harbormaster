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

// finalOf runs a script whose second call is the final one (MaxSteps 2).
func finalOf(first provider.Usage) func(edit ...func(*Limits)) (*fake.Provider, *Loop) {
	return func(edit ...func(*Limits)) (*fake.Provider, *Loop) {
		p := twoCalls(first)
		return p, newLoop(p, &recordingRunner{}, append([]func(*Limits){func(l *Limits) { l.MaxSteps = 2 }}, edit...)...)
	}
}

func TestLoop_FinalCallStartsOnlyIfPriorSpendPlusItsCostFits(t *testing.T) {
	first := provider.Usage{InputTokens: 50_000, OutputTokens: 2_000}
	setup := finalOf(first)
	_, l := setup()
	probe, err := run(t, l, baseInput())
	if err != nil {
		t.Fatal(err)
	}
	usage := usageEvents(probe)
	a1, p2 := usage[1], usage[2]
	if p2.Provisional != true {
		t.Fatalf("usage events = %v", eventTypes(probe))
	}

	cases := []struct {
		name    string
		ceiling int64
		wantOut int // 0: the final call is not made
	}{
		{"exactly fits", p2.CostMicroUSD, maxOutputTokensPerCall},
		{"one micro-dollar short lowers the output cap", p2.CostMicroUSD - 1, -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, l := setup()
			events, err := run(t, l, ceilingInput(tc.ceiling))
			if err != nil {
				t.Fatal(err)
			}
			reqs := p.Requests()
			switch {
			case tc.wantOut == 0:
				requireLimitReached(t, events)
				if len(reqs) != 1 {
					t.Errorf("%d requests, want 1: the answer allowance does not fit", len(reqs))
				}
			case tc.wantOut < 0:
				if len(reqs) != 2 || reqs[1].MaxOutputTokens >= maxOutputTokensPerCall || reqs[1].MaxOutputTokens < answerOutputTokens {
					t.Errorf("%d requests, final max output %d, want a lowered cap", len(reqs), reqs[len(reqs)-1].MaxOutputTokens)
				}
			default:
				if len(reqs) != 2 || reqs[1].MaxOutputTokens != tc.wantOut {
					t.Errorf("%d requests, final max output %d, want %d", len(reqs), reqs[len(reqs)-1].MaxOutputTokens, tc.wantOut)
				}
			}
			for _, e := range usageEvents(events) {
				if e.Provisional && e.CostMicroUSD > tc.ceiling {
					t.Errorf("provisional cost %d over the ceiling %d (first call cost %d)", e.CostMicroUSD, tc.ceiling, a1.CostMicroUSD)
				}
			}
		})
	}
}

func TestLoop_FinalCallNeedsRoomForTheAnswerAllowance(t *testing.T) {
	// MaxSteps 1 makes the very first call the final one.
	newFinal := func(maxTokens int64) (*fake.Provider, *Loop) {
		p := fake.New(fake.Text("ok", provider.Usage{InputTokens: 1, OutputTokens: 1}))
		return p, newLoop(p, &recordingRunner{}, func(l *Limits) { l.MaxSteps = 1; l.MaxRunTokens = maxTokens })
	}
	_, l := newFinal(1_000_000)
	probe, err := run(t, l, baseInput())
	if err != nil {
		t.Fatal(err)
	}
	estimate := probe[1].Usage.InputTokens // [0] is the notice, [1] the provisional usage

	p, l := newFinal(estimate + answerOutputTokens)
	if _, err := run(t, l, baseInput()); err != nil {
		t.Fatal(err)
	}
	if reqs := p.Requests(); len(reqs) != 1 || reqs[0].MaxOutputTokens != answerOutputTokens {
		t.Fatalf("%d requests, want 1 with a cap of exactly %d", len(reqs), answerOutputTokens)
	}

	p, l = newFinal(estimate + answerOutputTokens - 1)
	events, err := run(t, l, baseInput())
	if err != nil {
		t.Fatal(err)
	}
	requireLimitReached(t, events)
	if n := len(p.Requests()); n != 0 {
		t.Errorf("%d requests, want 0: the answer allowance does not fit", n)
	}

	// The same boundary in money: the first provisional cost has the full
	// output cap in it, 15 micro-dollars per output token at the test prices.
	priced := probe[1].CostMicroUSD - int64(maxOutputTokensPerCall-answerOutputTokens)*15
	for _, tc := range []struct {
		ceiling int64
		calls   int
	}{{priced, 1}, {priced - 1, 0}} {
		p := fake.New(fake.Text("ok", provider.Usage{InputTokens: 1, OutputTokens: 1}))
		l := newLoop(p, &recordingRunner{}, func(l *Limits) { l.MaxSteps = 1 })
		if _, err := run(t, l, ceilingInput(tc.ceiling)); err != nil {
			t.Fatal(err)
		}
		if n := len(p.Requests()); n != tc.calls {
			t.Errorf("ceiling %d: %d requests, want %d", tc.ceiling, n, tc.calls)
		}
	}
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
	for name, text := range map[string]string{"full reply": "a long answer that was cut", "empty reply": ""} {
		t.Run(name, func(t *testing.T) {
			p := fake.New(fake.Raw(provider.Response{Text: text, StopReason: "max_tokens", Usage: provider.Usage{InputTokens: 10, OutputTokens: 8192}}))
			events, err := run(t, newLoop(p, &recordingRunner{}), baseInput())
			if err != nil {
				t.Fatal(err)
			}
			requireLimitReached(t, events)
		})
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
