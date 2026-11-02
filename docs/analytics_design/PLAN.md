# Harbormaster Analytics — Implementation Plan

> **Decision (owner): the Proposed presentation was dropped.** The panels this plan calls "Proposed" were approved and ship as ordinary panels: no PROPOSED badge, no dashed style, no "needs new data" footer, no `showProposed` preference, and no `needs` field in the API (`ProposedDTO` is now `RollupPanelDTO`: `available`, `data_since`, `data`). A panel without data yet shows the standard empty state. The text below is the original plan and still uses the old names.

Read-only investigation of `/workspaces/harbormaster` (branch `feat/analytics`). Nothing was edited or committed. One throwaway schema (`hm_plan_scratch`) was created and dropped on the test DB to run EXPLAIN ANALYZE at target scale (1.2M `service_counts_history` rows, 100 clusters, 600 services).

Typesafe-ai skill was unavailable; option choices below state the alternatives, the pick and the reason.

---

## 0. Findings that shape the plan (verified in code)

| Fact | Where | Consequence |
|---|---|---|
| Scope + tag filter already exist: `scopeFrom(r)` parses `?account=&region=&tag=<id>&tag=<id>` into `scope{Vis store.Visibility, Filter store.TargetFilter, TagFilter}`; `narrowVisibility` makes `?tag=` only ever narrow. | `internal/api/scope.go` | Analytics reuses this unchanged. The prototype's All/Prod/Staging/Dev segmented control maps onto the global `?tag=` param (numeric ids from `me.tags`). |
| Visibility is applied in SQL via `queryArgs.visibility(alias, vis)` → `visibilitySQL{Joins, Predicate, Effective}` (left-joins `cluster_tags`/`service_tags`, effective tag = `coalesce(st.tag_id, ct.tag_id, builtin Dev)`), plus `queryArgs.target(alias, f)` for account/region. | `internal/store/snapshots_read.go:63-117` | Every analytics query uses exactly these two helpers. For clusters, `clusterVisiblePredicate`. |
| `operation_log` has **no type/action column**, only `text` + `details jsonb`; outcome rows from the OutcomeLogger have `actor_user_id IS NULL`; admin tag changes also land here. | `0001_owned.up.sql:96`, writers in `internal/deploy/{operate,rollout,register,outcome}.go`, `internal/api/{exec_ws,admin_tags}.go` | "Operations by type" must be derived (§2.4). Phase 1 adds `details.op` at write sites (code change, no schema change). |
| `revision_records` has only `task_definition_arn, registered_by_user_id, registered_at` — no account/region/cluster/service. | `0001_owned.up.sql:112` | Cannot be scoped or tag-filtered. "Revisions registered" uses the `operation_log` row written in the same tx by `register.go` (has service key + `details.task_definition_arn`). |
| `deployment_history` has no rollback/superseded_by/actor-name column; `kind in (deploy,restart,scale)`, `outcome in (completed,failed,superseded)`; rollbacks are stored as `kind='deploy'`. | `deployhist.go`, `rollout.go:76-80,102` | Rollback = deploy whose `revision` < previous deploy's revision for the same service (window `lag`). |
| `tasks.containers` is a jsonb array of `model.Container` **without json tags** → keys are `Name`, `ExitCode`. `stop_reason_group` has 7 fixed values (`status.StopReasonGroups`), only on STOPPED rows. `cpu`/`memory` are text CPU units / MiB. | `model/snapshot.go:133`, `status/stopreason.go` | Exit-code chart uses `jsonb_array_elements(containers)->>'ExitCode'`. Stop-reason chart uses the 7 groups verbatim (not the prototype's 6 labels). |
| `service_counts_history` PK `(account_id, region, cluster_name, service_name, ts)`, no other index; 1.2M rows ≈ the whole 7-day retention window (600 svc × 288/day × 7). | `0002_snapshots.up.sql:108` | A 7d query must scan the table; a `ts` index does not help (§3). |
| Retention: `Store.RunRetention(ctx, now, store.Retention)` runs hourly from the poller supervisor (leader-only, `supervisor.go`), via `timed` entries `{key, sql, age}`; config `RetentionConfig` parsed in `config.go`; README table under `## Configuration` → `### Retention`. No cron/daily mechanism exists. | `store/retention.go`, `poller/supervisor.go`, `poller/poller.go:ConfigFrom` | Rollups hook into the same supervisor tick, before `runRetention`. |
| `OutcomeLogger.decide` has `RolloutStateReason` for failures; `FinishDeployment(ctx, key, id, outcome, finishedAt)` has no reason param. Runs on **every** instance, idempotent via atomic claim. | `deploy/outcome.go`, `store/deployments.go:90`, `store/tx.go:45` | Add reason params; keep single-UPDATE claim. |
| CloudWatch: `metrics.Client.Fetch(ctx, target, span, queries)` chunks at 500 queries/call, pages NextToken, caches in `metrics_cache`; fake implements GetMetricData deterministically, counts calls as `"acct|region|cloudwatch|GetMetricData"`, `ThrottleCloudWatch` knob. | `metrics/client.go`, `awsx/fake/cloudwatch.go` | Util rollup uses `awsx.Clients.CloudWatch` directly (bypass the TTL cache) with one logical batch per target/day. |
| Contract tests: Go `contractShapes()` reflects DTO types into `web/src/test/contract/shapes.json` (`HM_UPDATE_CONTRACT=1 go test ./internal/api -run TestContract`); TS `contract.test.ts` checks fixtures/MSW bodies against it. nil slices → `null` breaks the contract. | `internal/api/contract_test.go`, `web/src/test/contract/` | Every analytics DTO must be a concrete struct (maps lose per-key typing); slices always non-nil. |
| Frontend: `react-router` v8 single package, TanStack Query v5, no chart lib. `LineChart` has no stacking/ref-line/hover; `Donut` is 88px with count legend; `Panel` has no footer/variant; `DataTable` supports `render`, `align`, `minWidth`, `rowStatus` bad/warn/info stripes, `pageSize`, but no footer. Preferences are localStorage (`useOverviewLayout` pattern); `/api/settings` is admin-only global. Tabs are `<Link>`s via `hrefFor(key)`. | `web/src/charts/*`, `components/*`, `app/overview/useOverviewState.ts`, `components/Tabs.tsx` | New `TimeSeriesChart`, extend `Donut`/`Panel` non-breakingly, localStorage prefs. |
| Poller does not sample cluster capacity anywhere; `countsLoop` → `recordCounts` → `InsertCountsHistory` every `HM_POLL_COUNTS_HISTORY` (5m). | `poller/loop.go`, `history.go` | `cluster_capacity_daily` is maintained incrementally from the same tick (§4.3). |

---

## 1. API contract

### 1.1 Routes and params

`GET /api/analytics/delivery`, `/reliability`, `/capacity`, `/operations` — registered in new `internal/api/analytics_routes.go` via `s.handle(mux, "GET /api/analytics/delivery", s.analyticsDelivery, routeOptions{})`, called from `routes()` next to `s.storageRoutes(mux)` (before the `/api/` 404 catch-all). Auth + ETag come from `routeOptions{}`; no admin requirement; read visibility via `sc.Vis` (View).

Query params (all existing semantics reused via `scopeFrom`):
- `account`, `region` — as every list endpoint (`store.TargetFilter`).
- `tag` — repeatable numeric tag id (`parseTagFilter`); narrows visibility, never widens.
- `range` — one of `24h|7d|30d|90d`; default `30d`; anything else → 422 `invalid` "range must be one of 24h, 7d, 30d or 90d." (new `analyticsRangeFrom(r) (AnalyticsRange, error)` in `internal/api/analytics.go`, mirroring `rangeFrom` in `observe_metrics.go`). On `capacity` the param is accepted and ignored (`window.range_applies=false`).

Bucket sizes: `24h→1h` (24 buckets), `7d→4h` (42), `30d→1d` (30), `90d→2d` (45) — same n as the prototype's `RANGES`. `to` = now truncated to 5 min (counts cadence; keeps ETag stable within a poll interval), `from = to − used`, buckets = `date_bin(size, ts, from)`; the last bucket may be partial.

### 1.2 Shared DTOs (Go `internal/api/analytics_dto.go`, TS `web/src/api/analytics.ts` types section)

```go
type AnalyticsWindowDTO struct {
    Requested    string    `json:"requested"`     // "30d"
    Used         string    `json:"used"`          // "7d"
    Clamped      bool      `json:"clamped"`
    RangeApplies bool      `json:"range_applies"` // false on capacity
    Bucket       string    `json:"bucket"`        // "1h"|"4h"|"1d"|"2d"
    From, To     time.Time `json:"from"/"to"`
    Retention    string    `json:"retention"`     // governing table's retention as Go duration text, e.g. "168h0m0s"→ normalise to "168h"
}
type AnalyticsSourceDTO struct { Table, Reads, Kept string; Setting *string `json:"setting"`; WindowUsed *string `json:"window_used"` }
type SeriesDTO struct { Timestamps []time.Time `json:"timestamps"`; Series []SeriesLineDTO `json:"series"` }
type SeriesLineDTO struct { Key, Label string; Values []*float64 `json:"values"` } // null = no sample; counts are never null (zero-filled)
type DonutDTO struct { Total float64; Slices []DonutSliceDTO } ; type DonutSliceDTO struct { Key, Label string; Value float64 }
type ProposedDTO[T any] struct {
    Available bool       `json:"available"`
    DataSince *time.Time `json:"data_since"` // min(day) of the rollup table once it has data
    Needs     *NeedsDTO  `json:"needs"`      // {table,size,how} while unavailable
    Data      *T         `json:"data"`
}
type TopTableDTO[R any] struct { Rows []R `json:"rows"`; Total int `json:"total"` } // Rows ≤ 50, Total = matching population
```
Shared envelope fields on each tab DTO: `tab string`, `window AnalyticsWindowDTO`, `sources []AnalyticsSourceDTO`, `polled_at time.Time`. KPIs are typed numeric structs (frontend formats; `*float64` where undefined, e.g. CFR with zero rollouts).

### 1.3 Per-tab response shapes

**`DeliveryDTO`**
- `kpis`: `rollouts int, rollouts_per_day float64, change_failure_rate *float64 (0..1), median_rollout_s *float64, p90_rollout_s *float64, time_to_recover_s *float64, rollbacks int, superseded int`
- `rollouts SeriesDTO` (keys `completed|failed|superseded`, stacked), `failure_rate SeriesDTO` (key `failure_rate`, percent, nullable), `duration SeriesDTO` (keys `median|p90`, seconds, nullable)
- `services TopTableDTO[DeliveryServiceRowDTO]` row: `service_id string (model.ServiceKey.ID()), service, cluster, account_id, tag TagDTO, rollouts int, failed int, failure_rate float64, median_s *float64, recover_s *float64, last_failed_at *time.Time`
- Proposed: `failure_reasons ProposedDTO[SeriesDTO]` (keys = failure_reason groups), `recent_failures ProposedDTO[TopTableDTO[FailedRolloutRowDTO]]` row: `at, service_id, service, cluster, from_revision *int, to_revision int, reason string, detail string, ran_for_s *float64`

**`ReliabilityDTO`**
- `kpis`: `availability *float64 (0..1), minutes_below_desired float64, services_below int, task_stops int, unexpected_stops int, out_of_memory int, placement_failures int`
- `availability SeriesDTO` (nullable %), `services_below SeriesDTO` (distinct services with any below sample in bucket), `stops_by_reason SeriesDTO` (7 keys, `expected: bool` flag per line for grey styling — add `Expected bool \`json:"expected"\`` to `SeriesLineDTO`), `unexpected_by_zone SeriesDTO` (keys = AZs seen, max 8 + "other"), `exit_codes SeriesDTO` (keys `1,137,143,255,other` — top 4 codes by count + other)
- `least_available TopTableDTO[ReliabilityServiceRowDTO]` row: `service_id, service, cluster, tag, availability float64, minutes_below float64, unexpected_stops int, top_reason *string`
- Proposed: `availability_90d ProposedDTO[SeriesDTO]`, `below_desired_90d ProposedDTO[SeriesDTO]`, `getting_worse ProposedDTO[TopTableDTO[AvailabilityTrendRowDTO]]` row `service_id, service, cluster, tag, previous float64, current float64, change_pts float64`

**`CapacityDTO`** (`window.used="7d"`, `range_applies=false`)
- `kpis`: `reserved_vcpu float64, reserved_mem_gib float64, running_tasks int, services int, spot_share *float64, at_scaling_max int, subnets_low_ips int`
- `reserved_vcpu_7d SeriesDTO` (one line per effective tag visible, key = tag id as string, label = tag name, stacked)
- `vcpu_by_account DonutDTO` (label = `aws_accounts.name` via `accountNames`), `tasks_by_capacity_provider DonutDTO` (key = capacity_provider or launch_type when provider empty, e.g. `EC2`)
- `clusters TopTableDTO[CapacityClusterRowDTO]` row `cluster_id, cluster, account_id, account_name, tag, tasks int, vcpu float64, mem_gib float64, share float64, spot_share float64`
- `at_scaling_max []AtMaxRowDTO` row `service_id, service, cluster, desired int, max_capacity int, at_max_since *time.Time` (duration from counts history, nil when unknown)
- `subnets_low []SubnetRowDTO` row `subnet_id, name, az, account_id, region, available_ips int, total_ips int (from cidr), used_pct float64`
- Proposed: `reserved_vs_used_90d ProposedDTO[SeriesDTO]` (keys `reserved|used_p95|used_avg`), `spot_share_90d ProposedDTO[SeriesDTO]`, `memory_90d ProposedDTO[SeriesDTO]` (keys `reserved|used_p95`), `over_reserved ProposedDTO[TopTableDTO[OverReservedRowDTO]]`

**`OperationsDTO`**
- `kpis`: `operations int, people int, revisions_registered int, scale_changes int, restarts int, tasks_stopped int, terminal_sessions int`
- `by_type SeriesDTO` (keys `deploy|rollback|task_definition|scale|restart|stop_task|terminal`), `hands_on SeriesDTO` (keys `restart|stop_task|terminal`), `restarts_per_100_deploys SeriesDTO` (nullable)
- `who TopTableDTO[OperatorRowDTO]` row `username, total, deploy, restart, scale, terminal int, last_at time.Time`
- `most_restarted TopTableDTO[RestartedServiceRowDTO]` row `service_id, service, cluster, tag, restarts, people int, last_at time.Time`

Contract: add `analyticsDelivery|analyticsReliability|analyticsCapacity|analyticsOperations` to `contractShapes()`; regenerate `shapes.json`; add fixture-based cases in `contract.test.ts`.

---

## 2. Data mapping per panel

Conventions used below: `h`=service_counts_history, `d`=deployment_history, `t`=tasks, `e`=service_events, `o`=operation_log, `s`=services. Every store method has signature shape `func (s *Store) Analytics<Tab>…(ctx, f TargetFilter, vis Visibility, from, to time.Time, bucket time.Duration) (…, error)` and builds SQL with `var q queryArgs; target := q.target("d", f); v := q.visibility("d", vis)` → `from deployment_history d` + `v.Joins` + `where target and v.Predicate and d.started_at >= $from and d.started_at < $to`. Tag filter needs nothing extra: `scopeFrom` already folded `?tag=` into `vis`. Effective tag for table rows comes from `v.Effective` selected as a column, mapped with `s.tagDTOs`/`inheritedTag` (`clusters.go:105`).

Retention clamping: `used = min(requested, retention(table))` where retention comes from `s.Cfg.Retention.<Field>` (0 = forever → no clamp). A tab's `window` uses the governing table; `sources[i].window_used` is set when a panel's table differs (e.g. placement failures from `service_events` at 30d while the tab is clamped to 7d). Implement once: `clampWindow(requested AnalyticsRange, retention time.Duration, now) windowSpec` in `internal/api/analytics.go`, unit-tested without DB.

### 2.1 Delivery — `deployment_history` (HM_RETENTION_DEPLOYMENT_HISTORY 2160h)
Rows: ~20/day × 90 = ~2K. One store call `AnalyticsDeployments(...) ([]DeploymentFact, error)` returning the raw visible rows in window (`key, deployment_id, kind, revision, started_at, finished_at, outcome, started_by_user_id, effective_tag`) and aggregating in Go — rows are tiny, and medians/p90/TTR/rollback need per-service ordering anyway (`percentile_cont` exists in PG but Go keeps one query and one code path for all 4 ranges).
- Rollouts & per day: count rows with `started_at` in window (all kinds; sub "deploy, restart and scale"); per day = n / used days.
- CFR: `failed / (completed + failed)`; open rows (outcome null) excluded.
- Median/p90 duration: `finished_at − started_at` over `outcome='completed'`; per bucket nullable.
- Time to recover: for each `failed` row, next `completed` of the same service with `started_at > failed.finished_at`; median of `completed.finished_at − failed.finished_at`. Uses rows up to `to` even if the recovery falls in window while the failure is before `from`? No — only failures within the window; recoveries may be after `from` only. Document.
- Rollbacks: deploys ordered by `started_at` per service; row counts as rollback when `kind='deploy'` and `revision < lag(revision)` (previous deploy/restart of the same service). Flag: the README's "deploys to a lower revision" is exactly this; Harbormaster's own `Rolled back` op text would be an alternative but misses rollbacks done in the console.
- Superseded: `outcome='superseded'`.
- Charts: stacked `completed|failed|superseded` per bucket (`date_bin` on `started_at`); failure-rate line per bucket (null when no completed+failed); duration median/p90 per bucket (null when no completed).
- Table: group by service, `failed > 0`, sort by failure rate desc, Rows ≤ 50, Total = services with ≥1 rollout.
- Proposed (Phase 2): `failure_reasons` stacked by `d.failure_reason` per bucket; `recent_failures` = last 50 failed rows with `failure_reason, failure_detail`, `from_revision` = lag(revision) of same service.

### 2.2 Reliability — `tasks` STOPPED (168h), `service_counts_history` (168h), `service_events` (720h)
Governing window: `min(range, counts retention)` → 30d/90d clamp to 7d; notice text from `window.clamped`.

Counts-history work is split into exactly **two scans** (see §3 for why not one per-service-per-bucket pass):
1. `AnalyticsAvailabilitySeries(ctx, f, vis, from, to, bucket) ([]AvailBucket, error)` — per bucket: `count(*) samples, count(*) filter (where running >= desired) ok, count(distinct (cluster_name, service_name)) filter (where running < desired) below_services`, restricted to visible services by `where (h.account_id,h.region,h.cluster_name,h.service_name) in (select … from services s <v.Joins> where target and v.Predicate)` (semi-join on 600 keys). Availability % = ok/samples (null when samples=0). Services-below line = `below_services`.
2. `AnalyticsAvailabilityByService(ctx, f, vis, from, to) ([]ServiceAvail, error)` — per service: `samples, ok, below = count(*) filter (where running < desired)`, join to services for visibility/tag; minutes_below = below × 5 (sample interval `HM_POLL_COUNTS_HISTORY`, pass `cfg.Poll.CountsHistory` minutes rather than hard-coding 5). Feeds KPI "Time below desired" (sum), "services_below" count, and the least-available table (sort availability asc, Rows ≤ 50).
- Stops: `AnalyticsStops(ctx, f, vis, from, to, bucket)` over `tasks t` with `t.last_status='STOPPED' and t.stopped_at >= from and < to` (uses `tasks_stopped_at_idx`), visibility via `v.Joins` on `t` (tasks have `service_name`; standalone tasks with null service_name get the cluster tag through `coalesce` — same as `ListTasks`). Returns per-bucket counts by `stop_reason_group` (7 groups; expected = `Replaced by a deployment`, `Scaled in`, `Stopped by a user`), per-bucket counts by `availability_zone` for unexpected groups, and per-bucket exit-code counts from `jsonb_array_elements(t.containers) c` where `(c->>'ExitCode')::int <> 0` (`cross join lateral`; top-4 codes computed in Go, rest → `other`). Three small GROUP BYs over ≤ ~20K rows; fine as three queries or one with `grouping sets`. KPIs: task_stops = all; unexpected = not in expected set; out_of_memory = group `Out of memory (exit 137)`; top_reason per service for the table = mode of unexpected groups.
- Placement failures: `AnalyticsPlacementFailures(ctx, f, vis, from30, to) (int, error)`: `count(*) from service_events e … where e.created_at >= $1 and e.message ilike '%unable to place%'` (uses `service_events_created_at_idx`); window = min(range, 720h) reported in `sources[].window_used`. ECS wording verified: "was unable to place a task".
- Proposed (Phase 2, all from `service_availability_daily`): 90d availability = `sum(samples_ok)/sum(samples)` per 2-day bucket; time below desired = `sum(minutes_below)/60` per bucket; getting-worse table = per service availability last 30 days vs previous 30, `change_pts = cur − prev`, sorted asc, only `change < 0`.

### 2.3 Capacity — latest poll (`tasks`, `services`, `task_definitions`, `scaling`, `subnets`) + counts history 7d
- Running-task facts: `AnalyticsRunningTasks(ctx, f, vis) ([]CapacityTaskFact, error)` → `cluster key, service_name, cpu, memory, capacity_provider, launch_type, effective_tag` for `t.last_status='RUNNING'` (≤1,000 rows; `tasks_last_status_idx`). Go sums exactly like `reserved()` in `clusters.go:124` (reuse/generalise it): vCPU = Σ atoi(cpu)/1024, mem GiB = Σ atoi(memory)/1024; spot share = provider `FARGATE_SPOT` / running. Donuts: by `account_id` (label via `accountNames`), by capacity provider (empty provider → `launch_type`). Clusters table: group by cluster, share = cluster vCPU / total, spot_share per cluster; `cluster_id = model.ClusterKey.ID()`; Rows ≤ 50.
  Flag: EC2 tasks without task-level `cpu` count as 0 (same as today's cluster card); note in the panel sub "task-level cpu only".
- At scaling max: `AnalyticsAtScalingMax(ctx, f, vis) ([]AtMaxRow, error)`: `services s join scaling sc using (keys) where sc.has_target and s.desired >= sc.max_capacity and sc.max_capacity > 0`, visibility on `s`. Duration: for those ≤ ~20 services, one query on counts history `select max(h.ts) filter (where h.running < $max) …` per service via `= any($keys)` restricted by PK prefix (EXPLAIN E2 shape, but only for the handful of at-max services, ~2 ms each); `at_max_since = coalesce(last_below_ts, min(ts))`, nil when no history.
- Subnets low: `AnalyticsSubnetsLow(ctx, f) ([]SubnetRow, error)`: `available_ips::float / (2^(32 - masklen(cidr::cidr)) - 5) < 0.10`, `used_pct` computed in SQL; subnets are not tag-scoped (no cluster/service) → apply account/region only, plus admin or any-visible check: non-admin users see subnets only in accounts/regions where they have at least one visible service (exists subquery on services with `v.Predicate`). Same rule the Networks page uses — confirm with `networks.go` when implementing.
- Reserved vCPU 7d stacked by tag: `AnalyticsReservedTrend(ctx, f, vis, from, to, 4h) ([]TrendPoint{bucket, tag_id, vcpu}, error)`: `select date_bin(...) b, m.tag_id, sum(h.running * m.cpu_units)/1024.0 from service_counts_history h join (select s.keys, <v.Effective> tag_id, coalesce(nullif(td.cpu,'')::int,0) cpu_units from services s <v.Joins> left join task_definitions td on td.account_id=s.account_id and td.region=s.region and td.arn=s.task_definition_arn where target and v.Predicate) m using (account_id,region,cluster_name,service_name) where h.ts >= from and h.ts < to group by 1,2`. One scan + hash join to a 600-row build (≈0.4–0.6 s at 1.2M rows; see §3).
- Proposed (Phase 2): `reserved_vs_used_90d` = `cluster_capacity_daily` (`vcpu_sum/samples`, `vcpu_max`) + `service_util_daily` cpu used avg/p95 summed over visible services per 2-day bucket; `spot_share_90d` = `spot_sum/tasks_sum`; `memory_90d` from `cluster_capacity_daily.mem_*` + `service_util_daily.mem_p95`; `over_reserved` = per service last-90d `cpu_p95/cpu_reserved`, `mem_p95/mem_reserved`, `unused_vcpu = (reserved − p95) × running`, sorted desc.

### 2.4 Operations — `operation_log` source=harbormaster (2160h), `revision_records`
Classification (SQL CASE as a store constant `opKindSQL`, selected as `op_kind`):
```
coalesce(o.details->>'op',
 case when o.actor_user_id is null then 'system'                         -- OutcomeLogger rows: excluded
      when o.details ? 'session_id' and o.text like 'Opened terminal%' then 'terminal'
      when o.details ? 'session_id' then 'terminal_close'                 -- excluded
      when o.details ? 'task_definition_arn' then 'task_definition'
      when o.details ? 'deployment_id' and o.text like 'Rolled back%' then 'rollback'
      when o.details ? 'deployment_id' and o.text like 'Restarted%' then 'restart'
      when o.details ? 'deployment_id' then 'deploy'
      when o.text like 'Changed desired count%' then 'scale'
      when o.text like 'Stopped task%' then 'stop_task'
      else 'other' end)                                                   -- incl. admin tag changes: excluded
```
Phase 1 also adds `"op": "<kind>"` to `Details` at the six write sites (`operate.go` ×2, `rollout.go`, `register.go`, `exec_ws.go` open/close, `outcome.go` → `"op":"outcome"`), so new rows no longer rely on text matching; the text heuristics remain for historical rows. Rationale: options were (a) text heuristics only — fragile; (b) new `kind` column — a schema change the brief forbids in Phase 1; (c) jsonb key — robust and schema-free. Chose (c)+(a) fallback.
- `AnalyticsOperations(ctx, f, vis, from, to, bucket)` returns per-bucket counts by `op_kind` (excluding system/terminal_close/other), plus `AnalyticsOperators(...)` grouped by `actor_username` (counts per kind, `max(ts)`, Rows ≤ 50), and `AnalyticsRestartedServices(...)` grouped by service for `op_kind='restart'` (`count(*)`, `count(distinct actor_user_id)`, `max(ts)`). Visibility: `v.Joins` on `o` (standalone-task rows have null `service_name` → cluster tag, same as `RecentOperations`). Index: `operation_log_service_ts_idx` leads with account_id; at ~2.5K rows/90d a seq scan is fine (§3).
- KPIs: operations = Σ counted kinds; people = `count(distinct actor_user_id)`; revisions registered = `task_definition` kind (flag: `revision_records` cannot be scoped — source footer still lists it as "forever" for honesty, with reads = "cross-check only"); scale changes; restarts; tasks stopped by hand = `stop_task`; terminal sessions = `terminal` (open rows; one per session).
- Charts: by_type stacked (`deploy, rollback, task_definition, scale, restart, stop_task, terminal`); hands-on lines (`restart, stop_task, terminal`); restarts per 100 deploys per bucket = `restart / (deploy+rollback) × 100`, null when no deploys.

---

## 3. Performance (measured on the test DB at target scale)

Row counts at 100 clusters / 600 services / 1,000 tasks: `service_counts_history` 1.21M (216 MB incl. PK); stopped `tasks` 7d ≈ 10–30K; `deployment_history` 90d ≈ 2K; `operation_log` 90d ≈ 2–5K; `service_events` 30d ≈ 100–200K; others ≤ 1K.

EXPLAIN ANALYZE results (PG default `work_mem=4MB`, 3 parallel workers):

| Query shape | Time | Note |
|---|---|---|
| B: 7d per-bucket aggregate, no joins, seq scan | 100–120 ms scan, 260 ms with 600-group per-service aggregate (D) | baseline floor: the whole table is the 7d window |
| B vs C: 24h per-bucket aggregate without / with `(ts)` index | 96 ms / 84 ms | index saves ~12 ms; **not worth an index** |
| A: 7d availability with visibility joins applied **per history row** | 608 ms | the two tag hash-joins cost 5× the scan |
| A3/A4: per-(service, bucket) grouping over 7d | 1.1–1.6 s (sort spills to disk at 4MB work_mem; 0.7 s with 64MB) | **avoid** this shape |
| E: window function (`sum(...) over partition`) over 7d | 2.2 s | avoid |
| E2/G: nested loop per service over PK (600 × 2016 rows) | 1.6–2.1 s | avoid for all-service queries; fine for ≤ 20 at-max services |

Decisions:
1. **No new indexes in Phase 1.** `tasks_stopped_at_idx`, `service_events_created_at_idx`, `tasks_last_status_idx` already cover the selective filters; `deployment_history`/`operation_log` are tiny; a `service_counts_history(ts)` index buys ~10 ms on 24h and nothing on 7d (the table *is* the window). Add indexes only in the Phase 2 migration for the new tables (§4.1).
2. Counts-history query shapes: per-bucket aggregates with the visibility applied as a **semi-join on the 600 visible service keys** (one hash probe per row instead of two), and per-service aggregates (600 groups fit in memory). Never group by (service, bucket) over the full window. Expected: Reliability ≈ 2 scans ≈ 350–450 ms wall when run concurrently; Capacity trend ≈ 400–600 ms.
3. Run each tab's independent store calls concurrently with `errgroup` (bounded to the tab's 3–6 queries) so endpoint latency ≈ slowest query, not the sum.
4. Account/region scope makes the PK prefix usable (`h.account_id = $1 and h.region = $2`), so scoped views are proportionally faster.
5. Optional, measurement-gated: a 60 s in-process result cache keyed by `(tab, range, TargetFilter, Vis.Admin, sorted Vis.TagIDs)` in `internal/api/analytics_cache.go`, only if the Phase 1 acceptance benchmark (`BenchmarkAnalytics_Reliability_1_2M` via storetest seeding 1.2M rows, skipped unless `HM_BENCH=1`) exceeds 700 ms p95. The key includes visibility, so no permission leak.
6. Phase 2 reads of daily tables are 100–600 rows/day × ≤ 365 days = ≤ 219K rows; index on `(day)` (below) keeps 90d scans and retention deletes to index range scans.

---

## 4. Phase 2

### 4.1 Migration `internal/store/migrations/0080_analytics.up.sql`
The repo has **no down migrations** (`migrate.go` embeds `*.up.sql` only) → none added. Lowercase style, `--` comments, `<table>_<cols>_idx` naming, each file one transaction.

```
service_availability_daily (account_id, region, cluster_name, service_name text not null, day date not null,
  samples int not null, samples_ok int not null, minutes_below int not null, unexpected_stops int not null,
  primary key (account_id, region, cluster_name, service_name, day))
  index service_availability_daily_day_idx (day)
cluster_capacity_daily (account_id, region, cluster_name text not null, day date not null,
  samples int not null, vcpu_sum double precision not null, vcpu_max double precision not null,
  mem_gib_sum double precision not null, mem_gib_max double precision not null,
  tasks_sum int not null, tasks_max int not null, spot_sum int not null,
  primary key (account_id, region, cluster_name, day))
  index cluster_capacity_daily_day_idx (day)
service_util_daily (account_id, region, cluster_name, service_name text not null, day date not null,
  cpu_avg, cpu_p95, mem_avg, mem_p95 double precision,        -- percent of reserved, null when Insights off
  cpu_reserved_units int, mem_reserved_mib int,               -- from the service's task definition at rollup time
  primary key (account_id, region, cluster_name, service_name, day))
  index service_util_daily_day_idx (day)
alter table deployment_history add column failure_reason text;   -- group: circuit_breaker|health_check|failed_to_start|out_of_memory|stopped_by_user|other
alter table deployment_history add column failure_detail text;   -- raw RolloutStateReason or last stopped task's reason
```
Deviation from README (flagged): `cluster_capacity_daily` stores sums + sample counts rather than averages so the row can be updated incrementally and idempotently every 5 minutes (avg = sum/samples on read); `failure_reason` is split into group + detail so the "Why rollouts failed" chart groups on an indexed-able enum and the table shows the raw text without a second lookup. Sizes stay within the README's estimates.

### 4.2 Nightly rollups — where, ordering, idempotency, backfill
- New package `internal/rollup` with `type Runner struct { Store *store.Store; Clients awsx.Clients; Now func() time.Time; UtilEnabled bool; Log *slog.Logger }` and `func (r *Runner) RunDue(ctx) (Result, error)`; `Result map[string]int` (rows written per table).
- Hook: `poller/supervisor.go` retention tick handler becomes `p.runRollups(ctx); p.runRetention(ctx)` (same goroutine, leader-only, hourly). Ordering guarantee: rollups always run in the same tick before retention deletes, and process complete UTC days `D−1 … ` back to the watermark, so source rows (counts 168h, stopped tasks 168h) are still present. If the leader was down > 6 days, the missing days are reported in the log and left as gaps (no fabricated rows).
- Due logic: a day `D` is due when `D < today(UTC)` and `D > watermark`, watermark = `max(day)` in the target table (per table), lower bound = `today − 6` for availability (source retention) and `today − 1` for util. First run on an empty table backfills 6 days of availability from existing counts history — this is the "backfill behaviour": historical data appears up to retention depth, `data_since` = min(day).
- Idempotency: `insert … on conflict (pk) do update set …` recomputing the day from source; re-running a day is safe. Rows are written in one tx per day per table.
- Store methods (`internal/store/analytics_daily.go`): `RollupAvailabilityDay(ctx, day) (int, error)` (one `insert … select` from counts history + stopped tasks grouped per service: `samples, samples_ok, minutes_below = below × interval, unexpected_stops = count stopped tasks of that service that day with group not in expected set`), `UpsertClusterCapacitySample(ctx, day, samples []ClusterCapacitySample) error` (incremental, see 4.3), `UpsertServiceUtilDay(ctx, day, rows []ServiceUtilRow) error`, `DailyDataSince(ctx) (map[string]*time.Time, error)` (min(day) per table, feeds `ProposedDTO.DataSince`), `DeleteDailyBefore` entries added to `RunRetention`'s `timed` slice keyed `service_availability_daily`, `cluster_capacity_daily`, `service_util_daily` on `day < $1::date`.
- Tests (storetest): `TestRollupAvailabilityDay_CountsAndStops`, `TestRollupAvailabilityDay_Idempotent` (run twice, same rows), `TestRunner_BackfillsSixDaysOnEmptyTable`, `TestRunner_SkipsUtilWhenDisabled`, `TestSupervisor_RollupRunsBeforeRetention` (poller: seed counts older than retention but within the backfill window; after one tick the daily row exists and the source rows are gone).

### 4.3 `cluster_capacity_daily` from the counts-history poll
`Loop.recordCounts` (poller/history.go) already runs every 5 min with `l.latest` services. Extend it: after `InsertCountsHistory`, call `store.RunningTaskCapacityByCluster(ctx, target) ([]ClusterCapacitySample, error)` (one SQL over RUNNING tasks of this account/region: Σ cpu/1024, Σ memory/1024, count, count spot per cluster — ≤ 1,000 rows) and `UpsertClusterCapacitySample(ctx, today, samples)` with `samples = samples+1, vcpu_sum += v, vcpu_max = greatest(…)…`. Reads on the Capacity tab use `vcpu_sum/samples`. Matches the README's "running tasks sampled on the counts-history poll" with one row per cluster per day and no per-sample table. Test: `TestLoop_CountsTickUpdatesClusterCapacityDaily` (two ticks → `samples=2`, `vcpu_max` ≥ `vcpu_sum/2`).

### 4.4 CloudWatch util rollup (`HM_ANALYTICS_UTIL`, default true)
- `rollup.Runner.utilDay(ctx, target awsx.Target, day)`: list visible-to-nobody-in-particular (all) services in the target whose cluster has Container Insights (`metrics.InsightsMode(cluster) != "disabled"`); build queries with `metrics.Query{Namespace:"ECS/ContainerInsights", MetricName: CpuUtilized|MemoryUtilized|CpuReserved|MemoryReserved, Stat: "Average"|"p95", Period: 86400, Dimensions: ClusterName, ServiceName}` with ids `cpu_avg_<n>`, `cpu_p95_<n>`, … (`n` = index; same sanitising as `TaskQueries`). 600 services × 6 queries = 3,600 queries → metrics.Client already chunks at 500 and pages; that is the "one batch per account+region per day" (one logical batch, ≤ 8 HTTP calls). Call `Clients.CloudWatch(ctx, target)` + `metrics` builders directly (not `Client.Fetch`, to bypass `metrics_cache` TTL and its 100-point span rules) with `StartTime=day 00:00Z, EndTime=day+1`. p95 of utilised ÷ average reserved → `cpu_p95`; `cpu_reserved_units`/`mem_reserved_mib` from the service's current `task_definitions` row.
- Throttling: `awsx.IsThrottle(err)` → log, leave the day undone (watermark unchanged) so the next hourly tick retries; at most one attempt per hour per target.
- Fake AWS: deterministic values exist for `ECS/ContainerInsights`; `fake.Fake.Calls()` lets tests assert `CallCounts["acct|region|cloudwatch|GetMetricData"]` == ⌈queries/500⌉ for one day, and `ThrottleCloudWatch=1` → no rows, second run succeeds. Services with `MetricProfile.Running=false` return 0 → row with zeros (still "available").
- Config: `Analytics struct { Util bool }` on `config.Config` (`HM_ANALYTICS_UTIL`, `reader.boolean`), plumbed to `rollup.Runner.UtilEnabled` in `cmd/harbormaster/serve.go` → `poller.Config.Rollup`.

### 4.5 OutcomeLogger `failure_reason`
- `decide` returns `(outcome, reason, silent)` today; extend to `(outcome string, failure failureInfo, silent bool)` where `failureInfo{Group, Detail string}` — `Group` from a new `deploy.ClassifyFailure(rolloutStateReason string, lastStop *store.StoppedTaskReason) string`: circuit breaker text → `circuit_breaker`; health-check text → `health_check`; otherwise if last stopped task of the deployment (`tasks.started_by = deployment_id`, newest `stopped_at`; new store method `LastStoppedTaskReason(ctx, key, deploymentID) (*StoppedTaskReason, error)` returning `stop_reason_group, stopped_reason`) maps `Out of memory → out_of_memory`, `Failed to start → failed_to_start`, `Stopped by a user → stopped_by_user`, else `other`. `Detail` = RolloutStateReason, or the task's `stopped_reason`.
- `FinishDeployment(ctx, key, id, outcome, finishedAt)` gains `failure *FailureInfo` (nil for non-failed) → `set outcome=$6, finished_at=$7, failure_reason=$8, failure_detail=$9 where … outcome is null` (same atomic claim). `Tx.FinishDeployment` mirrors. Existing callers updated. Operation text/details unchanged plus `details.failure_reason`.
- Tests: `TestOutcomeLogger_FailedStoresCircuitBreakerReason` (fake's reason text → `circuit_breaker`), `TestOutcomeLogger_FailedWithoutReasonUsesLastStoppedTask` (seed OOM stopped task with `started_by=deployment id` → `out_of_memory`), `TestClassifyFailure` table test, `TestFinishDeployment_WritesFailureColumns`.

### 4.6 Config, retention, README
- `RetentionConfig` += `AvailabilityDaily (HM_RETENTION_AVAILABILITY_DAILY, 8760h)`, `CapacityDaily (HM_RETENTION_CAPACITY_DAILY, 8760h)`, `UtilDaily (HM_RETENTION_UTIL_DAILY, 2160h)`; `store.Retention` += same; `poller.ConfigFrom` copies them; `RunRetention` `timed` += 3 entries (`day < $1::date`). Tests: `TestLoad_Defaults` `want` updated (full-struct equality), `TestRunRetention_DeletesByAge` extended with the three tables, `TestRunRetention_ZeroDurationSkipsTable` covers them.
- README `### Retention` table += 3 rows; a new `### Analytics` row for `HM_ANALYTICS_UTIL` (`true`, "Daily CloudWatch utilisation rollup for the Capacity tab; one GetMetricData batch per account and region a day"). `docs/analytics_design/README.md`'s "Data used on this page" settings column matches.
- Frontend Phase 2: the API now returns `available:true, data:…`; panels drop the badge/dashed style automatically; when `available:true` but `data_since` is less than the window → empty state "Daily data started on {data_since}" inside the panel (and series still render what exists). No frontend code switch needed beyond what Phase 1 renders from `ProposedDTO`.

---

## 5. Frontend

- **Route**: `router.tsx` adds `<Route path="analytics" element={<Navigate to="/analytics/delivery" replace />} />` and `<Route path="analytics/:tab" element={<AnalyticsPage />} />` next to `storage`. Invalid tab → `Navigate` to delivery (SettingsPage pattern). `useCrumbs([['Analytics','/analytics/delivery'],[tabLabel]])`.
- **Sidebar**: `<NavItem to="/analytics/delivery" glyph="AN" label="Analytics" alsoActiveFor={['/analytics']} />` after Storage in `Sidebar.tsx` (before the Admin/Account section header, which is where "between Storage and Settings" lands in the current markup). Test `Sidebar.test.tsx`: "shows Analytics between Storage and Settings" (order of nav link names) and active state on `/analytics/reliability`.
- **Page state** (`web/src/app/analytics/useAnalyticsState.ts`): `tab` from `useParams`; `range` from `?range=` (parse with fallback `30d`, writer via the `useParamSetter` pattern from `useOverviewState.ts`, `replace:true`); **tag** reuses the global `useFilters().filters.tag` / `setTags` — decision: options were (a) a separate page-level `?tag=` with names → collides with the shell's `?tag=` ids param owned by `useFilters`; (b) reuse the global param — identical semantics to the backend and zero backend work. Chose (b). The segmented control renders `All` + each tag from `me.tags` (`visibleTagIds`), "All" → `setTags([])`, a tag → `setTags([id])`; when the global MultiSelect holds several ids the segment shows none selected (both controls stay consistent because they read one source). Tabs: `<Tabs items hrefFor={(k) => \`/analytics/${k}${location.search}\`} />` so account/region/tag/range survive tab switches; each tab label gets a dim mono window suffix (`90d`, `7d`, `live + 7d`, `90d`) via `TabItem.badge`.
- **Hook** `web/src/api/analytics.ts` (storage.ts pattern): types for the DTOs in §1 (+ `AnalyticsTab`, `AnalyticsRange`), `useAnalytics(tab, range)` → `useQuery({ queryKey: ['analytics', tab, range, filters], queryFn: () => api.get<…>(\`/api/analytics/${tab}${queryString({ ...filters, range })}\`), ...metricsPolling })` (60 s refetch like metrics; `keepPreviousData` via `placeholderData` to avoid flashes on range change).
- **Charts** (`web/src/charts/`, no new deps):
  - New `TimeSeriesChart.tsx`: props `{ timestamps: string[]; series: { key; label; color; values: (number|null)[]; expected?: boolean; dashed?: boolean }[]; mode: 'line'|'area'|'stacked'; yMin?: number; yMax?: number; yFormat?: (v)=>string; reference?: { value: number; label: string }; xLabels: [string,string,string]; dim?: boolean; height?: 150 }`. Renders `viewBox 0 0 600 150` `preserveAspectRatio="none"`, 5 gridlines (`var(--row-border)`, bottom `var(--border-strong)`), y labels at max and midpoint as HTML (reuses `.py` approach from LineChart), strokes 1.6 (`vectorEffect non-scaling-stroke`), area fill 0.12, stacked fill 0.42 + 1.25 top stroke, dashed reference line with right label, `niceMax` from `format.ts`. Decision: new component rather than extending `LineChart` — LineChart's fixed 0..max axis, vertical `marks`, and 7-series collapse are used by Overview/Service pages; stacking + hover would change its layout contract. Shared CSS goes in `charts.module.css`.
  - `useChartHover(n)` hook + `ChartTooltip` (HTML overlay): pointer index = `round(x/width × (n−1))`, crosshair div, point dots for non-stacked, tooltip rows per series + "Total" for stacked, flips to the right-anchored side when `left > 60%`; `tipLabel(i)` = `formatAgo`-style relative time from `web/src/components/Ago.tsx` (`"3h ago"`, `"today"` for day buckets). Keyboard: tooltip also shows on focus + arrow keys (accessibility; cheap).
  - `Donut.tsx`: add optional props `size?: number` (default 88), `ring?: number` (default current), `centerLabel?`, `legend?: 'count'|'pct'` — defaults preserve current rendering so `charts.test.tsx` stays green; analytics passes `size=132 ring=16 legend='pct'`.
  - X axis: three labels (`xLabels`) spread with the existing `.px` style.
- **Panel anatomy**: extend `components/Panel.tsx` with optional `badge?: ReactNode` (rendered before title), `legend?: ReactNode` (right side; analytics passes a `ChartLegend` of 3px bar + label + mono value), `footer?: ReactNode` (`.foot`, mono 10.5px, `var(--chrome)` background), `variant?: 'default'|'proposed'` (`.proposed`: dashed `#3d3466` border, `#100f18` bg — add tokens `--proposed-border`/`--proposed-bg` to `tokens.css`, dark and light values). `web/src/app/analytics/AnalyticsPanel.tsx` wraps Panel: takes `source?: {table, kept}`, `proposed?: ProposedDTO<unknown>`, decides badge/footer text (`source <table> · kept N` vs `needs new data <table> · size · how`), applies `dim` to the chart when proposed, and renders the Phase 2 empty state when `available && data_since` is after the window start.
- **Tables**: `DataTable` as-is; cells via small helpers in `app/analytics/cells.tsx`: `MonoCell`, `BarCell` (56×6 track + mono value; not `Meter`, which prints `%` in a 38px column and is 8px), `ServiceLink` (to `/services/:id`), `TagChip`. Row stripes via `rowStatus` (bad/warn). Footer "Showing 8 of N services · Show all" rendered under the table inside the Panel footer slot; "Show all" switches `pageSize` from 8 to 50 (the API already returned ≤ 50; `total` is the population).
- **KPIs**: `KpiStrip` with `value` + `<small>` sub (Overview `withNote` pattern), `valueColor` for thresholds (CFR > 5% warn, availability < 99.5% warn).
- **Notice bar**: `Banner kind="warn"` when `window.clamped` ("Showing the last 7 days. Stopped tasks and task-count history are kept 7 days.") and always on Capacity (the README sentence). Range `SegmentedFilter` is rendered with `aria-disabled` + 0.45 opacity on Capacity and a toast "This page does not use the range filter" on click (`useToast`).
- **"Data used on this page"**: full-width `DataTable` from `sources[]` (Table / What this page reads / Kept / Setting), shown when `showSources`.
- **Settings `showSources` / `showProposed`**: options: (a) prototype-only toggles dropped; (b) per-browser localStorage preference; (c) server-side per-user preference (no endpoint exists; `/api/settings` is admin-only global). **Recommend (b)**: `useAnalyticsPrefs(): [{showSources, showProposed}, set]` in `app/analytics/prefs.ts`, key `harbormaster-analytics-prefs`, defaults `{true, true}`, exposed as two `Toggle`s in a small "Display" disclosure at the right of the tab bar. Reason: matches every existing preference (`theme`, `sidebar`, `overview-layout`), no backend, and the toggles are a viewing aid, not policy. Revisit (c) only if the owner wants admins to hide proposed panels for everyone.
- **MSW/fixtures**: `web/src/test/fixtures.ts` += `ANALYTICS_DELIVERY|RELIABILITY|CAPACITY|OPERATIONS` (deterministic, built from `FIXTURE_NOW`; reliability fixture has `window.clamped=true`; capacity has `range_applies=false`; one proposed panel `available:false`, one `available:true` with `data_since` for Phase 2 states). `web/src/test/msw.ts` += `http.get('/api/analytics/:tab', ...)` reading `range` and returning the fixture with `window.requested` echoed. Contract cases: four `['analytics…', () => FIXTURE]` entries.

---

## 6. Work breakdown

Model tiers per CLAUDE.md: **Opus** = task manager/reviewer; **Sonnet** = query, chart, rollup implementation; **Haiku** = mechanical mirrors (types.ts, fixtures, README rows, config fields) under Sonnet's review. Test-first throughout: write the named test, run it to see the expected failure (compile error on a missing symbol counts only if the test also asserts behaviour that will fail once the symbol exists), then implement.

### Phase 1 — contract first (serial gate), then backend ∥ frontend

**T1 (Sonnet, gate) — DTOs + contract.** Files: `internal/api/analytics_dto.go`, `internal/api/contract_test.go`, `web/src/api/analytics.ts` (types only), `web/src/test/fixtures.ts`, `web/src/test/msw.ts`, `web/src/test/contract/contract.test.ts`, regenerate `shapes.json`. Tests: `TestContract_ShapesMatchGolden` (Go) and `contract.test.ts` cases pass; `TestAnalyticsDTO_SlicesNeverNil` (marshal zero-value DTOs, assert no `null` arrays — use a `newDeliveryDTO()` constructor family). Acceptance: shapes.json contains the four analytics entries; `npm run typecheck` passes with fixtures typed against the TS types.

**T2 (Sonnet) — window/range/bucket helpers.** `internal/api/analytics.go`: `analyticsRangeFrom`, `clampWindow(req, retention, now) windowSpec{from,to,bucket,used,clamped}`, `bucketIndex(ts, spec) int`, zero-fill helpers. Tests (no DB): `TestClampWindow_ClampsToRetention` (30d vs 168h → used 7d, clamped), `TestClampWindow_ZeroRetentionKeepsRequested`, `TestClampWindow_BucketSizes` (table: 24h→1h/24, 7d→4h/42, 30d→1d/30, 90d→2d/45), `TestAnalyticsRangeFrom_RejectsUnknown` (422 message), `TestAnalyticsRangeFrom_DefaultIs30d`.
Parallel with T1 (no dependency).

**T3 (Sonnet) — store: Delivery.** `internal/store/analytics_delivery.go` + `_test.go`: `AnalyticsDeployments`. Seed via `InsertDeploymentIfNew`/`AttributeDeployment`/`FinishDeployment` + `SetClusterTag`/`SetServiceTag`. Tests: `TestAnalyticsDeployments_WindowAndScope` (rows outside [from,to) excluded; account filter), `TestAnalyticsDeployments_VisibilityHidesOtherTags` (viewer with Prod only), `TestAnalyticsDeployments_TagFilterNarrowsAdmin`. Then `internal/api/analytics_delivery.go` with pure aggregation funcs + tests: `TestDeliveryAggregate_CFR_RollbackDetection_TTR_Percentiles` (hand-built facts: 2 deploys rev 5→7→6 → 1 rollback; failed at T, completed at T+18m → TTR 18m; median/p90 on 10 durations), `TestDeliveryAggregate_EmptyWindowYieldsNullRates`. Handler test `TestAnalyticsDelivery_EndToEnd` (newTestServer + seedMockupTags + pollAll + seeded deployments; `omer` sees only his tags; `?range=1y` → 422; ETag 304 on repeat).

**T4 (Sonnet) — store: Reliability.** `analytics_reliability.go`: `AnalyticsAvailabilitySeries`, `AnalyticsAvailabilityByService`, `AnalyticsStops`, `AnalyticsPlacementFailures`. Tests: `TestAnalyticsAvailabilitySeries_BucketsAndSemiJoin` (3 services, one hidden by tag; one sample below desired → ok/samples and below_services correct per 4h bucket), `TestAnalyticsAvailabilityByService_MinutesBelowUsesInterval`, `TestAnalyticsStops_GroupsZonesExitCodes` (containers jsonb with `ExitCode` 137/1/0; standalone task falls back to cluster tag), `TestAnalyticsPlacementFailures_MatchesUnableToPlace`. Handler: `TestAnalyticsReliability_ClampsNinetyDaysToRetention` (`window.used=="7d"`, `clamped==true`, `sources` has service_events with `window_used=="30d"`), `TestAnalyticsReliability_ProposedPanelsUnavailableInPhase1` (`available:false`, `needs.table=="service_availability_daily"`).

**T5 (Sonnet) — store: Capacity.** `analytics_capacity.go`: `AnalyticsRunningTasks`, `AnalyticsAtScalingMax`, `AnalyticsSubnetsLow`, `AnalyticsReservedTrend`; generalise `reserved()` from `clusters.go` into a shared helper (behaviour-preserving; existing cluster tests cover it). Tests: `TestAnalyticsRunningTasks_SumsAndSpot`, `TestAnalyticsAtScalingMax_DurationFromHistory` (history below max until T−3h → `at_max_since ≈ T−3h`; no history → nil), `TestAnalyticsSubnetsLow_UsesCidrSize` (/24 with 20 free → low; 60 free → not), `TestAnalyticsReservedTrend_StackedByTagUsesCurrentTaskDefinition`. Handler: `TestAnalyticsCapacity_IgnoresRangeAndReportsIt` (`range_applies=false`, `used=="7d"`), `TestAnalyticsCapacity_NonAdminSeesOnlyVisibleClusters`.

**T6 (Sonnet) — store: Operations + `details.op`.** `analytics_operations.go` (+ `opKindSQL`), write-site changes in `deploy/operate.go`, `rollout.go`, `register.go`, `outcome.go`, `api/exec_ws.go`. Tests: `TestOpKindSQL_ClassifiesLegacyRows` (insert rows with the six historical texts/details and no `op` key → expected kinds; outcome row → excluded; tag change → `other`), `TestOpKindSQL_PrefersDetailsOp`, `TestAnalyticsOperators_CountsPerKindAndLast`, `TestAnalyticsRestartedServices_DistinctPeople`; existing deploy tests extended to assert `details["op"]` (`TestScale_LogsOpKind`, etc.). Handler `TestAnalyticsOperations_RevisionsRegisteredFromOperationLog`.

**T7 (Sonnet) — routes + concurrency + benchmark.** `analytics_routes.go`, `errgroup` fan-out per tab, `BenchmarkAnalytics_Reliability_1_2M` (storetest, generate 1.2M rows with `generate_series`, skipped unless `HM_BENCH=1`). Acceptance: all four endpoints < 700 ms p95 at 1.2M rows locally; otherwise implement §3.5 cache (`TestAnalyticsCache_KeyIncludesVisibility`).
Depends on T3–T6.

**F1 (Sonnet) — charts.** `charts/TimeSeriesChart.tsx`, `charts/useChartHover.ts`, `charts/ChartTooltip.tsx`, `Donut.tsx` extension, CSS. Vitest `TimeSeriesChart.test.tsx`: `renders one path per series and 5 gridlines`, `stacked mode offsets upper series by lower values` (read `d` points), `draws dashed reference line with label at value`, `y labels show max and midpoint with yFormat`, `clamps yMin/yMax`, `hover at 10% shows tooltip left-anchored; at 70% flips (style right set)`, `stacked tooltip includes Total`, `null values break the line`; `Donut.test` additions: `default size unchanged`, `legend pct mode shows value and percent`. Depends only on T1 (types) — can start immediately.

**F2 (Haiku → Sonnet review) — API hook, prefs, state.** `api/analytics.ts` hook, `app/analytics/{prefs.ts,useAnalyticsState.ts}`. Tests: `analytics.test.ts` ("sends range and scope params", "query key includes filters"), `prefs.test.ts` ("defaults true/true", "persists to localStorage", "survives bad JSON"), `useAnalyticsState.test.tsx` ("invalid range falls back to 30d", "setRange keeps account/region/tag").

**F3 (Sonnet) — panel anatomy + cells.** `components/Panel.tsx` (badge/legend/footer/variant), `tokens.css`, `app/analytics/{AnalyticsPanel.tsx,cells.tsx,ChartLegend.tsx}`. Tests: `Panel.test.tsx` additions ("renders footer slot", "proposed variant sets data-variant and badge"), `AnalyticsPanel.test.tsx` ("source footer text", "proposed footer text uses needs", "hides footer when showSources false", "shows 'Daily data started on' empty state when available but data_since after window"), `cells.test.tsx` ("BarCell width = pct%").

**F4 (Sonnet) — page + tabs + sidebar + route.** `app/analytics/{AnalyticsPage.tsx,DeliveryTab.tsx,ReliabilityTab.tsx,CapacityTab.tsx,OperationsTab.tsx,analytics.module.css}`, `router.tsx`, `Sidebar.tsx`. Tests (`renderApp`): `AnalyticsPage.test.tsx`: "/analytics redirects to /analytics/delivery", "unknown tab redirects", "tab links keep ?account&tag&range", "range segment updates ?range and refetches" (MSW asserts param), "tag segment calls setTags with the id and All clears it", "reliability shows clamped notice from window", "capacity dims range and shows fixed notice; clicking range shows toast", "proposed panels hidden when showProposed off", "Data used on this page lists sources", "KPI strip renders 6 items with formatted values" per tab (one test per tab with the fixture), `Sidebar.test.tsx` order test. Depends on F1–F3.

Parallelism: T1 and T2 first (½ day). Then {T3, T4, T5, T6} in parallel with {F1, F2, F3}; T7 after T3–T6; F4 after F1–F3. Final: Opus review per task; Fable/Opus e2e review runs `make lint && make test`.

### Phase 2

**P1 (Sonnet) — migration + config + retention.** `0080_analytics.up.sql`, `config.go` (+ `Analytics.Util`, 3 retention fields), `store/retention.go`, `poller.ConfigFrom`, README. Tests: `TestMigrate_AppliesThrough0080` (existing migrate test pattern), `TestLoad_Defaults` updated, `TestLoad_ParsesAnalyticsUtilFalse`, `TestRunRetention_DeletesByAge` extended (3 tables), `TestRunRetention_ZeroDurationSkipsTable` extended.

**P2 (Sonnet) — availability rollup + runner + supervisor hook.** `internal/rollup/{runner.go,availability.go}`, `store/analytics_daily.go`, `poller/supervisor.go`. Tests in §4.2.

**P3 (Sonnet) — cluster capacity sampling.** `poller/history.go`, `store/analytics_daily.go`. Test §4.3 + `TestUpsertClusterCapacitySample_Idempotent`.

**P4 (Sonnet) — util rollup.** `internal/rollup/util.go`, fake assertions. Tests: `TestUtilDay_OneLogicalBatchPerTarget` (600 services → ⌈3600/500⌉=8 calls, 600 rows), `TestUtilDay_SkipsClustersWithoutInsights`, `TestUtilDay_ThrottleLeavesDayUndone`, `TestRunner_UtilDisabledWritesNothing`.

**P5 (Sonnet) — OutcomeLogger failure_reason.** §4.5 tests. Independent of P2–P4; depends on P1.

**P6 (Sonnet) — Phase 2 read paths.** Store reads for the 8 proposed panels (`analytics_daily_read.go`), handlers fill `ProposedDTO{Available:true, DataSince, Data}` when `DailyDataSince` has the table. Tests: `TestAnalyticsReliability_NinetyDayPanelsFromDaily`, `TestAnalyticsCapacity_ReservedVsUsedFromDaily`, `TestAnalyticsDelivery_FailureReasonsAndRecentFailures`, `TestProposed_DataSinceReportedWhenPartial`. Frontend: fixtures gain `available:true` variants; `AnalyticsPanel` tests already cover rendering; add `ReliabilityTab.test.tsx` "renders 90-day panels without proposed styling when available".

Parallelism: P1 first; then P2, P3, P4, P5 in parallel; P6 after P2–P5.

---

## 7. Risks and open questions (blocking only)

1. **Tag filter UX vs. the shell's global `?tag=`.** The plan binds the page's All/Prod/Staging/Dev segment to the existing global tag ids param (names are dynamic from `me.tags`; builtin Dev + whatever tags exist). If you intended a page-local filter independent of the top-bar MultiSelect, say so — it would need a differently named param (`?atag=`) and the backend would have to accept it; everything else is unchanged.
2. **Deviations from the README schema** (need a yes/no): `cluster_capacity_daily` as sums+samples instead of avg/max columns; `failure_reason` split into `failure_reason` (group) + `failure_detail`. Both keep the "one migration, small tables" promise.
3. **Operations typing relies on text heuristics for historical rows** (no `kind` column exists; Phase 1 adds `details.op` for new rows). Accept that pre-existing rows are classified by text prefixes (`Changed desired count`, `Stopped task`, `Opened terminal`, `Registered task definition`, `Rolled back`, `Restarted`, else deploy when `deployment_id` present)? Alternative is adding a column in 0080, which the brief scopes out.
4. **"Revisions registered" cannot use `revision_records`** for scoped/tag-filtered counts (no account/region/service columns). The plan counts the paired `operation_log` row instead and lists `revision_records` in sources as cross-check only. OK?
5. **Rollups run on the leader only, hourly, and backfill at most 6 days of availability** (bounded by the 168h counts retention). A leader outage longer than that leaves gaps rather than inventing data. Acceptable?
6. **Latency budget.** At 1.2M counts rows the Reliability and Capacity tabs will sit around 400–600 ms (two full-table scans each; measured). If you want sub-200 ms, approve the 60 s in-process result cache (§3.5) up front rather than gating it on the benchmark.
7. **Test DB URL**: the provided URL has no password; the Makefile's `make test-db` prints one with `postgres:hm@`. The passwordless URL worked here (psql connected), so tests will run — just noting the mismatch in case CI uses the Makefile form.
