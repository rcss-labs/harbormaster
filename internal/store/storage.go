package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/rcss-labs/harbormaster/internal/model"
)

// ReplaceStorage upserts the EFS file systems and access points of one account and region,
// then deletes the rows of that scope that were not in the input. It runs in one transaction.
func (s *Store) ReplaceStorage(ctx context.Context, accountID, region string, fileSystems []model.FileSystem, accessPoints []model.AccessPoint) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if err := upsertFileSystems(ctx, tx, fileSystems); err != nil {
			return err
		}
		if err := upsertAccessPoints(ctx, tx, accessPoints); err != nil {
			return err
		}
		fsIDs, apIDs := []string{}, []string{}
		for _, fs := range fileSystems {
			fsIDs = append(fsIDs, fs.FileSystemID)
		}
		for _, ap := range accessPoints {
			apIDs = append(apIDs, ap.AccessPointID)
		}
		if err := deleteScopeNotIn(ctx, tx, "efs", "file_system_id", accountID, region, fsIDs); err != nil {
			return err
		}
		return deleteScopeNotIn(ctx, tx, "efs_access_points", "access_point_id", accountID, region, apIDs)
	})
}

func upsertFileSystems(ctx context.Context, tx pgx.Tx, fileSystems []model.FileSystem) error {
	if len(fileSystems) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, fs := range fileSystems {
		batch.Queue(
			`insert into efs (account_id, region, file_system_id, arn, name, size_bytes, throughput_mode,
			   provisioned_mibps, performance_mode, lifecycle_state, encrypted, vpc_id, mount_target_azs, raw, polled_at)
			 values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
			 on conflict (account_id, region, file_system_id) do update set
			   arn = excluded.arn, name = excluded.name, size_bytes = excluded.size_bytes,
			   throughput_mode = excluded.throughput_mode, provisioned_mibps = excluded.provisioned_mibps,
			   performance_mode = excluded.performance_mode, lifecycle_state = excluded.lifecycle_state,
			   encrypted = excluded.encrypted, vpc_id = excluded.vpc_id,
			   mount_target_azs = excluded.mount_target_azs, raw = excluded.raw, polled_at = excluded.polled_at`,
			fs.AccountID, fs.Region, fs.FileSystemID, fs.ARN, fs.Name, fs.SizeBytes, fs.ThroughputMode,
			fs.ProvisionedMiBps, fs.PerformanceMode, fs.LifecycleState, fs.Encrypted, fs.VPCID,
			nonNilStrings(fs.MountTargetAZs), rawOrEmpty(fs.Raw), polledAtOrNow(fs.PolledAt))
	}
	if err := execTxBatch(ctx, tx, batch); err != nil {
		return fmt.Errorf("replace storage: upsert efs: %w", err)
	}
	return nil
}

func upsertAccessPoints(ctx context.Context, tx pgx.Tx, accessPoints []model.AccessPoint) error {
	if len(accessPoints) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, ap := range accessPoints {
		batch.Queue(
			`insert into efs_access_points (account_id, region, access_point_id, file_system_id, name, root_path,
			   posix_uid, posix_gid, raw, polled_at)
			 values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			 on conflict (account_id, region, access_point_id) do update set
			   file_system_id = excluded.file_system_id, name = excluded.name, root_path = excluded.root_path,
			   posix_uid = excluded.posix_uid, posix_gid = excluded.posix_gid, raw = excluded.raw,
			   polled_at = excluded.polled_at`,
			ap.AccountID, ap.Region, ap.AccessPointID, ap.FileSystemID, ap.Name, ap.RootPath,
			ap.PosixUID, ap.PosixGID, rawOrEmpty(ap.Raw), polledAtOrNow(ap.PolledAt))
	}
	if err := execTxBatch(ctx, tx, batch); err != nil {
		return fmt.Errorf("replace storage: upsert access points: %w", err)
	}
	return nil
}

// ListFileSystems returns the cached EFS file systems in the scope, ordered by account, region and id.
func (s *Store) ListFileSystems(ctx context.Context, accountID, region string) ([]model.FileSystem, error) {
	rows, err := s.Pool.Query(ctx,
		`select account_id, region, file_system_id, arn, name, size_bytes, throughput_mode, provisioned_mibps,
		   performance_mode, lifecycle_state, encrypted, vpc_id, mount_target_azs, raw, polled_at
		 from efs where `+scopeWhere+` order by account_id, region, file_system_id`,
		accountID, region)
	if err != nil {
		return nil, fmt.Errorf("list file systems: %w", err)
	}
	defer rows.Close()
	fileSystems := []model.FileSystem{}
	for rows.Next() {
		var fs model.FileSystem
		var raw []byte
		if err := rows.Scan(&fs.AccountID, &fs.Region, &fs.FileSystemID, &fs.ARN, &fs.Name, &fs.SizeBytes,
			&fs.ThroughputMode, &fs.ProvisionedMiBps, &fs.PerformanceMode, &fs.LifecycleState, &fs.Encrypted,
			&fs.VPCID, &fs.MountTargetAZs, &raw, &fs.PolledAt); err != nil {
			return nil, fmt.Errorf("scan file system: %w", err)
		}
		fs.Raw = json.RawMessage(raw)
		fileSystems = append(fileSystems, fs)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list file systems: %w", err)
	}
	return fileSystems, nil
}

// ListAccessPoints returns the cached EFS access points in the scope, ordered by account, region and id.
func (s *Store) ListAccessPoints(ctx context.Context, accountID, region string) ([]model.AccessPoint, error) {
	rows, err := s.Pool.Query(ctx,
		`select account_id, region, access_point_id, file_system_id, name, root_path, posix_uid, posix_gid, raw, polled_at
		 from efs_access_points where `+scopeWhere+` order by account_id, region, access_point_id`,
		accountID, region)
	if err != nil {
		return nil, fmt.Errorf("list access points: %w", err)
	}
	defer rows.Close()
	accessPoints := []model.AccessPoint{}
	for rows.Next() {
		var ap model.AccessPoint
		var raw []byte
		if err := rows.Scan(&ap.AccountID, &ap.Region, &ap.AccessPointID, &ap.FileSystemID, &ap.Name, &ap.RootPath,
			&ap.PosixUID, &ap.PosixGID, &raw, &ap.PolledAt); err != nil {
			return nil, fmt.Errorf("scan access point: %w", err)
		}
		ap.Raw = json.RawMessage(raw)
		accessPoints = append(accessPoints, ap)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list access points: %w", err)
	}
	return accessPoints, nil
}
