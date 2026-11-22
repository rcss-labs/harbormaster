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

// ModelSchema names the shape of the cached rows. It is part of every model key, so a release that changes a cached
// row (a field added or renamed in store.ServiceRow, say) bumps it and never reads what an older task stored: during a
// rolling deploy the two versions simply use different entries.
const ModelSchema = "m1"

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
	// BumpAll sets the tokens of all keys (ten minute life) in one round trip, so a slice-wide write is one command
	// with one bound and a slow store cannot drop the later keys.
	BumpAll(ctx context.Context, ks []GenKey, token string) error
	// TouchAll sets a token only where there is none; where one exists it keeps the value and refreshes its life.
	TouchAll(ctx context.Context, ks []GenKey, token string) error
	// Tokens reads the tokens of all keys in one round trip; "" where one is missing.
	Tokens(ctx context.Context, ks ...GenKey) ([]string, error)
	// Get returns the model stored for id under token.
	Get(ctx context.Context, m Model, id, token string) (body []byte, ok bool, err error)
	// Put stores a model for ttl without overwriting an existing one; losing that race is not an error.
	Put(ctx context.Context, m Model, id, token string, body []byte, ttl time.Duration) error
}
