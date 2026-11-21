package signal

import (
	"context"

	"github.com/rcss-labs/harbormaster/internal/kv"
)

type kvBus struct{ c kv.Client }

// NewKVBus carries messages over PUBLISH and SUBSCRIBE on a channel named with the client's key prefix, so installs
// that share a server stay apart.
func NewKVBus(c kv.Client) Bus { return &kvBus{c: c} }

func (b *kvBus) Publish(ctx context.Context, ch Channel, payload string) error {
	return b.c.Publish(ctx, b.c.Key(string(ch)), payload)
}

func (b *kvBus) Subscribe(ctx context.Context, ch Channel, fn func(string), onGap func()) error {
	if onGap == nil {
		onGap = func() {}
	}
	return b.c.Subscribe(ctx, b.c.Key(string(ch)), fn, onGap)
}

type upper interface{ Up() bool }

// WithFallback publishes to primary while health says it is up (and to memory when a publish fails or it is down),
// and subscribes to both, so a message published into memory during an outage still reaches subscribers in this
// process. A subscriber on a fallback bus can be called from two goroutines at once (one per side).
func WithFallback(primary, memory Bus, health upper) Bus {
	return &fallbackBus{primary: primary, memory: memory, health: health}
}

type fallbackBus struct {
	primary, memory Bus
	health          upper
}

func (f *fallbackBus) Publish(ctx context.Context, ch Channel, payload string) error {
	if f.health.Up() {
		if err := f.primary.Publish(ctx, ch, payload); err == nil {
			return nil
		}
	}
	return f.memory.Publish(ctx, ch, payload)
}

func (f *fallbackBus) Subscribe(ctx context.Context, ch Channel, fn func(string), onGap func()) error {
	memoryDone := make(chan struct{})
	go func() {
		defer close(memoryDone)
		_ = f.memory.Subscribe(ctx, ch, fn, nil)
	}()
	err := f.primary.Subscribe(ctx, ch, fn, onGap)
	<-ctx.Done() // the primary returning early (a malformed channel, say) must not end the memory side
	<-memoryDone
	return err
}
