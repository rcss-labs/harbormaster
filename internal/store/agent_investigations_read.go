package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/rcss-labs/harbormaster/internal/model"
)

// GetInvestigationByIncident returns the investigation an incident is linked to, or ErrNotFound when there is none
// (the link goes with the incident when retention deletes it).
func (s *Store) GetInvestigationByIncident(ctx context.Context, incidentID int64) (Investigation, error) {
	v, err := scanInvestigation(s.Pool.QueryRow(ctx, `select `+investigationColumnList("v.")+`
		from agent_investigation_incidents l join agent_investigations v on v.id = l.investigation_id
		where l.incident_id = $1`, incidentID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Investigation{}, ErrNotFound
	}
	if err != nil {
		return Investigation{}, fmt.Errorf("get investigation by incident: %w", err)
	}
	return v, nil
}

// ExistingServices returns which of the given services are in the latest snapshot, in one query. A service that is
// not in the result is gone.
func (s *Store) ExistingServices(ctx context.Context, keys []model.ServiceKey) (map[model.ServiceKey]bool, error) {
	out := map[model.ServiceKey]bool{}
	if len(keys) == 0 {
		return out, nil
	}
	accounts, regions, clusters, services := make([]string, len(keys)), make([]string, len(keys)), make([]string, len(keys)), make([]string, len(keys))
	for i, k := range keys {
		accounts[i], regions[i], clusters[i], services[i] = k.AccountID, k.Region, k.Cluster, k.Service
	}
	rows, err := s.Pool.Query(ctx, `select sv.account_id, sv.region, sv.cluster_name, sv.service_name from services sv
		join unnest($1::text[], $2::text[], $3::text[], $4::text[]) k(account_id, region, cluster_name, service_name)
		  on sv.account_id = k.account_id and sv.region = k.region and sv.cluster_name = k.cluster_name and sv.service_name = k.service_name`,
		accounts, regions, clusters, services)
	if err != nil {
		return nil, fmt.Errorf("existing services: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var k model.ServiceKey
		if err := rows.Scan(&k.AccountID, &k.Region, &k.Cluster, &k.Service); err != nil {
			return nil, fmt.Errorf("existing services: %w", err)
		}
		out[k] = true
	}
	return out, rows.Err()
}
