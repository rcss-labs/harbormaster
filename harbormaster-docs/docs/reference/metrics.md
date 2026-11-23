# Metrics reference

Every Prometheus series that Harbormaster exports at `GET /metrics`, with type, labels and meaning. It is for operators who build dashboards and alerts. For how to scrape and what to alert on, see [Health and metrics](../operations/health-and-metrics.md).

## Endpoint

- `GET /metrics` is served by every role (`all`, `web`, `poller`, `coordinator`) in the Prometheus text format (version 0.0.4).
- It is off until `HM_METRICS_TOKEN` is set (404 without it). Send the token as `Authorization: Bearer <token>`. A wrong or missing token returns 401. The Terraform module creates and injects the token when `enable_metrics = true`.
- Metrics are in memory. They reset when the process restarts, so use `rate()` and `increase()`, not raw counter values.
- A poller or coordinator task is not behind the load balancer, so its `/metrics` is reachable only from the security groups you allow.
- A series carries real values only on the role that records it. On another role it is `0` or absent. Role `all` reports every one.

Histograms follow the Prometheus convention: `_bucket{le}`, `_sum` and `_count`.

Poll and focus histograms use buckets of 0.1, 0.25, 0.5, 1, 2.5, 5, 10 and 30 seconds. The KV command histogram uses 0.001, 0.005, 0.025, 0.1 and 0.25 seconds.

## Build and role

| Name | Type | Labels | Meaning | Role |
| --- | --- | --- | --- | --- |
| `harbormaster_build_info` | gauge | `version` | Build information. Always 1. | every role |
| `harbormaster_role_info` | gauge | `role`, `version` | The role this process runs (`all`, `web`, `poller` or `coordinator`). Always 1. | every role |

## AWS calls

| Name | Type | Labels | Meaning | Role |
| --- | --- | --- | --- | --- |
| `harbormaster_aws_api_calls_total` | counter | `account`, `region`, `service`, `operation`, `result`, `source` | AWS API calls made by Harbormaster. `result` is `ok`, `throttled` or `error`. `source` says why: `tier` (a polling tier), `focus` (a live page), `request` (a web request, or anything unlabelled) or `job` (a coordinator job). Use it to measure polling cost and AWS throttling. | every role that calls AWS |

## Polling

| Name | Type | Labels | Meaning | Role |
| --- | --- | --- | --- | --- |
| `harbormaster_poll_loop_duration_seconds` | histogram | `account`, `region`, `tier` | Duration of one poll loop run. | poller |
| `harbormaster_poll_loop_interval_seconds` | gauge | `account`, `region`, `tier` | The configured interval of the loop in seconds. | poller |
| `harbormaster_poll_loop_runs_total` | counter | `account`, `region`, `tier`, `result` | Poll loop runs. `result` is `ok` or `error`. | poller |
| `harbormaster_snapshot_rows_written_total` | counter | `table` | Rows inserted or updated by polling, by table. | poller |
| `harbormaster_poller_idle` | gauge | none | 1 while polling is slowed because nobody has used the UI recently (see `HM_POLL_IDLE_AFTER`). | poller, coordinator |

The `tier` label takes these values: `clusters`, `serviceList`, `services`, `runningTasks`, `stoppedTasks`, `fullTasks`, `target_health`, `scaling`, `alerts`, `network`, `storage`, and `focus` for live page reads.

## Leases and membership

| Name | Type | Labels | Meaning | Role |
| --- | --- | --- | --- | --- |
| `harbormaster_lease_slices_owned` | gauge | none | Account and region slices this process owns. | poller |
| `harbormaster_lease_weight_owned` | gauge | none | Total weight (clusters) of the slices this process owns. | poller |
| `harbormaster_lease_events_total` | counter | `kind`, `event` | Lease events. `kind` is `slice` or `coordinator`. `event` is `acquired`, `released`, `lost` or `takeover` (taken from an owner that stopped renewing). | poller; coordinator for `kind="coordinator"` |
| `harbormaster_lease_renew_failures_total` | counter | `kind` | Renewals that failed for a reason other than losing the lease. | poller, coordinator |
| `harbormaster_fenced_writes_rejected_total` | counter | none | Poller writes refused because the slice lease was lost. A rising value means a task was paused or partitioned. | poller |
| `harbormaster_coordinator_active` | gauge | none | 1 on the task that holds the coordinator lease and runs the background jobs. | coordinator |
| `harbormaster_poller_leader` | gauge | none | 1 on the task that holds the coordinator lease. Kept under its old name for existing alerts. | coordinator |
| `harbormaster_poll_members` | gauge | `role` | Live members by role, from member heartbeats. | poller, coordinator |

### Per-member and per-lease series

These gauges are read from the database at scrape time (with a 2 second bound) and appended to the output of web tasks. Scraping through the load balancer therefore gives an estate-wide view from any web task.

| Name | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `harbormaster_poll_member_slices` | gauge | `instance`, `roles` | Slices each live member owns, from its heartbeat. |
| `harbormaster_poll_member_weight` | gauge | `instance`, `roles` | Weight (clusters) of the slices each live member owns. |
| `harbormaster_poll_member_focus_objects` | gauge | `instance`, `roles` | Focused objects each live member serves. |
| `harbormaster_poll_member_heartbeat_age_seconds` | gauge | `instance`, `roles` | Seconds since each live member's last heartbeat. |
| `harbormaster_lease_epoch` | gauge | `kind`, `account`, `region`, `instance` | Epoch of each lease. `instance` is the holder, empty when the lease is free. |

## Live pages (focus)

| Name | Type | Labels | Meaning | Role |
| --- | --- | --- | --- | --- |
| `harbormaster_focus_admissions_total` | counter | `result` | Focus heartbeats by result: `admitted`, `refreshed`, `capped`, `user_capped`, `unavailable` or `not_found`. | web |
| `harbormaster_focus_objects` | gauge | `kind` | Focus loops running in this process. `kind` is `cluster` or `service`. | poller |
| `harbormaster_focus_interval_seconds` | gauge | `account`, `region` | Effective focus interval of a slice. It stretches under throttling or when many pages are open. | poller |
| `harbormaster_focus_cycle_duration_seconds` | histogram | `kind` | Duration of focus cycles. | poller |
| `harbormaster_pollnow_signals_total` | counter | `result` | Poll-now signals by what the poller did with them: `handled`, `coalesced` or `not_owner`. | poller |
| `harbormaster_pollnow_latency_seconds` | histogram | none | Time from a poll-now signal to the end of the cycle it caused. | poller |

## Snapshot cache

| Name | Type | Labels | Meaning | Role |
| --- | --- | --- | --- | --- |
| `harbormaster_snapshot_cache_requests_total` | counter | `model`, `result` | Cached page reads. `model` is `cluster`, `service` or `slice`. `result` is `hit`, `miss`, `bypass` or `error`. | web |
| `harbormaster_snapshot_cache_token_write_failures_total` | counter | none | Generation token writes that failed. Cached pages stay stale until the retry succeeds. | poller |

## Shared limits

| Name | Type | Labels | Meaning | Role |
| --- | --- | --- | --- | --- |
| `harbormaster_limit_decisions_total` | counter | `limit`, `result`, `backend` | Decisions of the shared limits. `result` is `allowed` or `refused`. `backend` is `kv` (the shared store) or `memory` (the per-task fallback while the store is down). `limit` names the limit, for example `exec`, `tail` and `search` (concurrent sessions) or the rate limiters such as `logsearch`, `notifytest`, `samllogin`, `focusopen`, `apitoken` and `oauthtoken`. | web |

## Shared store (Valkey or Redis)

These three are exported only when `HM_KV_URL` is set.

| Name | Type | Labels | Meaning | Role |
| --- | --- | --- | --- | --- |
| `harbormaster_kv_up` | gauge | none | 1 if the store is reachable. | every role |
| `harbormaster_kv_errors_total` | counter | `op` | Failed store commands, by command. | every role |
| `harbormaster_kv_command_duration_seconds` | histogram | `op` | Store command duration. | every role |

## Notifications and registries

| Name | Type | Labels | Meaning | Role |
| --- | --- | --- | --- | --- |
| `harbormaster_notification_deliveries_total` | counter | `destination`, `result` | Delivery attempts. `destination` is `slack`, `telegram`, `pagerduty`, `webhook` or `email`. `result` is `delivered`, `retried` or `failed`. | coordinator |
| `harbormaster_registry_requests_total` | counter | `host`, `outcome` | Reads of public image registries by the ARM64 platform check. `host` is one of a short allow-list (Docker Hub, public ECR, GHCR). `outcome` is `ok`, `not_found`, `denied`, `rate_limited`, `blocked` or `error`. | coordinator |

## Series that are not exported

The MCP gateway and the scanner export no metrics. The gateway logs one line per request instead; see [AI access](../guides/ai-access-mcp.md).
