package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/rcss-labs/harbormaster/internal/model"
)

// ScanAcceptance is a risk acceptance of one finding on one digest for one service. Active means not revoked and not
// expired; expiry is evaluated by the reader, never stored.
type ScanAcceptance struct {
	ID                                                  int64
	Digest, VulnID, PackageName, PackageVersion, Reason string
	Service                                             model.ServiceKey
	AcceptedBy                                          *int64
	AcceptedByUsername                                  string
	CreatedAt, ExpiresAt                                time.Time
	RevokedAt                                           *time.Time
	RevokedBy                                           *int64
}

const scanAcceptanceColumns = `a.id, a.digest, a.vuln_id, a.package_name, a.package_version, a.reason,
	a.account_id, a.region, a.cluster_name, a.service_name, a.accepted_by, coalesce(u.username, ''),
	a.created_at, a.expires_at, a.revoked_at, a.revoked_by`

const scanAcceptanceFrom = ` from scan_risk_acceptances a left join users u on u.id = a.accepted_by`

func scanScanAcceptance(row pgx.Row) (ScanAcceptance, error) {
	var a ScanAcceptance
	err := row.Scan(&a.ID, &a.Digest, &a.VulnID, &a.PackageName, &a.PackageVersion, &a.Reason,
		&a.Service.AccountID, &a.Service.Region, &a.Service.Cluster, &a.Service.Service, &a.AcceptedBy, &a.AcceptedByUsername,
		&a.CreatedAt, &a.ExpiresAt, &a.RevokedAt, &a.RevokedBy)
	return a, err
}

// FindingKey names one finding of a digest.
type FindingKey struct {
	VulnID, PackageName, PackageVersion string
}

// CreateScanAcceptances writes one acceptance per service in one transaction and returns their ids. The scan_images row
// is locked first, so two concurrent accepts of the same finding cannot both pass the duplicate check; a service that
// already has an active acceptance for the key gives ErrConflict and nothing is written. An unknown digest is ErrNotFound.
func (s *Store) CreateScanAcceptances(ctx context.Context, base ScanAcceptance, services []model.ServiceKey) ([]int64, error) {
	finding := FindingKey{VulnID: base.VulnID, PackageName: base.PackageName, PackageVersion: base.PackageVersion}
	return s.createScanAcceptances(ctx, base, []FindingKey{finding}, services, false)
}

// CreatePackageScanAcceptances accepts each finding for each service in one transaction, leaving out the pairs that
// already hold an active acceptance, and returns the ids written. Nothing left to accept is ErrConflict; an unknown
// digest is ErrNotFound. Digest, Reason, AcceptedBy, CreatedAt and ExpiresAt come from base.
func (s *Store) CreatePackageScanAcceptances(ctx context.Context, base ScanAcceptance, findings []FindingKey, services []model.ServiceKey) ([]int64, error) {
	return s.createScanAcceptances(ctx, base, findings, services, true)
}

// createScanAcceptances writes an acceptance per finding and service. An already accepted pair is skipped when
// skipAccepted is set and fails the whole call with ErrConflict otherwise.
func (s *Store) createScanAcceptances(ctx context.Context, base ScanAcceptance, findings []FindingKey, services []model.ServiceKey, skipAccepted bool) ([]int64, error) {
	if len(services) == 0 || len(findings) == 0 {
		return nil, errors.New("create scan acceptances: no services or findings")
	}
	createdAt := base.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	var ids []int64
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var locked string
		err := tx.QueryRow(ctx, `select digest from scan_images where digest = $1 for update`, base.Digest).Scan(&locked)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		for _, finding := range findings {
			for _, svc := range services {
				var exists bool
				if err := tx.QueryRow(ctx, `select exists (select 1 from scan_risk_acceptances
					where digest = $1 and vuln_id = $2 and package_name = $3 and package_version = $4
					  and account_id = $5 and region = $6 and cluster_name = $7 and service_name = $8
					  and revoked_at is null and expires_at > $9)`,
					base.Digest, finding.VulnID, finding.PackageName, finding.PackageVersion,
					svc.AccountID, svc.Region, svc.Cluster, svc.Service, createdAt).Scan(&exists); err != nil {
					return err
				}
				if exists && skipAccepted {
					continue
				}
				if exists {
					return ErrConflict
				}
				var id int64
				if err := tx.QueryRow(ctx, `insert into scan_risk_acceptances (digest, vuln_id, package_name, package_version,
					  account_id, region, cluster_name, service_name, reason, accepted_by, created_at, expires_at)
					values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) returning id`,
					base.Digest, finding.VulnID, finding.PackageName, finding.PackageVersion,
					svc.AccountID, svc.Region, svc.Cluster, svc.Service, base.Reason, base.AcceptedBy, createdAt, base.ExpiresAt).Scan(&id); err != nil {
					return err
				}
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			return ErrConflict
		}
		return nil
	})
	if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if err != nil {
		return nil, fmt.Errorf("create scan acceptances: %w", err)
	}
	return ids, nil
}

// RevokeScanAcceptance ends one acceptance and returns it. An unknown id is ErrNotFound; one already revoked is
// ErrConflict. A zero by records no user.
func (s *Store) RevokeScanAcceptance(ctx context.Context, id, by int64, now time.Time) (ScanAcceptance, error) {
	tag, err := s.Pool.Exec(ctx, `update scan_risk_acceptances set revoked_at = $2, revoked_by = nullif($3, 0::bigint)
		where id = $1 and revoked_at is null`, id, now, by)
	if err != nil {
		return ScanAcceptance{}, fmt.Errorf("revoke scan acceptance: %w", err)
	}
	acceptance, err := s.GetScanAcceptance(ctx, id)
	if err != nil {
		return ScanAcceptance{}, err
	}
	if tag.RowsAffected() == 0 {
		return acceptance, ErrConflict
	}
	return acceptance, nil
}

// ActiveScanAcceptances lists a digest's acceptances that are not revoked and expire after now.
func (s *Store) ActiveScanAcceptances(ctx context.Context, digest string, now time.Time) ([]ScanAcceptance, error) {
	rows, err := s.Pool.Query(ctx, `select `+scanAcceptanceColumns+scanAcceptanceFrom+`
		where a.digest = $1 and a.revoked_at is null and a.expires_at > $2 order by a.id`, digest, now)
	if err != nil {
		return nil, fmt.Errorf("active scan acceptances: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ScanAcceptance, error) { return scanScanAcceptance(row) })
	if err != nil {
		return nil, fmt.Errorf("active scan acceptances: %w", err)
	}
	return out, nil
}

func (s *Store) GetScanAcceptance(ctx context.Context, id int64) (ScanAcceptance, error) {
	a, err := scanScanAcceptance(s.Pool.QueryRow(ctx, `select `+scanAcceptanceColumns+scanAcceptanceFrom+` where a.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return ScanAcceptance{}, ErrNotFound
	}
	if err != nil {
		return ScanAcceptance{}, fmt.Errorf("get scan acceptance: %w", err)
	}
	return a, nil
}
