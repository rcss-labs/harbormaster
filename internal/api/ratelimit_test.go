package api

import (
	"testing"
	"time"
)

func TestIPBucket_CapacityAndRefill(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	bucket := newIPBucket(3, 1, func() time.Time { return now })

	for i := 0; i < 3; i++ {
		if !bucket.Allow("203.0.113.1") {
			t.Fatalf("request %d within capacity was refused", i+1)
		}
	}
	if bucket.Allow("203.0.113.1") {
		t.Fatal("request beyond capacity was allowed")
	}
	if !bucket.Allow("203.0.113.2") {
		t.Fatal("another IP must have its own bucket")
	}

	now = now.Add(1500 * time.Millisecond)
	if !bucket.Allow("203.0.113.1") {
		t.Fatal("one token should have refilled after 1.5s")
	}
	if bucket.Allow("203.0.113.1") {
		t.Fatal("only one token should have refilled after 1.5s")
	}

	now = now.Add(time.Hour)
	for i := 0; i < 3; i++ {
		if !bucket.Allow("203.0.113.1") {
			t.Fatalf("refill must cap at capacity; request %d refused", i+1)
		}
	}
	if bucket.Allow("203.0.113.1") {
		t.Fatal("refill exceeded capacity")
	}
}
