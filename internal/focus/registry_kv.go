package focus

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/rcss-labs/harbormaster/internal/kv"
)

// The scripts read time from the server (TIME), never from the caller, so tasks with skewed clocks agree. Only
// redis.call is used. Membership is found by scanning ZRANGEBYSCORE (at most MaxObjects members) because ZSCORE is not
// in the command allow-list.
const luaNow = `local t = redis.call('TIME'); local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000);`

// KEYS: estate set, user set. ARGV: member, ttl ms, max objects, max per user, user key ttl ms, estate key ttl ms.
// Returns 1 admitted, 2 refreshed, 3 capped, 4 user capped.
var heartbeatScript = kv.NewScript(luaNow + `
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now)
redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', now)
local function has(key, member)
  for _, m in ipairs(redis.call('ZRANGEBYSCORE', key, '-inf', '+inf')) do
    if m == member then return true end
  end
  return false
end
local inEstate = has(KEYS[1], ARGV[1])
local inUser = has(KEYS[2], ARGV[1])
if (not inUser) and redis.call('ZCARD', KEYS[2]) >= tonumber(ARGV[4]) then return 4 end
local result = 2
if not inEstate then
  if redis.call('ZCARD', KEYS[1]) >= tonumber(ARGV[3]) then return 3 end
  result = 1
end
local expiry = now + tonumber(ARGV[2])
redis.call('ZADD', KEYS[1], expiry, ARGV[1])
redis.call('ZADD', KEYS[2], expiry, ARGV[1])
redis.call('PEXPIRE', KEYS[1], tonumber(ARGV[6]))
redis.call('PEXPIRE', KEYS[2], tonumber(ARGV[5]))
return result`)

// KEYS: estate set, user set. ARGV: member, grace ms. Removes the user's claim and lowers the estate member's expiry to
// now + grace when it is later; never raises it.
var releaseScript = kv.NewScript(luaNow + `
redis.call('ZREM', KEYS[2], ARGV[1])
local limit = now + tonumber(ARGV[2])
local list = redis.call('ZRANGEBYSCORE', KEYS[1], '(' .. now, '+inf', 'WITHSCORES')
for i = 1, #list, 2 do
  if list[i] == ARGV[1] and tonumber(list[i + 1]) > limit then
    redis.call('ZADD', KEYS[1], limit, ARGV[1])
  end
end
return 1`)

type kvRegistry struct{ c kv.Client }

// NewKVRegistry keeps the registry in the shared store, so every web task and poller sees the same set.
func NewKVRegistry(c kv.Client) Registry { return &kvRegistry{c: c} }

func (r *kvRegistry) estateKey() string { return r.c.Key("focus") }

func (r *kvRegistry) userKey(userID int64) string {
	return r.c.Key("focus", "u", strconv.FormatInt(userID, 10))
}

func (r *kvRegistry) Heartbeat(ctx context.Context, o Object, userID int64, lim Limits) (Admission, error) {
	reply, err := r.c.Eval(ctx, heartbeatScript, []string{r.estateKey(), r.userKey(userID)}, []string{
		o.Member(), kv.Millis(lim.TTL), strconv.Itoa(lim.MaxObjects), strconv.Itoa(lim.MaxPerUser),
		kv.Millis(userKeyTTL), kv.Millis(2 * lim.TTL),
	})
	if err != nil {
		return "", err
	}
	got, err := reply.Ints()
	if err != nil || len(got) != 1 {
		return "", fmt.Errorf("focus: heartbeat reply: %v", err)
	}
	switch got[0] {
	case 1:
		return Admitted, nil
	case 2:
		return Refreshed, nil
	case 3:
		return Capped, nil
	case 4:
		return UserCapped, nil
	}
	return "", fmt.Errorf("focus: heartbeat reply %d", got[0])
}

func (r *kvRegistry) Release(ctx context.Context, o Object, userID int64) error {
	_, err := r.c.Eval(ctx, releaseScript, []string{r.estateKey(), r.userKey(userID)}, []string{o.Member(), kv.Millis(ReleaseGrace)})
	return err
}

// KEYS: estate set. Returns the members that have not expired on the server's clock; the exclusive bound drops one
// that expires exactly now.
var listScript = kv.NewScript(luaNow + `
return redis.call('ZRANGEBYSCORE', KEYS[1], '(' .. now, '+inf')`)

func (r *kvRegistry) List(ctx context.Context) ([]Object, error) {
	reply, err := r.c.Eval(ctx, listScript, []string{r.estateKey()}, nil)
	if err != nil {
		return nil, err
	}
	members, err := reply.Strings()
	if err != nil {
		return nil, err
	}
	return parseMembers(members), nil
}

// freshJSON is the stored form of Fresh.
type freshJSON struct {
	AtMS       int64  `json:"at_ms"`
	IntervalMS int64  `json:"interval_ms"`
	State      string `json:"state"`
}

func (r *kvRegistry) freshKey(o Object) string { return r.c.Key("fresh", o.Member()) }

func (r *kvRegistry) SetFresh(ctx context.Context, o Object, f Fresh) error {
	body, err := json.Marshal(freshJSON{AtMS: f.At.UnixMilli(), IntervalMS: f.Interval.Milliseconds(), State: f.State})
	if err != nil {
		return err
	}
	_, err = r.c.Set(ctx, r.freshKey(o), string(body), kv.SetOptions{TTL: freshTTL})
	return err
}

func (r *kvRegistry) Fresh(ctx context.Context, o Object) (Fresh, bool, error) {
	v, err := r.c.Get(ctx, r.freshKey(o))
	if err != nil || !v.OK {
		return Fresh{}, false, err
	}
	var j freshJSON
	if err := json.Unmarshal([]byte(v.S), &j); err != nil {
		return Fresh{}, false, nil // an unreadable entry is no evidence
	}
	return Fresh{At: time.UnixMilli(j.AtMS).UTC(), Interval: time.Duration(j.IntervalMS) * time.Millisecond, State: j.State}, true, nil
}
