package kv

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/valkey-io/valkey-go"
)

const (
	defaultPrefix      = "hm"
	defaultClientName  = "harbormaster"
	redialInterval     = time.Second
	dialTimeout        = time.Second
	resubscribeMin     = 250 * time.Millisecond
	resubscribeMax     = 5 * time.Second
	keyVersion         = "v1"
	errNotConnectedMsg = "kv: store is not connected"
)

var errNotConnected = errors.New(errNotConnectedMsg)

// Open prepares a client. It fails only for bad options. When the server cannot be reached the returned client
// is still usable: its commands fail fast and it dials again on later commands, at most once a second, so an
// outage at start never stops the process from starting.
func Open(o Options) (Client, error) {
	if o.Address == "" {
		return nil, errors.New("kv: address is required")
	}
	if o.Prefix == "" {
		o.Prefix = defaultPrefix
	}
	if o.ClientName == "" {
		o.ClientName = defaultClientName
	}
	c := &client{opts: o}
	c.dial() // first attempt now; a failure is retried by later commands
	return c, nil
}

type client struct {
	opts Options

	mu       sync.Mutex
	inner    valkey.Client
	dialing  bool
	lastDial time.Time
	closed   bool
}

func (c *client) clientOption() valkey.ClientOption {
	opt := valkey.ClientOption{
		InitAddress: []string{c.opts.Address},
		Username:    c.opts.Username,
		Password:    c.opts.Password,
		SelectDB:    c.opts.DB,
		ClientName:  c.opts.ClientName,
		// Only the common command subset reaches the server: no client-side caching or tracking, no CLIENT
		// SETINFO, and RESP3 (HELLO 3) so pub/sub shares the connection pool's protocol.
		ForceSingleClient: true, // no CLUSTER SLOTS probe: the cluster commands are outside the allow-list
		DisableCache:      true,
		ClientSetInfo:     valkey.DisableClientSetInfo,

		Dialer: net.Dialer{Timeout: dialTimeout},
		// Keep the command set small and predictable.
		DisableRetry: true,
		ShuffleInit:  false,
	}
	if c.opts.TLS {
		host, _, _ := net.SplitHostPort(c.opts.Address)
		opt.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}
	}
	return opt
}

// dial tries once to connect and reports whether the client is now connected. It never holds the mutex while
// dialing, so a slow dial does not block other callers.
func (c *client) dial() bool {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return false
	}
	if c.inner != nil {
		c.mu.Unlock()
		return true
	}
	if c.dialing {
		c.mu.Unlock()
		return false
	}
	c.dialing = true
	c.lastDial = time.Now()
	c.mu.Unlock()

	inner, err := valkey.NewClient(c.clientOption())

	c.mu.Lock()
	defer c.mu.Unlock()
	c.dialing = false
	if err != nil {
		return false
	}
	if c.closed {
		inner.Close()
		return false
	}
	c.inner = inner
	return true
}

// conn returns the connected client or errNotConnected. When there is none it starts a new dial in the
// background if the last attempt was over a second ago, so the calling command fails at once.
func (c *client) conn() (valkey.Client, error) {
	c.mu.Lock()
	inner, closed := c.inner, c.closed
	redial := inner == nil && !closed && !c.dialing && time.Since(c.lastDial) >= redialInterval
	c.mu.Unlock()
	if inner != nil {
		return inner, nil
	}
	if redial {
		go c.dial()
	}
	return nil, errNotConnected
}

func (c *client) Close() {
	c.mu.Lock()
	c.closed = true
	inner := c.inner
	c.inner = nil
	c.mu.Unlock()
	if inner != nil {
		inner.Close()
	}
}

func (c *client) Key(parts ...string) string {
	return c.opts.Prefix + ":" + keyVersion + ":" + strings.Join(parts, ":")
}

// observe reports a command name to the test hook.
func (c *client) observe(name string) {
	if c.opts.OnCommand != nil {
		c.opts.OnCommand(name)
	}
}

// do runs one command. name is the allow-list name; build receives the connected client's builder.
func (c *client) do(ctx context.Context, name string, build func(b valkey.Builder) valkey.Completed) valkey.ValkeyResult {
	inner, err := c.conn()
	if err != nil {
		return valkey.NewErrorResult(err)
	}
	c.observe(name)
	return inner.Do(ctx, build(inner.B()))
}

func (c *client) Get(ctx context.Context, key string) (Value, error) {
	res := c.do(ctx, "GET", func(b valkey.Builder) valkey.Completed { return b.Get().Key(key).Build() })
	return stringValue(res)
}

func stringValue(res valkey.ValkeyResult) (Value, error) {
	s, err := res.ToString()
	if err != nil {
		if valkey.IsValkeyNil(err) {
			return Value{}, nil
		}
		return Value{}, err
	}
	return Value{S: s, OK: true}, nil
}

func messageValues(res valkey.ValkeyResult) ([]Value, error) {
	msgs, err := res.ToArray()
	if err != nil {
		return nil, err
	}
	out := make([]Value, len(msgs))
	for i := range msgs {
		if msgs[i].IsNil() {
			continue
		}
		s, err := msgs[i].ToString()
		if err != nil {
			return nil, err
		}
		out[i] = Value{S: s, OK: true}
	}
	return out, nil
}

func (c *client) MGet(ctx context.Context, keys ...string) ([]Value, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	return messageValues(c.do(ctx, "MGET", func(b valkey.Builder) valkey.Completed { return b.Mget().Key(keys...).Build() }))
}

func (c *client) Set(ctx context.Context, key, value string, o SetOptions) (bool, error) {
	ms := o.TTL.Milliseconds()
	if o.TTL > 0 && ms < 1 {
		ms = 1
	}
	res := c.do(ctx, "SET", func(b valkey.Builder) valkey.Completed {
		s := b.Set().Key(key).Value(value)
		switch {
		case o.NX && ms > 0:
			return s.Nx().PxMilliseconds(ms).Build()
		case o.NX:
			return s.Nx().Build()
		case ms > 0:
			return s.PxMilliseconds(ms).Build()
		}
		return s.Build()
	})
	if err := res.Error(); err != nil {
		if valkey.IsValkeyNil(err) {
			return false, nil // NX and the key exists
		}
		return false, err
	}
	return true, nil
}

func (c *client) Del(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	return c.do(ctx, "DEL", func(b valkey.Builder) valkey.Completed { return b.Del().Key(keys...).Build() }).Error()
}

func (c *client) PExpire(ctx context.Context, key string, ttl time.Duration) error {
	ms := ttl.Milliseconds()
	if ms < 1 {
		ms = 1
	}
	return c.do(ctx, "PEXPIRE", func(b valkey.Builder) valkey.Completed {
		return b.Pexpire().Key(key).Milliseconds(ms).Build()
	}).Error()
}

func (c *client) ZAdd(ctx context.Context, key string, score float64, member string) error {
	return c.do(ctx, "ZADD", func(b valkey.Builder) valkey.Completed {
		return b.Zadd().Key(key).ScoreMember().ScoreMember(score, member).Build()
	}).Error()
}

func (c *client) ZRem(ctx context.Context, key string, members ...string) error {
	if len(members) == 0 {
		return nil
	}
	return c.do(ctx, "ZREM", func(b valkey.Builder) valkey.Completed {
		return b.Zrem().Key(key).Member(members...).Build()
	}).Error()
}

func (c *client) ZCard(ctx context.Context, key string) (int64, error) {
	return c.do(ctx, "ZCARD", func(b valkey.Builder) valkey.Completed { return b.Zcard().Key(key).Build() }).AsInt64()
}

// scoreArg formats a score bound the way ZRANGEBYSCORE reads it, including the infinities.
func scoreArg(f float64) string {
	switch {
	case math.IsInf(f, 1):
		return "+inf"
	case math.IsInf(f, -1):
		return "-inf"
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

func (c *client) ZRangeByScore(ctx context.Context, key string, min, max float64) ([]string, error) {
	return c.do(ctx, "ZRANGEBYSCORE", func(b valkey.Builder) valkey.Completed {
		return b.Zrangebyscore().Key(key).Min(scoreArg(min)).Max(scoreArg(max)).Build()
	}).AsStrSlice()
}

func (c *client) ZRemRangeByScore(ctx context.Context, key string, min, max float64) error {
	return c.do(ctx, "ZREMRANGEBYSCORE", func(b valkey.Builder) valkey.Completed {
		return b.Zremrangebyscore().Key(key).Min(scoreArg(min)).Max(scoreArg(max)).Build()
	}).Error()
}

func (c *client) HSet(ctx context.Context, key string, fields map[string]string) error {
	if len(fields) == 0 {
		return nil
	}
	return c.do(ctx, "HSET", func(b valkey.Builder) valkey.Completed {
		h := b.Hset().Key(key).FieldValue()
		for f, v := range fields {
			h = h.FieldValue(f, v)
		}
		return h.Build()
	}).Error()
}

func (c *client) HMGet(ctx context.Context, key string, fields ...string) ([]Value, error) {
	if len(fields) == 0 {
		return nil, nil
	}
	return messageValues(c.do(ctx, "HMGET", func(b valkey.Builder) valkey.Completed {
		return b.Hmget().Key(key).Field(fields...).Build()
	}))
}

// reply adapts a script result.
type reply struct{ msg valkey.ValkeyMessage }

func (r reply) Ints() ([]int64, error) {
	if arr, err := r.msg.ToArray(); err == nil {
		out := make([]int64, len(arr))
		for i := range arr {
			n, err := arr[i].AsInt64()
			if err != nil {
				return nil, err
			}
			out[i] = n
		}
		return out, nil
	}
	n, err := r.msg.AsInt64()
	if err != nil {
		return nil, err
	}
	return []int64{n}, nil
}

func (r reply) Strings() ([]string, error) {
	if arr, err := r.msg.ToArray(); err == nil {
		out := make([]string, len(arr))
		for i := range arr {
			if arr[i].IsNil() {
				continue
			}
			if arr[i].IsInt64() {
				n, _ := arr[i].AsInt64()
				out[i] = strconv.FormatInt(n, 10)
				continue
			}
			s, err := arr[i].ToString()
			if err != nil {
				return nil, err
			}
			out[i] = s
		}
		return out, nil
	}
	if r.msg.IsNil() {
		return nil, nil
	}
	s, err := r.msg.ToString()
	if err != nil {
		return nil, err
	}
	return []string{s}, nil
}

func (c *client) Eval(ctx context.Context, s *Script, keys, args []string) (Reply, error) {
	res := c.do(ctx, "EVALSHA", func(b valkey.Builder) valkey.Completed {
		return b.Evalsha().Sha1(s.sha).Numkeys(int64(len(keys))).Key(keys...).Arg(args...).Build()
	})
	if verr, ok := valkey.IsValkeyErr(res.Error()); ok && verr.IsNoScript() {
		res = c.do(ctx, "EVAL", func(b valkey.Builder) valkey.Completed {
			return b.Eval().Script(s.source).Numkeys(int64(len(keys))).Key(keys...).Arg(args...).Build()
		})
	}
	msg, err := res.ToMessage()
	if err != nil {
		return nil, err
	}
	return reply{msg: msg}, nil
}

func (c *client) Publish(ctx context.Context, channel, payload string) error {
	return c.do(ctx, "PUBLISH", func(b valkey.Builder) valkey.Completed {
		return b.Publish().Channel(channel).Message(payload).Build()
	}).Error()
}

func (c *client) Ping(ctx context.Context) error {
	return c.do(ctx, "PING", func(b valkey.Builder) valkey.Completed { return b.Ping().Build() }).Error()
}

func (c *client) ServerTime(ctx context.Context) (time.Time, error) {
	parts, err := c.do(ctx, "TIME", func(b valkey.Builder) valkey.Completed { return b.Time().Build() }).AsStrSlice()
	if err != nil {
		return time.Time{}, err
	}
	if len(parts) != 2 {
		return time.Time{}, fmt.Errorf("kv: TIME returned %d fields", len(parts))
	}
	sec, err1 := strconv.ParseInt(parts[0], 10, 64)
	usec, err2 := strconv.ParseInt(parts[1], 10, 64)
	if err1 != nil || err2 != nil {
		return time.Time{}, errors.New("kv: TIME returned a non-numeric value")
	}
	return time.Unix(sec, usec*1000), nil
}

func (c *client) Subscribe(ctx context.Context, channel string, onMessage func(payload string), onResubscribe func()) error {
	backoff := resubscribeMin
	first := true
	for ctx.Err() == nil {
		subscribedOnce, err := c.subscribeOnce(ctx, channel, onMessage, func() {
			if !first && onResubscribe != nil {
				onResubscribe()
			}
			first = false
		})
		if subscribedOnce {
			backoff = resubscribeMin
		} else {
			// An attempt that never subscribed means messages may have been published meanwhile: the subscription
			// that finally succeeds is a re-establishment too, even if it is the first one that worked.
			first = false
		}
		_ = err // the cause (a dropped connection, a refused dial) is retried; there is nothing to report
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > resubscribeMax {
			backoff = resubscribeMax
		}
	}
	return nil
}

// subscribeOnce holds one subscription until its connection drops or ctx ends. It reports whether the
// subscription was confirmed by the server, and calls onSubscribed at that moment.
func (c *client) subscribeOnce(ctx context.Context, channel string, onMessage func(string), onSubscribed func()) (bool, error) {
	if !c.dial() {
		return false, errNotConnected
	}
	inner, err := c.conn()
	if err != nil {
		return false, err
	}
	dc, _ := inner.Dedicate()
	// Close the connection instead of handing it back: returning a subscribed connection to the pool makes the
	// library send PUNSUBSCRIBE, SUNSUBSCRIBE and DISCARD, which are outside the allow-list.
	defer dc.Close()

	acked := make(chan struct{}, 1)
	dropped := dc.SetPubSubHooks(valkey.PubSubHooks{
		OnMessage: func(m valkey.PubSubMessage) { onMessage(m.Message) },
		OnSubscription: func(s valkey.PubSubSubscription) {
			if s.Kind == "subscribe" {
				select {
				case acked <- struct{}{}:
				default:
				}
			}
		},
	})
	c.observe("SUBSCRIBE")
	if err := dc.Do(ctx, dc.B().Subscribe().Channel(channel).Build()).Error(); err != nil {
		return false, err
	}
	select {
	case <-acked:
		onSubscribed()
	case err := <-dropped:
		return false, err
	case <-ctx.Done():
		return false, ctx.Err()
	}
	select {
	case err := <-dropped:
		return true, err
	case <-ctx.Done():
		return true, ctx.Err()
	}
}
