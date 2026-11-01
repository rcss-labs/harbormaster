package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/rcss-labs/harbormaster/internal/model"
)

// scopeWhere filters by account and region; an empty value matches every account or region.
const scopeWhere = `($1 = '' or account_id = $1) and ($2 = '' or region = $2)`

// ReplaceNetwork upserts the VPCs, subnets, edges and security groups of one account and region,
// then deletes the rows of that scope that were not in the input. It runs in one transaction.
func (s *Store) ReplaceNetwork(ctx context.Context, accountID, region string, vpcs []model.VPC, subnets []model.Subnet, edges []model.VPCEdge, sgs []model.SecurityGroup) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if err := upsertVPCs(ctx, tx, vpcs); err != nil {
			return err
		}
		if err := upsertSubnets(ctx, tx, subnets); err != nil {
			return err
		}
		if err := upsertVPCEdges(ctx, tx, edges); err != nil {
			return err
		}
		if err := upsertSecurityGroups(ctx, tx, sgs); err != nil {
			return err
		}
		vpcIDs, subnetIDs, edgeIDs, sgIDs := []string{}, []string{}, []string{}, []string{}
		for _, v := range vpcs {
			vpcIDs = append(vpcIDs, v.VPCID)
		}
		for _, sn := range subnets {
			subnetIDs = append(subnetIDs, sn.SubnetID)
		}
		for _, e := range edges {
			edgeIDs = append(edgeIDs, e.ResourceID)
		}
		for _, g := range sgs {
			sgIDs = append(sgIDs, g.GroupID)
		}
		if err := deleteScopeNotIn(ctx, tx, "vpcs", "vpc_id", accountID, region, vpcIDs); err != nil {
			return err
		}
		if err := deleteScopeNotIn(ctx, tx, "subnets", "subnet_id", accountID, region, subnetIDs); err != nil {
			return err
		}
		if err := deleteScopeNotIn(ctx, tx, "vpc_edges", "resource_id", accountID, region, edgeIDs); err != nil {
			return err
		}
		return deleteScopeNotIn(ctx, tx, "security_groups", "group_id", accountID, region, sgIDs)
	})
}

// deleteScopeNotIn deletes the rows of one account and region whose id column is not in keep.
// keep must be non-nil: a nil slice binds NULL, and NOT (x = ANY(NULL)) deletes nothing.
func deleteScopeNotIn(ctx context.Context, tx pgx.Tx, table, idColumn, accountID, region string, keep []string) error {
	sql := fmt.Sprintf(`delete from %s where account_id = $1 and region = $2 and not (%s = any($3))`, table, idColumn)
	if _, err := tx.Exec(ctx, sql, accountID, region, keep); err != nil {
		return fmt.Errorf("replace network: prune %s: %w", table, err)
	}
	return nil
}

// execTxBatch sends batch inside tx and returns the first failing statement's error, if any.
func execTxBatch(ctx context.Context, tx pgx.Tx, batch *pgx.Batch) error {
	results := tx.SendBatch(ctx, batch)
	for range batch.Len() {
		if _, err := results.Exec(); err != nil {
			results.Close()
			return err
		}
	}
	return results.Close()
}

func upsertVPCs(ctx context.Context, tx pgx.Tx, vpcs []model.VPC) error {
	if len(vpcs) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, v := range vpcs {
		batch.Queue(
			`insert into vpcs (account_id, region, vpc_id, name, cidr, is_default, raw, polled_at)
			 values ($1, $2, $3, $4, $5, $6, $7, $8)
			 on conflict (account_id, region, vpc_id) do update set
			   name = excluded.name, cidr = excluded.cidr, is_default = excluded.is_default,
			   raw = excluded.raw, polled_at = excluded.polled_at`,
			v.AccountID, v.Region, v.VPCID, v.Name, v.CIDR, v.IsDefault, rawOrEmpty(v.Raw), polledAtOrNow(v.PolledAt))
	}
	if err := execTxBatch(ctx, tx, batch); err != nil {
		return fmt.Errorf("replace network: upsert vpcs: %w", err)
	}
	return nil
}

func upsertSubnets(ctx context.Context, tx pgx.Tx, subnets []model.Subnet) error {
	if len(subnets) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, sn := range subnets {
		batch.Queue(
			`insert into subnets (account_id, region, subnet_id, vpc_id, name, cidr, az, available_ips, public, raw, polled_at)
			 values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			 on conflict (account_id, region, subnet_id) do update set
			   vpc_id = excluded.vpc_id, name = excluded.name, cidr = excluded.cidr, az = excluded.az,
			   available_ips = excluded.available_ips, public = excluded.public, raw = excluded.raw,
			   polled_at = excluded.polled_at`,
			sn.AccountID, sn.Region, sn.SubnetID, sn.VPCID, sn.Name, sn.CIDR, sn.AZ, sn.AvailableIPs,
			sn.Public, rawOrEmpty(sn.Raw), polledAtOrNow(sn.PolledAt))
	}
	if err := execTxBatch(ctx, tx, batch); err != nil {
		return fmt.Errorf("replace network: upsert subnets: %w", err)
	}
	return nil
}

func upsertVPCEdges(ctx context.Context, tx pgx.Tx, edges []model.VPCEdge) error {
	if len(edges) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, e := range edges {
		batch.Queue(
			`insert into vpc_edges (account_id, region, vpc_id, kind, resource_id, subnet_id, detail, raw, polled_at)
			 values ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			 on conflict (account_id, region, resource_id) do update set
			   vpc_id = excluded.vpc_id, kind = excluded.kind, subnet_id = excluded.subnet_id,
			   detail = excluded.detail, raw = excluded.raw, polled_at = excluded.polled_at`,
			e.AccountID, e.Region, e.VPCID, string(e.Kind), e.ResourceID, nullString(e.SubnetID), e.Detail,
			rawOrEmpty(e.Raw), polledAtOrNow(e.PolledAt))
	}
	if err := execTxBatch(ctx, tx, batch); err != nil {
		return fmt.Errorf("replace network: upsert vpc edges: %w", err)
	}
	return nil
}

func upsertSecurityGroups(ctx context.Context, tx pgx.Tx, groups []model.SecurityGroup) error {
	if len(groups) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, g := range groups {
		batch.Queue(
			`insert into security_groups (account_id, region, group_id, vpc_id, name, raw, polled_at)
			 values ($1, $2, $3, $4, $5, $6, $7)
			 on conflict (account_id, region, group_id) do update set
			   vpc_id = excluded.vpc_id, name = excluded.name, raw = excluded.raw, polled_at = excluded.polled_at`,
			g.AccountID, g.Region, g.GroupID, g.VPCID, g.Name, rawOrEmpty(g.Raw), polledAtOrNow(g.PolledAt))
	}
	if err := execTxBatch(ctx, tx, batch); err != nil {
		return fmt.Errorf("replace network: upsert security groups: %w", err)
	}
	return nil
}

// ListVPCs returns the cached VPCs in the scope, ordered by account, region and VPC id.
func (s *Store) ListVPCs(ctx context.Context, accountID, region string) ([]model.VPC, error) {
	rows, err := s.Pool.Query(ctx,
		`select account_id, region, vpc_id, name, cidr, is_default, raw, polled_at from vpcs
		 where `+scopeWhere+` order by account_id, region, vpc_id`,
		accountID, region)
	if err != nil {
		return nil, fmt.Errorf("list vpcs: %w", err)
	}
	defer rows.Close()
	vpcs := []model.VPC{}
	for rows.Next() {
		var v model.VPC
		var raw []byte
		if err := rows.Scan(&v.AccountID, &v.Region, &v.VPCID, &v.Name, &v.CIDR, &v.IsDefault, &raw, &v.PolledAt); err != nil {
			return nil, fmt.Errorf("scan vpc: %w", err)
		}
		v.Raw = json.RawMessage(raw)
		vpcs = append(vpcs, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list vpcs: %w", err)
	}
	return vpcs, nil
}

// GetVPC returns one cached VPC, or ErrNotFound when it has not been polled.
func (s *Store) GetVPC(ctx context.Context, accountID, region, vpcID string) (model.VPC, error) {
	var v model.VPC
	var raw []byte
	err := s.Pool.QueryRow(ctx,
		`select account_id, region, vpc_id, name, cidr, is_default, raw, polled_at from vpcs
		 where account_id = $1 and region = $2 and vpc_id = $3`,
		accountID, region, vpcID).
		Scan(&v.AccountID, &v.Region, &v.VPCID, &v.Name, &v.CIDR, &v.IsDefault, &raw, &v.PolledAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.VPC{}, ErrNotFound
	}
	if err != nil {
		return model.VPC{}, fmt.Errorf("get vpc: %w", err)
	}
	v.Raw = json.RawMessage(raw)
	return v, nil
}

// ListSubnets returns the cached subnets in the scope, ordered by account, region and subnet id.
func (s *Store) ListSubnets(ctx context.Context, accountID, region string) ([]model.Subnet, error) {
	rows, err := s.Pool.Query(ctx,
		`select account_id, region, subnet_id, vpc_id, name, cidr, az, available_ips, public, raw, polled_at
		 from subnets where `+scopeWhere+` order by account_id, region, subnet_id`,
		accountID, region)
	if err != nil {
		return nil, fmt.Errorf("list subnets: %w", err)
	}
	defer rows.Close()
	subnets := []model.Subnet{}
	for rows.Next() {
		var sn model.Subnet
		var raw []byte
		if err := rows.Scan(&sn.AccountID, &sn.Region, &sn.SubnetID, &sn.VPCID, &sn.Name, &sn.CIDR, &sn.AZ,
			&sn.AvailableIPs, &sn.Public, &raw, &sn.PolledAt); err != nil {
			return nil, fmt.Errorf("scan subnet: %w", err)
		}
		sn.Raw = json.RawMessage(raw)
		subnets = append(subnets, sn)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list subnets: %w", err)
	}
	return subnets, nil
}

// ListVPCEdges returns the gateways and endpoints attached to one VPC in the scope.
func (s *Store) ListVPCEdges(ctx context.Context, accountID, region, vpcID string) ([]model.VPCEdge, error) {
	rows, err := s.Pool.Query(ctx,
		`select account_id, region, vpc_id, kind, resource_id, subnet_id, detail, raw, polled_at
		 from vpc_edges where `+scopeWhere+` and vpc_id = $3 order by resource_id`,
		accountID, region, vpcID)
	if err != nil {
		return nil, fmt.Errorf("list vpc edges: %w", err)
	}
	defer rows.Close()
	edges := []model.VPCEdge{}
	for rows.Next() {
		var e model.VPCEdge
		var kind string
		var subnetID *string
		var raw []byte
		if err := rows.Scan(&e.AccountID, &e.Region, &e.VPCID, &kind, &e.ResourceID, &subnetID, &e.Detail,
			&raw, &e.PolledAt); err != nil {
			return nil, fmt.Errorf("scan vpc edge: %w", err)
		}
		e.Kind = model.VPCEdgeKind(kind)
		if subnetID != nil {
			e.SubnetID = *subnetID
		}
		e.Raw = json.RawMessage(raw)
		edges = append(edges, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list vpc edges: %w", err)
	}
	return edges, nil
}

// ListSecurityGroups returns the cached security groups in the scope whose ids are in ids.
func (s *Store) ListSecurityGroups(ctx context.Context, accountID, region string, ids []string) ([]model.SecurityGroup, error) {
	rows, err := s.Pool.Query(ctx,
		`select account_id, region, group_id, vpc_id, name, raw, polled_at from security_groups
		 where `+scopeWhere+` and group_id = any($3) order by account_id, region, group_id`,
		accountID, region, nonNilStrings(ids))
	if err != nil {
		return nil, fmt.Errorf("list security groups: %w", err)
	}
	defer rows.Close()
	groups := []model.SecurityGroup{}
	for rows.Next() {
		var g model.SecurityGroup
		var raw []byte
		if err := rows.Scan(&g.AccountID, &g.Region, &g.GroupID, &g.VPCID, &g.Name, &raw, &g.PolledAt); err != nil {
			return nil, fmt.Errorf("scan security group: %w", err)
		}
		g.Raw = json.RawMessage(raw)
		groups = append(groups, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list security groups: %w", err)
	}
	return groups, nil
}
