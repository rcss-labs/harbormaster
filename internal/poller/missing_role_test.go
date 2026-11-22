package poller

import (
	"context"
	"testing"
	"time"

	"github.com/aws/smithy-go"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

// noRoleClients refuses to build clients for one account, as the real client does when the derived role
// (harbormaster-access-poller) does not exist there yet.
type noRoleClients struct {
	*fake.Fake
	account string
}

func (c *noRoleClients) ECS(ctx context.Context, t awsx.Target) (awsx.ECSAPI, error) {
	if t.AccountID == c.account {
		return nil, &smithy.GenericAPIError{Code: "AccessDenied", Message: "not authorized to assume the poller role", Fault: smithy.FaultClient}
	}
	return c.Fake.ECS(ctx, t)
}

// An account whose derived role is missing fails its own slice with the AWS error, which the admin sees per slice; the
// other accounts keep polling.
func TestPoller_MissingSuffixedRoleFailsOnlyThatSlice(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	estate := fake.Generate(1, fake.Scale{Accounts: 2, RegionsPerAccount: 1, ClustersPerRegion: 1, ServicesPerCluster: 1, TasksPerService: 1})
	for _, a := range estate.Accounts {
		if _, err := st.CreateAccount(ctx, store.AWSAccount{Name: a.Name, AccountID: a.ID,
			RoleARN: "arn:aws:iam::" + a.ID + ":role/x", ExternalID: "e", Regions: a.Regions, Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	bad, good := estate.Accounts[0].ID, estate.Accounts[1].ID
	base := 100 * time.Millisecond
	cfg := Config{MaxInFlight: 4, AccountsRefresh: 100 * time.Millisecond, Coordinator: waitForCancel,
		Intervals: Intervals{Services: base, RunningTasks: base, FullTasks: base, StoppedTasks: base, Clusters: base, ServiceList: base,
			TargetHealth: base, Scaling: base, Alerts: base, Network: base, Storage: base}}
	p := New(st, &noRoleClients{Fake: fake.New(estate), account: bad}, cfg, discardLogger())
	p.timing = testTiming()
	p.retryInterval = 100 * time.Millisecond
	startRun(t, p)

	waitFor(t, 20*time.Second, "the good account to be polled and the bad one to record its error", func() bool {
		var polled, failed int
		if st.Pool.QueryRow(ctx, `select count(*) from poll_loops where account_id = $1 and tier = 'services' and last_ok_at is not null`, good).Scan(&polled) != nil {
			return false
		}
		if st.Pool.QueryRow(ctx, `select count(*) from poll_loops where account_id = $1 and last_error like '%AccessDenied%'`, bad).Scan(&failed) != nil {
			return false
		}
		return polled == 1 && failed > 0
	})
	var badOK int
	if err := st.Pool.QueryRow(ctx, `select count(*) from poll_loops where account_id = $1 and tier in ('clusters', 'services') and last_ok_at is not null`, bad).Scan(&badOK); err != nil || badOK != 0 {
		t.Fatalf("the account without its role has %d successful AWS polls, %v", badOK, err)
	}
}
