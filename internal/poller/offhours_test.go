package poller

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/offhours"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func offHoursReconcilerFor(st *store.Store, f *fake.Fake) *offhours.Reconciler {
	applier := &offhours.Applier{Store: st, Clients: f, Now: time.Now}
	return &offhours.Reconciler{Applier: applier, Store: st, Interval: 50 * time.Millisecond, Now: time.Now}
}

func TestSupervisor_StartsOffHoursOnlyWhileLeading(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	const account = "482109375512"
	if _, err := st.CreateAccount(ctx, store.AWSAccount{Name: "a", AccountID: account, RoleARN: "arn:aws:iam::" + account + ":role/x",
		ExternalID: "e", Regions: []string{"us-east-1"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	key := model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: account, Region: "us-east-1", Cluster: "core-services"}, Service: "payments-api"}
	if _, err := st.UpsertServices(ctx, []model.Service{{Key: key, Status: "ACTIVE", Desired: 3, LaunchType: "FARGATE",
		Raw: []byte(`{"Tags":[{"Key":"env","Value":"staging"}]}`), PolledAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateOffHoursSchedule(ctx, store.OffHoursSchedule{Name: "always", Enabled: true, TagKey: "env", TagValue: "staging",
		Timezone: "UTC", Awake: strings.Repeat("1", 168)}, 0); err != nil {
		t.Fatal(err)
	}
	newFake := func() *fake.Fake {
		f := fake.New(fake.MockupEstate(time.Now))
		f.Estate.Mu.Lock()
		f.Estate.ScalingConfig[key] = &fake.ScalingSeed{Min: 2, Max: 6}
		f.Estate.Mu.Unlock()
		return f
	}
	leaderAWS, followerAWS := newFake(), newFake()
	lock := randomLockKey()

	leader := newTestPoller(st, Config{AccountsRefresh: time.Hour, RetentionEvery: time.Hour, OffHours: offHoursReconcilerFor(st, leaderAWS)}, lock)
	leader.loopFactory = newLoopRecorder().factory
	startRun(t, leader)
	waitFor(t, 3*time.Second, "the first poller to lead", leader.IsLeader)

	follower := newTestPoller(st, Config{AccountsRefresh: time.Hour, RetentionEvery: time.Hour, OffHours: offHoursReconcilerFor(st, followerAWS)}, lock)
	follower.loopFactory = newLoopRecorder().factory
	startRun(t, follower)

	waitFor(t, 5*time.Second, "the leader's reconciler to enrol the service", func() bool {
		_, err := st.GetOffHoursEnrolment(ctx, key)
		return err == nil
	})
	time.Sleep(300 * time.Millisecond)
	if calls := followerAWS.Calls(); len(calls) != 0 {
		t.Fatalf("the follower's reconciler called AWS: %v", calls)
	}
}
