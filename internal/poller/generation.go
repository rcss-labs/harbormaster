package poller

import (
	"context"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/snapcache"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// generationWriteBound bounds one token write, so a slow store never slows a polling cycle.
const generationWriteBound = 250 * time.Millisecond

// markGeneration tells the snapshot cache that a cluster's rows changed (changed) or were checked and found the same.
// cluster "" means the whole slice. A change sets a new token for the cluster and for the slice; no change sets them
// only when missing and keeps them alive, so a quiet cluster keeps one token, and with it one cached model, for as
// long as it stays quiet. Every cycle that writes calls it after its commit, so a reader that finds the new token
// finds the new rows.
//
// It never fails a cycle: a store that is down or slow costs the cache, and readers fall back to Postgres. A bump that
// fails is not forgotten: the key is remembered as owed a bump, and the next call that covers it (every pass over the
// cluster does, and every call covers the slice) bumps it even when AWS shows nothing new.
func (l *Loop) markGeneration(ctx context.Context, cluster string, changed bool) {
	cache := l.p.cfg.Cache
	if cache == nil {
		return
	}
	keys := []snapcache.GenKey{l.genKey("")}
	switch {
	case cluster == "":
		for _, k := range l.currentClusters() {
			keys = append(keys, l.genKey(k.Cluster))
		}
	default:
		keys = append(keys, l.genKey(cluster))
	}
	var bump, touch []snapcache.GenKey
	l.genMu.Lock()
	for _, k := range keys {
		if changed || l.genOwed[k] {
			bump = append(bump, k)
		} else {
			touch = append(touch, k)
		}
	}
	l.genMu.Unlock()
	token := l.nextToken()
	// The write must outlive a cycle that is being cancelled: its rows are already committed.
	ctx = context.WithoutCancel(ctx)
	if len(bump) > 0 {
		l.bumpKeys(ctx, bump, token)
	}
	if len(touch) > 0 {
		wctx, cancel := context.WithTimeout(ctx, generationWriteBound)
		err := cache.TouchAll(wctx, touch, token)
		cancel()
		if err != nil {
			l.p.cfg.Telemetry.CountSnapshotTokenFailure()
			l.log.Debug("poller: snapshot cache touch failed", "keys", len(touch), "err", err)
		}
	}
}

// bumpKeys writes one new token to all the keys in one command, and remembers them as owed a bump when it fails.
func (l *Loop) bumpKeys(ctx context.Context, keys []snapcache.GenKey, token string) {
	wctx, cancel := context.WithTimeout(ctx, generationWriteBound)
	err := l.p.cfg.Cache.BumpAll(wctx, keys, token)
	cancel()
	l.genMu.Lock()
	for _, k := range keys {
		if err == nil {
			delete(l.genOwed, k)
			continue
		}
		if l.genOwed == nil {
			l.genOwed = map[snapcache.GenKey]bool{}
		}
		l.genOwed[k] = true
	}
	l.genMu.Unlock()
	if err != nil {
		l.p.cfg.Telemetry.CountSnapshotTokenFailure()
		l.log.Debug("poller: snapshot cache bump failed; it will be retried", "keys", len(keys), "err", err)
	}
}

// markSliceGone is markGeneration for a slice whose clusters were removed: the slice's summary changed, and so did the
// page of each removed cluster, which readers learn from its own token.
func (l *Loop) markSliceGone(ctx context.Context, clusters []string) {
	if l.p.cfg.Cache == nil {
		return
	}
	keys := []snapcache.GenKey{l.genKey("")}
	for _, name := range clusters {
		keys = append(keys, l.genKey(name))
	}
	l.bumpKeys(context.WithoutCancel(ctx), keys, l.nextToken())
}

func (l *Loop) genKey(cluster string) snapcache.GenKey {
	return snapcache.GenKey{AccountID: l.target.AccountID, Region: l.target.Region, Cluster: cluster}
}

func (l *Loop) currentClusters() []model.ClusterKey {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]model.ClusterKey(nil), l.clusters...)
}

// nextToken is a token for the lease epoch and the current millisecond, made strictly later than the loop's previous
// one: two cycles that commit within the same millisecond must not share a token, or a model read between their
// commits would be served for the second one's rows.
func (l *Loop) nextToken() string {
	l.genMu.Lock()
	defer l.genMu.Unlock()
	now := time.Now()
	if ms := now.UnixMilli(); ms <= l.genLastMS {
		now = time.UnixMilli(l.genLastMS + 1)
	}
	l.genLastMS = now.UnixMilli()
	return snapcache.NewToken(l.w.Lease().Epoch, now)
}

// changedClusterNames names the clusters whose row differs from what this loop last wrote (all of them when it cannot
// tell, which is safer than none).
func (l *Loop) changedClusterNames(rows []model.Cluster) []string {
	l.genMu.Lock()
	defer l.genMu.Unlock()
	var changed, all []string
	for _, c := range rows {
		all = append(all, c.Key.Cluster)
		if l.clusterHashes[c.Key] != store.ContentHash(c) {
			changed = append(changed, c.Key.Cluster)
		}
	}
	if len(changed) == 0 {
		return all
	}
	return changed
}

func (l *Loop) rememberClusterHashes(rows []model.Cluster) {
	l.genMu.Lock()
	defer l.genMu.Unlock()
	if l.clusterHashes == nil {
		l.clusterHashes = map[model.ClusterKey]string{}
	}
	for _, c := range rows {
		l.clusterHashes[c.Key] = store.ContentHash(c)
	}
}

// forgetClusterHashes drops the hashes of clusters that are no longer listed.
func (l *Loop) forgetClusterHashes(keep []model.ClusterKey) {
	l.genMu.Lock()
	defer l.genMu.Unlock()
	live := make(map[model.ClusterKey]bool, len(keep))
	for _, k := range keep {
		live[k] = true
	}
	for k := range l.clusterHashes {
		if !live[k] {
			delete(l.clusterHashes, k)
		}
	}
}
