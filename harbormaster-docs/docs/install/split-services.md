# Split services

This page explains how to run Harbormaster as three ECS services (web, poller, coordinator) with a Valkey or Redis store, and when that is worth doing. It is for engineers who run Harbormaster at larger scale or need more availability.

## When to use it

The default install is one ECS service running every part (`role=all`). It needs no store and no extra roles. Keep it unless you need one of these:

- Polling capacity, API capacity and background jobs that scale separately.
- More than one task of each part, for availability, with each part sized on its own.

An install that sets none of the split variables is unchanged by this feature. Several `all` tasks without a store are also supported, but split services give each part its own scaling and security group. See [High availability](../operations/high-availability.md).

## What it needs

- A Valkey or Redis store. The `web`, `poller` and `coordinator` roles refuse to start without `HM_KV_URL`, because focus heartbeats and kicks must reach the task that acts on them. Only `role=all` runs without a store.
- The `harbormaster-access` module re-applied in each managed account, with the three new task role ARNs (see below).
- Database capacity: each task holds its own connection pool. At the default task counts the default `db.t4g.micro` is enough. Set `db_instance_class = "db.t4g.small"` before you raise `web_max_count` or `poller_max_count`.

## The three services

| Service | Role | Default count (desired, min, max) | Behind the ALB | Autoscaling |
| --- | --- | --- | --- | --- |
| `harbormaster-web` | `web` | 2, 2, 6 | Yes, the only target | Requests per target and average CPU at 60 percent |
| `harbormaster-poller` | `poller` | 2, 2, 4 | No | Average CPU at 60 percent |
| `harbormaster-coordinator` | `coordinator` | 2 (fixed) | No | None. One task is active, the other waits |

Poller and coordinator tasks serve only `/healthz` and `/metrics`. Their security groups take no ingress, except the container port from `metrics_ingress_security_group_ids`.

Each service has its own task role, execution role and security group. These are created even when `split_services` is `false`, because a managed account can trust a role only once it exists.

## Module variables

| Variable | Default | Description |
| --- | --- | --- |
| `split_services` | `false` | Create the web, poller and coordinator services. Needs a cache. |
| `keep_legacy_service` | `true` | Keep the single `harbormaster-server` service. `false` needs `split_services = true`. |
| `cache_mode` | `none` | `create` builds ElastiCache. `external` uses the URL in `cache_url_secret_arn`. `none` uses no store. |
| `cache_engine` | `valkey` | `valkey` or `redis`. Used when `cache_mode = "create"`. |
| `cache_node_type` | `cache.t4g.micro` | Cache node size. |
| `cache_replicas` | `0` | 0 to 5. One or more adds replicas with Multi-AZ automatic failover. |
| `cache_url_secret_arn` | `null` | Required with `cache_mode = "external"`: a secret holding the full store URL. |
| `web_*_count`, `poller_*_count`, `coordinator_desired_count` | See above | Task counts. |
| `web_cpu`, `web_memory`, `poller_cpu`, `poller_memory`, `coordinator_cpu`, `coordinator_memory` | 512/1024, 512/1024, 256/512 | Task sizes. Each must be a valid Fargate pair. |
| `web_requests_per_target` | `1000` | Autoscaling target for requests per target per minute. |

See the [Terraform reference](../reference/terraform-harbormaster.md) for the full list.

## The store

With `cache_mode = "create"` the module builds an ElastiCache replication group named `harbormaster` (cluster mode off, TLS and at-rest encryption, an auth token kept in Secrets Manager as `harbormaster/kv-url`). The store holds only disposable state. If it is down, polling, sign-in and deploys carry on, page reads go to Postgres, and live refresh pauses. See [Architecture](../getting-started/architecture.md).

With `cache_mode = "external"`, the secret in `cache_url_secret_arn` holds a full `redis://`, `rediss://`, `valkey://` or `valkeys://` URL with the password. The module creates nothing for it, and all services must be able to reach it.

An alarm per cache node (`harbormaster-cache-evictions-...`) fires when evictions stay above 0 for 15 minutes. That means the store is too small: raise `cache_node_type`. Set `cache_alarm_actions` to be notified.

## Move from a single service to split services

Move in order. Each numbered run is one apply. Every change is backward compatible: the single service keeps working until run 4 removes it. Rolling back is the reverse run.

1. **Main module, new image, no variable changes.** Only `image` moves to a release that supports `--role`. The plan adds the six new roles, three new security groups and moves some existing resources. It must not replace or destroy the ECS service, load balancer, database, secrets, or any existing role or security group. Copy the new `web_task_role_arn`, `poller_task_role_arn` and `coordinator_task_role_arn` outputs.
2. **Access module, once per managed account.** Add the three role ARNs. Keep `harbormaster_task_role_arn`, `external_id` and `role_name` as they are. The plan creates `<role_name>-poller` and `<role_name>-coordinator` and changes the existing role's trust policy in place. This must come after run 1.
3. **Main module: switch on split services next to the single one.** Set `split_services = true`, `cache_mode = "create"` (or `external`), `cache_engine`, and keep `keep_legacy_service = true`. Consider `db_instance_class = "db.t4g.small"`. After the apply, a second plan must be empty. Check that lease slices are owned, the store is up on every task, and the target group shows healthy web targets.
4. **Main module: remove the single service.** Set `keep_legacy_service = false`. The plan only destroys the single service, its task definition and roles. The pollers and coordinator claim its leases within one tick.
5. **Access module, once per managed account.** Set `harbormaster_task_role_arn = null`, because the legacy role no longer exists and a trust policy cannot keep naming a deleted role.

The module README has the detailed expected plan and checks for each run: `terraform/modules/harbormaster/README.md` in the repository, section "Upgrade: single service to split services".

### Roll back

- To bring the single service back, set `keep_legacy_service = true`. To remove the split services, set `split_services = false`.
- After undoing run 4, repeat run 2 in every managed account straight away. The re-created legacy role has the same name but a new unique ID, and IAM trust policies match the ID. Until you do, only the legacy service is locked out of the access roles.

### Later releases

Roll the coordinator and poller services first and the web service last. Migrations are additive, so old and new tasks can overlap.

## Check it worked

- The sum of `harbormaster_lease_slices_owned` over the pollers equals the number of enabled account and region pairs.
- Exactly one coordinator task reports `harbormaster_coordinator_active` = 1.
- `harbormaster_kv_up` is 1 on every task.
- The target group has only healthy web targets, and a cluster page shows live refresh.

See [Health and metrics](../operations/health-and-metrics.md).

## Next steps

- [High availability](../operations/high-availability.md)
- [Connect AWS accounts](connect-aws-accounts.md)
- [Upgrading](upgrading.md)
