# Health and metrics

Harbormaster exposes a liveness endpoint and a Prometheus metrics endpoint on every role. This page explains what each one checks, how to scrape the metrics and which conditions to alarm on. It is for the people who run the install. The complete series list is in [Metrics reference](../reference/metrics.md).

## Health check

`GET /healthz` is the only health endpoint. There is no separate readiness endpoint.

| Role | What `/healthz` checks | Failure response |
| --- | --- | --- |
| `all`, `web` | A database ping | `503` with body `unavailable` |
| `poller` | A database ping, and that the lease manager finished a tick in the last 60 seconds | `503` with body `unavailable` |
| `coordinator` | A database ping | `503` with body `unavailable` |

A healthy task answers `200` with body `ok`. The check does not need a session or a token, and it does not call the shared store (Valkey or Redis) or AWS. A store outage or an AWS outage therefore never fails the health check.

Where the check runs:

- The Application Load Balancer target group calls `/healthz` every 15 seconds and expects `200`. Two good checks mark a target healthy and three bad checks mark it unhealthy.
- The container image runs `harbormaster healthcheck`, which calls `http://127.0.0.1:<port>/healthz` (the port comes from `HM_LISTEN`, default `:8480`) with a 2 second timeout. It exits non-zero unless the answer is `200`.
- The MCP gateway has its own `/healthz`. It makes no call to Harbormaster.

A poller or coordinator task is not behind the load balancer, so ECS relies on the container health check to replace it. A poller whose lease manager has stopped finishing ticks fails the check and ECS replaces it.

## Metrics endpoint

`GET /metrics` serves Prometheus text format. It is off until `HM_METRICS_TOKEN` is set. With no token set the endpoint answers `404`. The Terraform module creates the token and injects it when `enable_metrics = true` (the default).

Send the token as a bearer token:

```sh
curl -H "Authorization: Bearer $HARBORMASTER_METRICS_TOKEN" https://harbormaster.example.com/metrics
```

A missing or wrong token returns `401` with `WWW-Authenticate: Bearer realm="metrics"`. Read the token from the Secrets Manager secret `harbormaster/metrics-token`. Never paste it into chat or a ticket.

Every role serves `/metrics`, but each series has real values only on the role that produces it. On another role it is `0` or absent. The `all` role reports everything. Reachability depends on the role:

| Role | How to scrape |
| --- | --- |
| `all`, `web` | Through the load balancer, or directly on the task port |
| `poller`, `coordinator` | Directly on the task port. These tasks are not behind the load balancer. Their security groups admit the port only from `metrics_ingress_security_group_ids` |

A web task also reports per-member and per-lease series for every live task (`harbormaster_poll_member_*` and `harbormaster_lease_epoch`), read from the database at scrape time. Scraping any one web task is enough to see them. These series are cached for 5 seconds per task.

## What to alarm on

These are starting points. Tune the durations to your own scrape interval.

| Alarm | Condition | What it means | First step |
| --- | --- | --- | --- |
| Task unhealthy | The load balancer reports an unhealthy target, or ECS keeps replacing a task | The database is unreachable, or a poller stopped ticking | Check database connectivity and the task log. See [Troubleshooting](troubleshooting.md) |
| No slice owner | The sum of `harbormaster_lease_slices_owned` over all pollers is lower than your account-and-region count for more than a minute | A slice has no poller. Its data goes stale | Check that poller tasks run and can reach the database. See [High availability](high-availability.md) |
| No active coordinator | `sum(harbormaster_coordinator_active)` is 0 for more than a minute | Notifications, off-hours, scans, rollups and retention are not running | Check coordinator tasks |
| More than one coordinator | `sum(harbormaster_coordinator_active)` is above 1 | Should not happen. Treat it as an incident | Check for a mixed-version rollout and see the lease series |
| Fenced writes | `rate(harbormaster_fenced_writes_rejected_total[5m]) > 0` | A task was paused or partitioned and lost its lease | Look for CPU starvation, long pauses or a network cut on that task |
| Lease renewals failing | `rate(harbormaster_lease_renew_failures_total[5m]) > 0` | A task cannot reach the database to renew | Check database load and connections |
| Store down | `harbormaster_kv_up == 0` | The shared store is unreachable. Harbormaster falls back to Postgres and per-task limits | Check the cache. See [High availability](high-availability.md#when-the-store-is-down) |
| Store errors | `rate(harbormaster_kv_errors_total[5m])` rising, or `harbormaster_kv_command_duration_seconds` slow | The store is degraded | Check the cache node |
| Cache evictions | The `harbormaster-cache-evictions-*` CloudWatch alarm | The created cache is too small | Raise `cache_node_type` |
| AWS throttling | Poll loop intervals grow, or `harbormaster_aws_api_calls_total{result="throttled"}` rises for an account and region | AWS is throttling Harbormaster's calls | See [Throttling and limits](throttling-and-limits.md) |
| Poll loop failing | `harbormaster_poll_loop_runs_total{result="error"}` rising for an account and region | Polls fail. The UI shows the stale banner | Open the AWS accounts page for the last error. See [Troubleshooting](troubleshooting.md) |
| Notification delivery failing | `harbormaster_notification_deliveries_total{result="failed"}` rising | Alert deliveries are not arriving | Check the delivery log in the web app |

`result` on `harbormaster_aws_api_calls_total` is `ok`, `throttled` or `error`. On `harbormaster_notification_deliveries_total` it is `delivered`, `failed` or `retried`.

## Useful series

| Question | Series |
| --- | --- |
| Which version runs where? | `harbormaster_build_info`, `harbormaster_role_info` |
| How many tasks of each role are alive? | `harbormaster_poll_members{role}` |
| Which task owns which slice? | `harbormaster_lease_epoch{kind,account,region,instance}`, `harbormaster_poll_member_slices` |
| Is the poller idle (nobody using the UI)? | `harbormaster_poller_idle` (1 while idle) |
| What is Harbormaster calling in AWS, and why? | `harbormaster_aws_api_calls_total`, with the `source` label (`tier`, `focus`, `request`, `job`) |
| Is the snapshot cache working? | `harbormaster_snapshot_cache_requests_total{model,result}` (`hit`, `miss`, `bypass`, `error`) |
| Are shared limits using the store or memory? | `harbormaster_limit_decisions_total{limit,result,backend}` |

## Next steps

- [Metrics reference](../reference/metrics.md) for every series and label.
- [High availability](high-availability.md) for lease and store behaviour.
- [Troubleshooting](troubleshooting.md) for symptoms and fixes.
