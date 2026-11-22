// Package snapcache keeps unfiltered read models of the poller's AWS-derived rows in Valkey or Redis, under versioned
// keys, and filters them per request in Go. Nothing about tags, permissions or users is ever stored: Postgres says who
// may see a row, the store only says what AWS reported.
package snapcache

import (
	"context"
	"strconv"
	"time"
)

// TokenTTL is how long a generation token lives; every poller cycle refreshes it.
const TokenTTL = 10 * time.Minute

// GenKey names a generation token: a cluster's, or (with Cluster empty) its slice's.
type GenKey struct{ AccountID, Region, Cluster string }

// Model is the kind of a cached read model.
type Model string

const (
	ModelCluster Model = "cluster"
	ModelService Model = "service"
	ModelSlice   Model = "slice"
)

// NewToken builds a generation token from the writer's lease epoch and the time of the change. The epoch makes it
// unique across lease owners and the milliseconds across changes, so a token never repeats after a store restart.
func NewToken(epoch int64, now time.Time) string {
	return strconv.FormatInt(epoch, 10) + "-" + strconv.FormatInt(now.UnixMilli(), 10)
}

// Cache is the store side of the snapshot cache. Every method may fail; callers treat a failure as a miss.
type Cache interface {
	// Bump sets the key's token (ten minute life).
	Bump(ctx context.Context, k GenKey, token string) error
	// Touch sets the token only when there is none; when one exists it keeps the value and refreshes its life.
	Touch(ctx context.Context, k GenKey, token string) error
	// Tokens reads the tokens of all keys in one round trip; "" where one is missing.
	Tokens(ctx context.Context, ks ...GenKey) ([]string, error)
	// Get returns the model stored for id under token.
	Get(ctx context.Context, m Model, id, token string) (body []byte, ok bool, err error)
	// Put stores a model for ttl without overwriting an existing one; losing that race is not an error.
	Put(ctx context.Context, m Model, id, token string, body []byte, ttl time.Duration) error
}

type off struct{}

// Off is the cache used when no store is configured or the cache is switched off: it never hits and stores nothing.
func Off() Cache { return off{} }

func (off) Bump(context.Context, GenKey, string) error  { return nil }
func (off) Touch(context.Context, GenKey, string) error { return nil }
func (off) Tokens(_ context.Context, ks ...GenKey) ([]string, error) {
	return make([]string, len(ks)), nil
}
func (off) Get(context.Context, Model, string, string) ([]byte, bool, error) { return nil, false, nil }
func (off) Put(context.Context, Model, string, string, []byte, time.Duration) error {
	return nil
}
