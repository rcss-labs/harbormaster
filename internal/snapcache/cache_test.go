package snapcache

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/kv"
	"github.com/rcss-labs/harbormaster/internal/kv/kvtest"
)

var (
	ctx       = context.Background()
	clusterK  = GenKey{AccountID: "111111111111", Region: "us-east-1", Cluster: "prod"}
	sliceK    = GenKey{AccountID: "111111111111", Region: "us-east-1"}
	otherSlcK = GenKey{AccountID: "111111111111", Region: "eu-west-1"}
)

func eachCache(t *testing.T, fn func(t *testing.T, c Cache, raw kv.Client)) {
	t.Helper()
	kvtest.Each(t, func(t *testing.T, raw kv.Client) { fn(t, NewKV(raw), raw) })
}

func mustTokens(t *testing.T, c Cache, ks ...GenKey) []string {
	t.Helper()
	got, err := c.Tokens(ctx, ks...)
	if err != nil {
		t.Fatalf("Tokens: %v", err)
	}
	return got
}

func TestCache_MissThenPutThenHit(t *testing.T) {
	eachCache(t, func(t *testing.T, c Cache, _ kv.Client) {
		if _, ok, err := c.Get(ctx, ModelCluster, "c1", "1-100"); err != nil || ok {
			t.Fatalf("Get before Put = ok %v err %v, want a miss", ok, err)
		}
		body := []byte("\x1f\x8b binary \x00 body")
		if err := c.Put(ctx, ModelCluster, "c1", "1-100", body, time.Minute); err != nil {
			t.Fatalf("Put: %v", err)
		}
		got, ok, err := c.Get(ctx, ModelCluster, "c1", "1-100")
		if err != nil || !ok || string(got) != string(body) {
			t.Fatalf("Get after Put = %q ok %v err %v, want the stored bytes", got, ok, err)
		}
	})
}

func TestCache_BumpChangesWhatReadersAskFor(t *testing.T) {
	eachCache(t, func(t *testing.T, c Cache, _ kv.Client) {
		if err := c.BumpAll(ctx, []GenKey{clusterK}, "1-100"); err != nil {
			t.Fatal(err)
		}
		if got := mustTokens(t, c, clusterK); got[0] != "1-100" {
			t.Fatalf("token = %q, want 1-100", got[0])
		}
		if err := c.Put(ctx, ModelCluster, "c1", "1-100", []byte("old"), time.Minute); err != nil {
			t.Fatal(err)
		}
		if err := c.BumpAll(ctx, []GenKey{clusterK}, "1-200"); err != nil {
			t.Fatal(err)
		}
		tok := mustTokens(t, c, clusterK)[0]
		if tok != "1-200" {
			t.Fatalf("token after Bump = %q, want 1-200", tok)
		}
		if _, ok, _ := c.Get(ctx, ModelCluster, "c1", tok); ok {
			t.Fatal("a read under the new token found the old model")
		}
	})
}

func TestCache_TouchKeepsAnExistingTokenAndRefreshesItsTTL(t *testing.T) {
	eachCache(t, func(t *testing.T, c Cache, raw kv.Client) {
		if err := c.BumpAll(ctx, []GenKey{clusterK}, "1-100"); err != nil {
			t.Fatal(err)
		}
		// Shorten the token's life, then touch: the value stays and the life is back to minutes.
		if err := raw.PExpire(ctx, raw.Key("gen", clusterK.AccountID, clusterK.Region, clusterK.Cluster), 1500*time.Millisecond); err != nil {
			t.Fatal(err)
		}
		if err := c.TouchAll(ctx, []GenKey{clusterK}, "1-999"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Second)
		if got := mustTokens(t, c, clusterK); got[0] != "1-100" {
			t.Fatalf("token after Touch and 2 s = %q, want the original 1-100 still there", got[0])
		}
	})
}

func TestCache_TouchSetsAMissingToken(t *testing.T) {
	eachCache(t, func(t *testing.T, c Cache, _ kv.Client) {
		if err := c.TouchAll(ctx, []GenKey{sliceK}, "2-5"); err != nil {
			t.Fatal(err)
		}
		if got := mustTokens(t, c, sliceK); got[0] != "2-5" {
			t.Fatalf("token = %q, want 2-5", got[0])
		}
	})
}

func TestCache_TokensReturnsEmptyForMissing(t *testing.T) {
	eachCache(t, func(t *testing.T, c Cache, _ kv.Client) {
		if err := c.BumpAll(ctx, []GenKey{clusterK}, "1-1"); err != nil {
			t.Fatal(err)
		}
		got := mustTokens(t, c, clusterK, sliceK, otherSlcK)
		if len(got) != 3 || got[0] != "1-1" || got[1] != "" || got[2] != "" {
			t.Fatalf("tokens = %q, want [1-1 \"\" \"\"]", got)
		}
		if got := mustTokens(t, c); len(got) != 0 {
			t.Fatalf("tokens for no keys = %q", got)
		}
	})
}

func TestCache_ClusterAndSliceKeysDoNotCollide(t *testing.T) {
	eachCache(t, func(t *testing.T, c Cache, _ kv.Client) {
		if err := c.BumpAll(ctx, []GenKey{sliceK}, "1-1"); err != nil {
			t.Fatal(err)
		}
		if got := mustTokens(t, c, clusterK); got[0] != "" {
			t.Fatalf("the cluster token = %q after bumping only the slice", got[0])
		}
	})
}

func TestCache_ModelsExpireAfterTTL(t *testing.T) {
	eachCache(t, func(t *testing.T, c Cache, _ kv.Client) {
		if err := c.Put(ctx, ModelService, "s1", "1-1", []byte("x"), 1200*time.Millisecond); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := c.Get(ctx, ModelService, "s1", "1-1"); !ok {
			t.Fatal("a fresh model was not served")
		}
		time.Sleep(1600 * time.Millisecond)
		if _, ok, _ := c.Get(ctx, ModelService, "s1", "1-1"); ok {
			t.Fatal("the model outlived its TTL")
		}
	})
}

func TestCache_PutDoesNotOverwrite(t *testing.T) {
	eachCache(t, func(t *testing.T, c Cache, _ kv.Client) {
		if err := c.Put(ctx, ModelSlice, "a:r", "1-1", []byte("first"), time.Minute); err != nil {
			t.Fatal(err)
		}
		if err := c.Put(ctx, ModelSlice, "a:r", "1-1", []byte("second"), time.Minute); err != nil {
			t.Fatalf("a lost race must not be an error: %v", err)
		}
		got, _, _ := c.Get(ctx, ModelSlice, "a:r", "1-1")
		if string(got) != "first" {
			t.Fatalf("model = %q, want the first writer's", got)
		}
	})
}

func TestCache_ModelKindsDoNotCollide(t *testing.T) {
	eachCache(t, func(t *testing.T, c Cache, _ kv.Client) {
		if err := c.Put(ctx, ModelCluster, "same", "1-1", []byte("cluster"), time.Minute); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := c.Get(ctx, ModelService, "same", "1-1"); ok {
			t.Fatal("a service read found the cluster model")
		}
	})
}

// A model is stored under a key that names its schema, so an entry written by a task with another row shape is never read.
func TestCache_ModelKeysCarryTheSchemaVersion(t *testing.T) {
	eachCache(t, func(t *testing.T, c Cache, raw kv.Client) {
		if err := c.Put(ctx, ModelCluster, "c1", "1-1", []byte("new shape"), time.Minute); err != nil {
			t.Fatal(err)
		}
		v, err := raw.Get(ctx, raw.Key("snap", "cluster", ModelSchema, "c1", "1-1"))
		if err != nil || !v.OK || v.S != "new shape" {
			t.Fatalf("the model is not under the schema-versioned key: %+v %v", v, err)
		}
		// An entry in the layout an older release wrote (no schema segment) must not be served.
		if _, err := raw.Set(ctx, raw.Key("snap", "service", "s1", "1-1"), "old shape", kv.SetOptions{}); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := c.Get(ctx, ModelService, "s1", "1-1"); ok {
			t.Fatal("an entry without a schema version was served")
		}
	})
}

// slowKV delays every command and counts them, like a store that answers slowly.
type slowKV struct {
	kv.Client
	delay time.Duration
	cmds  atomic.Int64
}

func (s *slowKV) pause() { s.cmds.Add(1); time.Sleep(s.delay) }
func (s *slowKV) Get(c context.Context, k string) (kv.Value, error) {
	s.pause()
	return s.Client.Get(c, k)
}
func (s *slowKV) Set(c context.Context, k, v string, o kv.SetOptions) (bool, error) {
	s.pause()
	return s.Client.Set(c, k, v, o)
}
func (s *slowKV) PExpire(c context.Context, k string, d time.Duration) error {
	s.pause()
	return s.Client.PExpire(c, k, d)
}
func (s *slowKV) Eval(c context.Context, sc *kv.Script, keys, args []string) (kv.Reply, error) {
	s.pause()
	return s.Client.Eval(c, sc, keys, args)
}

// A slice-wide write is one command: with a store that takes 70 ms per command and a 250 ms bound, four keys written
// one at a time would not all fit; written together they do.
func TestCache_BumpAllAndTouchAllAreOneCommand(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, raw kv.Client) {
		slow := &slowKV{Client: raw, delay: 70 * time.Millisecond}
		c := NewKV(slow)
		keys := []GenKey{sliceK, clusterK, {AccountID: "111111111111", Region: "us-east-1", Cluster: "b"}, {AccountID: "111111111111", Region: "us-east-1", Cluster: "c"}}
		bounded, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		defer cancel()
		if err := c.BumpAll(bounded, keys, "3-1"); err != nil {
			t.Fatalf("BumpAll: %v", err)
		}
		if got := slow.cmds.Load(); got != 1 {
			t.Fatalf("BumpAll of 4 keys sent %d commands, want 1", got)
		}
		got := mustTokens(t, c, keys...)
		for i, token := range got {
			if token != "3-1" {
				t.Fatalf("key %d token = %q, want 3-1 (every key must be written)", i, token)
			}
		}

		// TouchAll: keeps the tokens that exist, sets the one that is missing, refreshes every life, in one command.
		missing := GenKey{AccountID: "111111111111", Region: "us-east-1", Cluster: "new"}
		for _, k := range keys {
			if err := raw.PExpire(ctx, raw.Key("gen", k.AccountID, k.Region, k.Cluster), 1500*time.Millisecond); err != nil && k.Cluster != "" {
				t.Fatal(err)
			}
		}
		if err := raw.PExpire(ctx, raw.Key("gen", sliceK.AccountID, sliceK.Region), 1500*time.Millisecond); err != nil {
			t.Fatal(err)
		}
		before := slow.cmds.Load()
		if err := c.TouchAll(bounded2(t), append(keys, missing), "3-2"); err != nil {
			t.Fatalf("TouchAll: %v", err)
		}
		if got := slow.cmds.Load() - before; got != 1 {
			t.Fatalf("TouchAll of 5 keys sent %d commands, want 1", got)
		}
		time.Sleep(2 * time.Second)
		now := mustTokens(t, c, append(keys, missing)...)
		for i := range keys {
			if now[i] != "3-1" {
				t.Fatalf("key %d token = %q after TouchAll and its old life, want the original 3-1 still there", i, now[i])
			}
		}
		if now[len(keys)] != "3-2" {
			t.Fatalf("the missing key = %q, want TouchAll to set 3-2", now[len(keys)])
		}
	})
}

func bounded2(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	t.Cleanup(cancel)
	return c
}

func TestNewToken_DiffersAcrossEpochsAndTime(t *testing.T) {
	at := time.UnixMilli(1_700_000_000_123)
	if got := NewToken(7, at); got != "7-1700000000123" {
		t.Fatalf("NewToken = %q, want 7-1700000000123", got)
	}
	if NewToken(7, at) == NewToken(8, at) {
		t.Fatal("tokens of two epochs are equal")
	}
	if NewToken(7, at) == NewToken(7, at.Add(time.Millisecond)) {
		t.Fatal("tokens a millisecond apart are equal")
	}
	if strings.ContainsAny(NewToken(1, at), " :") {
		t.Fatal("a token must be one key segment")
	}
}
