package poller

import (
	"bytes"
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
	"github.com/rcss-labs/harbormaster/internal/telemetry"
)

// fencedWrites are the 19 poller writes that must go through a store.SliceWriter.
var fencedWrites = map[string]bool{
	"UpsertClusters": true, "DeleteClustersNotIn": true, "DeleteServicesNotIn": true, "UpsertServices": true,
	"InsertServiceEvents": true, "UpsertTasks": true, "MarkTasksMissing": true, "UpsertTaskDefinition": true,
	"UpsertPollLoop": true, "SetAccountHealth": true, "InsertCountsHistory": true, "UpsertClusterCapacitySample": true,
	"InsertDeploymentIfNew": true, "UpsertTargetHealth": true, "DeleteTargetHealthNotIn": true, "UpsertScaling": true,
	"ReplaceNetwork": true, "ReplaceStorage": true, "ReplaceAlertsAndStatusRuns": true,
}

// The loops' store field is a read-only interface, so most unfenced writes do not compile; this catches the rest (a
// write through the Poller's own *store.Store, which the loops reach as p.store).
func TestPoller_LoopsWriteOnlyThroughTheSliceWriter(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
	if err != nil {
		t.Fatal(err)
	}
	files := 0
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			files++
			base := filepath.Base(fset.Position(f.Pos()).Filename)
			ast.Inspect(f, func(n ast.Node) bool {
				// The Poller's concrete store belongs to the lease machinery (poller.go, supervisor.go); a loop that
				// reaches it, directly or through an alias of it, could write unfenced. Loops use p.reads.
				if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "store" && base != "poller.go" && base != "supervisor.go" {
					t.Errorf("%s: %s.store is the Poller's concrete store; polling code must read through p.reads", fset.Position(sel.Pos()), exprName(sel.X))
				}
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !fencedWrites[sel.Sel.Name] {
					return true
				}
				if inner, ok := sel.X.(*ast.SelectorExpr); ok && (inner.Sel.Name == "store" || inner.Sel.Name == "Store") {
					t.Errorf("%s: %s.%s.%s is an unfenced poller write; use the slice writer", fset.Position(call.Pos()), exprName(inner.X), inner.Sel.Name, sel.Sel.Name)
				}
				return true
			})
		}
	}
	if files < 10 {
		t.Fatalf("parsed only %d non-test files; the check is not looking at the package", files)
	}
}

func exprName(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return exprName(v.X) + "." + v.Sel.Name
	}
	return "?"
}

// pausedClients makes the first ListClusters call block, as a frozen process or a stalled disk would, then return an
// empty cluster list (the stale view an old owner would write after its pause).
type pausedClients struct {
	awsx.Clients
	armed   atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (c *pausedClients) ECS(ctx context.Context, t awsx.Target) (awsx.ECSAPI, error) {
	api, err := c.Clients.ECS(ctx, t)
	return &pausedECS{ECSAPI: api, c: c}, err
}

type pausedECS struct {
	awsx.ECSAPI
	c *pausedClients
}

func (e *pausedECS) ListClusters(ctx context.Context, in *ecs.ListClustersInput, opts ...func(*ecs.Options)) (*ecs.ListClustersOutput, error) {
	if e.c.armed.CompareAndSwap(true, false) {
		close(e.c.entered)
		<-e.c.release // deliberately ignores ctx: a frozen process does not see its cancellation either
		return &ecs.ListClustersOutput{}, nil
	}
	return e.ECSAPI.ListClusters(ctx, in, opts...)
}

// A poller paused past its lease, then resumed: its next write must fail and its loop must stop, and the new owner's
// rows stay. The stale view it resumes with would, unfenced, delete every cluster of the slice.
func TestPoller_PausedOwnerCannotOverwriteAfterTakeover(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	target := loopTarget // an account the mockup estate knows
	if _, err := st.CreateAccount(ctx, store.AWSAccount{Name: "home", AccountID: target.AccountID,
		RoleARN: "arn:aws:iam::" + target.AccountID + ":role/x", ExternalID: "e", Regions: []string{target.Region}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	key := store.LeaseKey{Kind: store.LeaseSlice, AccountID: target.AccountID, Region: target.Region}

	clients := &pausedClients{Clients: fake.New(fake.MockupEstate(time.Now)), entered: make(chan struct{}), release: make(chan struct{})}
	clients.armed.Store(true)
	reg := telemetry.New()
	a := newTestPoller(st, Config{MaxInFlight: 4, AccountsRefresh: 50 * time.Millisecond, Telemetry: reg}, randomLockKey())
	a.clients = clients
	loopDone := make(chan struct{})
	a.loopFactory = func(ctx context.Context, tg awsx.Target, w *store.SliceWriter) {
		a.defaultLoopFactory(ctx, tg, w)
		close(loopDone)
	}
	startRun(t, a)
	select {
	case <-clients.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the old owner's loop never reached AWS")
	}

	// Another process takes the slice over, in one statement so the old owner's renewal cannot slip in between.
	newOwner := uuid.New()
	var epoch int64
	err := st.Pool.QueryRow(ctx, `update poll_leases set owner_id = $1, epoch = epoch + 1, expires_at = now() + interval '1 hour'
		where kind = 'slice' and account_id = $2 and region = $3 returning epoch`, newOwner, target.AccountID, target.Region).Scan(&epoch)
	if err != nil {
		t.Fatal(err)
	}
	wb, err := st.SliceWriter(store.Lease{Key: key, OwnerID: newOwner, Epoch: epoch})
	if err != nil {
		t.Fatal(err)
	}
	mine := []model.Cluster{
		{Key: model.ClusterKey{AccountID: target.AccountID, Region: target.Region, Cluster: "new-owner-1"}, Status: "ACTIVE"},
		{Key: model.ClusterKey{AccountID: target.AccountID, Region: target.Region, Cluster: "new-owner-2"}, Status: "ACTIVE"},
	}
	if res, err := wb.UpsertClusters(ctx, mine); err != nil || res.Inserted != 2 {
		t.Fatalf("new owner's write: %+v, %v", res, err)
	}

	close(clients.release) // the old owner wakes up with its stale view
	select {
	case <-loopDone:
	case <-time.After(10 * time.Second):
		t.Fatal("the old owner's loop did not stop after its write was rejected")
	}
	rows, err := st.Pool.Query(ctx, `select cluster_name from clusters order by cluster_name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		got = append(got, n)
	}
	if strings.Join(got, ",") != "new-owner-1,new-owner-2" {
		t.Fatalf("clusters = %v, want the new owner's two (the old owner's stale delete must not commit)", got)
	}
	// Either the stale write was rejected (counted) or the manager's renewal noticed the takeover first and cancelled the
	// loop before it wrote; what must never happen is a committed stale write, which the cluster rows above rule out.
	var metrics bytes.Buffer
	if err := reg.WritePrometheus(&metrics); err != nil {
		t.Fatal(err)
	}
	t.Logf("fenced writes rejected: %v", strings.Contains(metrics.String(), "harbormaster_fenced_writes_rejected_total 0\n") == false)
	var pollRows int
	if err := st.Pool.QueryRow(ctx, `select count(*) from poll_loops`).Scan(&pollRows); err != nil || pollRows != 0 {
		t.Fatalf("poll_loops rows = %d, %v; the lost slice must not record a cycle", pollRows, err)
	}
}

// holdSlice is the only place that counts rejected writes. With no manager running, take the slice over and write with the
// old lease: the write must be refused, counted, and the loop told to stop.
func TestPoller_HoldSliceCountsAStaleWriteAsRejected(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	target := loopTarget
	connectAccount(t, st, target)
	if err := st.EnsureSliceLease(ctx, target); err != nil {
		t.Fatal(err)
	}
	key := store.LeaseKey{Kind: store.LeaseSlice, AccountID: target.AccountID, Region: target.Region}
	old, ok, err := st.ClaimLease(ctx, key, uuid.New(), time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if _, err := st.Pool.Exec(ctx, `update poll_leases set owner_id = $1, epoch = epoch + 1 where kind = 'slice' and account_id = $2 and region = $3`,
		uuid.New(), target.AccountID, target.Region); err != nil {
		t.Fatal(err)
	}

	reg := telemetry.New()
	p := newTestPoller(st, Config{Telemetry: reg}, randomLockKey())
	var writeErr error
	p.loopFactory = func(lctx context.Context, _ awsx.Target, w *store.SliceWriter) {
		_, writeErr = w.UpsertClusters(lctx, []model.Cluster{{Key: model.ClusterKey{AccountID: target.AccountID, Region: target.Region, Cluster: "stale"}, Status: "ACTIVE"}})
		if lctx.Err() == nil {
			t.Error("the loop context was not cancelled by the rejected write")
		}
	}
	p.holdSlice(ctx, old)

	if !errors.Is(writeErr, store.ErrLeaseLost) {
		t.Fatalf("stale write error = %v, want ErrLeaseLost", writeErr)
	}
	var metrics bytes.Buffer
	if err := reg.WritePrometheus(&metrics); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(metrics.String(), "harbormaster_fenced_writes_rejected_total 1\n") {
		t.Fatalf("rejected-write counter did not reach 1:\n%s", metrics.String())
	}
}
