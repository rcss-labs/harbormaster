# ECS API budget

This runbook explains how many AWS API calls Harbormaster makes, how they fit in AWS's per-account and per-region rate limits, and when to change the polling intervals. It is for operators who size an install or investigate throttling. For the banners and settings, see [Throttling and limits](../throttling-and-limits.md).

Limits were taken from AWS documentation on 2026-11-01. Rate budgets are per account and region loop, because ECS throttles per account per region, whichever task makes the call. Splitting Harbormaster into web, poller and coordinator services changes nothing about the buckets.

## Sources

- [ECS request throttling](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/request-throttling.html)
- [ECS service quotas](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/service-quotas.html) (lists no rate limits; points to the throttling page)
- [EC2 throttling](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/throttling.html)
- [CloudWatch quotas](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/cloudwatch_limits.html)
- [EFS quotas](https://docs.aws.amazon.com/efs/latest/ug/limits.html) (no API request rates published)

AWS publishes no request rates for Application Auto Scaling and ELB. The figures for those services below are estimates.

## Documented ECS limits

All actions in a category share one token bucket. The bucket size is the burst. The refill rate is the sustained rate. Only buckets Harbormaster uses are listed.

| Operations | Bucket (category) | Bucket size | Refill per second |
| --- | --- | --- | --- |
| `ListClusters`, `DescribeClusters` | Cluster read actions | 50 | 20 |
| `ListServices`, `DescribeServices` | Service read actions | 100 | 20 |
| `ListTasks`, `DescribeTasks` | Cluster resource read actions | 100 | 20 |
| `DescribeTaskDefinition`, `ListTaskDefinitions` | Task definition read actions | 50 | 20 |

A throttled call fails with `ThrottlingException` (`Rate exceeded`, HTTP 400). Harbormaster treats `ThrottlingException`, `TooManyRequestsException`, `Throttling` and `RequestLimitExceeded` (the EC2 code) as throttling, plus the AWS SDK's own throttle set.

## Current defaults

| Tier | Variable | Default |
| --- | --- | --- |
| Services | `HM_POLL_SERVICES` | 30 s |
| Running tasks | `HM_POLL_RUNNING_TASKS` | 30 s |
| Full tasks | `HM_POLL_FULL_TASKS` | 5 min |
| Stopped tasks | `HM_POLL_STOPPED_TASKS` | 60 s |
| Clusters | `HM_POLL_CLUSTERS` | 60 s |
| Service list | `HM_POLL_SERVICE_LIST` | 60 s |
| Target health | `HM_POLL_TARGET_HEALTH` | 60 s |
| Scaling, network, storage | `HM_POLL_SCALING`, `HM_POLL_NETWORK`, `HM_POLL_STORAGE` | 5 min |
| Counts history | `HM_POLL_COUNTS_HISTORY` | 5 min |
| Idle mode | `HM_POLL_IDLE_AFTER` / `HM_POLL_IDLE_INTERVAL` | After 10 min of no UI activity, 5 min between runs |

At most `HM_POLL_MAX_INFLIGHT` (8) AWS calls run at once per account and region loop. Every tier, including scaling, target health, network and storage, shares that limit.

## How calls are counted

Calls are batched per cluster, so the counts below are per cluster, not per estate. `DescribeServices` takes 10 services per call. A full `DescribeTasks` refresh takes 100 tasks per call.

Reference estate: 12 slices, 300 clusters, 50 clusters in the largest slice, 15 services and 45 tasks per cluster, 9 services per cluster with a target group and 4 to 5 with auto scaling (assumed).

Per cluster per minute at normal pace: `DescribeServices` 4, `ListServices` 1, `ListTasks` RUNNING 2, `ListTasks` STOPPED 1, full `DescribeTasks` 0.2, and changed and stopped `DescribeTasks` 0.2 typical (up to 3 in heavy churn). That is 8.4 ECS calls typical and 11.2 in churn, plus 2 per slice per minute for the clusters tier.

| | Largest slice (50 clusters) | Estate (300 clusters) |
| --- | --- | --- |
| ECS | 422 per minute (7.0 per second) | 2,544 per minute |
| ELB target health | 473 per minute (7.9 per second) | 2,840 per minute |
| Application Auto Scaling | 46 per minute | 277 per minute |
| Network and storage | 2 per minute | 24 per minute |
| Total | about 940 per minute | about 5,700 per minute |

When idle (every tier at least 5 minutes apart), the largest slice makes about 200 calls per minute and the estate about 1,230.

In a soak test of the largest slice with nobody on a page, the service read bucket ran at 4.17 per second, the cluster-resource read bucket at 2.50 per second and the cluster read bucket at 0.03 per second. The ELB figures are estimates and are not verified: the test estate has no target groups.

## Live pages

A page open in a visible tab is read every `HM_FOCUS_INTERVAL` (5 seconds). Each read goes through a limiter per slice, so one page cannot exhaust a bucket.

| Per focused object | Calls per 5 second cycle | Per minute |
| --- | --- | --- |
| Service page | 4, plus 1 every third cycle | about 52 |
| Cluster page (15 services, 45 tasks) | 4 to 5 | about 53 (62 in churn) |

| Focused objects | Added calls per minute | Estate total at normal pace |
| --- | --- | --- |
| 1 | about 52 | about 5,750 |
| 10 | about 520 | about 6,200 |
| 50 (the cap) | about 2,600 | about 8,300 |

### The per-slice limiter

`HM_FOCUS_MAX_CALLS_PER_SEC` (default 5) sets one token bucket per rate category (service read, cluster-resource read, cluster read, ELB), refilling at that rate with a burst of twice it. When more pages are open in a slice than the rate allows, every page's interval in that slice stretches evenly, and each page says so. A throttle from AWS doubles the interval (up to the services interval), and five clean cycles halve it.

Normal pace plus focus, in the largest slice, at most:

| Bucket | Normal pace | Focus ceiling | Worst case | Share of 20 per second |
| --- | --- | --- | --- | --- |
| Service read | 4.2 per second | 5 per second | 9.2 per second | 46 percent |
| Cluster-resource read | 2.8 typical, 5.2 in churn | 5 per second | 7.8, or 10.2 in churn | 39 percent, or 51 percent in churn |
| Cluster read | 0.03 per second | 5 per second | 5 per second | 25 percent |

Soak results in the largest slice (50 clusters, 750 services, one poller):

| Focused pages | Service read: tier plus focus | Cluster-resource read: tier plus focus | Interval reported to the page |
| --- | --- | --- | --- |
| 0 | 4.17 per second | 2.50 per second | n/a |
| 10 | 4.17 + 3.17 = 7.33 per second | 2.50 + 2.33 = 4.83 per second | 5 s |
| 50 | 4.17 + 4.88 = 9.05 per second | 3.33 + 3.67 = 7.00 per second | 16 s |

With 50 pages open in one slice, pages refresh every 16 seconds instead of every 5. That is the limiter working, and each page shows it.

## Back-off

When a tier sees a throttle, its interval doubles, up to 60 seconds. Every fifth clean cycle halves it again toward the configured value. The web app shows `Slowed by AWS throttling` while the interval is backed off. In the soak test a tier that was throttled at 5 seconds returned to its base about 50 seconds after the limit lifted.

Start-up is staggered: the first cycles of the task tiers are spread over about 3 seconds, so a fresh start or a failover does not draw the whole burst bucket at once.

## When to change the intervals

- One account and region with far more than 20 clusters and 300 services: lengthen `HM_POLL_SERVICES` and the task tiers. `DescribeServices` is the largest consumer of the service read bucket. At twice the reference estate it would use about 80 percent of that bucket at the original 5 second interval.
- More accounts or regions add no load to any single bucket.
- Throttling persists after you lengthen intervals: reduce `HM_FOCUS_MAX_CALLS_PER_SEC` or `HM_FOCUS_MAX_OBJECTS`, or check that no other tool in the account calls the same ECS APIs heavily.

## Other buckets

| Service | Operations | Interval | Documented limit | Notes |
| --- | --- | --- | --- | --- |
| CloudWatch | `GetMetricData` (up to 500 queries per call) | On demand, 60 s cache | 500 requests per second per account and region (adjustable) | A handful of calls per page view. Far under 1 percent |
| Application Auto Scaling | `DescribeScalableTargets`, `DescribeScalingPolicies`, `DescribeScheduledActions` (namespace `ecs`), `DescribeScalingActivities` per scalable service | `HM_POLL_SCALING` (5 min) | Not published | Estimate only |
| ELB v2 | `DescribeTargetGroups` (batches of 20), `DescribeTargetHealth` per target group | `HM_POLL_TARGET_HEALTH` (60 s) | Not published | The largest non-ECS consumer when every service has its own target group. Review before you raise the service count. Estimate only |
| EC2 | `DescribeVpcs`, `DescribeSubnets`, `DescribeNatGateways`, `DescribeInternetGateways`, `DescribeVpcEndpoints`, `DescribeSecurityGroups` | `HM_POLL_NETWORK` (5 min) | Non-mutating describes: bucket 100, refill 20 per second. Unfiltered, unpaginated `DescribeSecurityGroups`: 50 and 10 per second | Six fixed calls plus pages of `DescribeSecurityGroups` per cycle. Security group lookups are always filtered and paged. Filters are sent in chunks of 100 values, because EC2 rejects more than 200 values in one filter (`FilterLimitExceeded`) |
| EFS | `DescribeFileSystems`, `DescribeAccessPoints`, `DescribeMountTargets` | `HM_POLL_STORAGE` (5 min) | Not published | A few calls per cycle at the expected estate. `DescribeMountTargets` runs once per file system |
| CloudWatch (EFS) | `GetMetricData` for EFS size, throughput and connections | On demand while a Storage page is open | 500 requests per second | About 3 calls per account and region per minute while a page is open |

## On-demand calls

### Deploy tab

The Deploy tab makes no polling calls. Calls happen when a user opens the tab, opens the image picker or submits a change set.

| Action | Calls |
| --- | --- |
| Open the tab | `DescribeServices` (1), three Application Auto Scaling describes, and `DescribeTargetGroups` when the service has target groups (0 or 1). That is 4 to 5 describes. A client cache keeps a reopen within 30 seconds from making new describes |
| Submit a change set | A pre-write re-read (4 plus G describes, where G is 1 with target groups), the writes (`RegisterTaskDefinition`, `UpdateService`, scaling target, policy and scheduled-action changes, one call each), then a post-write re-read of the same size. A scaling change adds 3 or 4 describes to refresh stored scaling |
| After a submit | The browser re-reads the settings immediately and again at 2, 4, 6, 8 and 10 seconds while the tab is open |
| Image picker | One ECR `DescribeImages` call per 1,000-tag page, up to 10 pages on a cache miss. The tag list is cached for 60 seconds per account, region and repository |

A register plus deploy on a service with no load balancer makes about 10 calls. A typical change set makes 1 to 8 writes. A retry sends only the unfinished steps.

### Service timeline

`GET /timeline` makes no AWS calls. The log-error lane calls CloudWatch Logs `FilterLogEvents`. Hard limits per request: 40 calls, 3 pages per bucket and prefix, and a 10 second deadline. Calls are paced to 2 per second per account and region per server task, which is 40 percent of the lowest regional quota of 5 per second. Identical requests share one scan. `FilterLogEvents` allows 25 requests per second in `us-east-1`, 5 per second in `eu-central-1` and 10 per second in most other regions, and the quota is not adjustable. A cold load can come back `partial` and fills in on the 60 second refetch. Nothing polls in the background.

### Image scanning

Scanning adds no polling tier. Everything is driven by a request and counted per scan cycle (`HM_SCAN_INTERVAL`, default 15 seconds) on the active coordinator.

| Where | Operation | When |
| --- | --- | --- |
| Harbormaster account, ECS | `RunTask` for the scanner, at most `HM_SCAN_MAX_TASKS` (1 to 4, default 1) per cycle | Only when a requested scan is waiting and fewer than the maximum run |
| Harbormaster account, ECS | `DescribeTasks`, one call per active run per cycle | Only while a run is active |
| Harbormaster account, ECS | `StopTask`, one per run | Only on `HM_SCAN_TASK_TIMEOUT` |
| Connected account, ECR | `DescribeImages`, one per scan request or per changed container the gate checks | User-driven |
| Scanner task | `GetAuthorizationToken`, `DescribeImages`, `BatchGetImage` and `GetDownloadUrlForLayer` per layer, plus S3 and one `AssumeRole` per account in the batch | Inside the scanner task only |
| `grype.anchore.io` (not AWS) | HTTPS `GET` of `latest.json` and the archive | At most one refresh attempt per hour, only when the data is older than `HM_SCAN_DB_REFRESH` |

The scanner's `DescribeTasks` is at most one call per 15 seconds while a run is active. That is under 0.1 percent of the cluster-resource read refill.

## CloudTrail

A focused object adds about 75,000 read events a day if its page stays visible all day. That is about $1.50 a day per always-open page on a trail that bills management events ($2 per 100,000). Record write-only management events on any trail you pay for. The `source` label on `harbormaster_aws_api_calls_total` (`tier`, `focus`, `request`, `job`) shows what each call was for. See [AWS costs](../aws-costs.md).

## What is not verified

ELB and Application Auto Scaling publish no request rates, and the soak test's fake estate has no target groups, so the ELB figures are estimates. The soak test does not run against a real AWS account, and its latency is a flat 25 ms per call.

## Related

- [Throttling and limits](../throttling-and-limits.md)
- [Health and metrics](../health-and-metrics.md)
- [AWS costs](../aws-costs.md)
