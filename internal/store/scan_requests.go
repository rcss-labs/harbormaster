package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/rcss-labs/harbormaster/internal/model"
)

// ScanRequest records that a service container asked for an image to be scanned.
type ScanRequest struct {
	ID                            int64
	Digest                        string
	Key                           model.ServiceKey
	Container, Image, Tag, Source string
	RequestedBy                   *int64
	RequestedByUsername           string
	RequestedAt                   time.Time
}

const scanRequestColumns = `r.id, r.digest, r.account_id, r.region, r.cluster_name, r.service_name, r.container, r.image,
	coalesce(r.tag, ''), r.source, r.requested_by, coalesce(u.username, ''), r.requested_at`

const scanRequestFrom = ` from scan_requests r left join users u on u.id = r.requested_by`

func scanScanRequest(row pgx.Row) (ScanRequest, error) {
	var r ScanRequest
	err := row.Scan(&r.ID, &r.Digest, &r.Key.AccountID, &r.Key.Region, &r.Key.Cluster, &r.Key.Service, &r.Container, &r.Image,
		&r.Tag, &r.Source, &r.RequestedBy, &r.RequestedByUsername, &r.RequestedAt)
	return r, err
}

// AddScanRequest records a request, or moves the time, source, requester, image and tag of the one already recorded for
// the same digest, service and container.
func (s *Store) AddScanRequest(ctx context.Context, r ScanRequest) (int64, error) {
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

// ScanRequestsForDigests groups the requests of the given digests, newest first.
func (s *Store) ScanRequestsForDigests(ctx context.Context, digests []string) (map[string][]ScanRequest, error) {
	rows, err := s.Pool.Query(ctx, `select `+scanRequestColumns+scanRequestFrom+`
		where r.digest = any($1) order by r.requested_at desc, r.id desc`, digests)
	if err != nil {
		return nil, fmt.Errorf("scan requests for digests: %w", err)
	}
	defer rows.Close()
	out := map[string][]ScanRequest{}
	for rows.Next() {
		r, err := scanScanRequest(rows)
		if err != nil {
			return nil, fmt.Errorf("scan requests for digests: %w", err)
		}
		out[r.Digest] = append(out[r.Digest], r)
	}
	return out, rows.Err()
}

// LatestScanRequest returns the newest request of a service container, matched by digest when one is given, else by tag.
func (s *Store) LatestScanRequest(ctx context.Context, key model.ServiceKey, container, tag, digest string) (ScanRequest, error) {
	match, arg := `coalesce(r.tag, '') = $6`, tag
	if digest != "" {
		match, arg = `r.digest = $6`, digest
	}
	r, err := scanScanRequest(s.Pool.QueryRow(ctx, `select `+scanRequestColumns+scanRequestFrom+`
		where r.account_id = $1 and r.region = $2 and r.cluster_name = $3 and r.service_name = $4 and r.container = $5
		  and `+match+` order by r.requested_at desc, r.id desc limit 1`,
		key.AccountID, key.Region, key.Cluster, key.Service, container, arg))
	if errors.Is(err, pgx.ErrNoRows) {
		return ScanRequest{}, ErrNotFound
	}
	if err != nil {
		return ScanRequest{}, fmt.Errorf("latest scan request: %w", err)
	}
	return r, nil
}

// VisibleScanImages lists images with at least one request from a service the viewer may see, using the visibility rule
// of the task queries (service tag, cluster tag, Dev). Newest request first.
func (s *Store) VisibleScanImages(ctx context.Context, f TargetFilter, vis Visibility) ([]ScanImage, error) {
	var q queryArgs
	v := q.visibility("r", vis)
	target := q.target("r", f)
	imgs, err := s.queryScanImages(ctx, `select `+scanImageColumns+` from scan_images
		where exists (select 1 from scan_requests r`+v.Joins+` where r.digest = scan_images.digest and `+target+` and `+v.Predicate+`)
		order by last_requested_at desc, digest`, q.values...)
	if err != nil {
		return nil, fmt.Errorf("visible scan images: %w", err)
	}
	return imgs, nil
}

// RequestingServices lists the distinct services that requested a digest. Their tags are resolved by the caller, so a
// deleted service still resolves by name.
func (s *Store) RequestingServices(ctx context.Context, digest string) ([]model.ServiceKey, error) {
	rows, err := s.Pool.Query(ctx, `select distinct account_id, region, cluster_name, service_name from scan_requests
		where digest = $1 order by account_id, region, cluster_name, service_name`, digest)
	if err != nil {
		return nil, fmt.Errorf("requesting services: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (model.ServiceKey, error) {
		var k model.ServiceKey
		err := row.Scan(&k.AccountID, &k.Region, &k.Cluster, &k.Service)
		return k, err
	})
	if err != nil {
		return nil, fmt.Errorf("requesting services: %w", err)
	}
	return out, nil
}
