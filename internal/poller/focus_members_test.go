package poller

import (
	"context"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/focus"
	"github.com/rcss-labs/harbormaster/internal/signal"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

// A poller that runs a focus loop reports it in its poll_members row.
func TestPoller_MemberRowCountsFocusedObjects(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	if _, err := st.CreateAccount(ctx, store.AWSAccount{Name: "homelab", AccountID: homelabAccount,
		RoleARN: "arn:aws:iam::" + homelabAccount + ":role/x", ExternalID: "e", Regions: []string{"us-east-1"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	reg := focus.NewMemoryRegistry(time.Now)
	cfg := Config{MaxInFlight: 4, Intervals: Intervals{Services: 30 * time.Second}, Registry: reg, Bus: signal.NewMemoryBus(),
		Focus: config.FocusConfig{Interval: 100 * time.Millisecond, TTL: 30 * time.Second, MaxObjects: 50, MaxPerUser: 5, MaxCallsPerSec: 50}}
	p := newTestPoller(st, cfg, randomLockKey())
	p.focusReadEvery = 50 * time.Millisecond
	startRun(t, p)
	waitFor(t, 20*time.Second, "the poller to hold the slice", func() bool { return heldCount(p) >= 1 })

	if _, err := reg.Heartbeat(ctx, paymentsObject(), 1, focus.Limits{TTL: time.Hour, MaxObjects: 50, MaxPerUser: 5}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "focus_objects > 0 on the member row", func() bool {
		members, err := st.LiveMembers(ctx, time.Minute)
		if err != nil {
			return false
		}
		for _, m := range members {
			if m.FocusObjects > 0 {
				return true
			}
		}
		return false
	})
}
