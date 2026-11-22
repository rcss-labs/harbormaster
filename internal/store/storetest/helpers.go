package storetest

import (
	"context"
	"fmt"

	"github.com/rcss-labs/harbormaster/internal/store"
)

// The helpers below are for tests that need a row changed in a way no production code path does. They live here, not
// in package store, so the shipped Store carries no method that only tests call.

// DeleteUser removes a user; their sessions and permissions cascade. It returns store.ErrNotFound if absent.
func DeleteUser(ctx context.Context, s *store.Store, id int64) error {
	tag, err := s.Pool.Exec(ctx, `delete from users where id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

// SetAdmin grants or revokes the global admin flag. It returns store.ErrNotFound if absent.
func SetAdmin(ctx context.Context, s *store.Store, id int64, admin bool) error {
	tag, err := s.Pool.Exec(ctx, `update users set is_admin = $2 where id = $1`, id, admin)
	if err != nil {
		return fmt.Errorf("set admin: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

// AddScanRequest records a request, or moves the time, source, requester, image and tag of the one already recorded for
// the same digest, service and container. Production records requests through Store.RequestScanImage.
func AddScanRequest(ctx context.Context, s *store.Store, r store.ScanRequest) (int64, error) {
	var id int64
	err := s.Pool.QueryRow(ctx, `insert into scan_requests (digest, account_id, region, cluster_name, service_name, container,
		  image, tag, source, requested_by, requested_at)
		values ($1, $2, $3, $4, $5, $6, $7, nullif($8, ''), $9, $10, $11)
		on conflict (digest, account_id, region, cluster_name, service_name, container) do update set
		  requested_at = excluded.requested_at, source = excluded.source, requested_by = excluded.requested_by,
		  image = excluded.image, tag = excluded.tag
		returning id`, r.Digest, r.Key.AccountID, r.Key.Region, r.Key.Cluster, r.Key.Service, r.Container,
		r.Image, r.Tag, r.Source, r.RequestedBy, r.RequestedAt).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("add scan request: %w", err)
	}
	return id, nil
}
