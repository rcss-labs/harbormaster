# High availability

Harbormaster can run as one task or as several, with or without a shared Valkey or Redis store. This page explains the supported setups, how leases decide which task does what, how failover works and what degrades when the store is down. It is for platform engineers who size and operate an install.

## Choose a setup

| Setup | Store needed | What you get |
| --- | --- | --- |
| One task (`HM_ROLE=all`, the default) | No | The simplest install. Nothing to configure. `make run` is the same thing |
| Several `all` tasks | Optional | Safe with or without a store. Leases decide who polls what |
| Split services (`web`, `poller`, `coordinator`) | Yes | Each role scales separately. `serve` with a split role refuses to start without `HM_KV_URL` |

The `harbormaster` Terraform module runs one task by default. With `split_services = true` it creates the three services and needs `cache_mode` set to `create` or `external`. See [Split services](../install/split-services.md) and [Terraform module: harbormaster](../reference/terraform-harbormaster.md).

## How leases work

Every account and region pair is a slice. Each slice has a row in the `poll_leases` table, and the poller task that holds the lease polls that slice and nothing else does.

| Setting | Value |
| --- | --- |
| Lease time to live | 30 seconds |
| Renewal | Every 10 seconds |
| Claim and rebalance tick | Every 5 seconds |
| Self-fence | A task that cannot renew for 20 seconds stops the slice itself, before the lease can expire |
| Rebalance | A task hands back at most one slice per 30 seconds |

Leases use the database clock only. A single poller claims every slice. With several pollers, slices are spread by weight (clusters per slice): a new task takes free slices up to its fair share and the others hand slices back until the shares are even.

### Fencing

Every database write a poller makes for a slice runs in a transaction that first checks, with a row lock, that its lease epoch is still current. A task that was paused (a stalled container, a long garbage collection) and comes back after another task took over finds its write rejected and stops that slice. It cannot overwrite the new owner's rows. `harbormaster_fenced_writes_rejected_total` counts the rejections. A rising value means a task was paused or partitioned.

### The coordinator

Notifications, off-hours, image scans, the ARM64 platform check, the deployment outcome log, the daily rollups and retention run only on the task that holds the single coordinator lease. With several coordinator tasks, one is active and the others wait. Each job re-checks the lease right before it sends, launches or changes anything. Duplicate prevention for notifications, enrolments and scans still rests on the database (unique keys and row locks), so a paused task cannot act twice.

`Send test` and a retry run on the web task that serves the request. When a web task changes something a job reads (a saved schedule, a changed route, a scan request), it asks the coordinator to run that job now over the store. Without a store, the job runs at its own interval.

## Failover

| Event | What happens | How long |
| --- | --- | --- |
| A task stops gracefully (deploy, scale-in) | It releases its leases. Another task claims them on its next tick | About 5 seconds |
| A task dies without releasing | Its leases expire and another task claims them | Up to 35 seconds (30 second lease plus one 5 second tick) |
| A task is paused and resumes | Its writes are fenced. It stops the slice | Immediate on the first write |

During the gap after a crash, the stale banner can appear for the affected account and region. See [Throttling and limits](throttling-and-limits.md#stale-banners).

All other state (sessions, snapshots, permissions) is in Postgres, so any web task can serve any request. Terminal and live-log WebSockets are served by whichever web task receives the connection, so the load balancer needs no stickiness.

## Upgrades and rollbacks

Older versions elected a leader with a Postgres advisory lock. A task that polls or coordinates now takes the same key in shared mode on one dedicated connection, and it claims leases only while it holds that key. While an old-version leader is alive no new task claims anything, and while any new task is alive no old task can become leader. A rolling deploy or a rollback never has two versions acting at once.

- `HM_LEGACY_LEADER_GATE` defaults to `true`. Set it to `false` only after no older version runs anywhere.
- If a task loses its gate connection (a database failover, a network cut), it notices at its next 5 second check and acts on the loss within about 8 seconds. In a mixed-version rollout, an old-version task that starts in that window could take the leader key while this task still polls. Fenced writes and the coordinator checks limit what overlapping tasks can do.
- For later releases of a split install, roll the coordinator and poller services first and web last. Migrations are additive, so old and new tasks can overlap, and cache keys carry a schema version.

## The shared store

The store is optional and holds only state that is cheap to lose. Every key expires.

| Held in the store | Notes |
| --- | --- |
| Open pages and when each was last read | The live-page registry |
| Poll-now and kick signals | Let a web task wake a poller or the coordinator |
| Shared limits | Terminal, live-tail and log-search sessions, three rate limits and log error-count pacing |
| Snapshot cache | Generation tokens and cached read models, when `HM_SNAPSHOT_CACHE` is on |

The store never holds sessions, users, permissions, tags, snapshots, alerts, the notification queue or the leases. Those are in Postgres, which stays the truth, and no write waits on the store.

### Configure the store

| Variable | Default | Notes |
| --- | --- | --- |
| `HM_KV_URL` | empty | A `redis://`, `rediss://`, `valkey://` or `valkeys://` URL with optional user, password and database number. It must use database 0. The URL can hold a password and is never logged |
| `HM_KV_PREFIX` | `hm` | Key and channel prefix. Use different prefixes when installs share one server |
| `HM_SNAPSHOT_CACHE` | `true` | Serve cluster, service and list pages from cached read models |
| `HM_SNAPSHOT_CACHE_TTL` | `30s` | Between 1s and 10m. Bounds how long a model can outlive its cluster |

An unreachable store never stops the server from starting. With `cache_mode = "create"` the module builds an ElastiCache replication group with TLS, at-rest encryption and `maxmemory-policy = volatile-lru`. Set `cache_replicas` to 1 or more for Multi-AZ automatic failover. Valkey 7.2 and Redis 7.0 or newer both work, because Harbormaster uses only a command subset both support. The `harbormaster-cache-evictions-*` alarm means the cache is too small: raise `cache_node_type`.

### When the store is down

The client marks the store down after three failed commands or a failed ping (pings run every 2 seconds). It marks it up again after three good pings. Each command is cut off after 250 ms, so a slow store cannot slow a page. The `harbormaster_kv_up` metric reports the state, and the log shows `kv: store is down` and `kv: store is up`.

| What | Behaviour while the store is down |
| --- | --- |
| Polling, alerts, notifications, off-hours, scans, deploys, sign-in | Unchanged. None of them need the store |
| Page reads | Straight from Postgres |
| Live pages (focus, poll-now) | Stop. The page says `Live refresh unavailable`. A focus loop that is already running ends once the poller's registry read has failed for `HM_FOCUS_TTL` |
| Limits and rate limiters | Counted per task in memory, with the usual values |
| Kicks | The job runs at its own interval |

With more than one task, a user can open up to the limit on each task while the store is down.

When the store returns, open pages re-register on their next heartbeat (every 10 seconds) and go live again. Sessions counted in memory during an outage are released from memory when they end, so the count is briefly split between the two backends.

### Split roles without a store

A split role (`web`, `poller`, `coordinator`) exits at start with a usage error that names `HM_KV_URL` when no store is configured. Without one, the web task's open pages would be its own memory, which no poller reads, so a page could never go live. Only `role=all` runs without a store. A store that is unreachable at run time is a different case and behaves as the table above describes.

## Database connections

Each task holds its own database pool. `HM_DB_MAX_CONNS` sets the largest pool. Unset, it is pgx's default for `all`, 6 for `web`, 8 for `poller` and 4 for `coordinator`. A set value must be at least 3. During a rollout each polling or coordinating task also holds one connection for the upgrade gate. At the Terraform module's maximum counts that is about 76 pool connections plus 6 gate connections. The default `db.t4g.micro` allows about 110. Use `db.t4g.small` or larger when you raise `web_max_count` above 6 or `poller_max_count` above 4.

## Check it worked

1. The sum of `harbormaster_lease_slices_owned` over all pollers equals the number of enabled account and region pairs.
2. Exactly one task reports `harbormaster_coordinator_active` = 1.
3. `harbormaster_kv_up` is 1 on every task (when a store is configured).
4. A cluster page shows `Live, every 5 s`.

## Next steps

- [Health and metrics](health-and-metrics.md) for the series to alarm on.
- [Backup and restore](backup-and-restore.md) for the database, which is the one component that holds the truth.
- [Troubleshooting](troubleshooting.md).
