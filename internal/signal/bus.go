// Package signal carries small wake-up messages between Harbormaster tasks: poll-now requests and kicks for the
// coordinator's jobs. Delivery is at most once, which is enough because every use has a repair path (the jobs also
// run at their interval).
package signal

import (
	"context"
	"sync"
)

// Channel names a message stream.
type Channel string

const (
	// PollNow asks the owner of a slice to poll it now.
	PollNow Channel = "pollnow"
	// Kick asks the active coordinator to run a job now; the payload names the job.
	Kick Channel = "kick"
)

// Bus publishes and subscribes to channels.
type Bus interface {
	Publish(ctx context.Context, ch Channel, payload string) error
	// Subscribe calls fn for each message until ctx ends. Delivery is at-most-once. onGap is called after the
	// subscription was re-established, so the caller can repair what it may have missed; nil is allowed. Handlers run
	// on the subscription goroutine and must not block.
	Subscribe(ctx context.Context, ch Channel, fn func(payload string), onGap func()) error
}

// memoryBufferSize is how many messages a memory subscriber may have waiting before newer ones are dropped.
const memoryBufferSize = 64

type memoryBus struct {
	mu   sync.Mutex
	subs map[Channel]map[*memorySub]struct{}
}

type memorySub struct{ ch chan string }

// NewMemoryBus returns a bus inside this process. It is the bus of a single-task install and the fallback when the
// shared store is down.
func NewMemoryBus() Bus {
	return &memoryBus{subs: map[Channel]map[*memorySub]struct{}{}}
}

func (b *memoryBus) Publish(_ context.Context, ch Channel, payload string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for sub := range b.subs[ch] {
		select {
		case sub.ch <- payload:
		default: // a stuck subscriber loses messages; it never holds the publisher up
		}
	}
	return nil
}

func (b *memoryBus) Subscribe(ctx context.Context, ch Channel, fn func(string), _ func()) error {
	sub := &memorySub{ch: make(chan string, memoryBufferSize)}
	b.mu.Lock()
	if b.subs[ch] == nil {
		b.subs[ch] = map[*memorySub]struct{}{}
	}
	b.subs[ch][sub] = struct{}{}
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.subs[ch], sub)
		b.mu.Unlock()
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case payload := <-sub.ch:
			fn(payload)
		}
	}
}
