package kv_test

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/kv"
	"github.com/rcss-labs/harbormaster/internal/kv/kvtest"
)

// designAllowList is the design's "Command allow-list", copied here on purpose so that editing
// kv.AllowedCommands alone cannot make the test agree with itself.
var designAllowList = []string{
	"GET", "MGET", "SET", "DEL", "PEXPIRE", "ZADD", "ZREM", "ZCARD", "ZRANGEBYSCORE", "ZREMRANGEBYSCORE",
	"HSET", "HMGET", "EVALSHA", "EVAL", "SCRIPT LOAD", "PUBLISH", "SUBSCRIBE", "UNSUBSCRIBE", "PING", "TIME",
	"HELLO", "AUTH", "CLIENT SETNAME", "INFO SERVER",
}

func TestAllowedCommands_MatchesTheDesign(t *testing.T) {
	if len(designAllowList) != 24 {
		t.Fatalf("test list has %d names, the design has 24", len(designAllowList))
	}
	want := map[string]bool{}
	for _, n := range designAllowList {
		want[n] = true
		if !kv.AllowedCommands[n] {
			t.Errorf("AllowedCommands lacks %q", n)
		}
	}
	for n := range kv.AllowedCommands {
		if !want[n] {
			t.Errorf("AllowedCommands has %q, which is not in the design", n)
		}
	}
}

// adminOnly are commands this test and kvtest send through the raw admin connection. They are test tooling, not
// the client under test.
var adminOnly = map[string]bool{
	"info":             true,
	"client|kill":      true,
	"script|flush":     true,
	"config|resetstat": true,
}

// serverName converts an allow-list name to the form INFO commandstats uses ("CLIENT SETNAME" is
// "client|setname"). "INFO SERVER" is the plain "info" command.
func serverName(allowed string) string {
	return strings.ToLower(strings.ReplaceAll(allowed, " ", "|"))
}

func TestAllowList_ServerSawOnlyAllowedCommands(t *testing.T) {
	urls := kvtest.URLs()
	if len(urls) == 0 {
		t.Skip(kvtest.EnvVar + " not set")
	}
	allowed := map[string]bool{"info": true}
	for n := range kv.AllowedCommands {
		allowed[serverName(n)] = true
	}
	for _, url := range urls {
		url := url
		t.Run(url[strings.LastIndex(url, ":")+1:], func(t *testing.T) {
			admin := kvtest.OpenAdmin(t, url)
			if _, err := admin.Do("CONFIG", "RESETSTAT"); err != nil {
				t.Fatal(err)
			}
			exerciseEveryMethod(t, kvtest.Open(t, url))

			stats, err := admin.Do("INFO", "commandstats")
			if err != nil {
				t.Fatal(err)
			}
			var seen, outside []string
			for _, line := range strings.Split(stats, "\n") {
				line = strings.TrimSpace(line)
				if !strings.HasPrefix(line, "cmdstat_") {
					continue
				}
				name, _, _ := strings.Cut(strings.TrimPrefix(line, "cmdstat_"), ":")
				seen = append(seen, name)
				if !allowed[name] && !adminOnly[name] {
					outside = append(outside, name)
				}
			}
			sort.Strings(seen)
			if len(outside) > 0 {
				t.Fatalf("the server saw commands outside the allow-list: %v (all seen: %v)", outside, seen)
			}
			for _, must := range []string{"hello", "get", "evalsha", "subscribe"} {
				found := false
				for _, s := range seen {
					found = found || s == must
				}
				if !found {
					t.Fatalf("expected the server to have seen %q; the test did not exercise the client (seen: %v)", must, seen)
				}
			}
		})
	}
}

func exerciseEveryMethod(t *testing.T, c kv.Client) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	key, hash, zset, channel := c.Key("a"), c.Key("h"), c.Key("z"), c.Key("ch")

	subCtx, stop := context.WithCancel(ctx)
	got := make(chan string, 4)
	done := make(chan struct{})
	go func() {
		_ = c.Subscribe(subCtx, channel, func(p string) { got <- p }, func() {})
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for delivered := false; !delivered && time.Now().Before(deadline); {
		must(c.Publish(ctx, channel, "x"))
		select {
		case <-got:
			delivered = true
		case <-time.After(30 * time.Millisecond):
		}
	}
	stop()
	<-done

	_, err := c.Set(ctx, key, "1", kv.SetOptions{NX: true, TTL: time.Minute})
	must(err)
	_, err = c.Get(ctx, key)
	must(err)
	_, err = c.MGet(ctx, key, c.Key("b"))
	must(err)
	must(c.PExpire(ctx, key, time.Minute))
	_, err = c.ZRangeByScore(ctx, zset, 0, 2)
	must(err)
	must(c.ZRem(ctx, zset, "m"))
	_, err = c.HMGet(ctx, hash, "f")
	must(err)
	_, err = c.Eval(ctx, kv.NewScript(`return redis.call('GET', KEYS[1])`), []string{key}, nil)
	must(err)
	must(c.Ping(ctx))
	_, err = c.ServerTime(ctx)
	must(err)
	must(c.Del(ctx, key, hash, zset))
}
