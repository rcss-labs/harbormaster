# ECS API budget

Retrieved 2026-11-01 from AWS documentation. Budgets are per account x region loop (ECS throttles per account per region).

Sources:
- ECS request throttling: https://docs.aws.amazon.com/AmazonECS/latest/developerguide/request-throttling.html
- ECS service quotas: https://docs.aws.amazon.com/AmazonECS/latest/developerguide/service-quotas.html (lists no rate limits; points to the throttling page)
- ECS API failures: https://docs.aws.amazon.com/AmazonECS/latest/developerguide/api_failures_messages.html (resource `failures[]` reasons only; throttling error codes come from the throttling page and Common Errors)
- EC2 throttling: https://docs.aws.amazon.com/AWSEC2/latest/APIReference/throttling.html
- CloudWatch quotas: https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/cloudwatch_limits.html
- EFS quotas: https://docs.aws.amazon.com/efs/latest/ug/limits.html (no API request rates published)
- Application Auto Scaling and ELB request rates: not found in the fetched docs; see "Other buckets".

## Documented limits

All actions in a category share one token bucket. Bucket size is the burst, refill rate is the sustained rate. Only the buckets Harbormaster uses are listed.

| Operation(s) | Bucket (category) | Bucket size | Refill / s | Source |
|---|---|---|---|---|
| `ListClusters`, `DescribeClusters` | Cluster read actions | 50 | 20 | ECS request-throttling |
| `ListServices`, `DescribeServices` | Service read actions | 100 | 20 | ECS request-throttling |
| `ListTasks`, `DescribeTasks` | Cluster resource read actions | 100 | 20 | ECS request-throttling |
| `DescribeTaskDefinition`, `ListTaskDefinitions` | Task definition read actions | 50 | 20 | ECS request-throttling |

Throttled calls fail with `ThrottlingException` ("Rate exceeded", HTTP 400). `awsx.IsThrottle` matches `ThrottlingException`, `TooManyRequestsException`, `Throttling` and `RequestLimitExceeded` (the EC2 code), and also the SDK's default throttle set, so it covers every code in the docs above.

## Harbormaster steady state per account x region

Estate per loop (spec 5.3): 20 clusters, 300 services (15 per cluster), 900 tasks (45 per cluster). Defaults are the current `internal/config` values.

Batching is per cluster in code (`chunk` runs inside the per-cluster loop), so call counts are per cluster, not per estate: `DescribeServices` takes 2 calls per cluster (15 services, batches of 10) = 40, not the 30 in the spec table; a full `DescribeTasks` refresh takes 1 call per cluster (45 tasks, batches of 100) = 20, not 9.

| Tier (env, default) | Operation | Calls per cycle | Interval | Calls/s | Share of bucket refill (20/s) |
|---|---|---|---|---|---|
| services (`HM_POLL_SERVICES`, 5 s) | `DescribeServices` | 40 | 5 s | 8.00 | 40.0 % |
| serviceList (`HM_POLL_SERVICE_LIST`, 60 s) | `ListServices` | 20 | 60 s | 0.33 | 1.7 % |
| runningTasks (`HM_POLL_RUNNING_TASKS`, 5 s) | `ListTasks` RUNNING | 20 | 5 s | 4.00 | 20.0 % |
| runningTasks, changed tasks (spec typical band) | `DescribeTasks` | 0-5 | 5 s | 0-1.00 | 0-5.0 % |
| runningTasks, dirty services (spec typical band) | `ListTasks` per changed service | 0-5 (assumed) | 5 s | 0-1.00 | 0-5.0 % |
| runningTasks, estate-wide rollout (code worst case) | `DescribeTasks`, one batch per cluster with changes | up to 20 | 5 s | up to 4.00 | up to 20 % (transient) |
| runningTasks, estate-wide rollout (code worst case) | `ListTasks` per dirty service | up to 1 per changed service (300 if every service changes) | 5 s | adds on top | transient |
| fullTasks (`HM_POLL_FULL_TASKS`, 30 s) | `DescribeTasks` | 20 | 30 s | 0.67 | 3.3 % |
| stoppedTasks (`HM_POLL_STOPPED_TASKS`, 15 s) | `ListTasks` STOPPED | 20 | 15 s | 1.33 | 6.7 % |
| stoppedTasks, new stops | `DescribeTasks` | 0-20 (worst case 20) | 15 s | 0-1.33 | 0-6.7 % |
| clusters (`HM_POLL_CLUSTERS`, 60 s) | `ListClusters` + `DescribeClusters` | 2 | 60 s | 0.03 | 0.2 % of the 20/s cluster-read bucket |
| task definitions | `DescribeTaskDefinition` | only for unseen revisions, cached forever | n/a | about 0 | about 0 % |

Per shared bucket (what ECS actually throttles on):

| Bucket | Typical | Worst case | Share of refill |
|---|---|---|---|
| Service read | 8.33/s | 8.33/s | 41.7 % |
| Cluster resource read | 6.00/s | 9.33/s (typical change band) | 30 % typical, 46.7 % at the top of the typical change band |
| Cluster resource read, estate-wide rollout | n/a | at least 11.33/s | at least 56.7 % (transient) |
| Cluster read | 0.03/s | 0.03/s | 0.2 % |

Caveats: busy clusters with more than 100 running or recently stopped tasks need extra `ListTasks` pages and `DescribeTasks` batches, which are not in these counts. The spec's "about 10-11 calls/s" is a total across buckets; the code's total is about 15/s, but spread over two 20/s buckets.

## Verdict

At the target estate (1x), every row and every shared bucket is at or below 50 % of its sustained refill rate. The largest are the Service read bucket (41.7 %) and the Cluster resource read bucket (46.7 % worst case, 30 % typical). No tier interval needs to change.

Rollout worst case: the code issues one `DescribeTasks` batch per cluster with changes plus one `ListTasks` per dirty service (`internal/poller/loop.go`, `syncRunningTasks`). In an estate-wide rollout that is up to 20 `DescribeTasks` per 5 s on top of the base: (20 `ListTasks` + 20 `DescribeTasks`) / 5 s = 8.00/s, plus stoppedTasks (20 + 20) / 15 s = 2.67/s, plus fullTasks 20 / 30 s = 0.67/s, total 11.33/s = 56.7 % of the 20/s refill, before the dirty-service `ListTasks`. Per the controller ruling, spec 5.3's 50 % rule applies to the sustained steady state, which is at or below 50 %. The rollout spike (about 57 % and up) is transient and is absorbed by the 100-token bucket and the throttle back-off. Task 7's soak should assert steady state, not the rollout spike.

Margin warning: the Cluster resource read bucket is close to the line in the worst case. The spec says to design for 2x headroom; if that means 2x clusters/services/tasks inside one account x region, `DescribeServices` alone becomes 80 % of the Service read bucket and the 50 % rule fails. Under 2x per loop, raise `HM_POLL_SERVICES` to 10 s (40 % at 2x). More accounts or regions do not add load to a bucket.

Known edge (ruling): the requirement is "under 50 %" at the 1x target estate, so `HM_POLL_SERVICES` stays at 5 s even though a 2x-estate reading would put `DescribeServices` at 80 % of the Service read bucket. Operators with a larger estate per account x region should raise it.

## Burst behaviour

Before the stagger change (commit 4d0af25), a fresh loop ran clusters and serviceList first (synchronously), then started every other tier at the same moment, with no stagger. First cycle demand per bucket, worst case, 20 clusters:

| Bucket | Calls in the first second | Bucket size | Fits? |
|---|---|---|---|
| Cluster read | 2 | 50 | yes |
| Service read | 20 `ListServices` + 40 `DescribeServices` = 60 | 100 | yes (60 %) |
| Cluster resource read | 20 `ListTasks` RUNNING + 20 `DescribeTasks` (all tasks are new) + 20 `ListTasks` STOPPED + up to 20 `DescribeTasks` (stopped) + up to 20 `DescribeTasks` (full) = 80-100 | 100 | no margin: reaches the whole bucket, and any cluster with more than 100 tasks or extra pages exceeds it |

The 8-in-flight limit does not spread this: at tens of milliseconds per call it still allows well over 100 calls per second. The SDK retries throttled calls with backoff, so a first-cycle overrun is recovered, but it records a throttle and doubles tier intervals, which shows the "slowed by AWS throttling" banner on every start and failover.

Requirement: the poller start-up must stagger the first cycles of the task tiers (runningTasks, stoppedTasks, fullTasks) at least 1 s apart (spread over about 3 s). With 20/s refill, a 1 s gap restores up to 20 tokens between tiers and keeps the bucket above zero. Test: `TestLoop_StartIsStaggeredAcrossTiers`.

## Other buckets

| Service | Operation | Interval | Documented limit | Budget |
|---|---|---|---|---|
| CloudWatch | `GetMetricData` (up to 500 queries per call) | on demand, 60 s Postgres cache | 500 requests/s per account per region (adjustable) | A handful of calls per page view; far under 1 % |
| Application Auto Scaling | `DescribeScalableTargets`, `DescribeScalingPolicies`, `DescribeScheduledActions` (namespace `ecs`), `DescribeScalingActivities` per scalable service | 60 s (`HM_POLL_SCALING`) | not published in the fetched docs | 3 paginated calls plus one per scalable service per minute: at most about 5 calls/s if all 300 services scale; typically under 1/s. Unverified against a documented limit |
| ELB v2 | `DescribeTargetGroups` (batches of 20), `DescribeTargetHealth` per target group | 15 s (`HM_POLL_TARGET_HEALTH`) | not published in the fetched docs | Up to about 300 target groups: 20/15 s plus 300/15 s = about 21 calls/s worst case, if every service has its own group. Unverified; this is the largest non-ECS consumer, review before raising the service count |
| EC2 (plan 05) | `DescribeVpcs`, `DescribeSubnets`, `DescribeNatGateways`, `DescribeInternetGateways`, `DescribeVpcEndpoints`, `DescribeSecurityGroups` | 5 min per account x region (`internal/poller/network.go`) | Non-mutating Describe*: bucket 100, refill 20/s; unfiltered, unpaginated `DescribeSecurityGroups`: bucket 50, refill 10/s | 6 fixed calls plus `ceil(SGs/100)` pages of `DescribeSecurityGroups` per 300 s, about 0.03/s per target; under 1 % of 20/s. `DescribeSecurityGroups` is always filtered by `group-id` (only groups used by services) and paged. EC2 rejects more than 200 values in one filter (`FilterLimitExceeded`), so the `group-id` and `vpc-id` filters are sent in chunks of 100 values, one paginated call series per chunk |
| EFS (plan 05) | `DescribeFileSystems`, `DescribeAccessPoints`, `DescribeMountTargets` | 5 min per account x region (`internal/poller/storage.go`) | request rates not published in the fetched docs | 2 + N_fs calls per 300 s per target (`DescribeMountTargets` is called once per file system); a few calls per 5 min at the expected estate. Unverified against a documented limit |
| CloudWatch (plan 05) | EFS `GetMetricData` (size 30 d, throughput 24 h, connections) | on demand while a Storage page is open; 60 s Postgres cache, 60 s page refetch | 500 requests/s | 3 `GetMetricData` per account x region per minute while a Storage page is open, fetched concurrently (at most 6 targets at once): about 1/s at 20 targets against 500/s; under 1 % |

## Changes made

- Config defaults: none. All `HM_POLL_*` defaults stay as they are.
- Start-up staggering (implemented): `Loop.Run` now delays the first cycle of stoppedTasks by one step and fullTasks by two (runningTasks starts at once), where the step is `poller.Config.StartStagger` (default 1 s, set in `ConfigFrom`; zero disables it). Covered by `TestLoop_StartIsStaggeredAcrossTiers`. `PollOnce`/`runOnce` stay un-staggered. Slow tiers still run once before the service list exists and then wait their full interval.
- Call counts differ from spec 5.3 (spec not edited), because batching is per cluster: `DescribeServices` 40 (was 30), full `DescribeTasks` 20 (was 9).

## Measured

Soak test `internal/poller/soak_test.go` (`make soak`, build tag soak, gated by `HM_SOAK=1`), 2026-11-01. Go 1.26.10, Apple M1 Pro (10 cores), darwin/arm64, Postgres 16 on localhost. Target estate (`fake.Generate(1, fake.TargetScale)`: 20 account x region loops, 400 clusters, 6 000 services, 18 000 tasks), production default intervals through `config.Load` and `poller.ConfigFrom`, real wall-clock time, fake call latency 5 ms, 60 s warm-up then measurement. Another agent ran a Playwright e2e suite on the same machine for part of the session (load average up to about 40), so timings are not from a quiet host; call counts do not depend on load. Figures are from the last full run, after the fixes listed below.

| Measurement (60 s steady-state window) | Result | Limit |
|---|---|---|
| Service read bucket (`ListServices`, `DescribeServices`) per loop | 7.67 - 9.00 calls/s (the top end is a window that caught one extra 5 s cycle) | 10.0/s (50 % of 20/s) |
| Cluster resource read bucket (`ListTasks`, `DescribeTasks`) per loop | 5.67 - 6.33 calls/s | 10.0/s |
| Cluster read bucket per loop | 0.03 calls/s | 10.0/s |
| Task definition read bucket | 0 (definitions are cached) | 10.0/s |
| ECS total per loop | 13.7 - 15.4 calls/s (documented estimate: about 15) | n/a |
| ECS total across 20 loops | 283 calls/s | 800 (20 loops x 4 buckets x 10) |
| Non-ECS calls | application-autoscaling only: 60 calls in 60 s for all 20 loops (0.05/s per loop); target health, network and storage made no calls (empty fake state, 5 min tiers) | n/a |
| Peak in-flight calls per loop, all AWS services | exactly 8 on all 20 loops | 8 |

Snapshot-table writes (`n_tup_ins + n_tup_upd` over `clusters`, `services`, `service_events`, `tasks`, `task_definitions` from `pg_stat_user_tables`, 24 400 rows at this scale):

| Scenario | Result | Limit |
|---|---|---|
| 13 cycles (65 s, so every 60 s tier runs once), no mutations, snapshot tables | 0 | 244 (1 %) |
| Same window, every other table | only `poll_loops` wrote: 1 045 (20 loops x 12 tiers, one row per tier cycle) | `poll_loops` at most 20 x 12 x 14 = 3 360; `service_status_runs` 60; `scaling` and any unlisted table 100; `service_counts_history` 6 100 (5 min tier, outside the window) |
| 10 cycles, 180 changed tasks per cycle (1 800 draws of 18 000 tasks) | `tasks` 1 724 (earlier runs 1 717, 1 729, 1 747) | at most 5 400 (3x), at least 1 620 (90 %: random draws repeat tasks) |
| Same, `service_status_runs` | 1 550 | at most 3 600 (2 per mutated task) |
| Same, `alerts` (replaced wholesale per target each cycle, not budgeted) | about 14 200 | n/a |

Back-off: with a 3 calls/s, burst 3 limit on one loop, its `services` tier interval doubled from 5 s to 10 s after the first throttled cycle; the other 19 loops stayed at 5 s. After lifting the limit, the interval was back at 5 s after about 50 s (five clean cycles at 10 s). Leader election: exactly one of two pollers led and polled; the follower recorded no loop runs; failover after cancelling the leader took 44-73 ms. That uses the test's 100 ms `retryInterval`; production retries every 5 s, so a real failover takes up to about 5 s.

Final soak run (2026-11-01, `make soak` equivalent with `-count=1`, 490 s, all five tests PASS, host load average about 6 to 18): service read bucket 7.67 - 8.33 calls/s per loop, cluster resource read bucket 5.67 - 6.33 calls/s, ECS total 13.37 - 14.70 calls/s per loop and 283 calls/s across 20 loops, peak in-flight 8 on every loop, 60 non-ECS calls (application-autoscaling) in the window. Static estate: 0 snapshot-table writes and 1 044 `poll_loops` writes; with 1 800 task mutations: `tasks` 1 744, `service_status_runs` 1 594. Back-off doubled to 10 s and returned to 5 s after about 50 s; failover took 71 ms.

Findings from the soak that led to fixes (all now in the code):

- Scaling and target-health (and network and storage) AWS calls bypassed the loop's in-flight semaphore, so a loop could briefly exceed `HM_POLL_MAX_INFLIGHT` (measured 9). Each of those tiers now holds one semaphore slot while it runs (`TestLoop_AllAWSCallsShareTheInFlightLimit`).
- `service_status_runs` was upserted for every service on every 5 s alerts cycle (about 60 000 row writes in 62 s on a static estate) because its `polls` counter changed every time. Consecutive-poll counts now live in memory in the alerts loop (only the degraded alert reads them) and a run is written only when a service's status changes (`TestAlertsLoop_UnchangedStatusWritesNoStatusRuns`). After a restart or failover the count starts over, so the degraded alert can appear a few polls later.
- The `scaling` tier rewrote all 6 000 rows every 60 s to refresh `polled_at` (6 000 writes in the 65 s static window, found by the per-table budget; the earlier 50 s window caught only 300). Identical rows are now left untouched (`TestScaling_UnchangedRowsAreNotRewritten`); data freshness comes from `poll_loops`.
