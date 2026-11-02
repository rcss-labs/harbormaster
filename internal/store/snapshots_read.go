package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
)

// ServiceRow is a service plus the tag that decides who may see it.
type ServiceRow struct {
	model.Service
	EffectiveTagID authz.TagID
	TagInherited   bool // true when the service has no tag of its own and uses its cluster's (or Dev)
}

// TaskRow is a task plus its effective tag and the columns derived at write time.
// ExitCode shadows the embedded model.Task.ExitCode method with the stored value.
type TaskRow struct {
	model.Task
	EffectiveTagID    authz.TagID
	ExitCode          *int
	StopReasonGroup   string
	ServiceEnableExec bool
}

// ClusterRow is a cluster plus the tag assigned to it.
type ClusterRow struct {
	model.Cluster
	TagID  authz.TagID
	TagSet bool // false = untagged, TagID is the Dev tag
}

// ---- visibility SQL helper -------------------------------------------------

// visibilitySQL is the SQL needed to apply the tag visibility rule to rows of a
// table (or alias) that has account_id, region, cluster_name and service_name.
type visibilitySQL struct {
	// Joins are LEFT JOINs to cluster_tags and service_tags; append after the FROM clause.
	Joins string
	// Effective is the effective tag id expression:
	// COALESCE(service tag, cluster tag, Dev). Standalone rows have a NULL
	// service_name, never match service_tags, and so fall back to cluster/Dev.
	Effective string
	// Inherited is true when the row has no service tag of its own.
	Inherited string
	// Predicate is "$admin OR effective = ANY($tagIDs)".
	Predicate string
	// Args are the values for the two placeholders $start and $start+1. Append
	// them to the query args once; reusing the same start for another alias
	// needs no further args.
	Args []any
}

// visibilityFor builds the visibility SQL for rows aliased alias. Placeholders
// are numbered from start: $start is the admin flag and $start+1 the tag ids.
func visibilityFor(alias string, vis Visibility, start int) visibilitySQL {
	ct, st := "ct_"+alias, "st_"+alias
	ids := vis.TagIDs
	if ids == nil {
		ids = []int64{}
	}
	effective := fmt.Sprintf("coalesce(%s.tag_id, %s.tag_id, (select id from tags where builtin))", st, ct)
	return visibilitySQL{
		Joins: fmt.Sprintf(
			` left join cluster_tags %[2]s on %[2]s.account_id = %[1]s.account_id and %[2]s.region = %[1]s.region and %[2]s.cluster_name = %[1]s.cluster_name`+
				` left join service_tags %[3]s on %[3]s.account_id = %[1]s.account_id and %[3]s.region = %[1]s.region and %[3]s.cluster_name = %[1]s.cluster_name and %[3]s.service_name = %[1]s.service_name`,
			alias, ct, st),
		Effective: effective,
		Inherited: st + ".tag_id is null",
		Predicate: fmt.Sprintf("($%d::bool or %s = any($%d::bigint[]))", start, effective, start+1),
		Args:      []any{vis.Admin, ids},
	}
}

// clusterVisiblePredicate is true for a cluster row aliased clusterAlias when
// the caller is admin or can see at least one of its services. serviceVis must
// be built for the alias "sv".
func clusterVisiblePredicate(clusterAlias string, serviceVis visibilitySQL, adminParam int) string {
	return fmt.Sprintf(
		`($%d::bool or exists (select 1 from services sv%s where sv.account_id = %s.account_id and sv.region = %s.region and sv.cluster_name = %s.cluster_name and %s))`,
		adminParam, serviceVis.Joins, clusterAlias, clusterAlias, clusterAlias, serviceVis.Predicate)
}

// queryArgs accumulates positional arguments and hands out their placeholders.
type queryArgs struct{ values []any }

func (q *queryArgs) add(v any) string {
	q.values = append(q.values, v)
	return fmt.Sprintf("$%d", len(q.values))
}

func (q *queryArgs) visibility(alias string, vis Visibility) visibilitySQL {
	v := visibilityFor(alias, vis, len(q.values)+1)
	q.values = append(q.values, v.Args...)
	return v
}

func (q *queryArgs) target(alias string, f TargetFilter) string {
	var parts []string
	if f.AccountID != "" {
		parts = append(parts, fmt.Sprintf("%s.account_id = %s", alias, q.add(f.AccountID)))
	}
	if f.Region != "" {
		parts = append(parts, fmt.Sprintf("%s.region = %s", alias, q.add(f.Region)))
	}
	if len(parts) == 0 {
		return "true"
	}
	return strings.Join(parts, " and ")
}

func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func nullable[T any](p *T, dst *T) {
	if p != nil {
		*dst = *p
	}
}

func unmarshalColumn(raw []byte, dst any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, dst)
}

// ---- column lists and scanners ---------------------------------------------

func coalesceText(alias, col string) string { return fmt.Sprintf("coalesce(%s.%s, '')", alias, col) }
func coalesceInt(alias, col string) string  { return fmt.Sprintf("coalesce(%s.%s, 0)", alias, col) }
func coalesceBool(alias, col string) string { return fmt.Sprintf("coalesce(%s.%s, false)", alias, col) }

func clusterColumns(a string) string {
	return strings.Join([]string{
		a + ".account_id", a + ".region", a + ".cluster_name", coalesceText(a, "arn"), coalesceText(a, "status"),
		coalesceInt(a, "running_tasks"), coalesceInt(a, "pending_tasks"), coalesceInt(a, "active_services"),
		coalesceBool(a, "container_insights"), a + ".capacity_providers", a + ".raw", a + ".polled_at",
	}, ", ")
}

func clusterDest(c *model.Cluster) []any {
	return []any{&c.Key.AccountID, &c.Key.Region, &c.Key.Cluster, &c.ARN, &c.Status, &c.RunningTasks,
		&c.PendingTasks, &c.ActiveServices, &c.ContainerInsights, &c.CapacityProviders, &c.Raw, &c.PolledAt}
}

func serviceColumns(a string) string {
	text := func(c string) string { return coalesceText(a, c) }
	num := func(c string) string { return coalesceInt(a, c) }
	flag := func(c string) string { return coalesceBool(a, c) }
	return strings.Join([]string{
		a + ".account_id", a + ".region", a + ".cluster_name", a + ".service_name", text("arn"), text("status"),
		num("desired"), num("running"), num("pending"), text("task_definition_arn"), text("family"), num("revision"),
		text("launch_type"), text("platform_version"), text("scheduling_strategy"), text("deployment_controller"),
		num("min_healthy_percent"), num("max_percent"), flag("circuit_breaker_enable"),
		flag("circuit_breaker_rollback"), flag("enable_execute_command"), a + ".subnets", a + ".security_groups",
		flag("assign_public_ip"), a + ".load_balancers", a + ".deployments", a + ".created_at", a + ".raw",
		a + ".polled_at",
	}, ", ")
}

type serviceScan struct {
	svc                        *model.Service
	loadBalancers, deployments []byte
	createdAt                  *time.Time
}

func newServiceScan(svc *model.Service) *serviceScan { return &serviceScan{svc: svc} }

func (s *serviceScan) dest() []any {
	v := s.svc
	return []any{&v.Key.AccountID, &v.Key.Region, &v.Key.Cluster, &v.Key.Service, &v.ARN, &v.Status,
		&v.Desired, &v.Running, &v.Pending, &v.TaskDefinitionARN, &v.Family, &v.Revision, &v.LaunchType,
		&v.PlatformVersion, &v.SchedulingStrategy, &v.DeploymentController, &v.MinHealthyPercent, &v.MaxPercent,
		&v.CircuitBreakerEnable, &v.CircuitBreakerRollback, &v.EnableExecuteCommand, &v.Subnets,
		&v.SecurityGroups, &v.AssignPublicIP, &s.loadBalancers, &s.deployments, &s.createdAt, &v.Raw, &v.PolledAt}
}

func (s *serviceScan) finish() error {
	nullable(s.createdAt, &s.svc.CreatedAt)
	if err := unmarshalColumn(s.loadBalancers, &s.svc.LoadBalancers); err != nil {
		return fmt.Errorf("decode load balancers: %w", err)
	}
	if err := unmarshalColumn(s.deployments, &s.svc.Deployments); err != nil {
		return fmt.Errorf("decode deployments: %w", err)
	}
	return nil
}

// taskColumns lists the task columns; lite replaces raw and containers by NULL.
func taskColumns(a string, lite bool) string {
	text := func(c string) string { return coalesceText(a, c) }
	containers, raw := a+".containers", a+".raw"
	if lite {
		containers, raw = "null::jsonb", "null::jsonb"
	}
	return strings.Join([]string{
		a + ".account_id", a + ".region", a + ".cluster_name", a + ".task_arn", text("service_name"),
		text(`"group"`), text("started_by"), text("task_definition_arn"), text("family"), coalesceInt(a, "revision"),
		text("last_status"), text("desired_status"), text("health_status"), text("launch_type"),
		text("capacity_provider"), text("cpu"), text("memory"), text("availability_zone"), text("subnet_id"),
		text("private_ip"), text("public_ip"), text("eni_id"), a + ".created_at", a + ".started_at",
		a + ".stopping_at", a + ".stopped_at", text("stop_code"), text("stopped_reason"),
		text("stop_reason_group"), a + ".exit_code", containers, raw, a + ".polled_at", "coalesce(" + a + ".enable_execute_command, false)",
	}, ", ")
}

type taskScan struct {
	row        *TaskRow
	containers []byte
	createdAt  *time.Time
}

func (s *taskScan) dest() []any {
	t := &s.row.Task
	return []any{&t.Cluster.AccountID, &t.Cluster.Region, &t.Cluster.Cluster, &t.ARN, &t.ServiceName, &t.Group,
		&t.StartedBy, &t.TaskDefinitionARN, &t.Family, &t.Revision, &t.LastStatus, &t.DesiredStatus,
		&t.HealthStatus, &t.LaunchType, &t.CapacityProvider, &t.CPU, &t.Memory, &t.AvailabilityZone, &t.SubnetID,
		&t.PrivateIP, &t.PublicIP, &t.ENIID, &s.createdAt, &t.StartedAt, &t.StoppingAt, &t.StoppedAt, &t.StopCode,
		&t.StoppedReason, &s.row.StopReasonGroup, &s.row.ExitCode, &s.containers, &t.Raw, &t.PolledAt, &t.EnableExecuteCommand}
}

func (s *taskScan) finish() error {
	nullable(s.createdAt, &s.row.Task.CreatedAt)
	if err := unmarshalColumn(s.containers, &s.row.Task.Containers); err != nil {
		return fmt.Errorf("decode containers: %w", err)
	}
	return nil
}

// ---- clusters ---------------------------------------------------------------

func (s *Store) queryClusters(ctx context.Context, f TargetFilter, vis Visibility, only *model.ClusterKey) ([]ClusterRow, error) {
	var q queryArgs
	target := q.target("c", f)
	svcVis := q.visibility("sv", vis)
	devTag := `(select id from tags where builtin)`
	where := target + " and " + clusterVisiblePredicate("c", svcVis, len(q.values)-1)
	if only != nil {
		where += fmt.Sprintf(" and c.account_id = %s and c.region = %s and c.cluster_name = %s",
			q.add(only.AccountID), q.add(only.Region), q.add(only.Cluster))
	}
	sql := `select ` + clusterColumns("c") + `, coalesce(ct.tag_id, ` + devTag + `), ct.tag_id is not null
	  from clusters c left join cluster_tags ct on ct.account_id = c.account_id and ct.region = c.region
	   and ct.cluster_name = c.cluster_name
	  where ` + where + ` order by c.account_id, c.region, c.cluster_name`
	rows, err := s.Pool.Query(ctx, sql, q.values...)
	if err != nil {
		return nil, fmt.Errorf("list clusters: %w", err)
	}
	defer rows.Close()
	clusters := []ClusterRow{}
	for rows.Next() {
		var row ClusterRow
		dest := append(clusterDest(&row.Cluster), &row.TagID, &row.TagSet)
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("scan cluster: %w", err)
		}
		clusters = append(clusters, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list clusters: %w", err)
	}
	return clusters, nil
}

// ListClusters returns the clusters the caller may see: every cluster for an
// admin, otherwise those with at least one visible service.
func (s *Store) ListClusters(ctx context.Context, f TargetFilter, vis Visibility) ([]ClusterRow, error) {
	return s.queryClusters(ctx, f, vis, nil)
}

// GetCluster returns one cluster, or ErrNotFound when missing or invisible.
func (s *Store) GetCluster(ctx context.Context, k model.ClusterKey, vis Visibility) (ClusterRow, error) {
	rows, err := s.queryClusters(ctx, TargetFilter{}, vis, &k)
	if err != nil {
		return ClusterRow{}, err
	}
	if len(rows) == 0 {
		return ClusterRow{}, ErrNotFound
	}
	return rows[0], nil
}

// ---- services ---------------------------------------------------------------

func (s *Store) queryServices(ctx context.Context, f TargetFilter, cluster string, only *model.ServiceKey, vis Visibility) ([]ServiceRow, error) {
	var q queryArgs
	where := q.target("sv", f)
	v := q.visibility("sv", vis)
	where += " and " + v.Predicate
	if cluster != "" {
		where += " and sv.cluster_name = " + q.add(cluster)
	}
	if only != nil {
		where += fmt.Sprintf(" and sv.account_id = %s and sv.region = %s and sv.cluster_name = %s and sv.service_name = %s",
			q.add(only.AccountID), q.add(only.Region), q.add(only.Cluster), q.add(only.Service))
	}
	sql := `select ` + serviceColumns("sv") + `, ` + v.Effective + `, ` + v.Inherited +
		` from services sv` + v.Joins + ` where ` + where + ` order by sv.account_id, sv.region, sv.cluster_name, sv.service_name`
	rows, err := s.Pool.Query(ctx, sql, q.values...)
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	defer rows.Close()
	services := []ServiceRow{}
	for rows.Next() {
		var row ServiceRow
		scan := newServiceScan(&row.Service)
		if err := rows.Scan(append(scan.dest(), &row.EffectiveTagID, &row.TagInherited)...); err != nil {
			return nil, fmt.Errorf("scan service: %w", err)
		}
		if err := scan.finish(); err != nil {
			return nil, err
		}
		services = append(services, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	return services, nil
}

// ListServices returns visible services ordered by cluster then name; cluster "" means all.
func (s *Store) ListServices(ctx context.Context, f TargetFilter, cluster string, vis Visibility) ([]ServiceRow, error) {
	return s.queryServices(ctx, f, cluster, nil, vis)
}

// GetServiceView returns one service with its tag, or ErrNotFound when missing or invisible.
func (s *Store) GetServiceView(ctx context.Context, k model.ServiceKey, vis Visibility) (ServiceRow, error) {
	rows, err := s.queryServices(ctx, TargetFilter{}, "", &k, vis)
	if err != nil {
		return ServiceRow{}, err
	}
	if len(rows) == 0 {
		return ServiceRow{}, ErrNotFound
	}
	return rows[0], nil
}

// GetService returns a service regardless of visibility; callers check authz themselves.
func (s *Store) GetService(ctx context.Context, k model.ServiceKey) (model.Service, error) {
	var svc model.Service
	scan := newServiceScan(&svc)
	err := s.Pool.QueryRow(ctx,
		`select `+serviceColumns("sv")+` from services sv
		 where sv.account_id = $1 and sv.region = $2 and sv.cluster_name = $3 and sv.service_name = $4`,
		k.AccountID, k.Region, k.Cluster, k.Service).Scan(scan.dest()...)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Service{}, ErrNotFound
	}
	if err != nil {
		return model.Service{}, fmt.Errorf("get service: %w", err)
	}
	if err := scan.finish(); err != nil {
		return model.Service{}, err
	}
	return svc, nil
}

// ---- tasks ------------------------------------------------------------------

// GetTask returns a task regardless of visibility; callers check authz themselves.
func (s *Store) GetTask(ctx context.Context, accountID, region, taskARN string) (model.Task, error) {
	var row TaskRow
	scan := &taskScan{row: &row}
	err := s.Pool.QueryRow(ctx,
		`select `+taskColumns("t", false)+` from tasks t where t.account_id = $1 and t.region = $2 and t.task_arn = $3`,
		accountID, region, taskARN).Scan(scan.dest()...)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Task{}, ErrNotFound
	}
	if err != nil {
		return model.Task{}, fmt.Errorf("get task: %w", err)
	}
	if err := scan.finish(); err != nil {
		return model.Task{}, err
	}
	return row.Task, nil
}

const (
	// stoppedErrorSQL: a STOPPED task that failed (non-zero exit or never started).
	stoppedErrorSQL = `coalesce(t.last_status = 'STOPPED' and (coalesce(t.exit_code, 0) <> 0 or t.stop_code = 'TaskFailedToStart'), false)`
	// problemSQL: a failed stop or an unhealthy task; never NULL.
	problemSQL = `(` + stoppedErrorSQL + ` or coalesce(t.health_status = 'UNHEALTHY', false))`
	// runningSQL mirrors model.Task.IsRunning.
	runningSQL = `t.last_status is distinct from 'STOPPED'`
)

// TaskQuery selects tasks. Filter is problems|running|stopped|all (default all);
// Limit 0 means no limit.
type TaskQuery struct {
	TargetFilter
	Vis              Visibility
	Cluster, Service string
	Filter           string
	Q                string
	Limit, Offset    int
	// StoppedAfter hides STOPPED rows whose stopped_at is older than it; zero = no window.
	StoppedAfter time.Time
}

func (s *Store) queryTaskRows(ctx context.Context, sql string, args []any) ([]TaskRow, error) {
	rows, err := s.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("query tasks: %w", err)
	}
	defer rows.Close()
	tasks := []TaskRow{}
	for rows.Next() {
		var row TaskRow
		scan := &taskScan{row: &row}
		if err := rows.Scan(append(scan.dest(), &row.EffectiveTagID, &row.ServiceEnableExec)...); err != nil {
			return nil, fmt.Errorf("scan task: %w", err)
		}
		if err := scan.finish(); err != nil {
			return nil, err
		}
		tasks = append(tasks, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query tasks: %w", err)
	}
	return tasks, nil
}

// taskFrom is the shared FROM clause: tasks, visibility joins and the owning service.
func taskFrom(v visibilitySQL) string {
	return ` from tasks t` + v.Joins + ` left join services ts on ts.account_id = t.account_id and ts.region = t.region
	 and ts.cluster_name = t.cluster_name and ts.service_name = t.service_name`
}

// ListTasks returns one page of visible tasks and the size of the whole filtered set.
func (s *Store) ListTasks(ctx context.Context, tq TaskQuery) ([]TaskRow, int, error) {
	var q queryArgs
	where := []string{q.target("t", tq.TargetFilter)}
	v := q.visibility("t", tq.Vis)
	where = append(where, v.Predicate)
	if tq.Cluster != "" {
		where = append(where, "t.cluster_name = "+q.add(tq.Cluster))
	}
	if tq.Service != "" {
		where = append(where, "t.service_name = "+q.add(tq.Service))
	}
	if !tq.StoppedAfter.IsZero() {
		where = append(where, "(t.last_status is distinct from 'STOPPED' or t.stopped_at >= "+q.add(tq.StoppedAfter)+")")
	}
	switch tq.Filter {
	case "running":
		where = append(where, runningSQL)
	case "stopped":
		where = append(where, "t.last_status = 'STOPPED'")
	case "problems":
		where = append(where, "("+runningSQL+" or "+problemSQL+")")
	}
	if tq.Q != "" {
		esc := likeEscape(tq.Q)
		suffix, contains, prefix := q.add(esc), q.add(esc), q.add(esc)
		where = append(where, fmt.Sprintf(
			`(t.task_arn ilike '%%' || %s or t.family ilike '%%' || %s || '%%' or t.service_name ilike '%%' || %s || '%%' or t.private_ip like %s || '%%')`,
			suffix, contains, contains, prefix))
	}
	whereSQL := strings.Join(where, " and ")
	from := taskFrom(v)

	var total int
	if err := s.Pool.QueryRow(ctx, `select count(*)`+from+` where `+whereSQL, q.values...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count tasks: %w", err)
	}

	sql := `select ` + taskColumns("t", false) + `, ` + v.Effective + `, ` + coalesceBool("ts", "enable_execute_command") +
		from + ` where ` + whereSQL + ` order by ` + problemSQL + ` desc, (` + runningSQL + `) desc,
		 coalesce(t.started_at, t.created_at) desc, t.task_arn`
	args := q.values
	if tq.Limit > 0 {
		sql += ` limit ` + fmt.Sprintf("$%d", len(args)+1)
		args = append(args[:len(args):len(args)], tq.Limit)
	}
	if tq.Offset > 0 {
		sql += ` offset ` + fmt.Sprintf("$%d", len(args)+1)
		args = append(args[:len(args):len(args)], tq.Offset)
	}
	items, err := s.queryTaskRows(ctx, sql, args)
	if err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// RunningTasks returns visible non-STOPPED tasks without raw and containers.
func (s *Store) RunningTasks(ctx context.Context, f TargetFilter, vis Visibility) ([]TaskRow, error) {
	var q queryArgs
	target := q.target("t", f)
	v := q.visibility("t", vis)
	sql := `select ` + taskColumns("t", true) + `, ` + v.Effective + `, ` + coalesceBool("ts", "enable_execute_command") +
		taskFrom(v) + ` where ` + target + ` and ` + v.Predicate + ` and ` + runningSQL +
		` order by t.account_id, t.region, t.cluster_name, t.task_arn`
	return s.queryTaskRows(ctx, sql, q.values)
}

// TaskSummary aggregates visible tasks for the dashboard.
type TaskSummary struct {
	RunningHealthy, RunningUnhealthy, StoppedError, StoppedNormal int
	ByAZ                                                          map[string]int
	ByCluster                                                     map[model.ClusterKey]struct{ Fargate, Spot int }
	StopReasons                                                   map[string]int // STOPPED since the cutoff, by reason group
}

// TaskSummaryQuery scopes a task summary; Cluster and Service narrow it when non-empty.
type TaskSummaryQuery struct {
	TargetFilter
	Vis              Visibility
	Cluster, Service string
	Since            time.Time // stopped tasks older than this are ignored
}

// TaskSummary counts running and recently stopped (stopped_at >= since) tasks.
func (s *Store) TaskSummary(ctx context.Context, f TargetFilter, vis Visibility, since time.Time) (TaskSummary, error) {
	return s.TaskSummaryFor(ctx, TaskSummaryQuery{TargetFilter: f, Vis: vis, Since: since})
}

// TaskSummaryFor is TaskSummary narrowed to one cluster and/or service.
func (s *Store) TaskSummaryFor(ctx context.Context, tq TaskSummaryQuery) (TaskSummary, error) {
	f, vis, since := tq.TargetFilter, tq.Vis, tq.Since
	summary := TaskSummary{
		ByAZ:        map[string]int{},
		ByCluster:   map[model.ClusterKey]struct{ Fargate, Spot int }{},
		StopReasons: map[string]int{},
	}
	var q queryArgs
	target := q.target("t", f)
	v := q.visibility("t", vis)
	base := taskFrom(v) + ` where ` + target + ` and ` + v.Predicate
	if tq.Cluster != "" {
		base += ` and t.cluster_name = ` + q.add(tq.Cluster)
	}
	if tq.Service != "" {
		base += ` and t.service_name = ` + q.add(tq.Service)
	}
	baseArgs := append([]any(nil), q.values...) // without the cutoff, for queries that do not use it
	sinceParam := q.add(since)

	err := s.Pool.QueryRow(ctx, `select
	   count(*) filter (where `+runningSQL+` and t.health_status is distinct from 'UNHEALTHY'),
	   count(*) filter (where `+runningSQL+` and t.health_status = 'UNHEALTHY'),
	   count(*) filter (where t.last_status = 'STOPPED' and t.stopped_at >= `+sinceParam+` and `+stoppedErrorSQL+`),
	   count(*) filter (where t.last_status = 'STOPPED' and t.stopped_at >= `+sinceParam+` and not `+stoppedErrorSQL+`)`+
		base, q.values...).Scan(&summary.RunningHealthy, &summary.RunningUnhealthy, &summary.StoppedError, &summary.StoppedNormal)
	if err != nil {
		return TaskSummary{}, fmt.Errorf("task summary counts: %w", err)
	}

	azRows, err := s.Pool.Query(ctx, `select `+coalesceText("t", "availability_zone")+`, count(*)`+base+
		` and `+runningSQL+` group by 1`, baseArgs...)
	if err != nil {
		return TaskSummary{}, fmt.Errorf("task summary by az: %w", err)
	}
	for azRows.Next() {
		var az string
		var n int
		if err := azRows.Scan(&az, &n); err != nil {
			azRows.Close()
			return TaskSummary{}, fmt.Errorf("scan az count: %w", err)
		}
		if az != "" {
			summary.ByAZ[az] = n
		}
	}
	azRows.Close()
	if err := azRows.Err(); err != nil {
		return TaskSummary{}, fmt.Errorf("task summary by az: %w", err)
	}

	clusterRows, err := s.Pool.Query(ctx, `select t.account_id, t.region, t.cluster_name,
	   count(*) filter (where t.capacity_provider is distinct from 'FARGATE_SPOT'),
	   count(*) filter (where t.capacity_provider = 'FARGATE_SPOT')`+base+
		` and `+runningSQL+` group by 1, 2, 3`, baseArgs...)
	if err != nil {
		return TaskSummary{}, fmt.Errorf("task summary by cluster: %w", err)
	}
	for clusterRows.Next() {
		var k model.ClusterKey
		var fargate, spot int
		if err := clusterRows.Scan(&k.AccountID, &k.Region, &k.Cluster, &fargate, &spot); err != nil {
			clusterRows.Close()
			return TaskSummary{}, fmt.Errorf("scan cluster count: %w", err)
		}
		summary.ByCluster[k] = struct{ Fargate, Spot int }{fargate, spot}
	}
	clusterRows.Close()
	if err := clusterRows.Err(); err != nil {
		return TaskSummary{}, fmt.Errorf("task summary by cluster: %w", err)
	}

	reasonRows, err := s.Pool.Query(ctx, `select `+coalesceText("t", "stop_reason_group")+`, count(*)`+base+
		` and t.last_status = 'STOPPED' and t.stopped_at >= `+sinceParam+` group by 1`, q.values...)
	if err != nil {
		return TaskSummary{}, fmt.Errorf("task summary stop reasons: %w", err)
	}
	defer reasonRows.Close()
	for reasonRows.Next() {
		var group string
		var n int
		if err := reasonRows.Scan(&group, &n); err != nil {
			return TaskSummary{}, fmt.Errorf("scan stop reason count: %w", err)
		}
		summary.StopReasons[group] = n
	}
	if err := reasonRows.Err(); err != nil {
		return TaskSummary{}, fmt.Errorf("task summary stop reasons: %w", err)
	}
	return summary, nil
}

// ---- service events ---------------------------------------------------------

func scanServiceEvents(rows pgx.Rows) ([]model.ServiceEvent, error) {
	defer rows.Close()
	events := []model.ServiceEvent{}
	for rows.Next() {
		var e model.ServiceEvent
		var createdAt *time.Time
		if err := rows.Scan(&e.Key.AccountID, &e.Key.Region, &e.Key.Cluster, &e.Key.Service, &e.ID, &createdAt, &e.Message); err != nil {
			return nil, fmt.Errorf("scan service event: %w", err)
		}
		nullable(createdAt, &e.CreatedAt)
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("service events: %w", err)
	}
	return events, nil
}

const eventColumns = `e.account_id, e.region, e.cluster_name, e.service_name, e.event_id, e.created_at, coalesce(e.message, '')`

// ServiceEvents returns a service's events newest first; limit 0 means no limit.
func (s *Store) ServiceEvents(ctx context.Context, k model.ServiceKey, limit, offset int) ([]model.ServiceEvent, error) {
	rows, err := s.Pool.Query(ctx,
		`select `+eventColumns+` from service_events e
		 where e.account_id = $1 and e.region = $2 and e.cluster_name = $3 and e.service_name = $4
		 order by e.created_at desc, e.event_id limit nullif($5, 0) offset $6`,
		k.AccountID, k.Region, k.Cluster, k.Service, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("service events: %w", err)
	}
	return scanServiceEvents(rows)
}

// RecentServiceEventsPerService returns each service's newest limit events (newest first) for the
// target, in one query. No visibility filter: the alerts tier derives for every service.
func (s *Store) RecentServiceEventsPerService(ctx context.Context, f TargetFilter, limit int) (map[model.ServiceKey][]model.ServiceEvent, error) {
	var q queryArgs
	target := q.target("e", f)
	limitParam := q.add(limit)
	rows, err := s.Pool.Query(ctx,
		`select `+eventColumns+` from (
		   select e.*, row_number() over (partition by e.account_id, e.region, e.cluster_name, e.service_name
		     order by e.created_at desc, e.event_id) as rn
		   from service_events e where `+target+`
		 ) e where e.rn <= `+limitParam+`
		 order by e.created_at desc, e.event_id`, q.values...)
	if err != nil {
		return nil, fmt.Errorf("recent service events per service: %w", err)
	}
	events, err := scanServiceEvents(rows)
	if err != nil {
		return nil, err
	}
	byService := map[model.ServiceKey][]model.ServiceEvent{}
	for _, e := range events {
		byService[e.Key] = append(byService[e.Key], e)
	}
	return byService, nil
}

// RecentServiceEvents returns the newest events of visible services; limit 0 means no limit.
func (s *Store) RecentServiceEvents(ctx context.Context, f TargetFilter, vis Visibility, limit int) ([]model.ServiceEvent, error) {
	var q queryArgs
	target := q.target("e", f)
	v := q.visibility("e", vis)
	limitParam := q.add(limit)
	sql := `select ` + eventColumns + ` from service_events e` + v.Joins + ` where ` + target + ` and ` + v.Predicate +
		` order by e.created_at desc, e.event_id limit nullif(` + limitParam + `, 0)`
	rows, err := s.Pool.Query(ctx, sql, q.values...)
	if err != nil {
		return nil, fmt.Errorf("recent service events: %w", err)
	}
	return scanServiceEvents(rows)
}

// LastEventAtByCluster returns the newest visible service event time per cluster.
func (s *Store) LastEventAtByCluster(ctx context.Context, f TargetFilter, vis Visibility) (map[model.ClusterKey]time.Time, error) {
	var q queryArgs
	target := q.target("e", f)
	v := q.visibility("e", vis)
	rows, err := s.Pool.Query(ctx,
		`select e.account_id, e.region, e.cluster_name, max(e.created_at) from service_events e`+v.Joins+
			` where `+target+` and `+v.Predicate+` and e.created_at is not null group by 1, 2, 3`, q.values...)
	if err != nil {
		return nil, fmt.Errorf("last event by cluster: %w", err)
	}
	defer rows.Close()
	latest := map[model.ClusterKey]time.Time{}
	for rows.Next() {
		var k model.ClusterKey
		var at time.Time
		if err := rows.Scan(&k.AccountID, &k.Region, &k.Cluster, &at); err != nil {
			return nil, fmt.Errorf("scan last event: %w", err)
		}
		latest[k] = at
	}
	return latest, rows.Err()
}

// ---- task definitions, history, stops, search --------------------------------

// GetTaskDefinition returns a stored task definition revision, or ErrNotFound.
func (s *Store) GetTaskDefinition(ctx context.Context, accountID, region, arn string) (model.TaskDefinitionRef, error) {
	var td model.TaskDefinitionRef
	var containers, efs []byte
	var registeredAt *time.Time
	err := s.Pool.QueryRow(ctx,
		`select account_id, region, arn, coalesce(family, ''), coalesce(revision, 0), coalesce(cpu, ''),
		   coalesce(memory, ''), coalesce(network_mode, ''), coalesce(task_role_arn, ''),
		   coalesce(execution_role_arn, ''), requires_compatibilities, containers, efs_volumes, registered_at,
		   raw, polled_at
		 from task_definitions where account_id = $1 and region = $2 and arn = $3`,
		accountID, region, arn).Scan(&td.AccountID, &td.Region, &td.ARN, &td.Family, &td.Revision, &td.CPU,
		&td.Memory, &td.NetworkMode, &td.TaskRoleARN, &td.ExecutionRoleARN, &td.RequiresCompatibilities,
		&containers, &efs, &registeredAt, &td.Raw, &td.PolledAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.TaskDefinitionRef{}, ErrNotFound
	}
	if err != nil {
		return model.TaskDefinitionRef{}, fmt.Errorf("get task definition: %w", err)
	}
	nullable(registeredAt, &td.RegisteredAt)
	if err := unmarshalColumn(containers, &td.Containers); err != nil {
		return model.TaskDefinitionRef{}, fmt.Errorf("decode task definition containers: %w", err)
	}
	if err := unmarshalColumn(efs, &td.EFSVolumes); err != nil {
		return model.TaskDefinitionRef{}, fmt.Errorf("decode task definition volumes: %w", err)
	}
	return td, nil
}

// CountsPoint is the summed service counts at one sample time.
type CountsPoint struct {
	TS                        time.Time
	Running, Desired, Pending int
}

// CountsHistory sums visible services' count samples per timestamp since the cutoff.
// cluster and service narrow the set when non-empty.
func (s *Store) CountsHistory(ctx context.Context, f TargetFilter, cluster, service string, vis Visibility, since time.Time) ([]CountsPoint, error) {
	var q queryArgs
	where := q.target("h", f)
	v := q.visibility("h", vis)
	where += " and " + v.Predicate + " and h.ts >= " + q.add(since)
	if cluster != "" {
		where += " and h.cluster_name = " + q.add(cluster)
	}
	if service != "" {
		where += " and h.service_name = " + q.add(service)
	}
	rows, err := s.Pool.Query(ctx,
		`select h.ts, sum(coalesce(h.running, 0)), sum(coalesce(h.desired, 0)), sum(coalesce(h.pending, 0))
		 from service_counts_history h`+v.Joins+` where `+where+` group by h.ts order by h.ts`, q.values...)
	if err != nil {
		return nil, fmt.Errorf("counts history: %w", err)
	}
	defer rows.Close()
	points := []CountsPoint{}
	for rows.Next() {
		var p CountsPoint
		if err := rows.Scan(&p.TS, &p.Running, &p.Desired, &p.Pending); err != nil {
			return nil, fmt.Errorf("scan counts point: %w", err)
		}
		points = append(points, p)
	}
	return points, rows.Err()
}

// StopBucket counts tasks stopped within one hour.
type StopBucket struct {
	Hour          time.Time
	Normal, Error int
}

// StopsPerHour buckets visible STOPPED tasks by stop hour since the cutoff;
// hours without stops are absent (the caller zero-fills).
func (s *Store) StopsPerHour(ctx context.Context, f TargetFilter, cluster string, vis Visibility, since time.Time) ([]StopBucket, error) {
	var q queryArgs
	where := q.target("t", f)
	v := q.visibility("t", vis)
	where += " and " + v.Predicate + " and t.last_status = 'STOPPED' and t.stopped_at >= " + q.add(since)
	if cluster != "" {
		where += " and t.cluster_name = " + q.add(cluster)
	}
	rows, err := s.Pool.Query(ctx,
		`select date_trunc('hour', t.stopped_at) as hour, count(*) filter (where not `+stoppedErrorSQL+`),
		   count(*) filter (where `+stoppedErrorSQL+`)
		 from tasks t`+v.Joins+` where `+where+` group by 1 order by 1`, q.values...)
	if err != nil {
		return nil, fmt.Errorf("stops per hour: %w", err)
	}
	defer rows.Close()
	buckets := []StopBucket{}
	for rows.Next() {
		var b StopBucket
		if err := rows.Scan(&b.Hour, &b.Normal, &b.Error); err != nil {
			return nil, fmt.Errorf("scan stop bucket: %w", err)
		}
		buckets = append(buckets, b)
	}
	return buckets, rows.Err()
}

// SearchHit is one global-search result.
type SearchHit struct {
	Kind       string // cluster, service or task
	Cluster    model.ClusterKey
	Service    string
	TaskARN    string
	Label, Sub string
}

// Search finds visible clusters and services by name (substring) and running
// tasks by id prefix or private IP prefix. Results are ordered cluster,
// service, task; limit 0 means no limit.
func (s *Store) Search(ctx context.Context, f TargetFilter, vis Visibility, q string, limit int) ([]SearchHit, error) {
	var a queryArgs
	esc := likeEscape(q)
	qParam := a.add(esc)
	svcVis := a.visibility("sv", vis)
	adminParam := len(a.values) - 1
	taskVis := visibilityFor("t", vis, adminParam)
	// serviceVis for the cluster EXISTS and the service query share the "sv" alias scope-wise.
	clusterTarget, serviceTarget, taskTarget := a.target("c", f), a.target("sv", f), a.target("t", f)
	limitParam := a.add(limit)

	sql := `select kind, account_id, region, cluster_name, service_name, task_arn, label, sub from (
	  select 1 as ord, 'cluster' as kind, c.account_id, c.region, c.cluster_name, '' as service_name, '' as task_arn,
	    c.cluster_name as label, c.account_id || ' · ' || c.region as sub, c.cluster_name as sort_name
	   from clusters c where ` + clusterTarget + ` and c.cluster_name ilike '%' || ` + qParam + ` || '%'
	    and ` + clusterVisiblePredicate("c", svcVis, adminParam) + `
	  union all
	  select 2, 'service', sv.account_id, sv.region, sv.cluster_name, sv.service_name, '',
	    sv.service_name, sv.cluster_name, sv.service_name
	   from services sv` + svcVis.Joins + ` where ` + serviceTarget + ` and sv.service_name ilike '%' || ` + qParam + ` || '%'
	    and ` + svcVis.Predicate + `
	  union all
	  select 3, 'task', t.account_id, t.region, t.cluster_name, coalesce(t.service_name, ''), t.task_arn,
	    substring(t.task_arn from '[^/]*$'), coalesce(t.service_name, t."group", ''), t.task_arn
	   from tasks t` + taskVis.Joins + ` where ` + taskTarget + ` and ` + runningSQL + `
	    and (substring(t.task_arn from '[^/]*$') ilike ` + qParam + ` || '%' or t.private_ip like ` + qParam + ` || '%')
	    and ` + taskVis.Predicate + `
	) hits order by ord, sort_name limit nullif(` + limitParam + `, 0)`

	rows, err := s.Pool.Query(ctx, sql, a.values...)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	defer rows.Close()
	hits := []SearchHit{}
	for rows.Next() {
		var h SearchHit
		if err := rows.Scan(&h.Kind, &h.Cluster.AccountID, &h.Cluster.Region, &h.Cluster.Cluster, &h.Service,
			&h.TaskARN, &h.Label, &h.Sub); err != nil {
			return nil, fmt.Errorf("scan search hit: %w", err)
		}
		hits = append(hits, h)
	}
	return hits, rows.Err()
}
