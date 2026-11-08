package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/rcss-labs/harbormaster/internal/model"
)

// PreDeployScan reports whether a service scans images before deploy; false when it has no row. The setting is keyed by
// the service's name, so a recreated service keeps it.
func (s *Store) PreDeployScan(ctx context.Context, key model.ServiceKey) (bool, error) {
	var on bool
	err := s.Pool.QueryRow(ctx, `select pre_deploy_scan from service_scan_settings
		where account_id = $1 and region = $2 and cluster_name = $3 and service_name = $4`,
		key.AccountID, key.Region, key.Cluster, key.Service).Scan(&on)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("pre-deploy scan setting: %w", err)
	}
	return on, nil
}

// SetPreDeployScan stores the setting and reports whether the value changed; a service with no row counts as off. It is
// one statement, so concurrent callers see exactly one change. A zero by records no user.
func (s *Store) SetPreDeployScan(ctx context.Context, key model.ServiceKey, on bool, by int64, now time.Time) (bool, error) {
	// A row is inserted only when turning on (a missing row already means off); an existing row is updated only when
	// the value differs, so RowsAffected is 1 exactly when the setting changed.
	tag, err := s.Pool.Exec(ctx, `insert into service_scan_settings (account_id, region, cluster_name, service_name, pre_deploy_scan, updated_at, updated_by)
		select $1, $2, $3, $4, $5::boolean, $6, nullif($7, 0::bigint)
		where $5::boolean or exists (select 1 from service_scan_settings
		  where account_id = $1 and region = $2 and cluster_name = $3 and service_name = $4)
		on conflict (account_id, region, cluster_name, service_name) do update set
		  pre_deploy_scan = excluded.pre_deploy_scan, updated_at = excluded.updated_at, updated_by = excluded.updated_by
		where service_scan_settings.pre_deploy_scan is distinct from excluded.pre_deploy_scan`,
		key.AccountID, key.Region, key.Cluster, key.Service, on, now, by)
	if err != nil {
		return false, fmt.Errorf("set pre-deploy scan setting: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}
