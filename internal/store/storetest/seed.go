package storetest

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// Seeder writes snapshot rows into a test database the way a poller does: through a SliceWriter that holds the slice's
// lease, so seeding exercises the fenced path and the store has no unfenced public writer. Each call claims the lease
// of every slice its rows touch, writes, and releases it again; a lease row that did not exist before is removed, so
// seeding leaves no trace in poll_leases. It fails when a poller in the same test holds the slice.
type Seeder struct{ s *store.Store }

// Seed returns the seeder of a test store.
func Seed(s *store.Store) *Seeder { return &Seeder{s: s} }

// inSlice runs fn with the fenced writer of one slice.
func (d *Seeder) inSlice(ctx context.Context, t awsx.Target, fn func(w *store.SliceWriter) error) error {
	key := store.LeaseKey{Kind: store.LeaseSlice, AccountID: t.AccountID, Region: t.Region}
	var existed bool
	if err := d.s.Pool.QueryRow(ctx,
		`select exists (select 1 from poll_leases where kind = 'slice' and account_id = $1 and region = $2)`,
		t.AccountID, t.Region).Scan(&existed); err != nil {
		return fmt.Errorf("seed: look up lease: %w", err)
	}
	if err := d.s.EnsureSliceLease(ctx, t); err != nil {
		return err
	}
	lease, ok, err := d.s.ClaimLeaseOnce(ctx, key, uuid.New(), time.Minute)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("seed: slice %s/%s is held by another owner", t.AccountID, t.Region)
	}
	w, err := d.s.SliceWriter(lease)
	if err != nil {
		return err
	}
	werr := fn(w)
	if err := d.s.ReleaseLease(ctx, lease); err != nil && werr == nil {
		werr = err
	}
	if !existed {
		if _, err := d.s.Pool.Exec(ctx,
			`delete from poll_leases where kind = 'slice' and account_id = $1 and region = $2 and owner_id is null`,
			t.AccountID, t.Region); err != nil && werr == nil {
			werr = err
		}
	}
	return werr
}

// bySlice groups items by the slice they belong to, in first-seen order.
func bySlice[T any](items []T, target func(T) awsx.Target) (order []awsx.Target, groups map[awsx.Target][]T) {
	groups = map[awsx.Target][]T{}
	for _, it := range items {
		t := target(it)
		if _, seen := groups[t]; !seen {
			order = append(order, t)
		}
		groups[t] = append(groups[t], it)
	}
	return order, groups
}

func sliceOf(k model.ClusterKey) awsx.Target {
	return awsx.Target{AccountID: k.AccountID, Region: k.Region}
}

// forEachSlice writes each slice's share of items with write and returns the number of slices written.
func forEachSlice[T any](ctx context.Context, d *Seeder, items []T, target func(T) awsx.Target, write func(w *store.SliceWriter, part []T) error) error {
	order, groups := bySlice(items, target)
	for _, t := range order {
		part := groups[t]
		if err := d.inSlice(ctx, t, func(w *store.SliceWriter) error { return write(w, part) }); err != nil {
			return err
		}
	}
	return nil
}

func add(a *store.WriteResult, b store.WriteResult) {
	a.Inserted += b.Inserted
	a.Updated += b.Updated
	a.Unchanged += b.Unchanged
}

func (d *Seeder) UpsertClusters(ctx context.Context, rows []model.Cluster) (store.WriteResult, error) {
	var total store.WriteResult
	err := forEachSlice(ctx, d, rows, func(c model.Cluster) awsx.Target { return sliceOf(c.Key) },
		func(w *store.SliceWriter, part []model.Cluster) error {
			r, err := w.UpsertClusters(ctx, part)
			add(&total, r)
			return err
		})
	return total, err
}

func (d *Seeder) UpsertServices(ctx context.Context, rows []model.Service) (store.WriteResult, error) {
	var total store.WriteResult
	err := forEachSlice(ctx, d, rows, func(s model.Service) awsx.Target { return sliceOf(s.Key.ClusterKey) },
		func(w *store.SliceWriter, part []model.Service) error {
			r, err := w.UpsertServices(ctx, part)
			add(&total, r)
			return err
		})
	return total, err
}

func (d *Seeder) InsertServiceEvents(ctx context.Context, rows []model.ServiceEvent) (int, error) {
	total := 0
	err := forEachSlice(ctx, d, rows, func(e model.ServiceEvent) awsx.Target { return sliceOf(e.Key.ClusterKey) },
		func(w *store.SliceWriter, part []model.ServiceEvent) error {
			n, err := w.InsertServiceEvents(ctx, part)
			total += n
			return err
		})
	return total, err
}

func (d *Seeder) UpsertTasks(ctx context.Context, rows []model.Task) (store.WriteResult, error) {
	var total store.WriteResult
	err := forEachSlice(ctx, d, rows, func(k model.Task) awsx.Target { return sliceOf(k.Cluster) },
		func(w *store.SliceWriter, part []model.Task) error {
			r, err := w.UpsertTasks(ctx, part)
			add(&total, r)
			return err
		})
	return total, err
}

func (d *Seeder) InsertCountsHistory(ctx context.Context, ts time.Time, rows []model.Service) error {
	return forEachSlice(ctx, d, rows, func(s model.Service) awsx.Target { return sliceOf(s.Key.ClusterKey) },
		func(w *store.SliceWriter, part []model.Service) error { return w.InsertCountsHistory(ctx, ts, part) })
}

func (d *Seeder) UpsertTargetHealth(ctx context.Context, rows []model.TargetHealth) error {
	return forEachSlice(ctx, d, rows, func(h model.TargetHealth) awsx.Target { return sliceOf(h.Key.ClusterKey) },
		func(w *store.SliceWriter, part []model.TargetHealth) error { return w.UpsertTargetHealth(ctx, part) })
}

func (d *Seeder) UpsertPollLoop(ctx context.Context, p store.PollLoop) error {
	return d.inSlice(ctx, p.Target, func(w *store.SliceWriter) error { return w.UpsertPollLoop(ctx, p) })
}
