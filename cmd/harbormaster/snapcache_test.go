package main

import (
	"testing"

	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/kv"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// Without a store, or with HM_SNAPSHOT_CACHE=false, there is no cache at all: the poller writes no tokens and the
// handlers take today's path (a nil reader).
func TestSnapshotCache_OffWithoutAStoreOrWhenSwitchedOff(t *testing.T) {
	health := kv.NewHealth(nil, nil, nil)
	on := config.Config{SnapshotCache: true, SnapshotCacheTTL: 30e9}
	off := config.Config{SnapshotCache: false, SnapshotCacheTTL: 30e9}
	if buildSnapshotCache(nil, on) != nil || buildSnapshotReader(nil, on, &store.Store{}, quietLog()) != nil {
		t.Error("a cache was built without a store")
	}
	if buildSnapshotCache(health, off) != nil || buildSnapshotReader(health, off, &store.Store{}, quietLog()) != nil {
		t.Error("a cache was built with HM_SNAPSHOT_CACHE=false")
	}
	if buildSnapshotCache(health, on) == nil {
		t.Error("no cache with a store and the setting on")
	}
	reader := buildSnapshotReader(health, on, &store.Store{}, quietLog())
	if reader == nil {
		t.Fatal("no reader with a store and the setting on")
	}
	if reader.TTL != on.SnapshotCacheTTL {
		t.Errorf("reader TTL = %v, want HM_SNAPSHOT_CACHE_TTL %v", reader.TTL, on.SnapshotCacheTTL)
	}
	if reader.Up == nil || reader.Up() {
		t.Error("the reader must follow the store's health (down until the first ping)")
	}
}
