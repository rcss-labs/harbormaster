package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ScanRun is one scanner task (kind scan).
type ScanRun struct {
	ID                                                        int64
	Kind, TaskARN, ClusterARN                                 string
	BatchSize                                                 int
	Large                                                     bool
	Status                                                    string
	StartedAt                                                 time.Time
	FinishedAt                                                *time.Time
	ExitCode                                                  *int
	StopReason                                                string
	DBBuiltAt                                                 *time.Time
	DBChecksum, GrypeVersion, SyftVersion, ScannerImageDigest string
	ImagesOK, ImagesFailed                                    int
	Error                                                     string
}

// ScanRunPatch holds what the finish of a run records. It has no DB fields:
// those stay as recorded at launch.
type ScanRunPatch struct {
	ExitCode                                                         *int
	StopReason, GrypeVersion, SyftVersion, ScannerImageDigest, Error string
	ImagesOK, ImagesFailed                                           int
}

type ScanRunsSummary struct {
	Running         int
	RunningImages   int
	LastSuccessAt   *time.Time
	LastSuccessKind string
	LastError       string
	FailingSince    *time.Time
}

// ScanDBState is the single row describing the vulnerability database in the bucket.
type ScanDBState struct {
	BuiltAt                              *time.Time
	Checksum, ArchiveKey                 string
	ImportedAt, LastRefreshAt            *time.Time
	LastRefreshError, ScannerImageDigest string
	RefreshingSince                      *time.Time
}

const scanRunColumns = `id, kind, coalesce(task_arn, ''), coalesce(cluster_arn, ''), batch_size, large, status, started_at,
	finished_at, exit_code, coalesce(stop_reason, ''), db_built_at, coalesce(db_checksum, ''), coalesce(grype_version, ''),
	coalesce(syft_version, ''), coalesce(scanner_image_digest, ''), coalesce(images_ok, 0), coalesce(images_failed, 0),
	coalesce(error, '')`

func scanScanRun(row pgx.Row) (ScanRun, error) {
	var r ScanRun
	err := row.Scan(&r.ID, &r.Kind, &r.TaskARN, &r.ClusterARN, &r.BatchSize, &r.Large, &r.Status, &r.StartedAt,
		&r.FinishedAt, &r.ExitCode, &r.StopReason, &r.DBBuiltAt, &r.DBChecksum, &r.GrypeVersion,
		&r.SyftVersion, &r.ScannerImageDigest, &r.ImagesOK, &r.ImagesFailed, &r.Error)
	return r, err
}

// CreateScanRun records a launching run and its batch in one transaction.
func (s *Store) CreateScanRun(ctx context.Context, run ScanRun, digests []string) (int64, error) {
	var id int64
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `insert into scan_runs (kind, cluster_arn, batch_size, large, status, started_at, db_built_at, db_checksum,
			  scanner_image_digest)
			values ($1, nullif($2, ''), $3, $4, 'launching', $5, $6, nullif($7, ''), nullif($8, '')) returning id`,
			run.Kind, run.ClusterARN, run.BatchSize, run.Large, run.StartedAt, run.DBBuiltAt, run.DBChecksum, run.ScannerImageDigest).Scan(&id)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `insert into scan_run_images (run_id, digest) select $1, unnest($2::text[]) on conflict do nothing`, id, digests)
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("create scan run: %w", err)
	}
	return id, nil
}

// SetScanRunTask records the ECS task of a launching run and marks it running.
func (s *Store) SetScanRunTask(ctx context.Context, id int64, taskARN string) error {
	if _, err := s.Pool.Exec(ctx, `update scan_runs set task_arn = $2, status = 'running' where id = $1 and status = 'launching'`, id, taskARN); err != nil {
		return fmt.Errorf("set scan run task: %w", err)
	}
	return nil
}

func (s *Store) ActiveScanRuns(ctx context.Context) ([]ScanRun, error) {
	rows, err := s.Pool.Query(ctx, `select `+scanRunColumns+` from scan_runs where status in ('launching', 'running') order by id`)
	if err != nil {
		return nil, fmt.Errorf("active scan runs: %w", err)
	}
	defer rows.Close()
	var out []ScanRun
	for rows.Next() {
		r, err := scanScanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// FinishScanRun claims the run's end. It reports false when another caller already finished it,
// which makes a repeated import harmless. The database fields stay as recorded at launch.
func (s *Store) FinishScanRun(ctx context.Context, id int64, status string, patch ScanRunPatch) (bool, error) {
	tag, err := s.Pool.Exec(ctx, `update scan_runs set status = $2, finished_at = now(), exit_code = $3,
		  stop_reason = nullif($4, ''), grype_version = nullif($5, ''), syft_version = nullif($6, ''),
		  scanner_image_digest = coalesce(nullif($7, ''), scanner_image_digest), error = nullif($8, ''),
		  images_ok = $9, images_failed = $10
		where id = $1 and status in ('launching', 'running')`,
		id, status, patch.ExitCode, patch.StopReason, patch.GrypeVersion, patch.SyftVersion, patch.ScannerImageDigest,
		patch.Error, patch.ImagesOK, patch.ImagesFailed)
	if err != nil {
		return false, fmt.Errorf("finish scan run: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// FinishedScanRunIDsBefore lists finished runs older than cutoff whose bucket objects are not yet deleted.
func (s *Store) FinishedScanRunIDsBefore(ctx context.Context, cutoff time.Time, limit int) ([]int64, error) {
	rows, err := s.Pool.Query(ctx, `select id from scan_runs
		where status not in ('launching', 'running') and finished_at < $1 and objects_deleted_at is null
		order by finished_at, id limit $2`, cutoff, limit)
	if err != nil {
		return nil, fmt.Errorf("finished scan runs: %w", err)
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) MarkScanRunObjectsDeleted(ctx context.Context, id int64, now time.Time) error {
	if _, err := s.Pool.Exec(ctx, `update scan_runs set objects_deleted_at = $2 where id = $1`, id, now); err != nil {
		return fmt.Errorf("mark scan run objects deleted: %w", err)
	}
	return nil
}

func (s *Store) ScanRunDigests(ctx context.Context, id int64) ([]string, error) {
	rows, err := s.Pool.Query(ctx, `select digest from scan_run_images where run_id = $1 order by digest`, id)
	if err != nil {
		return nil, fmt.Errorf("scan run digests: %w", err)
	}
	defer rows.Close()
	digests := []string{}
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		digests = append(digests, d)
	}
	return digests, rows.Err()
}

func (s *Store) LatestScanRun(ctx context.Context, kind string) (ScanRun, error) {
	r, err := scanScanRun(s.Pool.QueryRow(ctx, `select `+scanRunColumns+` from scan_runs where kind = $1
		order by started_at desc, id desc limit 1`, kind))
	if errors.Is(err, pgx.ErrNoRows) {
		return ScanRun{}, ErrNotFound
	}
	if err != nil {
		return ScanRun{}, fmt.Errorf("latest scan run: %w", err)
	}
	return r, nil
}

// ScanRunsSummary reports active runs, the last success, and the failure streak since it.
// A failure is a run that ended failed or timed_out.
func (s *Store) ScanRunsSummary(ctx context.Context) (ScanRunsSummary, error) {
	var sum ScanRunsSummary
	err := s.Pool.QueryRow(ctx, `select
		  (select count(*) from scan_runs where status in ('launching', 'running')),
		  (select count(*) from scan_run_images ri join scan_runs r on r.id = ri.run_id where r.status in ('launching', 'running'))`).
		Scan(&sum.Running, &sum.RunningImages)
	if err != nil {
		return sum, fmt.Errorf("scan runs summary: %w", err)
	}
	err = s.Pool.QueryRow(ctx, `select finished_at, kind from scan_runs where status = 'succeeded'
		order by finished_at desc, id desc limit 1`).Scan(&sum.LastSuccessAt, &sum.LastSuccessKind)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return sum, fmt.Errorf("scan runs summary: %w", err)
	}
	err = s.Pool.QueryRow(ctx, `select min(finished_at) from scan_runs
		where status in ('failed', 'timed_out') and ($1::timestamptz is null or finished_at > $1)`, sum.LastSuccessAt).Scan(&sum.FailingSince)
	if err != nil {
		return sum, fmt.Errorf("scan runs summary: %w", err)
	}
	if sum.FailingSince != nil {
		err = s.Pool.QueryRow(ctx, `select coalesce(error, '') from scan_runs
			where status in ('failed', 'timed_out') and finished_at >= $1
			order by finished_at desc, id desc limit 1`, sum.FailingSince).Scan(&sum.LastError)
		if err != nil {
			return sum, fmt.Errorf("scan runs summary: %w", err)
		}
	}
	return sum, nil
}

// ScanDBState returns the vulnerability database state, or the zero value when none is stored.
func (s *Store) ScanDBState(ctx context.Context) (ScanDBState, error) {
	var st ScanDBState
	err := s.Pool.QueryRow(ctx, `select built_at, coalesce(checksum, ''), coalesce(archive_key, ''), imported_at, last_refresh_at,
		  coalesce(last_refresh_error, ''), coalesce(scanner_image_digest, ''), refreshing_since
		from scan_db_state where id = 1`).
		Scan(&st.BuiltAt, &st.Checksum, &st.ArchiveKey, &st.ImportedAt, &st.LastRefreshAt, &st.LastRefreshError, &st.ScannerImageDigest, &st.RefreshingSince)
	if errors.Is(err, pgx.ErrNoRows) {
		return ScanDBState{}, nil
	}
	if err != nil {
		return ScanDBState{}, fmt.Errorf("scan db state: %w", err)
	}
	return st, nil
}

func (s *Store) SetScanDBState(ctx context.Context, st ScanDBState) error {
	_, err := s.Pool.Exec(ctx, `insert into scan_db_state (id, built_at, checksum, archive_key, imported_at, last_refresh_at,
		  last_refresh_error, scanner_image_digest, refreshing_since)
		values (1, $1, nullif($2, ''), nullif($3, ''), $4, $5, nullif($6, ''), nullif($7, ''), $8)
		on conflict (id) do update set built_at = excluded.built_at, checksum = excluded.checksum, archive_key = excluded.archive_key,
		  imported_at = excluded.imported_at, last_refresh_at = excluded.last_refresh_at,
		  last_refresh_error = excluded.last_refresh_error, scanner_image_digest = excluded.scanner_image_digest,
		  refreshing_since = excluded.refreshing_since`,
		st.BuiltAt, st.Checksum, st.ArchiveKey, st.ImportedAt, st.LastRefreshAt, st.LastRefreshError, st.ScannerImageDigest, st.RefreshingSince)
	if err != nil {
		return fmt.Errorf("set scan db state: %w", err)
	}
	return nil
}

// IncrementImportAttempts counts one more try to finish a run's import and returns the new total.
func (s *Store) IncrementImportAttempts(ctx context.Context, runID int64) (int, error) {
	var attempts int
	err := s.Pool.QueryRow(ctx, `update scan_runs set import_attempts = import_attempts + 1 where id = $1 returning import_attempts`, runID).Scan(&attempts)
	if err != nil {
		return 0, fmt.Errorf("increment import attempts: %w", err)
	}
	return attempts, nil
}

// NewerActiveRunDigests returns those digests that a later run, still active, also lists. A finished older run must leave
// them to that run.
func (s *Store) NewerActiveRunDigests(ctx context.Context, runID int64, digests []string) ([]string, error) {
	rows, err := s.Pool.Query(ctx, `select distinct ri.digest from scan_run_images ri join scan_runs r on r.id = ri.run_id
		where ri.digest = any($1) and ri.run_id > $2 and r.status in ('launching', 'running')`, digests, runID)
	if err != nil {
		return nil, fmt.Errorf("newer active run digests: %w", err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("newer active run digests: %w", err)
	}
	return out, nil
}
