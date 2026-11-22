package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/model"
)

// ErrWrongSlice means a write named a row outside the slice (account x region) its writer holds the lease of.
var ErrWrongSlice = errors.New("row does not belong to the leased slice")

// SliceWriter is the only way the poller writes snapshot rows. It can be built only from a slice lease, and every call
// is its own transaction that starts by re-checking, with a row lock, that the lease is still held at its epoch, so a
// write of an old epoch can never commit after a takeover.
type SliceWriter struct {
	s     *Store
	lease Lease

	mu         sync.Mutex
	onRejected func()
}

// SliceWriter returns the fenced writer of a held slice lease.
func (s *Store) SliceWriter(l Lease) (*SliceWriter, error) {
	if l.Key.Kind != LeaseSlice {
		return nil, errors.New("slice writer needs a slice lease")
	}
	return &SliceWriter{s: s, lease: l}, nil
}

// Lease returns the lease this writer is fenced by.
func (w *SliceWriter) Lease() Lease { return w.lease }

// OnRejected sets a function called once for every write rejected with ErrLeaseLost.
func (w *SliceWriter) OnRejected(fn func()) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.onRejected = fn
}

// own returns ErrWrongSlice unless the row belongs to the leased slice.
func (w *SliceWriter) own(accountID, region string) error {
	k := w.lease.Key
	if accountID != k.AccountID || region != k.Region {
		return fmt.Errorf("%w: %s/%s, the lease is for %s/%s", ErrWrongSlice, accountID, region, k.AccountID, k.Region)
	}
	return nil
}

// fenceIdleTimeout and fenceStatementTimeout bound a fenced write transaction on the server. They are variables so tests can shorten them.
var (
	fenceIdleTimeout      = 10 * time.Second
	fenceStatementTimeout = 30 * time.Second
)

// fenceSQL is the design's check. FOR KEY SHARE lets many writes of one owner run in parallel and does not conflict with
// the owner's renewal (a non-key update), but conflicts with a takeover's FOR UPDATE: the takeover waits for these
// transactions, and every write that starts after it finds a different owner or epoch.
//
// The lease must also be unexpired by the database clock. An owner that stopped renewing may not start new writes, so
// overlapping writes of an expired owner cannot keep the row share-locked for ever and starve the takeover.
const fenceSQL = `select 1 from poll_leases
 where kind = 'slice' and account_id = $1 and region = $2 and owner_id = $3 and epoch = $4
   and expires_at > now()
 for key share`

// fenceTimeoutsSQL bounds this transaction on the server, whatever its client does: a frozen or partitioned owner that
// leaves the transaction open (holding the fence lock) is cut off by the server after fenceIdleTimeout, so a takeover
// always gets through. It must run as its own statement before the fence: a timeout set inside a statement does not
// bound that same statement, and the fence can wait on a takeover's row lock.
const fenceTimeoutsSQL = `select set_config('idle_in_transaction_session_timeout', $1, true),
  set_config('statement_timeout', $2, true)`

// fencedDo runs fn in one transaction that starts with the fence. The transaction holds the lease row's key-share lock
// until it commits, which is what makes the epoch check, not the clock, the arbiter. fn gets the transaction as its db,
// so a write that opens its own transaction runs in a savepoint and stays atomic with the fence.
func fencedDo[T any](ctx context.Context, w *SliceWriter, fn func(q db) (T, error)) (T, error) {
	var result T
	err := pgx.BeginFunc(ctx, w.s.Pool, func(tx pgx.Tx) error {
		k := w.lease.Key
		if _, err := tx.Exec(ctx, fenceTimeoutsSQL, fmt.Sprintf("%dms", fenceIdleTimeout.Milliseconds()), fmt.Sprintf("%dms", fenceStatementTimeout.Milliseconds())); err != nil {
			return fmt.Errorf("set write timeouts: %w", err)
		}
		var one int
		err := tx.QueryRow(ctx, fenceSQL, k.AccountID, k.Region, w.lease.OwnerID, w.lease.Epoch).Scan(&one)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: slice %s/%s epoch %d", ErrLeaseLost, k.AccountID, k.Region, w.lease.Epoch)
		}
		if err != nil {
			return fmt.Errorf("fence slice write: %w", err)
		}
		result, err = fn(tx)
		return err
	})
	if err != nil {
		if errors.Is(err, ErrLeaseLost) {
			w.mu.Lock()
			fn := w.onRejected
			w.mu.Unlock()
			if fn != nil {
				fn()
			}
		}
		var zero T
		return zero, err
	}
	return result, nil
}

// CheckLease runs the fence alone: it returns ErrLeaseLost when the lease has moved on and nil otherwise. A write with
// no rows skips the fence, so a caller that must not report work done after losing the lease calls this.
func (w *SliceWriter) CheckLease(ctx context.Context) error {
	_, err := fencedDo(ctx, w, func(db) (struct{}, error) { return struct{}{}, nil })
	return err
}

func (w *SliceWriter) ownKey(k model.ClusterKey) error { return w.own(k.AccountID, k.Region) }

func (w *SliceWriter) ownTarget(t awsx.Target) error { return w.own(t.AccountID, t.Region) }

func (w *SliceWriter) UpsertClusters(ctx context.Context, rows []model.Cluster) (WriteResult, error) {
	for _, r := range rows {
		if err := w.ownKey(r.Key); err != nil {
			return WriteResult{}, err
		}
	}
	if len(rows) == 0 {
		return WriteResult{}, nil
	}
	return fencedDo(ctx, w, func(q db) (WriteResult, error) { return upsertClusters(ctx, q, rows) })
}

func (w *SliceWriter) DeleteClustersNotIn(ctx context.Context, t awsx.Target, keep []model.ClusterKey) (int64, error) {
	if err := w.ownTarget(t); err != nil {
		return 0, err
	}
	return fencedDo(ctx, w, func(q db) (int64, error) { return deleteClustersNotIn(ctx, q, t, keep) })
}

func (w *SliceWriter) DeleteServicesNotIn(ctx context.Context, k model.ClusterKey, keep []string) (int64, error) {
	if err := w.ownKey(k); err != nil {
		return 0, err
	}
	return fencedDo(ctx, w, func(q db) (int64, error) { return deleteServicesNotIn(ctx, q, k, keep) })
}

func (w *SliceWriter) UpsertServices(ctx context.Context, rows []model.Service) (WriteResult, error) {
	for _, r := range rows {
		if err := w.ownKey(r.Key.ClusterKey); err != nil {
			return WriteResult{}, err
		}
	}
	if len(rows) == 0 {
		return WriteResult{}, nil
	}
	return fencedDo(ctx, w, func(q db) (WriteResult, error) { return upsertServices(ctx, q, rows) })
}

func (w *SliceWriter) InsertServiceEvents(ctx context.Context, rows []model.ServiceEvent) (int, error) {
	for _, r := range rows {
		if err := w.ownKey(r.Key.ClusterKey); err != nil {
			return 0, err
		}
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return fencedDo(ctx, w, func(q db) (int, error) { return insertServiceEvents(ctx, q, rows) })
}

func (w *SliceWriter) UpsertTasks(ctx context.Context, rows []model.Task) (WriteResult, error) {
	for _, r := range rows {
		if err := w.ownKey(r.Cluster); err != nil {
			return WriteResult{}, err
		}
	}
	if len(rows) == 0 {
		return WriteResult{}, nil
	}
	return fencedDo(ctx, w, func(q db) (WriteResult, error) { return upsertTasks(ctx, q, rows) })
}

func (w *SliceWriter) MarkTasksMissing(ctx context.Context, k model.ClusterKey, arns []string, now time.Time) error {
	if err := w.ownKey(k); err != nil {
		return err
	}
	if len(arns) == 0 {
		return nil
	}
	_, err := fencedDo(ctx, w, func(q db) (struct{}, error) { return struct{}{}, markTasksMissing(ctx, q, k, arns, now) })
	return err
}

func (w *SliceWriter) UpsertTaskDefinition(ctx context.Context, td model.TaskDefinitionRef) error {
	if err := w.own(td.AccountID, td.Region); err != nil {
		return err
	}
	_, err := fencedDo(ctx, w, func(q db) (struct{}, error) { return struct{}{}, upsertTaskDefinition(ctx, q, td) })
	return err
}

func (w *SliceWriter) UpsertPollLoop(ctx context.Context, p PollLoop) error {
	if err := w.ownTarget(p.Target); err != nil {
		return err
	}
	_, err := fencedDo(ctx, w, func(q db) (struct{}, error) { return struct{}{}, upsertPollLoop(ctx, q, p) })
	return err
}

// SetAccountHealth records the outcome of the slice's poll on its account. Only the account must match: the account row
// is shared by every region's slice, and each slice's last poll is the latest word, as before leases.
func (w *SliceWriter) SetAccountHealth(ctx context.Context, accountID string, okAt *time.Time, lastError string) error {
	if accountID != w.lease.Key.AccountID {
		return ErrWrongSlice
	}
	_, err := fencedDo(ctx, w, func(q db) (struct{}, error) {
		return struct{}{}, setAccountHealth(ctx, q, accountID, okAt, lastError)
	})
	return err
}

func (w *SliceWriter) InsertCountsHistory(ctx context.Context, ts time.Time, rows []model.Service) error {
	for _, r := range rows {
		if err := w.ownKey(r.Key.ClusterKey); err != nil {
			return err
		}
	}
	if len(rows) == 0 {
		return nil
	}
	_, err := fencedDo(ctx, w, func(q db) (struct{}, error) { return struct{}{}, insertCountsHistory(ctx, q, ts, rows) })
	return err
}

func (w *SliceWriter) UpsertClusterCapacitySample(ctx context.Context, day time.Time, samples []ClusterCapacitySample) error {
	for _, c := range samples {
		if err := w.ownKey(c.Cluster); err != nil {
			return err
		}
	}
	if len(samples) == 0 {
		return nil
	}
	_, err := fencedDo(ctx, w, func(q db) (struct{}, error) {
		return struct{}{}, upsertClusterCapacitySample(ctx, q, day, samples)
	})
	return err
}

func (w *SliceWriter) InsertDeploymentIfNew(ctx context.Context, r DeploymentRecord) (bool, error) {
	if err := w.ownKey(r.Key.ClusterKey); err != nil {
		return false, err
	}
	return fencedDo(ctx, w, func(q db) (bool, error) { return insertDeploymentIfNew(ctx, q, r) })
}

func (w *SliceWriter) UpsertTargetHealth(ctx context.Context, rows []model.TargetHealth) error {
	for _, r := range rows {
		if err := w.ownKey(r.Key.ClusterKey); err != nil {
			return err
		}
	}
	if len(rows) == 0 {
		return nil
	}
	_, err := fencedDo(ctx, w, func(q db) (struct{}, error) { return struct{}{}, upsertTargetHealth(ctx, q, rows) })
	return err
}

func (w *SliceWriter) DeleteTargetHealthNotIn(ctx context.Context, t awsx.Target, keep []string) error {
	if err := w.ownTarget(t); err != nil {
		return err
	}
	_, err := fencedDo(ctx, w, func(q db) (struct{}, error) { return struct{}{}, deleteTargetHealthNotIn(ctx, q, t, keep) })
	return err
}

func (w *SliceWriter) UpsertScaling(ctx context.Context, rows []model.Scaling) error {
	for _, r := range rows {
		if err := w.ownKey(r.Key.ClusterKey); err != nil {
			return err
		}
	}
	if len(rows) == 0 {
		return nil
	}
	_, err := fencedDo(ctx, w, func(q db) (struct{}, error) { return struct{}{}, upsertScaling(ctx, q, rows) })
	return err
}

func (w *SliceWriter) ReplaceNetwork(ctx context.Context, accountID, region string, vpcs []model.VPC, subnets []model.Subnet, edges []model.VPCEdge, sgs []model.SecurityGroup) error {
	if err := w.own(accountID, region); err != nil {
		return err
	}
	for _, v := range vpcs {
		if err := w.own(v.AccountID, v.Region); err != nil {
			return err
		}
	}
	for _, v := range subnets {
		if err := w.own(v.AccountID, v.Region); err != nil {
			return err
		}
	}
	for _, v := range edges {
		if err := w.own(v.AccountID, v.Region); err != nil {
			return err
		}
	}
	for _, v := range sgs {
		if err := w.own(v.AccountID, v.Region); err != nil {
			return err
		}
	}
	_, err := fencedDo(ctx, w, func(q db) (struct{}, error) {
		return struct{}{}, replaceNetwork(ctx, q, accountID, region, vpcs, subnets, edges, sgs)
	})
	return err
}

func (w *SliceWriter) ReplaceStorage(ctx context.Context, accountID, region string, fileSystems []model.FileSystem, accessPoints []model.AccessPoint) error {
	if err := w.own(accountID, region); err != nil {
		return err
	}
	for _, v := range fileSystems {
		if err := w.own(v.AccountID, v.Region); err != nil {
			return err
		}
	}
	for _, v := range accessPoints {
		if err := w.own(v.AccountID, v.Region); err != nil {
			return err
		}
	}
	_, err := fencedDo(ctx, w, func(q db) (struct{}, error) {
		return struct{}{}, replaceStorage(ctx, q, accountID, region, fileSystems, accessPoints)
	})
	return err
}

func (w *SliceWriter) ReplaceAlertsAndStatusRuns(ctx context.Context, t awsx.Target, alerts []model.Alert, runs []StatusRun) error {
	if err := w.ownTarget(t); err != nil {
		return err
	}
	for _, a := range alerts {
		if err := w.ownKey(a.Key.ClusterKey); err != nil {
			return err
		}
	}
	for _, r := range runs {
		if err := w.ownKey(r.Key.ClusterKey); err != nil {
			return err
		}
	}
	_, err := fencedDo(ctx, w, func(q db) (struct{}, error) {
		return struct{}{}, replaceAlertsAndStatusRuns(ctx, q, t, alerts, runs)
	})
	return err
}
