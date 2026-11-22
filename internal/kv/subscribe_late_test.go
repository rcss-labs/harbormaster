package kv_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/kv"
	"github.com/rcss-labs/harbormaster/internal/kv/kvtest"
)

// A subscriber that starts while the store is unreachable has missed everything published so far, so the first
// subscription that does succeed must call the repair hook, just like a re-established one.
func TestSubscribe_CallsOnResubscribeWhenTheFirstSubscribeSucceedsLate(t *testing.T) {
	urls := kvtest.URLs()
	if len(urls) == 0 {
		t.Skip(kvtest.EnvVar + " not set")
	}
	for _, url := range urls {
		url := url
		t.Run(url[strings.LastIndex(url, ":")+1:], func(t *testing.T) {
			proxied, cut, restore := kvtest.Proxy(t, url)
			o, err := kv.ParseURL(proxied)
			if err != nil {
				t.Fatal(err)
			}
			o.Prefix = "tlate"
			c, err := kv.Open(o)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()

			cut()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var mu sync.Mutex
			gaps := 0
			go func() {
				_ = c.Subscribe(ctx, c.Key("kick"), func(string) {}, func() { mu.Lock(); gaps++; mu.Unlock() })
			}()
			time.Sleep(500 * time.Millisecond) // the first attempts fail against the cut proxy
			restore()
			waitFor(t, "onResubscribe after the late first subscription", func() bool { mu.Lock(); defer mu.Unlock(); return gaps >= 1 })
		})
	}
}
