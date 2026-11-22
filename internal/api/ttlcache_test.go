package api

import (
	"errors"
	"testing"
	"time"
)

func TestTTLCache_OneLoadPerWindowAndErrorsAreNotKept(t *testing.T) {
	var c ttlCache[int]
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	calls := 0
	load := func() (int, error) { calls++; return calls, nil }

	for _, offset := range []time.Duration{0, time.Second, 4 * time.Second} {
		if got, _ := c.get(t0.Add(offset), 5*time.Second, load); got != 1 {
			t.Fatalf("at +%v got %d, want the first value", offset, got)
		}
	}
	if calls != 1 {
		t.Fatalf("%d loads within the window, want 1", calls)
	}
	if got, _ := c.get(t0.Add(5*time.Second), 5*time.Second, load); got != 2 || calls != 2 {
		t.Fatalf("after the window got %d with %d loads, want a reload", got, calls)
	}

	failing := errors.New("db down")
	if _, err := c.get(t0.Add(time.Minute), 5*time.Second, func() (int, error) { return 0, failing }); !errors.Is(err, failing) {
		t.Fatalf("err = %v", err)
	}
	if got, _ := c.get(t0.Add(time.Minute), 5*time.Second, load); got != 3 {
		t.Fatalf("a failed load must not be cached; got %d", got)
	}
}
