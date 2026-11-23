# Throttling and limits

Harbormaster reads AWS constantly, and AWS throttles API calls per account and region. This page explains the throttling and stale banners in the web app, how Harbormaster backs off, how to tune the polling intervals and which limits protect your estate and your AWS bill. It is for operators of a Harbormaster install.

For the full list of settings, see [Configuration](../reference/configuration.md). For the call-rate maths behind the defaults, see the [ECS API budget runbook](runbooks/ecs-api-budget.md).

## Banners

The web app shows two banners per account and region.

| Banner | Meaning | What Harbormaster does |
| --- | --- | --- |
| **Slowed by AWS throttling** | AWS is throttling Harbormaster's calls for that account and region, so updates arrive more slowly | Doubles the poll interval of the affected tier (up to 60 seconds), and halves it again after five clean cycles |
| **Data is N min old** | The last poll for that account and region failed. The banner shows the age of the data and the last error | Keeps serving the last good data and keeps retrying. `Data is unavailable` means no poll has succeeded yet |

Per-tier freshness also appears in the header of a page as, for example, `Auto scaling is 4 min old`, `Auto scaling is unavailable` or `Auto scaling updates are slowed by AWS throttling`.

### Stale banners

A stale banner means a poll failed. Common causes:

- Harbormaster cannot assume the access role in that account. See [AssumeRole failures](troubleshooting.md#assumerole-failures).
- A split poller or coordinator cannot assume its suffixed role (`<role_name>-poller`, `<role_name>-coordinator`) in that account. Apply the access module there.
- The database is unreachable, so the poller cannot write.
- A poller crashed and its slices have not moved yet. Takeover takes up to 35 seconds. See [High availability](high-availability.md#failover).

The AWS accounts page shows each slice's last poll error. Start there.

### Live-page pills

A cluster or service page open in a visible tab is read every `HM_FOCUS_INTERVAL` (5 seconds). The header pill tells you what is happening:

| Pill | Meaning |
| --- | --- |
| **Live, every 5 s** | The page is read at the focus interval |
| **Refreshing every N s** | More pages are open in the account and region than `HM_FOCUS_MAX_CALLS_PER_SEC` allows, so every page's interval stretches evenly |
| **Slowed by AWS throttling** | AWS throttled the slice. The focus interval backs off until it recovers |
| **Normal pace (live limit reached)** | The install or the user reached `HM_FOCUS_MAX_OBJECTS` or `HM_FOCUS_MAX_PER_USER`. The page still gets its immediate read when it opens |
| **Live refresh unavailable** | Nothing proves that a poller reads the page, for example the shared store is down or no poller holds the slice. Normal polling continues |

## How AWS throttles ECS

ECS throttles per account and region, in token buckets shared by every call of a kind. Which task makes the call does not matter.

| Calls | Bucket size | Refill per second |
| --- | --- | --- |
| Cluster reads (`ListClusters`, `DescribeClusters`) | 50 | 20 |
| Service reads (`ListServices`, `DescribeServices`) | 100 | 20 |
| Cluster-resource reads (`ListTasks`, `DescribeTasks`) | 100 | 20 |
| Task-definition reads | 50 | 20 |

At the reference estate of 20 clusters and 300 services in one account and region, the defaults use a fraction of each bucket, and a rollout spike stays inside the burst. A much larger estate in one account and region should lengthen the `HM_POLL_*` intervals. More accounts or regions add no load to a bucket.

Harbormaster treats `ThrottlingException`, `TooManyRequestsException`, `Throttling`, `RequestLimitExceeded` and the AWS SDK's own throttle set as throttling.

## Polling intervals

| Variable | Default | Polls |
| --- | --- | --- |
| `HM_POLL_SERVICES` | `30s` | Services |
| `HM_POLL_RUNNING_TASKS` | `30s` | Running tasks |
| `HM_POLL_FULL_TASKS` | `5m` | Full task refresh |
| `HM_POLL_STOPPED_TASKS` | `60s` | Stopped tasks |
| `HM_POLL_CLUSTERS` | `60s` | Clusters |
| `HM_POLL_SERVICE_LIST` | `60s` | Service list |
| `HM_POLL_TARGET_HEALTH` | `60s` | Load balancer target health |
| `HM_POLL_SCALING` | `5m` | Application Auto Scaling |
| `HM_POLL_NETWORK` | `5m` | VPCs, subnets, gateways, endpoints, security groups |
| `HM_POLL_STORAGE` | `5m` | EFS |
| `HM_POLL_COUNTS_HISTORY` | `5m` | Task-count history sampling |
| `HM_POLL_JITTER` | `500ms` | Random jitter added to schedules |
| `HM_POLL_MAX_INFLIGHT` | `8` | Concurrent AWS calls per slice loop |
| `HM_POLL_IDLE_AFTER` | `10m` | No UI activity for this long puts the poller in idle mode. `0` disables idle mode |
| `HM_POLL_IDLE_INTERVAL` | `5m` | While idle, every tier waits at least this long between runs. Minimum 1s when idle mode is on |

Set these through the module's `extra_env`. To reduce load, raise the intervals or lower `HM_POLL_IDLE_AFTER`. Only a visible browser tab counts as activity, but a dashboard left open on a screen keeps Harbormaster at full speed. A busy script or AI agent that calls the API at least every `HM_POLL_IDLE_AFTER` also holds polling at full pace.

## Live-page limits

| Variable | Default | Meaning |
| --- | --- | --- |
| `HM_FOCUS_INTERVAL` | `5s` | How often a visible page is read from AWS. Minimum 2s |
| `HM_FOCUS_TTL` | `30s` | How long an open page stays registered without a heartbeat. Must be longer than 12s |
| `HM_FOCUS_MAX_OBJECTS` | `50` | Most clusters and services read at the focus interval across the install |
| `HM_FOCUS_MAX_PER_USER` | `5` | Most pages one user can have read at the focus interval |
| `HM_FOCUS_MAX_CALLS_PER_SEC` | `5` | AWS calls a slice's focus reads may make per second in each rate category (burst is twice that) |

## Limits

These limits protect AWS quotas, your bill and the database.

| Variable | Default | Meaning |
| --- | --- | --- |
| `HM_PAGE_SIZE_DEFAULT` | `100` | Default API page size |
| `HM_PAGE_SIZE_MAX` | `500` | Maximum API page size |
| `HM_MAX_DESIRED_COUNT` | `50` | Highest desired count Harbormaster will set on a service. Must be at least 1 |
| `HM_METRICS_CACHE_TTL` | `60s` | How long metric query results are reused |
| `HM_LOG_TAIL_IDLE` | `30m` | Close a live log tail after this long without activity |
| `HM_LOG_TAILS_PER_USER` | `2` | Concurrent live log tails per user |
| `HM_LOG_HISTORY_LIMIT` | `500` | Most log events returned by one history query |
| `HM_LOG_SEARCH_TIMEOUT` | `60s` | How long one Logs Insights search may run |
| `HM_LOG_SEARCH_MAX_SERVICES` | `20` | Most services one log search may cover |
| `HM_LOG_SEARCH_MAX_RANGE` | `168h` | Longest time range of one log search |
| `HM_LOG_SEARCH_MAX_GB` | `10` | Most GiB one search may scan before it stops and returns the partial result. `0` removes the cap |
| `HM_EXEC_IDLE_TIMEOUT` | `20m` | Close a terminal session after this long without keystrokes or resizes. Output does not count |
| `HM_EXEC_MAX_DURATION` | `4h` | Close a terminal session this long after it opened |
| `HM_EXEC_MAX_SESSIONS_PER_USER` | `3` | Concurrent terminal sessions per user |

Each user can run one log search at a time. The per-user session limits (terminals, live tails, log searches) are counted across all tasks when `HM_KV_URL` is set, and per task otherwise. While the store is down they fall back to per-task counts. `harbormaster_limit_decisions_total{limit,result,backend}` shows each decision, with `limit` set to `exec`, `tail` or `search`, `result` to `allowed` or `refused`, and `backend` to `kv` or `memory`.

### Rate limits

| Limit | Value |
| --- | --- |
| Sign-in attempts per user | `HM_LOGIN_MAX_PER_USER`, default 5 per `HM_LOGIN_WINDOW` (default `15m`) |
| Sign-in attempts per client IP | `HM_LOGIN_MAX_PER_IP`, default 30 per `HM_LOGIN_WINDOW` |
| Requests authenticated by an access token | A burst of 120, then 2 per second per user. The answer is HTTP 429 with `Retry-After`. These values are fixed |
| Log search by access token | A per-search scan cap and a daily scan budget per user; see [Access tokens](../guides/access-tokens.md) |

A store problem never blocks sign-in or a search. If the limiter fails, the request is allowed.

## Next steps

- [AWS costs](aws-costs.md) for what polling costs outside the Harbormaster account.
- [ECS API budget runbook](runbooks/ecs-api-budget.md) for call counts and measured rates.
- [Health and metrics](health-and-metrics.md).
