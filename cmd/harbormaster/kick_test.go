package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/signal"
)

type failingBus struct {
	signal.Bus
	err   error
	block bool
}

func (b failingBus) Publish(ctx context.Context, _ signal.Channel, _ string) error {
	if b.block {
		<-ctx.Done()
		return ctx.Err()
	}
	return b.err
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// A kick is a courtesy: whatever the store does, the request that asked for it carries on.
func TestKick_PublishFailureNeverFailsTheRequest(t *testing.T) {
	for name, bus := range map[string]signal.Bus{
		"error": failingBus{err: errors.New("store down")},
		"hang":  failingBus{block: true},
	} {
		k := &kickPublisher{bus: bus, log: quietLog()}
		start := time.Now()
		k.Kick()
		k.KickOffHours()
		k.KickNotify()
		if d := time.Since(start); d > 1500*time.Millisecond {
			t.Errorf("%s: three kicks took %v; each is bounded to %v", name, d, kickPublishTimeout)
		}
	}
}

// Without a store the web side and the coordinator share one process and one memory bus: a kick still arrives.
func TestKick_AllRoleStillKicksInProcess(t *testing.T) {
	bus := newKickBus(nil)
	got := make(chan string, 4)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { _ = bus.Subscribe(ctx, signal.Kick, func(p string) { got <- p }, nil) }()
	k := &kickPublisher{bus: bus, log: quietLog()}
	deadline := time.After(1 * time.Second)
	for {
		k.KickOffHours()
		select {
		case p := <-got:
			if p != "offhours" {
				t.Fatalf("kick payload = %q, want offhours", p)
			}
			return
		case <-time.After(20 * time.Millisecond):
		case <-deadline:
			t.Fatal("no kick within 1s")
		}
	}
}
