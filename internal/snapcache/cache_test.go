package snapcache

import (
	"context"
	"strings"
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
		if err := c.Bump(ctx, clusterK, "1-100"); err != nil {
			t.Fatal(err)
		}
		if got := mustTokens(t, c, clusterK); got[0] != "1-100" {
			t.Fatalf("token = %q, want 1-100", got[0])
		}
		if err := c.Put(ctx, ModelCluster, "c1", "1-100", []byte("old"), time.Minute); err != nil {
			t.Fatal(err)
		}
		if err := c.Bump(ctx, clusterK, "1-200"); err != nil {
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
		if err := c.Bump(ctx, clusterK, "1-100"); err != nil {
			t.Fatal(err)
		}
		// Shorten the token's life, then touch: the value stays and the life is back to minutes.
		if err := raw.PExpire(ctx, raw.Key("gen", clusterK.AccountID, clusterK.Region, clusterK.Cluster), 1500*time.Millisecond); err != nil {
			t.Fatal(err)
		}
		if err := c.Touch(ctx, clusterK, "1-999"); err != nil {
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
		if err := c.Touch(ctx, sliceK, "2-5"); err != nil {
			t.Fatal(err)
		}
		if got := mustTokens(t, c, sliceK); got[0] != "2-5" {
			t.Fatalf("token = %q, want 2-5", got[0])
		}
	})
}

func TestCache_TokensReturnsEmptyForMissing(t *testing.T) {
	eachCache(t, func(t *testing.T, c Cache, _ kv.Client) {
		if err := c.Bump(ctx, clusterK, "1-1"); err != nil {
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
		if err := c.Bump(ctx, sliceK, "1-1"); err != nil {
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

func TestOff_NeverHits(t *testing.T) {
	c := Off()
	if err := c.Bump(ctx, clusterK, "1-1"); err != nil {
		t.Fatal(err)
	}
	if err := c.Touch(ctx, clusterK, "1-1"); err != nil {
		t.Fatal(err)
	}
	if err := c.Put(ctx, ModelCluster, "c1", "1-1", []byte("x"), time.Minute); err != nil {
		t.Fatal(err)
	}
	if got := mustTokens(t, c, clusterK, sliceK); len(got) != 2 || got[0] != "" || got[1] != "" {
		t.Fatalf("Off tokens = %q, want two empty", got)
	}
	if _, ok, err := c.Get(ctx, ModelCluster, "c1", "1-1"); ok || err != nil {
		t.Fatalf("Off Get = ok %v err %v, want a clean miss", ok, err)
	}
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
