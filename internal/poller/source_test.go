package poller

import (
	"context"
	"sync"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/awsx"
)

func TestLoop_TierCallsAreLabelledTier(t *testing.T) {
	h := mockupHarness(t)
	var mu sync.Mutex
	counts := map[awsx.Source]int{}
	h.fake.OnCall = func(ctx context.Context, _ awsx.Target, _, _, _ string) {
		mu.Lock()
		defer mu.Unlock()
		counts[awsx.SourceFrom(ctx)]++
	}
	h.runOnce(t)

	mu.Lock()
	defer mu.Unlock()
	if counts[awsx.SourceTier] == 0 {
		t.Fatalf("no call was labelled %q: %v", awsx.SourceTier, counts)
	}
	if n := counts[awsx.SourceRequest]; n != 0 {
		t.Fatalf("%d tier calls were left labelled %q: %v", n, awsx.SourceRequest, counts)
	}
}
