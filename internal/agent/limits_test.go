package agent

import (
	"context"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/agent/provider"
	"github.com/rcss-labs/harbormaster/internal/agent/provider/fake"
)

func twoToolTurn() *fake.Provider {
	return fake.New(
		fake.ToolCalls(provider.Usage{InputTokens: 10, OutputTokens: 5}, overviewCall("c1"), overviewCall("c2")),
		fake.Text("never reached", provider.Usage{InputTokens: 1, OutputTokens: 1}),
	)
}

func oneToolTurn() *fake.Provider {
	return fake.New(
		fake.ToolCalls(provider.Usage{InputTokens: 10, OutputTokens: 5}, overviewCall("c1")),
		fake.Text("never reached", provider.Usage{InputTokens: 1, OutputTokens: 1}),
	)
}

func TestLoop_CancelBetweenToolsSkipsTheRest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	runner := &recordingRunner{withContext: func(context.Context, string) (string, bool) {
		cancel()
		return "{}", false
	}}
	p := twoToolTurn()
	if _, err := runWithContext(t, ctx, newLoop(p, runner), baseInput()); err != context.Canceled {
		t.Fatalf("err = %v", err)
	}
	if n := len(runner.Calls()); n != 1 {
		t.Errorf("tools ran %d times, want 1", n)
	}
}

func TestLoop_CancelAfterTheLastToolDoesNotCallTheModelAgain(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	runner := &recordingRunner{withContext: func(context.Context, string) (string, bool) {
		cancel()
		return "{}", false
	}}
	p := oneToolTurn()
	if _, err := runWithContext(t, ctx, newLoop(p, runner), baseInput()); err != context.Canceled {
		t.Fatalf("err = %v", err)
	}
	if n := len(p.Requests()); n != 1 {
		t.Errorf("%d model calls, want 1", n)
	}
}

func TestLoop_TimeoutBetweenToolsSkipsTheRest(t *testing.T) {
	runner := &recordingRunner{withContext: func(context.Context, string) (string, bool) {
		time.Sleep(150 * time.Millisecond)
		return "{}", false
	}}
	events, err := run(t, newLoop(twoToolTurn(), runner, func(l *Limits) { l.Timeout = 50 * time.Millisecond }), baseInput())
	if err != nil {
		t.Fatal(err)
	}
	requireLimitReached(t, events)
	if n := len(runner.Calls()); n != 1 {
		t.Errorf("tools ran %d times, want 1", n)
	}
}

func TestLoop_TimeoutAfterTheLastToolDoesNotCallTheModelAgain(t *testing.T) {
	runner := &recordingRunner{withContext: func(context.Context, string) (string, bool) {
		time.Sleep(150 * time.Millisecond)
		return "{}", false
	}}
	p := oneToolTurn()
	events, err := run(t, newLoop(p, runner, func(l *Limits) { l.Timeout = 50 * time.Millisecond }), baseInput())
	if err != nil {
		t.Fatal(err)
	}
	requireLimitReached(t, events)
	if n := len(p.Requests()); n != 1 {
		t.Errorf("%d model calls, want 1", n)
	}
}

func TestLoop_ToolRunnerGetsTheRunContext(t *testing.T) {
	runner := &recordingRunner{withContext: func(ctx context.Context, _ string) (string, bool) {
		<-ctx.Done()
		return "cut off", true
	}}
	start := time.Now()
	events, err := run(t, newLoop(oneToolTurn(), runner, func(l *Limits) { l.Timeout = 50 * time.Millisecond }), baseInput())
	if err != nil {
		t.Fatal(err)
	}
	requireLimitReached(t, events)
	if time.Since(start) > 2*time.Second {
		t.Errorf("run took %v", time.Since(start))
	}
}
