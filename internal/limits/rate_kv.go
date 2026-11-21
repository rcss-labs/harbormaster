package limits

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"strconv"
	"time"

	"github.com/rcss-labs/harbormaster/internal/kv"
)

// maxKeyBytes is the longest caller-supplied key used as is; longer ones are hashed so an IP or id cannot grow a
// store key without bound.
const maxKeyBytes = 128

// KEYS[1] hash; ARGV: capacity, refill per second, key ttl ms. Returns 1 when a token was taken.
var bucketScript = kv.NewScript(luaNow + `
local v = redis.call('HMGET', KEYS[1], 'tokens', 'last')
local capacity = tonumber(ARGV[1])
local tokens = tonumber(v[1])
local last = tonumber(v[2])
if not tokens or not last then tokens = capacity; last = now end
local elapsed = math.max(0, now - last) / 1000
tokens = math.min(capacity, tokens + elapsed * tonumber(ARGV[2]))
local allowed = 0
if tokens >= 1 then tokens = tokens - 1; allowed = 1 end
redis.call('HSET', KEYS[1], 'tokens', tostring(tokens), 'last', tostring(now))
redis.call('PEXPIRE', KEYS[1], tonumber(ARGV[3]))
return allowed`)

// KEYS[1] hash; ARGV: interval ms. Books the next slot and returns the wait in ms.
var reserveScript = kv.NewScript(luaNow + `
local v = redis.call('HMGET', KEYS[1], 'next')
local every = tonumber(ARGV[1])
local nxt = tonumber(v[1]) or 0
local slot = math.max(now, nxt)
redis.call('HSET', KEYS[1], 'next', tostring(slot + every))
redis.call('PEXPIRE', KEYS[1], 2 * every + (slot - now))
return slot - now`)

type kvRate struct{ c kv.Client }

// NewKVRate keeps the buckets and pacer slots in the store, so every task draws from the same ones.
func NewKVRate(c kv.Client) Rate { return &kvRate{c: c} }

func (r *kvRate) key(name, key string) string {
	if len(key) > maxKeyBytes {
		sum := sha256.Sum256([]byte(key))
		key = hex.EncodeToString(sum[:])
	}
	return r.c.Key("rate", name, key)
}

func (r *kvRate) Allow(ctx context.Context, name, key string, capacity int, refillPerSecond float64) (bool, error) {
	ttl := time.Hour
	if refillPerSecond > 0 {
		ttl = time.Duration(2 * float64(capacity) / refillPerSecond * float64(time.Second))
	}
	ttlMS := max(ttl.Milliseconds(), 1000)
	reply, err := r.c.Eval(ctx, bucketScript, []string{r.key(name, key)}, []string{
		strconv.Itoa(capacity), strconv.FormatFloat(refillPerSecond, 'f', -1, 64), strconv.FormatInt(ttlMS, 10),
	})
	if err != nil {
		return false, err
	}
	got, err := reply.Ints()
	if err != nil || len(got) != 1 {
		return false, err
	}
	return got[0] == 1, nil
}

func (r *kvRate) Reserve(ctx context.Context, name, key string, every time.Duration) (time.Duration, error) {
	everyMS := max(every.Milliseconds(), 1)
	reply, err := r.c.Eval(ctx, reserveScript, []string{r.key(name, key)}, []string{strconv.FormatInt(everyMS, 10)})
	if err != nil {
		return 0, err
	}
	got, err := reply.Ints()
	if err != nil || len(got) != 1 {
		return 0, err
	}
	if got[0] < 0 || got[0] > math.MaxInt32 {
		got[0] = 0
	}
	return time.Duration(got[0]) * time.Millisecond, nil
}
