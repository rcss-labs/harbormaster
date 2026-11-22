package limits

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"sync"
	"time"

	"github.com/rcss-labs/harbormaster/internal/kv"
)

// Lua helper shared by the scripts: now in milliseconds from the server clock, so tasks with skewed clocks agree.
// Only redis.call is used, and time is never taken from the caller.
const luaNow = `local t = redis.call('TIME'); local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000);`

// KEYS[1] set; ARGV: max, expiry ms, member, key ttl ms. Returns 1 when the slot was taken, 0 when full.
var acquireScript = kv.NewScript(luaNow + `
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now)
if redis.call('ZCARD', KEYS[1]) >= tonumber(ARGV[1]) then return 0 end
redis.call('ZADD', KEYS[1], now + tonumber(ARGV[2]), ARGV[3])
redis.call('PEXPIRE', KEYS[1], tonumber(ARGV[4]))
return 1`)

// KEYS[1] set; ARGV: expiry ms, member, key ttl ms. Extends one holder's expiry.
var refreshScript = kv.NewScript(luaNow + `
redis.call('ZADD', KEYS[1], now + tonumber(ARGV[1]), ARGV[2])
redis.call('PEXPIRE', KEYS[1], tonumber(ARGV[3]))
return 1`)

// KEYS[1] set. Returns the number of unexpired holders.
var countScript = kv.NewScript(luaNow + `
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now)
return redis.call('ZCARD', KEYS[1])`)

// KVConcurrency counts sessions in the store: a sorted set per limit and user whose members are session ids scored
// by their expiry. A holder re-adds its member every Refresh, so a task that dies loses its slots within Expiry.
type KVConcurrency struct {
	// Refresh, Expiry and KeyTTL are fields so tests can shorten them.
	Refresh time.Duration // 15 s
	Expiry  time.Duration // 45 s
	KeyTTL  time.Duration // 60 s

	c        kv.Client
	stopCtx  context.Context
	stopAll  context.CancelFunc
	releases sync.WaitGroup
}

// NewKVConcurrency builds the shared counter on c.
func NewKVConcurrency(c kv.Client) *KVConcurrency {
	ctx, cancel := context.WithCancel(context.Background())
	return &KVConcurrency{
		Refresh: 15 * time.Second,
		Expiry:  45 * time.Second,
		KeyTTL:  60 * time.Second,
		c:       c,
		stopCtx: ctx,
		stopAll: cancel,
	}
}

// Close stops every refresher without removing its member, as if the task had died: the slots expire on their own.
func (k *KVConcurrency) Close() {
	k.stopAll()
	k.releases.Wait()
}

func (k *KVConcurrency) key(limit string, userID int64) string {
	return k.c.Key("lim", limit, strconv.FormatInt(userID, 10))
}

func (k *KVConcurrency) Acquire(ctx context.Context, limit string, userID int64, max int) (func(), bool, error) {
	key := k.key(limit, userID)
	member := randomID()
	reply, err := k.c.Eval(ctx, acquireScript, []string{key}, []string{
		strconv.Itoa(max), kv.Millis(k.Expiry), member, kv.Millis(k.KeyTTL),
	})
	if err != nil {
		return func() {}, false, err
	}
	got, err := reply.Ints()
	if err != nil || len(got) != 1 {
		return func() {}, false, err
	}
	if got[0] != 1 {
		return func() {}, false, nil
	}

	stop := make(chan struct{})
	var once sync.Once
	k.releases.Add(1)
	go func() {
		defer k.releases.Done()
		tick := time.NewTicker(k.Refresh)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-k.stopCtx.Done():
				return
			case <-tick.C:
				rctx, cancel := context.WithTimeout(k.stopCtx, 2*time.Second)
				_, _ = k.c.Eval(rctx, refreshScript, []string{key}, []string{kv.Millis(k.Expiry), member, kv.Millis(k.KeyTTL)})
				cancel()
			}
		}
	}()
	return func() {
		once.Do(func() {
			close(stop)
			// Not the request context, which may already be cancelled; the client bounds the call.
			rctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = k.c.ZRem(rctx, key, member)
		})
	}, true, nil
}

func (k *KVConcurrency) Count(ctx context.Context, limit string, userID int64) (int, error) {
	reply, err := k.c.Eval(ctx, countScript, []string{k.key(limit, userID)}, nil)
	if err != nil {
		return 0, err
	}
	got, err := reply.Ints()
	if err != nil || len(got) != 1 {
		return 0, err
	}
	return int(got[0]), nil
}

func randomID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
