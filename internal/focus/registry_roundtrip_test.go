package focus

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/kv"
	"github.com/rcss-labs/harbormaster/internal/kv/kvtest"
)

// commandCountKV counts every command that reaches the store.
type commandCountKV struct {
	kv.Client
	n atomic.Int64
}

func (c *commandCountKV) ServerTime(ctx context.Context) (t time.Time, err error) {
	c.n.Add(1)
	return c.Client.ServerTime(ctx)
}

func (c *commandCountKV) ZRangeByScore(ctx context.Context, key string, min, max float64) ([]string, error) {
	c.n.Add(1)
	return c.Client.ZRangeByScore(ctx, key, min, max)
}

func (c *commandCountKV) Eval(ctx context.Context, s *kv.Script, keys, args []string) (kv.Reply, error) {
	c.n.Add(1)
	return c.Client.Eval(ctx, s, keys, args)
}

func TestKVRegistry_ListIsOneRoundTrip(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, c kv.Client) {
		counted := &commandCountKV{Client: c}
		r := NewKVRegistry(counted)
		o := serviceObject("web")
		heartbeat(t, r, o, 1, limits())
		counted.n.Store(0)
		if got := members(t, r); len(got) != 1 || got[0] != o.Member() {
			t.Fatalf("List = %v, want [%s]", got, o.Member())
		}
		if n := counted.n.Load(); n != 1 {
			t.Fatalf("List took %d store round trips, want 1", n)
		}
	})
}
