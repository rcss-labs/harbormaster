package lease

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func blockedNames(n int) []string {
	var out []string
	for i := range n {
		out = append(out, fmt.Sprintf("blk%d", i))
	}
	return out
}

// Slow claims (behind a dead owner's open transaction) and a slow holder stop must not starve the renewals of healthy
// slices: a tick that is busy for many seconds used to leave them unrenewed until the self-fence cancelled them.
func TestManager_BlockedClaimsAndASlowStopDoNotSelfFenceHealthySlices(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	healthy := []string{"acct00", "acct01", "acct02"}
	all := append(append([]string{}, healthy...), "gone")
	all = append(all, blockedNames(7)...)

	// Seven expired leases whose dead owners left a write transaction open: a claim waits on the row lock.
	for _, name := range blockedNames(7) {
		k := sliceKeyOf(name)
		ensureAccounts(st, awsx.Target{AccountID: name, Region: "r"})
		if err := st.EnsureSliceLease(ctx, awsx.Target{AccountID: name, Region: "r"}); err != nil {
			t.Fatal(err)
		}
		l, ok, err := st.ClaimLease(ctx, k, uuid.New(), time.Minute)
		if err != nil || !ok {
			t.Fatalf("claim %s: %v %v", name, ok, err)
		}
		conn, err := st.Pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		held := conn.Hijack()
		t.Cleanup(func() { _ = held.Close(context.Background()) })
		if _, err := held.Exec(ctx, `begin`); err != nil {
			t.Fatal(err)
		}
		var one int
		if err := held.QueryRow(ctx, `select 1 from poll_leases where kind = 'slice' and account_id = $1 and region = $2
			and owner_id = $3 and epoch = $4 for key share`, k.AccountID, k.Region, l.OwnerID, l.Epoch).Scan(&one); err != nil {
			t.Fatal(err)
		}
		expireLease(t, st, k)
	}

	rec := newRecorder(newExclusivity())
	slowStop := HolderFunc(func(hctx context.Context, l store.Lease) {
		if l.Key.AccountID == "gone" {
			<-hctx.Done()
			time.Sleep(2 * time.Second) // a holder that takes long to finish its in-flight work
			return
		}
		rec.Hold(hctx, l)
	})
	targets := newTargets(all...)
	m := newManager(st, managerOpts{roles: []string{"poller"}, slices: slowStop, targets: targets})
	start(t, m)
	waitFor(t, 10*time.Second, "the healthy slices to be held", func() bool {
		for _, n := range healthy {
			if rec.ctxOf(sliceKeyOf(n)) == nil {
				return false
			}
		}
		return true
	})
	hctxs := map[string]context.Context{}
	for _, n := range healthy {
		hctxs[n] = rec.ctxOf(sliceKeyOf(n))
	}
	// "gone" leaves: its slow stop blocks the tick for 2 s on top of the blocked claims.
	targets.set(append(append([]string{}, healthy...), blockedNames(7)...)...)

	stays(t, 9*time.Second, "no healthy slice being self-fenced", func() bool {
		for n, c := range hctxs {
			if c.Err() != nil {
				t.Logf("%s was cancelled", n)
				return false
			}
		}
		return true
	})
}

// A poller that crashed holds nothing once its leases expire, but its member row stays "live" until MemberLive passes.
// It must not count towards the fair share (nor block the leftover rule) meanwhile, or the survivors would not take its
// slices until the row ages out.
func TestManager_IgnoresAStaleMemberThatHoldsNothingWhenSplittingSlices(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	ghost := uuid.New()
	if err := st.HeartbeatMember(ctx, store.Member{InstanceID: ghost, Roles: []string{"poller"}, Version: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(ctx, `update poll_members set heartbeat_at = now() - interval '3 seconds' where instance_id = $1`, ghost); err != nil {
		t.Fatal(err)
	}
	rec := newRecorder(newExclusivity())
	m := newManager(st, managerOpts{roles: []string{"poller"}, slices: rec, targets: newTargets("acct00", "acct01")})
	m.Timing.MemberLive = 10 * time.Second // the ghost stays "live" for 7 more seconds
	start(t, m)
	waitFor(t, 2*time.Second, "both slices to be held although a silent member row still counts as live", func() bool { return len(m.Held()) == 2 })
}
