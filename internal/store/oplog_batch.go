package store

import (
	"context"
	"encoding/json"
	"fmt"
)

// AppendOperations records several operations in one statement, so they all land or none does. Each row is stored
// as AppendOperation would store it.
func (s *Store) AppendOperations(ctx context.Context, ops []Operation) error {
	if len(ops) == 0 {
		return nil
	}
	n := len(ops)
	accounts, regions, clusters := make([]string, n), make([]string, n), make([]string, n)
	services, details := make([]*string, n), make([]*string, n)
	actors := make([]*int64, n)
	sources, texts := make([]string, n), make([]string, n)
	for i, op := range ops {
		accounts[i], regions[i], clusters[i], services[i] = op.AccountID, op.Region, op.Cluster, op.Service
		actors[i], sources[i], texts[i] = op.ActorUserID, op.Source, op.Text
		if op.Details != nil {
			encoded, err := json.Marshal(op.Details)
			if err != nil {
				return fmt.Errorf("encode operation details: %w", err)
			}
			text := string(encoded)
			details[i] = &text
		}
	}
	_, err := s.Pool.Exec(ctx,
		`insert into operation_log (account_id, region, cluster_name, service_name, service_arn,
		   actor_user_id, actor_username, source, text, details)
		 select o.account_id, o.region, o.cluster_name, o.service_name,
		   (select sv.arn from services sv where sv.account_id = o.account_id and sv.region = o.region
		      and sv.cluster_name = o.cluster_name and sv.service_name = o.service_name),
		   o.actor_user_id,
		   (select u.username from users u where u.id = o.actor_user_id),
		   o.source, o.text, o.details::jsonb
		 from unnest($1::text[], $2::text[], $3::text[], $4::text[], $5::bigint[], $6::text[], $7::text[], $8::text[])
		   with ordinality as o(account_id, region, cluster_name, service_name, actor_user_id, source, text, details, ord)
		 order by o.ord`,
		accounts, regions, clusters, services, actors, sources, texts, details)
	if err != nil {
		return fmt.Errorf("append operations: %w", err)
	}
	return nil
}
