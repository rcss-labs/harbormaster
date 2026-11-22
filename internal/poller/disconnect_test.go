package poller

import (
	"context"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

// Disconnecting an account removes its rows for good: the poller that owns its slice is still running with a lease and
// credentials (its own target list is refreshed only every AccountsRefresh), and nothing it does afterwards may bring
// the rows back, neither a write of a loop that is mid-cycle nor a claim of the slice from a stale target list.
func TestPoller_DisconnectedAccountStaysGone(t *testing.T) {
	st := storetest.Open(t)
	estate := fake.Generate(1, fake.Scale{Accounts: 2, RegionsPerAccount: 1, ClustersPerRegion: 2, ServicesPerCluster: 2, TasksPerService: 2})
	var accounts []store.AWSAccount
	var targets []awsx.Target
	for _, a := range estate.Accounts {
		created, err := st.CreateAccount(context.Background(), store.AWSAccount{Name: a.Name, AccountID: a.ID,
			RoleARN: "arn:aws:iam::" + a.ID + ":role/x", ExternalID: "e", Regions: a.Regions, Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		accounts = append(accounts, created)
		targets = append(targets, awsx.Target{AccountID: a.ID, Region: a.Regions[0]})
	}
	gone, kept := accounts[0], accounts[1]

	base := 100 * time.Millisecond
	cfg := Config{MaxInFlight: 4, AccountsRefresh: 10 * time.Second, Coordinator: waitForCancel,
		Intervals: Intervals{Services: base, RunningTasks: base, FullTasks: base, StoppedTasks: base, Clusters: base, ServiceList: base,
			TargetHealth: base, Scaling: base, Alerts: base, Network: base, Storage: base}}
	p := New(st, fake.New(estate), cfg, discardLogger())
	p.timing = testTiming()
	p.retryInterval = 100 * time.Millisecond
	startRun(t, p)

	ctx := context.Background()
	count := func(query string, args ...any) int {
		var n int
		if err := st.Pool.QueryRow(ctx, query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	rowsOf := func(accountID string) int {
		return count(`select (select count(*) from services where account_id = $1)
		            + (select count(*) from tasks where account_id = $1)
		            + (select count(*) from clusters where account_id = $1)
		            + (select count(*) from poll_loops where account_id = $1)`, accountID)
	}
	waitFor(t, 20*time.Second, "both accounts to be polled", func() bool {
		return rowsOf(gone.AccountID) > 0 && rowsOf(kept.AccountID) > 0 &&
			count(`select count(*) from poll_leases where kind = 'slice' and owner_id is not null`) == 2
	})

	if err := st.DeleteAccount(ctx, gone.ID); err != nil {
		t.Fatal(err)
	}
	// A poller that still holds the old target list tries to bring the slice back.
	if err := st.EnsureSliceLeases(ctx, targets); err != nil {
		t.Fatal(err)
	}

	time.Sleep(15 * base) // many poll intervals and several claim ticks
	if n := rowsOf(gone.AccountID); n != 0 {
		t.Errorf("the disconnected account has %d rows again after its poller kept running", n)
	}
	if n := count(`select count(*) from poll_leases where kind = 'slice' and account_id = $1`, gone.AccountID); n != 0 {
		t.Errorf("the disconnected account has %d lease rows again", n)
	}
	if rowsOf(kept.AccountID) == 0 {
		t.Error("the other account lost its rows")
	}
}
