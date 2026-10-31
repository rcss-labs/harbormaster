package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/status"
)

// WriteResult counts what an upsert batch did to the table.
type WriteResult struct{ Inserted, Updated, Unchanged int }

// ContentHash returns the sha256 hex of the JSON encoding of v with its Raw and
// PolledAt fields zeroed, so only the normalized fields decide "changed".
// v must be a struct or a pointer to one.
func ContentHash(v any) string {
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
	}
	if rv.Kind() == reflect.Struct {
		cp := reflect.New(rv.Type()).Elem()
		cp.Set(rv)
		for _, name := range []string{"Raw", "PolledAt"} {
			if f := cp.FieldByName(name); f.IsValid() && f.CanSet() {
				f.Set(reflect.Zero(f.Type()))
			}
		}
		v = cp.Interface()
	}
	encoded, err := json.Marshal(v)
	if err != nil {
		// Snapshot structs contain only marshalable fields; a failure is a programming error.
		panic(fmt.Sprintf("content hash: %v", err))
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func rawOrEmpty(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return []byte("{}")
	}
	return raw
}

// jsonArray marshals a slice as a JSON array, never "null".
func jsonArray(v any) []byte {
	encoded, err := json.Marshal(v)
	if err != nil || string(encoded) == "null" {
		return []byte("[]")
	}
	return encoded
}

func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

func nullString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func polledAtOrNow(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now()
	}
	return t
}

// upsertBatch runs every statement in one transaction. Each statement must end
// in RETURNING (xmax = 0): a row means inserted/updated, no row means unchanged.
func (s *Store) upsertBatch(ctx context.Context, what string, statements []batchStatement) (WriteResult, error) {
	var result WriteResult
	if len(statements) == 0 {
		return result, nil
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return result, fmt.Errorf("upsert %s: %w", what, err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

	batch := &pgx.Batch{}
	for _, st := range statements {
		batch.Queue(st.sql, st.args...)
	}
	results := tx.SendBatch(ctx, batch)
	for range statements {
		var inserted bool
		err := results.QueryRow().Scan(&inserted)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			result.Unchanged++
		case err != nil:
			results.Close()
			return WriteResult{}, fmt.Errorf("upsert %s: %w", what, err)
		case inserted:
			result.Inserted++
		default:
			result.Updated++
		}
	}
	if err := results.Close(); err != nil {
		return WriteResult{}, fmt.Errorf("upsert %s: %w", what, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return WriteResult{}, fmt.Errorf("upsert %s: %w", what, err)
	}
	return result, nil
}

type batchStatement struct {
	sql  string
	args []any
}

const upsertClusterSQL = `
insert into clusters (account_id, region, cluster_name, arn, status, running_tasks, pending_tasks,
  active_services, container_insights, capacity_providers, content_hash, raw, polled_at)
values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
on conflict (account_id, region, cluster_name) do update set
  arn = excluded.arn, status = excluded.status, running_tasks = excluded.running_tasks,
  pending_tasks = excluded.pending_tasks, active_services = excluded.active_services,
  container_insights = excluded.container_insights, capacity_providers = excluded.capacity_providers,
  content_hash = excluded.content_hash, raw = excluded.raw, polled_at = excluded.polled_at
where clusters.content_hash <> excluded.content_hash
returning (xmax = 0) as inserted`

// UpsertClusters writes clusters whose content changed; identical rows are left untouched.
func (s *Store) UpsertClusters(ctx context.Context, rows []model.Cluster) (WriteResult, error) {
	statements := make([]batchStatement, 0, len(rows))
	for _, c := range rows {
		statements = append(statements, batchStatement{upsertClusterSQL, []any{
			c.Key.AccountID, c.Key.Region, c.Key.Cluster, nullString(c.ARN), nullString(c.Status),
			c.RunningTasks, c.PendingTasks, c.ActiveServices, c.ContainerInsights,
			nonNilStrings(c.CapacityProviders), ContentHash(c), rawOrEmpty(c.Raw), polledAtOrNow(c.PolledAt),
		}})
	}
	return s.upsertBatch(ctx, "clusters", statements)
}

// DeleteClustersNotIn removes the account+region's clusters missing from keep,
// together with their services, tasks, service events and counts history.
func (s *Store) DeleteClustersNotIn(ctx context.Context, t awsx.Target, keep []model.ClusterKey) (int64, error) {
	names := make([]string, 0, len(keep))
	for _, k := range keep {
		if k.AccountID == t.AccountID && k.Region == t.Region {
			names = append(names, k.Cluster)
		}
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("delete clusters not in: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

	// Child tables first, while the clusters rows still identify what is stale.
	for _, table := range []string{"services", "tasks", "service_events", "service_counts_history"} {
		_, err := tx.Exec(ctx,
			`delete from `+table+` where account_id = $1 and region = $2 and not (cluster_name = any($3))`,
			t.AccountID, t.Region, names)
		if err != nil {
			return 0, fmt.Errorf("delete %s of removed clusters: %w", table, err)
		}
	}
	tag, err := tx.Exec(ctx,
		`delete from clusters where account_id = $1 and region = $2 and not (cluster_name = any($3))`,
		t.AccountID, t.Region, names)
	if err != nil {
		return 0, fmt.Errorf("delete clusters not in: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("delete clusters not in: %w", err)
	}
	return tag.RowsAffected(), nil
}

const upsertServiceSQL = `
insert into services (account_id, region, cluster_name, service_name, arn, status, desired, running, pending,
  task_definition_arn, family, revision, launch_type, platform_version, scheduling_strategy,
  deployment_controller, min_healthy_percent, max_percent, circuit_breaker_enable, circuit_breaker_rollback,
  enable_execute_command, subnets, security_groups, assign_public_ip, load_balancers, deployments,
  created_at, content_hash, raw, polled_at)
values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22,
  $23, $24, $25, $26, $27, $28, $29, $30)
on conflict (account_id, region, cluster_name, service_name) do update set
  arn = excluded.arn, status = excluded.status, desired = excluded.desired, running = excluded.running,
  pending = excluded.pending, task_definition_arn = excluded.task_definition_arn, family = excluded.family,
  revision = excluded.revision, launch_type = excluded.launch_type, platform_version = excluded.platform_version,
  scheduling_strategy = excluded.scheduling_strategy, deployment_controller = excluded.deployment_controller,
  min_healthy_percent = excluded.min_healthy_percent, max_percent = excluded.max_percent,
  circuit_breaker_enable = excluded.circuit_breaker_enable,
  circuit_breaker_rollback = excluded.circuit_breaker_rollback,
  enable_execute_command = excluded.enable_execute_command, subnets = excluded.subnets,
  security_groups = excluded.security_groups, assign_public_ip = excluded.assign_public_ip,
  load_balancers = excluded.load_balancers, deployments = excluded.deployments,
  created_at = excluded.created_at, content_hash = excluded.content_hash, raw = excluded.raw,
  polled_at = excluded.polled_at
where services.content_hash <> excluded.content_hash
returning (xmax = 0) as inserted`

// UpsertServices writes services whose content changed; identical rows are left untouched.
func (s *Store) UpsertServices(ctx context.Context, rows []model.Service) (WriteResult, error) {
	statements := make([]batchStatement, 0, len(rows))
	for _, v := range rows {
		statements = append(statements, batchStatement{upsertServiceSQL, []any{
			v.Key.AccountID, v.Key.Region, v.Key.Cluster, v.Key.Service, nullString(v.ARN), nullString(v.Status),
			v.Desired, v.Running, v.Pending, nullString(v.TaskDefinitionARN), nullString(v.Family), v.Revision,
			nullString(v.LaunchType), nullString(v.PlatformVersion), nullString(v.SchedulingStrategy),
			nullString(v.DeploymentController), v.MinHealthyPercent, v.MaxPercent, v.CircuitBreakerEnable,
			v.CircuitBreakerRollback, v.EnableExecuteCommand, nonNilStrings(v.Subnets),
			nonNilStrings(v.SecurityGroups), v.AssignPublicIP, jsonArray(v.LoadBalancers), jsonArray(v.Deployments),
			nullTime(v.CreatedAt), ContentHash(v), rawOrEmpty(v.Raw), polledAtOrNow(v.PolledAt),
		}})
	}
	return s.upsertBatch(ctx, "services", statements)
}

// DeleteServicesNotIn removes the cluster's services missing from keep. Their
// tasks stay so stopped history survives.
func (s *Store) DeleteServicesNotIn(ctx context.Context, k model.ClusterKey, keep []string) (int64, error) {
	if keep == nil {
		keep = []string{}
	}
	tag, err := s.Pool.Exec(ctx,
		`delete from services where account_id = $1 and region = $2 and cluster_name = $3
		   and not (service_name = any($4))`, k.AccountID, k.Region, k.Cluster, keep)
	if err != nil {
		return 0, fmt.Errorf("delete services not in: %w", err)
	}
	return tag.RowsAffected(), nil
}

// InsertServiceEvents stores events not seen before and returns how many were new.
func (s *Store) InsertServiceEvents(ctx context.Context, rows []model.ServiceEvent) (int, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("insert service events: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

	inserted := 0
	for _, e := range rows {
		encoded, err := json.Marshal(e)
		if err != nil {
			return 0, fmt.Errorf("encode service event: %w", err)
		}
		tag, err := tx.Exec(ctx,
			`insert into service_events (account_id, region, cluster_name, service_name, event_id, created_at,
			   message, content_hash, raw, polled_at)
			 values ($1, $2, $3, $4, $5, $6, $7, $8, $9, now())
			 on conflict do nothing`,
			e.Key.AccountID, e.Key.Region, e.Key.Cluster, e.Key.Service, e.ID, nullTime(e.CreatedAt),
			e.Message, ContentHash(e), encoded)
		if err != nil {
			return 0, fmt.Errorf("insert service event: %w", err)
		}
		inserted += int(tag.RowsAffected())
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("insert service events: %w", err)
	}
	return inserted, nil
}

const upsertTaskSQL = `
insert into tasks (account_id, region, task_arn, cluster_name, service_name, "group", started_by,
  task_definition_arn, family, revision, last_status, desired_status, health_status, launch_type,
  capacity_provider, cpu, memory, availability_zone, subnet_id, private_ip, public_ip, eni_id,
  created_at, started_at, stopping_at, stopped_at, stop_code, stopped_reason, stop_reason_group,
  exit_code, containers, content_hash, raw, polled_at)
values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22,
  $23, $24, $25, $26, $27, $28, $29, $30, $31, $32, $33, $34)
on conflict (account_id, region, task_arn) do update set
  cluster_name = excluded.cluster_name, service_name = excluded.service_name, "group" = excluded."group",
  started_by = excluded.started_by, task_definition_arn = excluded.task_definition_arn,
  family = excluded.family, revision = excluded.revision, last_status = excluded.last_status,
  desired_status = excluded.desired_status, health_status = excluded.health_status,
  launch_type = excluded.launch_type, capacity_provider = excluded.capacity_provider, cpu = excluded.cpu,
  memory = excluded.memory, availability_zone = excluded.availability_zone, subnet_id = excluded.subnet_id,
  private_ip = excluded.private_ip, public_ip = excluded.public_ip, eni_id = excluded.eni_id,
  created_at = excluded.created_at, started_at = excluded.started_at, stopping_at = excluded.stopping_at,
  stopped_at = excluded.stopped_at, stop_code = excluded.stop_code, stopped_reason = excluded.stopped_reason,
  stop_reason_group = excluded.stop_reason_group, exit_code = excluded.exit_code,
  containers = excluded.containers, content_hash = excluded.content_hash, raw = excluded.raw,
  polled_at = excluded.polled_at
where tasks.content_hash <> excluded.content_hash
  and (tasks.last_status is distinct from 'STOPPED' or excluded.last_status = 'STOPPED')
returning (xmax = 0) as inserted`

// UpsertTasks writes tasks whose content changed, computing exit_code and (for
// STOPPED tasks) stop_reason_group. Tasks absent from rows are never deleted. STOPPED is
// terminal: a non-STOPPED snapshot never replaces a STOPPED row (counted as unchanged).
func (s *Store) UpsertTasks(ctx context.Context, rows []model.Task) (WriteResult, error) {
	statements := make([]batchStatement, 0, len(rows))
	for _, t := range rows {
		var stopGroup *string
		if t.LastStatus == "STOPPED" {
			group := status.StopReasonGroup(t)
			stopGroup = &group
		}
		statements = append(statements, batchStatement{upsertTaskSQL, []any{
			t.Cluster.AccountID, t.Cluster.Region, t.ARN, t.Cluster.Cluster, nullString(t.ServiceName),
			nullString(t.Group), nullString(t.StartedBy), nullString(t.TaskDefinitionARN), nullString(t.Family),
			t.Revision, nullString(t.LastStatus), nullString(t.DesiredStatus), nullString(t.HealthStatus),
			nullString(t.LaunchType), nullString(t.CapacityProvider), nullString(t.CPU), nullString(t.Memory),
			nullString(t.AvailabilityZone), nullString(t.SubnetID), nullString(t.PrivateIP),
			nullString(t.PublicIP), nullString(t.ENIID), nullTime(t.CreatedAt), t.StartedAt, t.StoppingAt,
			t.StoppedAt, nullString(t.StopCode), nullString(t.StoppedReason), stopGroup, t.ExitCode(),
			jsonArray(t.Containers), ContentHash(t), rawOrEmpty(t.Raw), polledAtOrNow(t.PolledAt),
		}})
	}
	return s.upsertBatch(ctx, "tasks", statements)
}

// KnownTaskARNs lists the cluster's task ARNs with the given last_status. An empty
// lastStatus means "any status other than STOPPED" (RUNNING, PENDING, ACTIVATING, STOPPING, ...).
func (s *Store) KnownTaskARNs(ctx context.Context, k model.ClusterKey, lastStatus string) ([]string, error) {
	rows, err := s.Pool.Query(ctx,
		`select task_arn from tasks where account_id = $1 and region = $2 and cluster_name = $3
		   and ($4 = '' and last_status is distinct from 'STOPPED' or $4 <> '' and last_status = $4)
		 order by task_arn`, k.AccountID, k.Region, k.Cluster, lastStatus)
	if err != nil {
		return nil, fmt.Errorf("known task arns: %w", err)
	}
	defer rows.Close()
	arns := []string{}
	for rows.Next() {
		var arn string
		if err := rows.Scan(&arn); err != nil {
			return nil, fmt.Errorf("scan task arn: %w", err)
		}
		arns = append(arns, arn)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("known task arns: %w", err)
	}
	return arns, nil
}

// MarkTasksMissing stops the cluster's rows listed in arns that are not yet STOPPED
// (RUNNING, PENDING, ACTIVATING, STOPPING ... tasks ECS no longer reports):
// STOPPED, stop_code 'Unknown', stopped_at = now if unset. content_hash is replaced so the row counts as changed.
func (s *Store) MarkTasksMissing(ctx context.Context, k model.ClusterKey, arns []string, now time.Time) error {
	if len(arns) == 0 {
		return nil
	}
	_, err := s.Pool.Exec(ctx,
		`update tasks set last_status = 'STOPPED', desired_status = 'STOPPED', stop_code = 'Unknown',
		   stopped_at = coalesce(stopped_at, $4), content_hash = 'missing:' || $4::text, polled_at = $4
		 where account_id = $1 and region = $2 and cluster_name = $3
		   and last_status is distinct from 'STOPPED' and task_arn = any($5)`,
		k.AccountID, k.Region, k.Cluster, now, arns)
	if err != nil {
		return fmt.Errorf("mark tasks missing: %w", err)
	}
	return nil
}

// UpsertTaskDefinition stores a task definition revision; they are immutable, so existing rows are kept.
func (s *Store) UpsertTaskDefinition(ctx context.Context, td model.TaskDefinitionRef) error {
	_, err := s.Pool.Exec(ctx,
		`insert into task_definitions (account_id, region, arn, family, revision, cpu, memory, network_mode,
		   task_role_arn, execution_role_arn, requires_compatibilities, containers, efs_volumes,
		   registered_at, raw, polled_at)
		 values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
		 on conflict (account_id, region, arn) do nothing`,
		td.AccountID, td.Region, td.ARN, nullString(td.Family), td.Revision, nullString(td.CPU),
		nullString(td.Memory), nullString(td.NetworkMode), nullString(td.TaskRoleARN),
		nullString(td.ExecutionRoleARN), nonNilStrings(td.RequiresCompatibilities), jsonArray(td.Containers),
		jsonArray(td.EFSVolumes), nullTime(td.RegisteredAt), rawOrEmpty(td.Raw), polledAtOrNow(td.PolledAt))
	if err != nil {
		return fmt.Errorf("upsert task definition: %w", err)
	}
	return nil
}

// HasTaskDefinition reports whether the revision is already stored.
func (s *Store) HasTaskDefinition(ctx context.Context, accountID, region, arn string) (bool, error) {
	var exists bool
	err := s.Pool.QueryRow(ctx,
		`select exists (select 1 from task_definitions where account_id = $1 and region = $2 and arn = $3)`,
		accountID, region, arn).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("has task definition: %w", err)
	}
	return exists, nil
}

// InsertCountsHistory records one (running, desired, pending) sample per service at ts.
func (s *Store) InsertCountsHistory(ctx context.Context, ts time.Time, rows []model.Service) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("insert counts history: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

	batch := &pgx.Batch{}
	for _, v := range rows {
		batch.Queue(
			`insert into service_counts_history (account_id, region, cluster_name, service_name, ts, running, desired, pending)
			 values ($1, $2, $3, $4, $5, $6, $7, $8) on conflict do nothing`,
			v.Key.AccountID, v.Key.Region, v.Key.Cluster, v.Key.Service, ts, v.Running, v.Desired, v.Pending)
	}
	results := tx.SendBatch(ctx, batch)
	for range rows {
		if _, err := results.Exec(); err != nil {
			results.Close()
			return fmt.Errorf("insert counts history: %w", err)
		}
	}
	if err := results.Close(); err != nil {
		return fmt.Errorf("insert counts history: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("insert counts history: %w", err)
	}
	return nil
}
