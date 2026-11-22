package snapcache

import (
	"context"
	"log/slog"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/telemetry"
)

// loadBound bounds one Postgres load that several requests may be waiting on.
const loadBound = 20 * time.Second

// Reader reads the three models through the cache: the generation token names the model to ask for, a hit decodes it,
// and a miss loads it from Postgres for everyone and stores it under that token. Whatever goes wrong with the store
// (down, slow, garbage) is a Postgres read, never an error for the user. Callers filter what they get per request
// (filter.go); the models are shared between requests and must not be changed.
type Reader struct {
	Cache     Cache
	Store     *store.Store
	TTL       time.Duration
	Telemetry *telemetry.Registry
	Log       *slog.Logger
	// Up reports whether the shared store is usable; nil means always. A store that is down is not asked.
	Up func() bool

	// Loaders, replaceable by tests that count Postgres reads.
	loadCluster func(context.Context, *store.Store, model.ClusterKey) (ClusterModel, error)
	loadService func(context.Context, *store.Store, model.ServiceKey) (ServiceModel, error)
	loadSlice   func(context.Context, *store.Store, awsx.Target) (SliceSummary, error)

	group singleflight.Group
}

// Cluster returns the cluster model, or store.ErrNotFound.
func (r *Reader) Cluster(ctx context.Context, k model.ClusterKey) (ClusterModel, error) {
	load := r.loadCluster
	if load == nil {
		load = LoadCluster
	}
	return read(ctx, r, ModelCluster, k.ID(), GenKey{AccountID: k.AccountID, Region: k.Region, Cluster: k.Cluster},
		func(ctx context.Context) (ClusterModel, error) { return load(ctx, r.Store, k) })
}

// Service returns the service model, which follows its cluster's token, or store.ErrNotFound.
func (r *Reader) Service(ctx context.Context, k model.ServiceKey) (ServiceModel, error) {
	load := r.loadService
	if load == nil {
		load = LoadService
	}
	return read(ctx, r, ModelService, k.ID(), GenKey{AccountID: k.AccountID, Region: k.Region, Cluster: k.Cluster},
		func(ctx context.Context) (ServiceModel, error) { return load(ctx, r.Store, k) })
}

// Slice returns one account x region's summary.
func (r *Reader) Slice(ctx context.Context, t awsx.Target) (SliceSummary, error) {
	load := r.loadSlice
	if load == nil {
		load = LoadSlice
	}
	return read(ctx, r, ModelSlice, t.AccountID+":"+t.Region, GenKey{AccountID: t.AccountID, Region: t.Region},
		func(ctx context.Context) (SliceSummary, error) { return load(ctx, r.Store, t) })
}

func (r *Reader) count(m Model, result string) {
	if r.Telemetry != nil {
		r.Telemetry.CountSnapshotCache(string(m), result)
	}
}

func (r *Reader) warn(msg string, err error) {
	if r.Log != nil {
		r.Log.Debug(msg, "err", err)
	}
}

func read[T any](ctx context.Context, r *Reader, m Model, id string, gk GenKey, load func(context.Context) (T, error)) (T, error) {
	var zero T
	if r.Up != nil && !r.Up() {
		r.count(m, "bypass")
		return load(ctx)
	}
	tokens, err := r.Cache.Tokens(ctx, gk)
	if err != nil {
		r.warn("snapshot cache token read failed; reading Postgres", err)
		r.count(m, "error")
		return load(ctx)
	}
	token := ""
	if len(tokens) > 0 {
		token = tokens[0]
	}
	if token == "" {
		// The poller has not marked this cluster (yet): nothing says which model is current, so nothing is stored.
		r.count(m, "bypass")
		return load(ctx)
	}
	body, ok, err := r.Cache.Get(ctx, m, id, token)
	if err != nil {
		r.warn("snapshot cache read failed; reading Postgres", err)
		r.count(m, "error")
		return load(ctx)
	}
	if ok {
		var out T
		if err := Decode(body, &out); err == nil {
			r.count(m, "hit")
			return out, nil
		} else {
			r.warn("snapshot cache entry undecodable; reading Postgres", err)
		}
		r.count(m, "error")
		return load(ctx)
	}

	// Miss: load for everyone and store under the token that was read. A poller that commits meanwhile has bumped the
	// token, so what is stored here is never asked for again.
	v, err, _ := r.group.Do(string(m)+"|"+id+"|"+token, func() (any, error) {
		lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), loadBound)
		defer cancel()
		built, err := load(lctx)
		if err != nil {
			return zero, err
		}
		if body, err := Encode(built); err == nil {
			if err := r.Cache.Put(lctx, m, id, token, body, r.TTL); err != nil {
				r.warn("snapshot cache store failed", err)
			}
		} else {
			r.warn("snapshot model not encodable", err)
		}
		return built, nil
	})
	if err != nil {
		return zero, err
	}
	r.count(m, "miss")
	return v.(T), nil
}
