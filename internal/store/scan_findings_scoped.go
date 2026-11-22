package store

import (
	"context"
	"fmt"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/scan"
)

// OpenFindingCountsByScope is OpenFindingCounts for many digests at once, each with its own service set (the map
// value). A finding is accepted when every service of its digest's set has an active acceptance. Digests with an
// empty set are not counted.
func (s *Store) OpenFindingCountsByScope(ctx context.Context, scopes map[string][]model.ServiceKey, now time.Time) (map[string]scan.SeverityCounts, map[string]int, error) {
	var digests, scopeDigests, accounts, regions, clusters, services []string
	for digest, keys := range scopes {
		if len(keys) == 0 {
			continue
		}
		digests = append(digests, digest)
		for _, key := range keys {
			scopeDigests = append(scopeDigests, digest)
			accounts, regions, clusters, services = append(accounts, key.AccountID), append(regions, key.Region),
				append(clusters, key.Cluster), append(services, key.Service)
		}
	}
	if len(digests) == 0 {
		return map[string]scan.SeverityCounts{}, map[string]int{}, nil
	}
	rows, err := s.Pool.Query(ctx, `select f.digest, f.severity,
		  not exists (select 1 from unnest($3::text[], $4::text[], $5::text[], $6::text[], $7::text[]) as s(digest, account_id, region, cluster_name, service_name)
		    where s.digest = f.digest and not exists (select 1 from scan_risk_acceptances a where a.digest = f.digest and a.vuln_id = f.vuln_id
		      and a.package_name = f.package_name and a.package_version = f.package_version
		      and a.revoked_at is null and a.expires_at > $2
		      and a.account_id = s.account_id and a.region = s.region and a.cluster_name = s.cluster_name and a.service_name = s.service_name)) as accepted,
		  count(*)
		from scan_findings f where f.digest = any($1) group by 1, 2, 3`,
		digests, now, scopeDigests, accounts, regions, clusters, services)
	if err != nil {
		return nil, nil, fmt.Errorf("open finding counts by scope: %w", err)
	}
	defer rows.Close()
	return foldFindingCountRows(rows)
}
