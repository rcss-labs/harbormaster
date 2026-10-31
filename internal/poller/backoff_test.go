package poller

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/smithy-go"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func TestTier_DoublesOnThrottleAndHalvesAfterFiveClean(t *testing.T) {
	tr := newTier("services", 5*time.Second)
	for _, want := range []time.Duration{10, 20, 40, 60, 60} {
		tr.onThrottle()
		if tr.cur != want*time.Second {
			t.Fatalf("after throttle cur = %v, want %v", tr.cur, want*time.Second)
		}
	}
	// 60 s -> 30 -> 15 -> 7.5 -> base (5 s), one halving per five clean cycles.
	for _, want := range []time.Duration{30 * time.Second, 15 * time.Second, 7500 * time.Millisecond, 5 * time.Second} {
		for i := 0; i < 4; i++ {
			tr.onSuccess()
			if tr.cur == want {
				t.Fatalf("halved before five clean cycles (cur = %v)", tr.cur)
			}
		}
		tr.onSuccess()
		if tr.cur != want {
			t.Fatalf("after five clean cycles cur = %v, want %v", tr.cur, want)
		}
	}
	tr.onSuccess()
	if tr.cur != tr.base {
		t.Fatalf("cur drifted below base: %v", tr.cur)
	}
}

// denyingClients makes DescribeServices fail with AccessDeniedException while deny is set.
type denyingClients struct {
	*fake.Fake
	deny atomic.Bool
}

func (d *denyingClients) ECS(ctx context.Context, t awsx.Target) (awsx.ECSAPI, error) {
	api, err := d.Fake.ECS(ctx, t)
	if err != nil {
		return nil, err
	}
	return &denyingECS{ECSAPI: api, deny: &d.deny}, nil
}

type denyingECS struct {
	awsx.ECSAPI
	deny *atomic.Bool
}

func (d *denyingECS) DescribeServices(ctx context.Context, in *ecs.DescribeServicesInput, opts ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error) {
	if d.deny.Load() {
		return nil, &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "not authorized to ecs:DescribeServices", Fault: smithy.FaultClient}
	}
	return d.ECSAPI.DescribeServices(ctx, in, opts...)
}

func fastConfig() Config {
	base := 5 * time.Millisecond
	return Config{
		MaxInFlight: 4,
		Intervals:   Intervals{Services: base, RunningTasks: base, FullTasks: base, StoppedTasks: base, Clusters: base, ServiceList: base},
	}
}

func newFastLoop(t *testing.T, clients awsx.Clients, st *store.Store, cfg Config) *Loop {
	t.Helper()
	p := New(st, clients, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return newLoop(p, loopTarget)
}

func (l *Loop) tierDef(t *testing.T, name string) tierDef {
	t.Helper()
	for _, d := range l.defs {
		if d.tier.name == name {
			return d
		}
	}
	t.Fatalf("no tier %q", name)
	return tierDef{}
}

func servicesLoopRow(t *testing.T, st *store.Store) store.PollLoop {
	t.Helper()
	loops, err := st.ListPollLoops(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range loops {
		if p.Target == loopTarget && p.Tier == tierServices {
			return p
		}
	}
	t.Fatal("no poll_loops row for tier services")
	return store.PollLoop{}
}

func TestLoop_ThrottleMarksPollLoop(t *testing.T) {
	st := storetest.Open(t)
	f := fake.New(fake.MockupEstate(func() time.Time { return loopNow }))
	l := newFastLoop(t, f, st, fastConfig())
	ctx := context.Background()
	if err := l.runOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if row := servicesLoopRow(t, st); row.Throttled() || row.Failures != 0 {
		t.Fatalf("healthy row = %+v", row)
	}

	f.Throttle = 3
	svc := l.tierDef(t, tierServices)
	_ = l.runTier(ctx, svc)
	row := servicesLoopRow(t, st)
	if !row.Throttled() {
		t.Fatalf("interval %v not above base %v after throttling", row.Interval, row.BaseInterval)
	}
	if row.Failures != 0 {
		t.Errorf("throttling counted as failure: failures = %d", row.Failures)
	}
	if row.LastError == "" {
		t.Error("throttle error not recorded in last_error")
	}

	for i := 0; i < 5; i++ {
		if err := l.runTier(ctx, svc); err != nil {
			t.Fatalf("clean cycle: %v", err)
		}
	}
	if row := servicesLoopRow(t, st); row.Throttled() || row.LastError != "" {
		t.Fatalf("after clean cycles row = %+v, want back at base", row)
	}
}

func TestLoop_ThreeFailuresMarksFailing(t *testing.T) {
	st := storetest.Open(t)
	clients := &denyingClients{Fake: fake.New(fake.MockupEstate(func() time.Time { return loopNow }))}
	l := newFastLoop(t, clients, st, fastConfig())
	ctx := context.Background()
	if err := l.runOnce(ctx); err != nil {
		t.Fatal(err)
	}

	clients.deny.Store(true)
	svc := l.tierDef(t, tierServices)
	for i := 0; i < 3; i++ {
		if err := l.runTier(ctx, svc); err == nil {
			t.Fatal("expected denied error")
		}
	}
	row := servicesLoopRow(t, st)
	if row.Failures < 3 || !row.Failing() {
		t.Fatalf("failures = %d, want >= 3", row.Failures)
	}
	if !strings.Contains(row.LastError, "AccessDenied") {
		t.Errorf("last_error = %q, want AccessDenied text", row.LastError)
	}
	if row.Throttled() {
		t.Error("non-throttle failures must not change the interval")
	}

	clients.deny.Store(false)
	if err := l.runTier(ctx, svc); err != nil {
		t.Fatal(err)
	}
	if row := servicesLoopRow(t, st); row.Failures != 0 || row.LastError != "" || row.LastOKAt == nil {
		t.Fatalf("after success row = %+v", row)
	}
}

func TestIsThrottleInJoin(t *testing.T) {
	throttle := &smithy.GenericAPIError{Code: "ThrottlingException"}
	denied := &smithy.GenericAPIError{Code: "AccessDeniedException"}
	if !errorsThrottled(errors.Join(denied, throttle)) {
		t.Error("a throttle anywhere in a joined error must count")
	}
	if errorsThrottled(errors.Join(denied)) {
		t.Error("access denied is not a throttle")
	}
}

func TestLoop_AccountHealthKeepsLastGoodTimeAndError(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	if _, err := st.CreateAccount(ctx, store.AWSAccount{Name: "homelab", AccountID: homelabAccount, RoleARN: "arn:aws:iam::482109375512:role/hm", ExternalID: "x", Regions: []string{"us-east-1"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	clients := &denyingClients{Fake: fake.New(fake.MockupEstate(func() time.Time { return loopNow }))}
	l := newFastLoop(t, clients, st, fastConfig())
	if err := l.runOnce(ctx); err != nil {
		t.Fatal(err)
	}
	account := func() store.AWSAccount {
		a, err := st.GetAccountByAccountID(ctx, homelabAccount)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	good := account()
	if good.LastOKAt == nil || good.LastError != "" {
		t.Fatalf("healthy account = %+v", good)
	}

	clients.deny.Store(true)
	svc := l.tierDef(t, tierServices)
	for i := 0; i < 3; i++ {
		_ = l.runTier(ctx, svc)
	}
	failing := account()
	if failing.LastOKAt == nil || !failing.LastOKAt.Equal(*good.LastOKAt) {
		t.Errorf("last_ok_at = %v, want the last good time %v kept", failing.LastOKAt, good.LastOKAt)
	}
	if !strings.Contains(failing.LastError, "AccessDenied") {
		t.Errorf("last_error = %q, want AccessDenied text", failing.LastError)
	}

	clients.deny.Store(false)
	time.Sleep(5 * time.Millisecond)
	if err := l.runTier(ctx, svc); err != nil {
		t.Fatal(err)
	}
	recovered := account()
	if recovered.LastError != "" || recovered.LastOKAt == nil || !recovered.LastOKAt.After(*good.LastOKAt) {
		t.Errorf("recovered account = %+v, want last_error cleared and last_ok_at advanced", recovered)
	}
}
