package store

import (
	"context"
	"fmt"

	"github.com/rcss-labs/harbormaster/internal/model"
)

// NotificationTargetCluster is a cluster a route's scope can select, with its account's display name.
type NotificationTargetCluster struct {
	Key         model.ClusterKey
	AccountName string
}

// NotificationTargetService is an ACTIVE service a route's scope can select.
type NotificationTargetService struct {
	Key         model.ServiceKey
	AccountName string
}

// NotificationTargets is everything the scope picker can offer except tags.
type NotificationTargets struct {
	Accounts []AWSAccount                // every connected account, by name
	Clusters []NotificationTargetCluster // every cluster in the snapshot, by account name, region, name
	Services []NotificationTargetService // every ACTIVE service, by account name, region, cluster, name
}

// NotificationTargets lists the accounts, clusters and services a route can be scoped to. It applies no
// visibility filter: it serves global admins and the dispatcher only.
func (s *Store) NotificationTargets(ctx context.Context) (NotificationTargets, error) {
	accounts, err := s.ListAccounts(ctx)
	if err != nil {
		return NotificationTargets{}, err
	}
	out := NotificationTargets{Accounts: accounts, Clusters: []NotificationTargetCluster{}, Services: []NotificationTargetService{}}

	rows, err := s.Pool.Query(ctx, `select c.account_id, c.region, c.cluster_name, coalesce(a.name, c.account_id)
		from clusters c left join aws_accounts a on a.account_id = c.account_id
		order by coalesce(a.name, c.account_id), c.account_id, c.region, c.cluster_name`)
	if err != nil {
		return NotificationTargets{}, fmt.Errorf("notification targets: clusters: %w", err)
	}
	for rows.Next() {
		var c NotificationTargetCluster
		if err := rows.Scan(&c.Key.AccountID, &c.Key.Region, &c.Key.Cluster, &c.AccountName); err != nil {
			rows.Close()
			return NotificationTargets{}, fmt.Errorf("notification targets: clusters: %w", err)
		}
		out.Clusters = append(out.Clusters, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return NotificationTargets{}, err
	}

	rows, err = s.Pool.Query(ctx, `select sv.account_id, sv.region, sv.cluster_name, sv.service_name, coalesce(a.name, sv.account_id)
		from services sv left join aws_accounts a on a.account_id = sv.account_id
		where sv.status = 'ACTIVE'
		order by coalesce(a.name, sv.account_id), sv.account_id, sv.region, sv.cluster_name, sv.service_name`)
	if err != nil {
		return NotificationTargets{}, fmt.Errorf("notification targets: services: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var sv NotificationTargetService
		if err := rows.Scan(&sv.Key.AccountID, &sv.Key.Region, &sv.Key.Cluster, &sv.Key.Service, &sv.AccountName); err != nil {
			return NotificationTargets{}, fmt.Errorf("notification targets: services: %w", err)
		}
		out.Services = append(out.Services, sv)
	}
	return out, rows.Err()
}
