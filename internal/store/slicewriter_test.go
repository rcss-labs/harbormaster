package store_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

const (
	swAccount = "111111111111"
	swRegion  = "eu-west-1"
)

var (
	swTarget  = awsx.Target{AccountID: swAccount, Region: swRegion}
	swCluster = model.ClusterKey{AccountID: swAccount, Region: swRegion, Cluster: "core"}
	swService = model.ServiceKey{ClusterKey: swCluster, Service: "api"}
)

// swHold ensures the slice's row and claims it as a fresh owner.
func swHold(t testing.TB, s *store.Store) (store.Lease, *store.SliceWriter) {
	t.Helper()
	k := ensureSlice(t, s, swAccount, swRegion)
	l := mustClaim(t, s, k, uuid.New())
	w, err := s.SliceWriter(l)
	if err != nil {
		t.Fatalf("SliceWriter: %v", err)
	}
	return l, w
}

// swTakeOver expires the held lease in the database and claims it as another owner.
func swTakeOver(t testing.TB, s *store.Store) store.Lease {
	t.Helper()
	k := sliceKey(swAccount, swRegion)
	expireLease(t, s, k)
	return mustClaim(t, s, k, uuid.New())
}

func swCount(t testing.TB, s *store.Store, table string) int {
	t.Helper()
	var n int
	if err := s.Pool.QueryRow(context.Background(), `select count(*) from `+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func swCluster1(name string) model.Cluster {
	return model.Cluster{Key: model.ClusterKey{AccountID: swAccount, Region: swRegion, Cluster: name}, Status: "ACTIVE"}
}

func TestSliceWriter_RejectsACoordinatorLease(t *testing.T) {
	s := storetest.Open(t)
	if err := s.EnsureCoordinatorLease(context.Background()); err != nil {
		t.Fatal(err)
	}
	l := mustClaim(t, s, store.LeaseKey{Kind: store.LeaseCoordinator}, uuid.New())
	if _, err := s.SliceWriter(l); err == nil {
		t.Fatal("SliceWriter accepted a coordinator lease")
	}
}

func TestSliceWriter_WritesWhileLeaseIsHeld(t *testing.T) {
	s := storetest.Open(t)
	_, w := swHold(t, s)
	ctx := context.Background()

	res, err := w.UpsertClusters(ctx, []model.Cluster{swCluster1("a"), swCluster1("b")})
	if err != nil || res.Inserted != 2 {
		t.Fatalf("UpsertClusters = %+v, %v; want 2 inserted", res, err)
	}
	// Same result shape as the unfenced Store method on identical input.
	res, err = w.UpsertClusters(ctx, []model.Cluster{swCluster1("a")})
	if err != nil || res.Unchanged != 1 {
		t.Fatalf("second UpsertClusters = %+v, %v; want 1 unchanged", res, err)
	}
	if n := swCount(t, s, "clusters"); n != 2 {
		t.Fatalf("clusters = %d, want 2", n)
	}
	if res, err := w.UpsertClusters(ctx, nil); err != nil || res != (store.WriteResult{}) {
		t.Fatalf("empty UpsertClusters = %+v, %v", res, err)
	}
}

func TestSliceWriter_StaleEpochIsRejected(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	a, wa := swHold(t, s)
	var rejected atomic.Int32
	wa.OnRejected(func() { rejected.Add(1) })
	b := swTakeOver(t, s)
	wb, err := s.SliceWriter(b)
	if err != nil {
		t.Fatal(err)
	}

	_, err = wa.UpsertServices(ctx, []model.Service{{Key: swService, Desired: 1}})
	if !errors.Is(err, store.ErrLeaseLost) {
		t.Fatalf("old owner's write = %v, want ErrLeaseLost", err)
	}
	if n := swCount(t, s, "services"); n != 0 {
		t.Fatalf("services = %d, want none: the rejected write must not commit", n)
	}
	if rejected.Load() != 1 {
		t.Fatalf("OnRejected called %d times, want 1", rejected.Load())
	}
	if res, err := wb.UpsertServices(ctx, []model.Service{{Key: swService, Desired: 2}}); err != nil || res.Inserted != 1 {
		t.Fatalf("new owner's write = %+v, %v", res, err)
	}
	if a.Epoch+1 != b.Epoch {
		t.Fatalf("epochs %d then %d", a.Epoch, b.Epoch)
	}
}

func TestSliceWriter_ReleasedLeaseIsRejected(t *testing.T) {
	s := storetest.Open(t)
	l, w := swHold(t, s)
	if err := s.ReleaseLease(context.Background(), l); err != nil {
		t.Fatal(err)
	}
	if _, err := w.UpsertClusters(context.Background(), []model.Cluster{swCluster1("a")}); !errors.Is(err, store.ErrLeaseLost) {
		t.Fatalf("write after release = %v, want ErrLeaseLost", err)
	}
	if n := swCount(t, s, "clusters"); n != 0 {
		t.Fatalf("clusters = %d, want none", n)
	}
}

// holdKeyShare opens a transaction on its own connection that holds the fence lock of the lease, as a slow write in
// progress would. commit releases it.
func holdKeyShare(t testing.TB, s *store.Store, l store.Lease) (backendPID int, commit func()) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var one int
	err = tx.QueryRow(ctx, `select 1 from poll_leases where kind = 'slice' and account_id = $1 and region = $2
		and owner_id = $3 and epoch = $4 for key share`, l.Key.AccountID, l.Key.Region, l.OwnerID, l.Epoch).Scan(&one)
	if err != nil {
		t.Fatalf("take the fence lock: %v", err)
	}
	if err := tx.QueryRow(ctx, `select pg_backend_pid()`).Scan(&backendPID); err != nil {
		t.Fatal(err)
	}
	done := false
	commit = func() {
		if !done {
			done = true
			if err := tx.Commit(ctx); err != nil {
				t.Errorf("commit the held write: %v", err)
			}
		}
	}
	t.Cleanup(commit)
	return backendPID, commit
}

func TestSliceWriter_TakeoverWaitsForOpenWrite(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	a, wa := swHold(t, s)
	k := sliceKey(swAccount, swRegion)
	pid, commitA := holdKeyShare(t, s, a)
	expireLease(t, s, k)

	type claim struct {
		l   store.Lease
		ok  bool
		err error
	}
	claimed := make(chan claim, 1)
	go func() {
		l, ok, err := s.ClaimLease(ctx, k, uuid.New(), leaseTTL)
		claimed <- claim{l, ok, err}
	}()

	// Wait until Postgres reports a backend blocked behind the open write; only then is the claim provably waiting.
	deadline := time.Now().Add(5 * time.Second)
	for {
		var blocked int
		if err := s.Pool.QueryRow(ctx, `select count(*) from pg_stat_activity where $1 = any(pg_blocking_pids(pid))`, pid).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the takeover never blocked behind the open write")
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case c := <-claimed:
		t.Fatalf("takeover returned (%+v) while the old owner's write was still open", c)
	default:
	}

	commitA()
	c := <-claimed
	if c.err != nil || !c.ok {
		t.Fatalf("takeover after the write committed = ok %v, err %v", c.ok, c.err)
	}
	if _, err := wa.UpsertClusters(ctx, []model.Cluster{swCluster1("late")}); !errors.Is(err, store.ErrLeaseLost) {
		t.Fatalf("old owner's next write = %v, want ErrLeaseLost", err)
	}
}

func TestSliceWriter_RenewalIsNotBlockedByOpenWrite(t *testing.T) {
	s := storetest.Open(t)
	l, _ := swHold(t, s)
	_, commit := holdKeyShare(t, s, l)
	defer commit()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.RenewLease(ctx, l, leaseTTL); err != nil {
		t.Fatalf("renewal while a write holds the fence lock = %v, want it to go through at once", err)
	}
}

func TestSliceWriter_ManyWritesRunInParallel(t *testing.T) {
	s := storetest.Open(t)
	_, w := swHold(t, s)
	const writers = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := w.UpsertClusters(context.Background(), []model.Cluster{swCluster1(fmt.Sprintf("c%d", i))}); err != nil {
				t.Errorf("writer %d: %v", i, err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if n := swCount(t, s, "clusters"); n != writers {
		t.Fatalf("clusters = %d, want %d", n, writers)
	}
}

func TestSliceWriter_RejectsRowsOfAnotherSlice(t *testing.T) {
	s := storetest.Open(t)
	_, w := swHold(t, s)
	ctx := context.Background()
	foreign := model.Cluster{Key: model.ClusterKey{AccountID: "999999999999", Region: swRegion, Cluster: "x"}}
	otherRegion := model.Cluster{Key: model.ClusterKey{AccountID: swAccount, Region: "us-east-1", Cluster: "x"}}

	for name, rows := range map[string][]model.Cluster{
		"other account": {foreign},
		"other region":  {otherRegion},
		"mixed":         {swCluster1("ok"), foreign},
	} {
		if _, err := w.UpsertClusters(ctx, rows); !errors.Is(err, store.ErrWrongSlice) {
			t.Errorf("%s: err = %v, want ErrWrongSlice", name, err)
		}
	}
	if n := swCount(t, s, "clusters"); n != 0 {
		t.Fatalf("clusters = %d, want none (a batch with a foreign row writes nothing)", n)
	}
	if _, err := w.DeleteClustersNotIn(ctx, awsx.Target{AccountID: "999999999999", Region: swRegion}, nil); !errors.Is(err, store.ErrWrongSlice) {
		t.Errorf("DeleteClustersNotIn of another target: %v, want ErrWrongSlice", err)
	}
	now := time.Now()
	if err := w.SetAccountHealth(ctx, "999999999999", &now, ""); !errors.Is(err, store.ErrWrongSlice) {
		t.Errorf("SetAccountHealth of another account: %v, want ErrWrongSlice", err)
	}
	if err := w.ReplaceNetwork(ctx, swAccount, "us-east-1", nil, nil, nil, nil); !errors.Is(err, store.ErrWrongSlice) {
		t.Errorf("ReplaceNetwork of another region: %v, want ErrWrongSlice", err)
	}
}

// swWrites is one non-empty call of each of the 19 fenced writes, in the plan's order.
func swWrites(now time.Time) []struct {
	name string
	call func(ctx context.Context, w *store.SliceWriter) error
} {
	type call = func(ctx context.Context, w *store.SliceWriter) error
	return []struct {
		name string
		call call
	}{
		{"UpsertClusters", func(ctx context.Context, w *store.SliceWriter) error {
			_, err := w.UpsertClusters(ctx, []model.Cluster{{Key: swCluster}})
			return err
		}},
		{"DeleteClustersNotIn", func(ctx context.Context, w *store.SliceWriter) error {
			_, err := w.DeleteClustersNotIn(ctx, swTarget, []model.ClusterKey{swCluster})
			return err
		}},
		{"DeleteServicesNotIn", func(ctx context.Context, w *store.SliceWriter) error {
			_, err := w.DeleteServicesNotIn(ctx, swCluster, []string{"api"})
			return err
		}},
		{"UpsertServices", func(ctx context.Context, w *store.SliceWriter) error {
			_, err := w.UpsertServices(ctx, []model.Service{{Key: swService}})
			return err
		}},
		{"InsertServiceEvents", func(ctx context.Context, w *store.SliceWriter) error {
			_, err := w.InsertServiceEvents(ctx, []model.ServiceEvent{{Key: swService, ID: "e1", CreatedAt: now, Message: "m"}})
			return err
		}},
		{"UpsertTasks", func(ctx context.Context, w *store.SliceWriter) error {
			_, err := w.UpsertTasks(ctx, []model.Task{{Cluster: swCluster, ARN: "arn:task/1", ServiceName: "api", LastStatus: "RUNNING"}})
			return err
		}},
		{"MarkTasksMissing", func(ctx context.Context, w *store.SliceWriter) error {
			return w.MarkTasksMissing(ctx, swCluster, []string{"arn:task/1"}, now)
		}},
		{"UpsertTaskDefinition", func(ctx context.Context, w *store.SliceWriter) error {
			return w.UpsertTaskDefinition(ctx, model.TaskDefinitionRef{AccountID: swAccount, Region: swRegion, ARN: "arn:td/api:1", Family: "api", Revision: 1})
		}},
		{"UpsertPollLoop", func(ctx context.Context, w *store.SliceWriter) error {
			return w.UpsertPollLoop(ctx, store.PollLoop{Target: swTarget, Tier: "services", LastOKAt: &now, Interval: time.Minute, BaseInterval: time.Minute})
		}},
		{"SetAccountHealth", func(ctx context.Context, w *store.SliceWriter) error {
			return w.SetAccountHealth(ctx, swAccount, &now, "")
		}},
		{"InsertCountsHistory", func(ctx context.Context, w *store.SliceWriter) error {
			return w.InsertCountsHistory(ctx, now, []model.Service{{Key: swService, Running: 1, Desired: 1}})
		}},
		{"UpsertClusterCapacitySample", func(ctx context.Context, w *store.SliceWriter) error {
			return w.UpsertClusterCapacitySample(ctx, now, []store.ClusterCapacitySample{{Cluster: swCluster, VCPU: 1, MemGiB: 2, Tasks: 1}})
		}},
		{"InsertDeploymentIfNew", func(ctx context.Context, w *store.SliceWriter) error {
			_, err := w.InsertDeploymentIfNew(ctx, store.DeploymentRecord{Key: swService, DeploymentID: "d1", Kind: "deploy", StartedAt: now})
			return err
		}},
		{"UpsertTargetHealth", func(ctx context.Context, w *store.SliceWriter) error {
			return w.UpsertTargetHealth(ctx, []model.TargetHealth{{Key: swService, TargetGroupARN: "arn:tg/1", PolledAt: now}})
		}},
		{"DeleteTargetHealthNotIn", func(ctx context.Context, w *store.SliceWriter) error {
			return w.DeleteTargetHealthNotIn(ctx, swTarget, []string{"arn:tg/1"})
		}},
		{"UpsertScaling", func(ctx context.Context, w *store.SliceWriter) error {
			return w.UpsertScaling(ctx, []model.Scaling{{Key: swService, PolledAt: now}})
		}},
		{"ReplaceNetwork", func(ctx context.Context, w *store.SliceWriter) error {
			return w.ReplaceNetwork(ctx, swAccount, swRegion, []model.VPC{{AccountID: swAccount, Region: swRegion, VPCID: "vpc-1", PolledAt: now}}, nil, nil, nil)
		}},
		{"ReplaceStorage", func(ctx context.Context, w *store.SliceWriter) error {
			return w.ReplaceStorage(ctx, swAccount, swRegion, []model.FileSystem{{AccountID: swAccount, Region: swRegion, FileSystemID: "fs-1", PolledAt: now}}, nil)
		}},
		{"ReplaceAlertsAndStatusRuns", func(ctx context.Context, w *store.SliceWriter) error {
			return w.ReplaceAlertsAndStatusRuns(ctx, swTarget,
				[]model.Alert{{Key: swService, Kind: "unhealthy", Severity: model.AlertWarning, Title: "t", Text: "x", At: now}},
				[]store.StatusRun{{Key: swService, Status: "steady", Since: now, Polls: 1}})
		}},
	}
}

func TestSliceWriter_CoversEveryPollerWrite(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	if _, err := s.CreateAccount(ctx, store.AWSAccount{Name: "a", AccountID: swAccount, RoleARN: "arn:aws:iam::111111111111:role/x", ExternalID: "e", Regions: []string{swRegion}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	_, w := swHold(t, s)
	writes := swWrites(time.Now().UTC().Truncate(time.Microsecond))
	if len(writes) != 19 {
		t.Fatalf("table has %d writes, want the 19 poller writes", len(writes))
	}
	for _, c := range writes {
		if err := c.call(ctx, w); err != nil {
			t.Errorf("%s while held: %v", c.name, err)
		}
	}
	swTakeOver(t, s)
	for _, c := range writes {
		if err := c.call(ctx, w); !errors.Is(err, store.ErrLeaseLost) {
			t.Errorf("%s after takeover = %v, want ErrLeaseLost", c.name, err)
		}
	}
}

// The race the design rests on: an owner writes as fast as it can while another process takes the lease over. Every
// write of the old epoch either commits before the takeover completes (the takeover waits for it) or is rejected; none
// may commit after the new owner's claim returned.
func TestSliceWriter_OldEpochWriteCannotCommitAfterTakeover(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	_, wa := swHold(t, s)
	k := sliceKey(swAccount, swRegion)

	var seq, committed atomic.Int64
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var lostCount atomic.Int64
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				n := seq.Add(1)
				_, err := wa.InsertServiceEvents(ctx, []model.ServiceEvent{{Key: swService, ID: fmt.Sprintf("a-%d", n), CreatedAt: time.Now(), Message: "old owner"}})
				switch {
				case err == nil:
					committed.Add(1)
				case errors.Is(err, store.ErrLeaseLost):
					lostCount.Add(1)
				default:
					t.Errorf("unexpected write error: %v", err)
					return
				}
				time.Sleep(200 * time.Microsecond)
			}
		}()
	}
	// Let the old owner get going, then expire its lease and take it over (retrying when the claim times out behind
	// a busy writer, as the manager does on its next tick).
	for committed.Load() < 20 {
		time.Sleep(time.Millisecond)
	}
	expireLease(t, s, k)
	var b store.Lease
	for attempt := 0; ; attempt++ {
		l, ok, err := s.ClaimLease(ctx, k, uuid.New(), time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			b = l
			break
		}
		if attempt > 50 {
			t.Fatal("the takeover never got the row lock")
		}
	}
	// B's claim has returned. Everything the old owner committed is visible now; nothing may be added later.
	atClaim := swCount(t, s, "service_events")
	time.Sleep(300 * time.Millisecond) // the old owner keeps trying
	close(stop)
	wg.Wait()
	if after := swCount(t, s, "service_events"); after != atClaim {
		t.Fatalf("service_events grew from %d to %d after the new owner's claim returned: an old-epoch write committed", atClaim, after)
	}
	if lostCount.Load() == 0 {
		t.Fatal("the old owner was never rejected, so the race was not exercised")
	}
	if int64(atClaim) != committed.Load() {
		t.Fatalf("rows = %d but the old owner counts %d committed writes", atClaim, committed.Load())
	}
	// And the new owner can write.
	wb, err := s.SliceWriter(b)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := wb.InsertServiceEvents(ctx, []model.ServiceEvent{{Key: swService, ID: "b-1", CreatedAt: time.Now(), Message: "new owner"}}); err != nil || n != 1 {
		t.Fatalf("new owner's write = %d, %v", n, err)
	}
}
