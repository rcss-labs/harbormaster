package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/rcss-labs/harbormaster/internal/awsx"
)

// Lease kinds stored in poll_leases.kind.
const (
	LeaseSlice       = "slice"
	LeaseCoordinator = "coordinator"
)

// LeaseKey names one lease: a slice is one account x region, the coordinator has empty account and region.
type LeaseKey struct{ Kind, AccountID, Region string }

// Lease is a held lease. Epoch is the fencing token: it rises on every change of owner.
type Lease struct {
	Key       LeaseKey
	OwnerID   uuid.UUID
	Epoch     int64
	ExpiresAt time.Time
}

// ErrLeaseLost means the lease is no longer held by the caller (another owner, a newer epoch, or released).
var ErrLeaseLost = errors.New("lease lost")

// LeaseRow is a lease as stored. Free means no owner or an expired one.
type LeaseRow struct {
	Lease
	Free                  bool
	AcquiredAt, RenewedAt *time.Time
}

// lockTimeout bounds how long a claim or a release waits for one row lock behind another transaction's writes.
const lockTimeout = "3s"

// ClaimDeadline bounds a whole ClaimLease call. A lock wait can repeat (the row lock, then each transaction that shares
// it), so lock_timeout alone does not bound the call; a claim that runs out of time reports false and is retried by
// the caller's next tick. Callers give their context more than this so the claim ends itself.
const ClaimDeadline = 5 * time.Second

// pgLockNotAvailable is the SQLSTATE of a lock_timeout expiry.
const pgLockNotAvailable = "55P03"

func isLockTimeout(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == pgLockNotAvailable
}

// EnsureSliceLeases inserts a lease row for every target of an account that still exists and has none, and deletes free rows of slices that no
// longer exist. A held row of a removed target stays until its owner releases it. Epochs restart at 0 for a
// recreated row, which is safe because owner ids are new on every process start and a manager never re-claims
// a key whose previous holder is still running.
func (s *Store) EnsureSliceLeases(ctx context.Context, targets []awsx.Target) error {
	accounts := make([]string, 0, len(targets))
	regions := make([]string, 0, len(targets))
	for _, t := range targets {
		accounts = append(accounts, t.AccountID)
		regions = append(regions, t.Region)
	}
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`insert into poll_leases (kind, account_id, region)
			 select 'slice', a, r from unnest($1::text[], $2::text[]) as t(a, r)
			 where exists (select 1 from aws_accounts where account_id = t.a)
			 on conflict do nothing`, accounts, regions); err != nil {
			return err
		}
		_, err := tx.Exec(ctx,
			`delete from poll_leases p
			 where p.kind = 'slice' and (p.owner_id is null or p.expires_at < now())
			   and not exists (select 1 from unnest($1::text[], $2::text[]) as t(a, r)
			                   where t.a = p.account_id and t.r = p.region)`, accounts, regions)
		return err
	})
	if err != nil {
		return fmt.Errorf("ensure slice leases: %w", err)
	}
	return nil
}

// EnsureSliceLease inserts the lease row of one slice when missing and deletes nothing. It is for callers that act on
// a single slice (a one-shot poll) and must not reconcile the whole set.
func (s *Store) EnsureSliceLease(ctx context.Context, t awsx.Target) error {
	if _, err := s.Pool.Exec(ctx,
		`insert into poll_leases (kind, account_id, region) values ('slice', $1, $2) on conflict do nothing`,
		t.AccountID, t.Region); err != nil {
		return fmt.Errorf("ensure slice lease: %w", err)
	}
	return nil
}

// EnsureCoordinatorLease inserts the coordinator row when missing.
func (s *Store) EnsureCoordinatorLease(ctx context.Context) error {
	if _, err := s.Pool.Exec(ctx,
		`insert into poll_leases (kind, account_id, region) values ('coordinator', '', '') on conflict do nothing`); err != nil {
		return fmt.Errorf("ensure coordinator lease: %w", err)
	}
	return nil
}

// ClaimLease takes a free or expired lease for owner and raises its epoch. It reports false when another owner
// still holds it, when the row does not exist, or when the row lock is not free within 3 s.
//
// The first read takes no lock, so a claim attempt on a live lease never queues behind its owner's writes. The claim
// itself selects the row FOR UPDATE: a plain UPDATE of owner_id and epoch would only take FOR NO KEY UPDATE, which does
// not conflict with the FOR KEY SHARE the owner's fenced writes hold, and a takeover must wait for those writes.
func (s *Store) ClaimLease(ctx context.Context, key LeaseKey, owner uuid.UUID, ttl time.Duration) (Lease, bool, error) {
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, ClaimDeadline)
	defer cancel()
	l, ok, err := s.claimLease(ctx, key, owner, ttl)
	if err != nil && parent.Err() == nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return Lease{}, false, nil // out of time behind a lock: not claimed, try again later
	}
	return l, ok, err
}

func (s *Store) claimLease(ctx context.Context, key LeaseKey, owner uuid.UUID, ttl time.Duration) (Lease, bool, error) {
	var live bool
	err := s.Pool.QueryRow(ctx,
		`select owner_id is not null and expires_at >= now() from poll_leases
		 where kind = $1 and account_id = $2 and region = $3`,
		key.Kind, key.AccountID, key.Region).Scan(&live)
	if errors.Is(err, pgx.ErrNoRows) {
		return Lease{}, false, nil
	}
	if err != nil {
		return Lease{}, false, fmt.Errorf("claim lease: %w", err)
	}
	if live {
		return Lease{}, false, nil
	}

	l := Lease{Key: key, OwnerID: owner}
	claimed := false
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `set local lock_timeout = '`+lockTimeout+`'`); err != nil {
			return err
		}
		// The condition is re-evaluated after the lock wait, so a row renewed or claimed meanwhile is not taken.
		var epoch int64
		err := tx.QueryRow(ctx,
			`select epoch from poll_leases
			 where kind = $1 and account_id = $2 and region = $3
			   and (owner_id is null or expires_at < clock_timestamp())
			 for update`, key.Kind, key.AccountID, key.Region).Scan(&epoch)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx,
			`update poll_leases set owner_id = $4, epoch = epoch + 1,
			   expires_at = clock_timestamp() + $5 * interval '1 millisecond',
			   acquired_at = clock_timestamp(), renewed_at = clock_timestamp()
			 where kind = $1 and account_id = $2 and region = $3
			 returning epoch, expires_at`,
			key.Kind, key.AccountID, key.Region, owner, ttl.Milliseconds()).Scan(&l.Epoch, &l.ExpiresAt); err != nil {
			return err
		}
		claimed = true
		return nil
	})
	if isLockTimeout(err) {
		return Lease{}, false, nil
	}
	if err != nil {
		return Lease{}, false, fmt.Errorf("claim lease: %w", err)
	}
	if !claimed {
		return Lease{}, false, nil
	}
	return l, true, nil
}

// RenewLease extends the lease by ttl from now. It is a plain UPDATE of non-key columns (FOR NO KEY UPDATE), which does
// not conflict with the owner's own fenced writes (FOR KEY SHARE), so a long write never delays a renewal.
// ErrLeaseLost when the owner or epoch no longer match.
func (s *Store) RenewLease(ctx context.Context, l Lease, ttl time.Duration) error {
	tag, err := s.Pool.Exec(ctx,
		`update poll_leases set expires_at = now() + $6 * interval '1 millisecond', renewed_at = now()
		 where kind = $1 and account_id = $2 and region = $3 and owner_id = $4 and epoch = $5`,
		l.Key.Kind, l.Key.AccountID, l.Key.Region, l.OwnerID, l.Epoch, ttl.Milliseconds())
	if err != nil {
		return fmt.Errorf("renew lease: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrLeaseLost
	}
	return nil
}

// ReleaseLease frees a lease the caller holds, after the writes that are in flight under it have finished (FOR UPDATE
// waits for them). The epoch is not changed; the next claim raises it. A lease already lost is not an error.
func (s *Store) ReleaseLease(ctx context.Context, l Lease) error {
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `set local lock_timeout = '`+lockTimeout+`'`); err != nil {
			return err
		}
		var one int
		err := tx.QueryRow(ctx,
			`select 1 from poll_leases
			 where kind = $1 and account_id = $2 and region = $3 and owner_id = $4 and epoch = $5
			 for update`, l.Key.Kind, l.Key.AccountID, l.Key.Region, l.OwnerID, l.Epoch).Scan(&one)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx,
			`update poll_leases set owner_id = null, expires_at = now()
			 where kind = $1 and account_id = $2 and region = $3`, l.Key.Kind, l.Key.AccountID, l.Key.Region)
		return err
	})
	if err != nil {
		return fmt.Errorf("release lease: %w", err)
	}
	return nil
}

// CheckLease is the guard before an external side effect: it returns ErrLeaseLost unless the lease is still held at
// this epoch with at least minRemaining left. It takes no lock; it cannot fence, only avoid acting on stale decisions.
func (s *Store) CheckLease(ctx context.Context, l Lease, minRemaining time.Duration) error {
	var one int
	err := s.Pool.QueryRow(ctx,
		`select 1 from poll_leases
		 where kind = $1 and account_id = $2 and region = $3 and owner_id = $4 and epoch = $5
		   and expires_at >= now() + $6 * interval '1 millisecond'`,
		l.Key.Kind, l.Key.AccountID, l.Key.Region, l.OwnerID, l.Epoch, minRemaining.Milliseconds()).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrLeaseLost
	}
	if err != nil {
		return fmt.Errorf("check lease: %w", err)
	}
	return nil
}

// ListLeases returns every lease row ordered by kind, account and region.
func (s *Store) ListLeases(ctx context.Context) ([]LeaseRow, error) {
	rows, err := s.Pool.Query(ctx,
		`select kind, account_id, region, coalesce(owner_id, '00000000-0000-0000-0000-000000000000'::uuid), epoch,
		   expires_at, acquired_at, renewed_at, (owner_id is null or expires_at < now())
		 from poll_leases order by kind, account_id, region`)
	if err != nil {
		return nil, fmt.Errorf("list leases: %w", err)
	}
	defer rows.Close()
	out := []LeaseRow{}
	for rows.Next() {
		var r LeaseRow
		if err := rows.Scan(&r.Key.Kind, &r.Key.AccountID, &r.Key.Region, &r.OwnerID, &r.Epoch,
			&r.ExpiresAt, &r.AcquiredAt, &r.RenewedAt, &r.Free); err != nil {
			return nil, fmt.Errorf("scan lease: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list leases: %w", err)
	}
	return out, nil
}

// SliceWeights returns the number of clusters per account x region, the weight the rebalance planner uses.
// Slices without clusters are absent (the planner counts them as 1).
func (s *Store) SliceWeights(ctx context.Context) (map[awsx.Target]int, error) {
	rows, err := s.Pool.Query(ctx, `select account_id, region, count(*) from clusters group by account_id, region`)
	if err != nil {
		return nil, fmt.Errorf("slice weights: %w", err)
	}
	defer rows.Close()
	out := map[awsx.Target]int{}
	for rows.Next() {
		var t awsx.Target
		var n int
		if err := rows.Scan(&t.AccountID, &t.Region, &n); err != nil {
			return nil, fmt.Errorf("scan slice weight: %w", err)
		}
		out[t] = n
	}
	return out, rows.Err()
}
