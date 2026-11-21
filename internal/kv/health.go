package kv

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/rcss-labs/harbormaster/internal/telemetry"
)

const (
	defaultCommandTimeout = 250 * time.Millisecond
	defaultPingEvery      = 2 * time.Second
	defaultPingTimeout    = 500 * time.Millisecond
	failedCommandsToDown  = 3
	goodPingsToUp         = 3
)

// Health tracks whether the store is usable and bounds every command, so a slow or absent store costs freshness
// and nothing else. The state starts down. The first PING that succeeds brings it up at once; after any failure
// it takes three consecutive good pings. Three consecutive failed commands, or one failed PING from Run, bring
// it down.
type Health struct {
	inner Client
	reg   *telemetry.Registry
	log   *slog.Logger

	// Constants kept as fields so tests can shorten them.
	commandTimeout time.Duration
	pingEvery      time.Duration
	pingTimeout    time.Duration

	mu         sync.Mutex
	up         bool
	sawFailure bool // false until the first failed ping or command: the first good ping then suffices
	failedCmds int
	goodPings  int
}

// NewHealth wraps c. reg and log may be nil in tests that do not look at them.
func NewHealth(c Client, reg *telemetry.Registry, log *slog.Logger) *Health {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	h := &Health{
		inner:          c,
		reg:            reg,
		log:            log,
		commandTimeout: defaultCommandTimeout,
		pingEvery:      defaultPingEvery,
		pingTimeout:    defaultPingTimeout,
	}
	if reg != nil {
		reg.SetKVUp(false)
	}
	return h
}

// Up reports whether the store is currently considered usable.
func (h *Health) Up() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.up
}

// Client returns the wrapped client: the same interface, with every command bounded and counted. Subscribe is
// not bounded, because it runs until its context ends.
func (h *Health) Client() Client { return &healthClient{h: h} }

// Run pings until ctx ends.
func (h *Health) Run(ctx context.Context) {
	h.ping(ctx)
	t := time.NewTicker(h.pingEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.ping(ctx)
		}
	}
}

func (h *Health) ping(ctx context.Context) {
	pctx, cancel := context.WithTimeout(ctx, h.pingTimeout)
	defer cancel()
	start := time.Now()
	err := h.inner.Ping(pctx)
	if ctx.Err() != nil {
		return
	}
	h.record("ping", time.Since(start), err)
	h.mu.Lock()
	defer h.mu.Unlock()
	if err != nil {
		h.sawFailure = true
		h.goodPings = 0
		h.setUpLocked(false)
		return
	}
	if h.up {
		return
	}
	h.goodPings++
	if !h.sawFailure || h.goodPings >= goodPingsToUp {
		h.setUpLocked(true)
	}
}

// commandDone records the outcome of one bounded command.
func (h *Health) commandDone(op string, d time.Duration, err error) {
	h.record(op, d, err)
	h.mu.Lock()
	defer h.mu.Unlock()
	if err == nil {
		h.failedCmds = 0
		return
	}
	h.failedCmds++
	if h.failedCmds >= failedCommandsToDown {
		h.sawFailure = true
		h.goodPings = 0
		h.setUpLocked(false)
	}
}

func (h *Health) record(op string, d time.Duration, err error) {
	if h.reg == nil {
		return
	}
	h.reg.ObserveKVCommand(op, d)
	if err != nil {
		h.reg.CountKVError(op)
	}
}

// setUpLocked records a transition once. It logs no address: the URL can carry credentials.
func (h *Health) setUpLocked(up bool) {
	if h.up == up {
		return
	}
	h.up = up
	h.failedCmds = 0
	if h.reg != nil {
		h.reg.SetKVUp(up)
	}
	if up {
		h.log.Info("kv: store is up")
	} else {
		h.log.Warn("kv: store is down")
	}
}

// healthClient bounds each command to the timeout and reports the outcome to Health.
type healthClient struct{ h *Health }

// run executes fn under the command timeout. A command that failed only because its caller gave up is not
// counted against the store.
func (c *healthClient) run(ctx context.Context, op string, fn func(ctx context.Context) error) error {
	cctx, cancel := context.WithTimeout(ctx, c.h.commandTimeout)
	defer cancel()
	start := time.Now()
	err := fn(cctx)
	if err != nil && ctx.Err() != nil {
		return err
	}
	c.h.commandDone(op, time.Since(start), err)
	return err
}

func (c *healthClient) Get(ctx context.Context, key string) (v Value, err error) {
	err = c.run(ctx, "get", func(ctx context.Context) (e error) { v, e = c.h.inner.Get(ctx, key); return })
	return
}

func (c *healthClient) MGet(ctx context.Context, keys ...string) (v []Value, err error) {
	err = c.run(ctx, "mget", func(ctx context.Context) (e error) { v, e = c.h.inner.MGet(ctx, keys...); return })
	return
}

func (c *healthClient) Set(ctx context.Context, key, value string, o SetOptions) (stored bool, err error) {
	err = c.run(ctx, "set", func(ctx context.Context) (e error) { stored, e = c.h.inner.Set(ctx, key, value, o); return })
	return
}

func (c *healthClient) Del(ctx context.Context, keys ...string) error {
	return c.run(ctx, "del", func(ctx context.Context) error { return c.h.inner.Del(ctx, keys...) })
}

func (c *healthClient) PExpire(ctx context.Context, key string, ttl time.Duration) error {
	return c.run(ctx, "pexpire", func(ctx context.Context) error { return c.h.inner.PExpire(ctx, key, ttl) })
}

func (c *healthClient) ZAdd(ctx context.Context, key string, score float64, member string) error {
	return c.run(ctx, "zadd", func(ctx context.Context) error { return c.h.inner.ZAdd(ctx, key, score, member) })
}

func (c *healthClient) ZRem(ctx context.Context, key string, members ...string) error {
	return c.run(ctx, "zrem", func(ctx context.Context) error { return c.h.inner.ZRem(ctx, key, members...) })
}

func (c *healthClient) ZCard(ctx context.Context, key string) (n int64, err error) {
	err = c.run(ctx, "zcard", func(ctx context.Context) (e error) { n, e = c.h.inner.ZCard(ctx, key); return })
	return
}

func (c *healthClient) ZRangeByScore(ctx context.Context, key string, min, max float64) (v []string, err error) {
	err = c.run(ctx, "zrangebyscore", func(ctx context.Context) (e error) {
		v, e = c.h.inner.ZRangeByScore(ctx, key, min, max)
		return
	})
	return
}

func (c *healthClient) ZRemRangeByScore(ctx context.Context, key string, min, max float64) error {
	return c.run(ctx, "zremrangebyscore", func(ctx context.Context) error {
		return c.h.inner.ZRemRangeByScore(ctx, key, min, max)
	})
}

func (c *healthClient) HSet(ctx context.Context, key string, fields map[string]string) error {
	return c.run(ctx, "hset", func(ctx context.Context) error { return c.h.inner.HSet(ctx, key, fields) })
}

func (c *healthClient) HMGet(ctx context.Context, key string, fields ...string) (v []Value, err error) {
	err = c.run(ctx, "hmget", func(ctx context.Context) (e error) { v, e = c.h.inner.HMGet(ctx, key, fields...); return })
	return
}

func (c *healthClient) Eval(ctx context.Context, s *Script, keys, args []string) (r Reply, err error) {
	err = c.run(ctx, "eval", func(ctx context.Context) (e error) { r, e = c.h.inner.Eval(ctx, s, keys, args); return })
	return
}

func (c *healthClient) Publish(ctx context.Context, channel, payload string) error {
	return c.run(ctx, "publish", func(ctx context.Context) error { return c.h.inner.Publish(ctx, channel, payload) })
}

func (c *healthClient) Subscribe(ctx context.Context, channel string, onMessage func(string), onResubscribe func()) error {
	return c.h.inner.Subscribe(ctx, channel, onMessage, onResubscribe)
}

func (c *healthClient) Ping(ctx context.Context) error {
	return c.run(ctx, "ping", func(ctx context.Context) error { return c.h.inner.Ping(ctx) })
}

func (c *healthClient) ServerTime(ctx context.Context) (t time.Time, err error) {
	err = c.run(ctx, "time", func(ctx context.Context) (e error) { t, e = c.h.inner.ServerTime(ctx); return })
	return
}

func (c *healthClient) Key(parts ...string) string { return c.h.inner.Key(parts...) }

// Close closes the underlying client.
func (c *healthClient) Close() { c.h.inner.Close() }
