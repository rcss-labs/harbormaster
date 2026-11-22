package snapcache

import (
	"context"
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
	return k.c.Key("snap", string(m), id, token)
}

func (k kvCache) Bump(ctx context.Context, g GenKey, token string) error {
	_, err := k.c.Set(ctx, k.genKey(g), token, kv.SetOptions{TTL: TokenTTL})
	return err
}

func (k kvCache) Touch(ctx context.Context, g GenKey, token string) error {
	key := k.genKey(g)
	stored, err := k.c.Set(ctx, key, token, kv.SetOptions{TTL: TokenTTL, NX: true})
	if err != nil || stored {
		return err
	}
	return k.c.PExpire(ctx, key, TokenTTL)
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

func (k kvCache) Get(ctx context.Context, m Model, id, token string) ([]byte, bool, error) {
	v, err := k.c.Get(ctx, k.modelKey(m, id, token))
	if err != nil || !v.OK {
		return nil, false, err
	}
	return []byte(v.S), true, nil
}

func (k kvCache) Put(ctx context.Context, m Model, id, token string, body []byte, ttl time.Duration) error {
	_, err := k.c.Set(ctx, k.modelKey(m, id, token), string(body), kv.SetOptions{TTL: ttl, NX: true})
	return err
}
