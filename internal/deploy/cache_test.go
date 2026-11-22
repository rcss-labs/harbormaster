package deploy

import (
	"fmt"
	"testing"
	"time"
)

func TestTTLCache_StaysWithinItsCapacity(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	c := newTTLCache[int](time.Hour)
	for i := 0; i < 3000; i++ {
		c.put(fmt.Sprintf("key-%d", i), i, now.Add(time.Duration(i)*time.Second))
	}
	if got := len(c.entries); got > 1024 {
		t.Fatalf("%d entries after 3000 puts, want at most 1024", got)
	}
	last := now.Add(2999 * time.Second)
	if v, ok := c.get("key-2999", last); !ok || v != 2999 {
		t.Errorf("newest entry = %d, %v; want it kept", v, ok)
	}
}

func TestTTLCache_PutDropsExpiredEntriesNeverRead(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	c := newTTLCache[int](time.Minute)
	for i := 0; i < 10; i++ {
		c.put(fmt.Sprintf("old-%d", i), i, now)
	}
	c.put("fresh", 1, now.Add(2*time.Minute))
	if got := len(c.entries); got != 1 {
		t.Fatalf("%d entries, want only the fresh one (the 10 expired were never read)", got)
	}
}
