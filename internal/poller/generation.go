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
// It never fails a cycle: a store that is down or slow costs the cache, and readers fall back to Postgres.
func (l *Loop) markGeneration(ctx context.Context, cluster string, changed bool) {
	cache := l.p.cfg.Cache
	if cache == nil {
		return
	}
	// The write must outlive a cycle that is being cancelled: its rows are already committed.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), generationWriteBound)
	defer cancel()

	keys := []snapcache.GenKey{l.genKey("")}
	switch {
	case cluster == "":
		for _, k := range l.currentClusters() {
			keys = append(keys, l.genKey(k.Cluster))
		}
	default:
		keys = append(keys, l.genKey(cluster))
	}
	token := l.nextToken()
	for _, k := range keys {
		var err error
		if changed {
			err = cache.Bump(ctx, k, token)
		} else {
			err = cache.Touch(ctx, k, token)
		}
		if err != nil {
			l.log.Debug("poller: snapshot cache token not written", "cluster", k.Cluster, "err", err)
			return // the store is failing: the other keys would fail the same way
		}
	}
}

// markSliceGone is markGeneration for a slice whose clusters were removed: only the slice's summary changed (a
// removed cluster's own token just expires).
func (l *Loop) markSliceGone(ctx context.Context) {
	cache := l.p.cfg.Cache
	if cache == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), generationWriteBound)
	defer cancel()
	if err := cache.Bump(ctx, l.genKey(""), l.nextToken()); err != nil {
		l.log.Debug("poller: snapshot cache token not written", "err", err)
	}
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
