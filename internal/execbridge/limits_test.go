package execbridge_test

import (
	"testing"

	"github.com/rcss-labs/harbormaster/internal/execbridge"
)

func TestLimiter_AllowsUpToMaxPerUser(t *testing.T) {
	l := execbridge.NewLimiter(3)
	for i := 0; i < 3; i++ {
		if _, ok := l.Acquire(7); !ok {
			t.Fatalf("Acquire #%d for user 7 refused, want ok", i+1)
		}
	}
	if _, ok := l.Acquire(7); ok {
		t.Fatal("fourth Acquire for user 7 ok, want refused")
	}
	if _, ok := l.Acquire(8); !ok {
		t.Fatal("Acquire for user 8 refused, want ok (limits are per user)")
	}
}

func TestLimiter_ReleaseFreesSlot(t *testing.T) {
	l := execbridge.NewLimiter(3)
	var releases []func()
	for i := 0; i < 3; i++ {
		r, _ := l.Acquire(7)
		releases = append(releases, r)
	}
	releases[0]()
	r, ok := l.Acquire(7)
	if !ok {
		t.Fatal("Acquire after release refused, want ok")
	}
	_ = r
	if got := l.Count(7); got != 3 {
		t.Fatalf("Count(7) = %d, want 3", got)
	}
}

func TestLimiter_ReleaseIsIdempotent(t *testing.T) {
	l := execbridge.NewLimiter(3)
	r1, _ := l.Acquire(7)
	_, _ = l.Acquire(7)
	r1()
	r1()
	if got := l.Count(7); got != 1 {
		t.Fatalf("Count(7) = %d after double release, want 1", got)
	}
}
