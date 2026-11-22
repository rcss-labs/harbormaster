package snapcache

import (
	"context"
	"strconv"
	"time"

	"github.com/rcss-labs/harbormaster/internal/kv"
)

type kvCache struct{ c kv.Client }

// NewKV keeps tokens and models in the shared store.
func NewKV(c kv.Client) Cache { return kvCache{c: c} }

func (k kvCache) genKey(g GenKey) string {
	if g.Cluster == "" {
		return k.c.Key("gen", g.AccountID, g.Region)
	}
	return k.c.Key("gen", g.AccountID, g.Region, g.Cluster)
}

func (k kvCache) modelKey(m Model, id, token string) string {
	return k.c.Key("snap", string(m), ModelSchema, id, token)
}

// The scripts write every key in one round trip. They use only redis.call and no clock: the life is an argument.
// KEYS: the token keys. ARGV: token, life in ms.
var bumpAllScript = kv.NewScript(`
for _, key in ipairs(KEYS) do
  redis.call('SET', key, ARGV[1], 'PX', tonumber(ARGV[2]))
end
return #KEYS`)

// Same arguments; a key that exists keeps its token and only gets its life back.
var touchAllScript = kv.NewScript(`
for _, key in ipairs(KEYS) do
  if not redis.call('SET', key, ARGV[1], 'NX', 'PX', tonumber(ARGV[2])) then
    redis.call('PEXPIRE', key, tonumber(ARGV[2]))
  end
end
return #KEYS`)

func (k kvCache) genKeys(gs []GenKey) []string {
	keys := make([]string, len(gs))
	for i, g := range gs {
		keys[i] = k.genKey(g)
	}
	return keys
}

func (k kvCache) BumpAll(ctx context.Context, gs []GenKey, token string) error {
	if len(gs) == 0 {
		return nil
	}
	_, err := k.c.Eval(ctx, bumpAllScript, k.genKeys(gs), []string{token, strconv.FormatInt(TokenTTL.Milliseconds(), 10)})
	return err
}

func (k kvCache) TouchAll(ctx context.Context, gs []GenKey, token string) error {
	if len(gs) == 0 {
		return nil
	}
	_, err := k.c.Eval(ctx, touchAllScript, k.genKeys(gs), []string{token, strconv.FormatInt(TokenTTL.Milliseconds(), 10)})
	return err
}

func (k kvCache) Tokens(ctx context.Context, gs ...GenKey) ([]string, error) {
	out := make([]string, len(gs))
	if len(gs) == 0 {
		return out, nil
	}
	keys := make([]string, len(gs))
	for i, g := range gs {
		keys[i] = k.genKey(g)
	}
	vals, err := k.c.MGet(ctx, keys...)
	if err != nil {
		return nil, err
	}
	for i := range out {
		if i < len(vals) && vals[i].OK {
			out[i] = vals[i].S
		}
	}
	return out, nil
}

// lookupScript reads a token and the model it names in one call. KEYS[1]: the token key. ARGV[1]: the model key up to
// and including the separator before the token. It returns {} without a token, {token, false} for a miss.
var lookupScript = kv.NewScript(`
local token = redis.call('GET', KEYS[1])
if not token then return {} end
return {token, redis.call('GET', ARGV[1] .. token)}`)

func (k kvCache) Lookup(ctx context.Context, m Model, id string, g GenKey) (string, []byte, bool, error) {
	rep, err := k.c.Eval(ctx, lookupScript, []string{k.genKey(g)}, []string{k.modelKey(m, id, "")})
	if err != nil {
		return "", nil, false, err
	}
	parts, err := rep.Strings()
	if err != nil {
		return "", nil, false, err
	}
	if len(parts) == 0 || parts[0] == "" {
		return "", nil, false, nil
	}
	if len(parts) < 2 || parts[1] == "" { // a stored model is never empty (it is gzip)
		return parts[0], nil, false, nil
	}
	return parts[0], []byte(parts[1]), true, nil
}

func (k kvCache) GetMany(ctx context.Context, m Model, ids, tokens []string) ([]Entry, error) {
	out := make([]Entry, len(ids))
	var keys []string
	var at []int
	for i, id := range ids {
		if tokens[i] == "" {
			continue
		}
		keys = append(keys, k.modelKey(m, id, tokens[i]))
		at = append(at, i)
	}
	if len(keys) == 0 {
		return out, nil
	}
	vals, err := k.c.MGet(ctx, keys...)
	if err != nil {
		return nil, err
	}
	for j, i := range at {
		if j < len(vals) && vals[j].OK {
			out[i] = Entry{Body: []byte(vals[j].S), OK: true}
		}
	}
	return out, nil
}

func (k kvCache) Get(ctx context.Context, m Model, id, token string) ([]byte, bool, error) {
	v, err := k.c.Get(ctx, k.modelKey(m, id, token))
	if err != nil || !v.OK {
		return nil, false, err
	}
	return []byte(v.S), true, nil
}

func (k kvCache) Put(ctx context.Context, m Model, id, token string, body []byte, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = TokenTTL // a model with no life would outlive every token that could ask for it
	}
	_, err := k.c.Set(ctx, k.modelKey(m, id, token), string(body), kv.SetOptions{TTL: ttl, NX: true})
	return err
}
