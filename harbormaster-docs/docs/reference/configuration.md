# Configuration reference

Every environment variable that the four Harbormaster binaries read, with defaults and valid ranges. Use it when you run a binary directly or add extra environment to the Terraform module; the module already sets the variables it needs.

## How settings are read

- All settings are environment variables. There are no config files.
- Unset and empty variables take the default shown.
- Durations use Go syntax: `500ms`, `30s`, `5m`, `12h`. Booleans accept the Go forms (`true`, `false`, `1`, `0`, `t`, `f`).
- A malformed or out-of-range value stops startup with an error that names the variable. The first error is reported.
- Secret values (database URL, session key, SMTP password, KV URL) are never written to logs or error messages.

The **Read by** column uses these names:

| Name | Binary | Role |
| --- | --- | --- |
| `harbormaster` | `harbormaster` | The server: `serve`, `migrate`, `create-admin`, `revoke-tokens` and `healthcheck` all load the same settings (see [CLI reference](cli.md)) |
| `mcp` | `harbormaster-mcp` | The MCP gateway |
| `scanner` | `harbormaster-scanner` | The image scan task |
| `agent` | `harbormaster-agent` | The AI agent service |

Roles (`all`, `web`, `poller`, `coordinator`) are explained in [Architecture](../getting-started/architecture.md). A variable marked `harbormaster` is read by every role unless the description says which role uses it.

## Server

| Variable | Default | Read by | Description |
| --- | --- | --- | --- |
| `HM_LISTEN` | `:8480` | `harbormaster` | Address the HTTP server listens on. `harbormaster healthcheck` uses its port. |
| `HM_ROLE` | `all` | `harbormaster` | Which parts this process runs: `all`, `web`, `poller` or `coordinator`. The `serve --role` flag overrides it. Any other value stops startup. |
| `HM_DB_MAX_CONNS` | unset (`0`) | `harbormaster` | Largest database pool. Unset uses pgx's default for `all`, 6 for `web`, 8 for `poller` and 4 for `coordinator`. A set value must be at least 3. |
| `HM_BASE_URL` | `http://127.0.0.1:8480` | `harbormaster` | Public URL of the app. Used in notification links, SSO URLs and the WebSocket origin check. |
| `HM_DATABASE_URL` | none | `harbormaster` | Postgres URL. Either this or `HM_DATABASE_SECRET_ARN` is required. |
| `HM_DATABASE_SECRET_ARN` | none | `harbormaster` | Secrets Manager secret with the database credentials: a `postgres://` URL, or JSON with `username`, `password`, `host` and optionally `port` (default `5432`), `dbname` (default `postgres`) and `sslmode` (default `require`). Secrets without a `host` (RDS-managed master secrets) are rejected. Used only when `HM_DATABASE_URL` is empty. |
| `HM_SESSION_KEY` | none | `harbormaster` | Secret mixed into session id hashes. It also encrypts the SSO signing key and notification route secrets. Set a long random value in production. Required before single sign-on can be configured. Changing it makes stored notification route secrets unreadable. |
| `HM_BOOTSTRAP_ADMIN_PASSWORD_SECRET_ARN` | none | `harbormaster` | Secrets Manager secret holding the initial `admin` password. Read at start by roles that serve the API, outside fake mode. |
| `HM_METRICS_TOKEN` | none | `harbormaster` | Bearer token for `GET /metrics`. Empty disables the endpoint (404). Whitespace is trimmed. |
| `HM_COOKIE_SECURE` | `true` | `harbormaster` | Sets the `Secure` flag on cookies. Use `false` only for plain-HTTP local runs. |
| `HM_FAKE_AWS` | `false` | `harbormaster` | Same as `serve --fake-aws`: serve an in-memory AWS estate with demo data. Never set in production. Also turns image scanning on. |
| `HM_TRUSTED_PROXY` | `false` | `harbormaster` | Trust the rightmost `X-Forwarded-For` entry as the client IP. Enable only behind a proxy that appends the real client address. |
| `HM_LOG_LEVEL` | `info` | `harbormaster` | Log level (`debug`, `info`, `warn`, `error`). An unrecognised value falls back to `info`. Logs are JSON on standard output. |
| `HM_SESSION_IDLE` | `12h` | `harbormaster` | Idle session timeout. |
| `HM_SESSION_ABSOLUTE` | `168h` | `harbormaster` | Maximum session lifetime. |
| `HM_LOGIN_MAX_PER_USER` | `5` | `harbormaster` | Login attempts allowed per user in the window. |
| `HM_LOGIN_MAX_PER_IP` | `30` | `harbormaster` | Login attempts allowed per client IP in the window. |
| `HM_LOGIN_WINDOW` | `15m` | `harbormaster` | Login rate-limit window. |
| `HM_LEGACY_LEADER_GATE` | `true` | `harbormaster` | Makes a task that polls or coordinates take the old Postgres advisory lock in shared mode before claiming leases, so old and new versions never both act during an upgrade or rollback. Set `false` only once no older version runs. |

## Shared store (Valkey or Redis)

The store is optional with `role=all`. It is required for the `web`, `poller` and `coordinator` roles: `serve` refuses to start without `HM_KV_URL`.

| Variable | Default | Read by | Description |
| --- | --- | --- | --- |
| `HM_KV_URL` | none | `harbormaster` | Store URL with scheme `redis`, `rediss`, `valkey` or `valkeys`, and optional user, password and database number. Empty means no store. Only database `0` is accepted. An unreachable store never stops the server from starting. |
| `HM_KV_PREFIX` | `hm` | `harbormaster` | Key and channel prefix: 1 to 32 lowercase letters, digits, `-` or `_`. Installs sharing one server need different prefixes. |
| `HM_SNAPSHOT_CACHE` | `true` | `harbormaster` | With a store, serve the cluster, service and list pages from versioned read models in the store instead of Postgres. `false` reads Postgres for every request. No effect without a store. |
| `HM_SNAPSHOT_CACHE_TTL` | `30s` | `harbormaster` | How long a cached read model lives. Between `1s` and `10m`. |

## Polling intervals

Defaults suit a UI someone is watching. Larger estates in one account and region should lengthen them. See [Throttling and limits](../operations/throttling-and-limits.md).

| Variable | Default | Read by | Description |
| --- | --- | --- | --- |
| `HM_POLL_SERVICES` | `30s` | `harbormaster` | Service poll interval. |
| `HM_POLL_RUNNING_TASKS` | `30s` | `harbormaster` | Running-task poll interval. |
| `HM_POLL_FULL_TASKS` | `5m` | `harbormaster` | Full task poll interval. |
| `HM_POLL_STOPPED_TASKS` | `60s` | `harbormaster` | Stopped-task poll interval. |
| `HM_POLL_CLUSTERS` | `60s` | `harbormaster` | Cluster poll interval. |
| `HM_POLL_SERVICE_LIST` | `60s` | `harbormaster` | Service list poll interval. |
| `HM_POLL_COUNTS_HISTORY` | `5m` | `harbormaster` | Task-count history sampling interval. |
| `HM_POLL_TARGET_HEALTH` | `60s` | `harbormaster` | Load balancer target health poll interval. |
| `HM_POLL_SCALING` | `5m` | `harbormaster` | Application Auto Scaling poll interval. |
| `HM_POLL_NETWORK` | `5m` | `harbormaster` | Networking poll interval (VPCs, subnets, gateways, endpoints, security groups). |
| `HM_POLL_STORAGE` | `5m` | `harbormaster` | EFS storage poll interval. |
| `HM_POLL_JITTER` | `500ms` | `harbormaster` | Random jitter added to poll schedules. |
| `HM_POLL_MAX_INFLIGHT` | `8` | `harbormaster` | Maximum concurrent AWS calls. |
| `HM_POLL_IDLE_AFTER` | `10m` | `harbormaster` | With no UI activity (no unexpired session seen and no access token used) for this long, polling goes idle. `0` disables idle mode. Negative values are rejected. |
| `HM_POLL_IDLE_INTERVAL` | `5m` | `harbormaster` | While idle, every poll tier waits at least this long between runs. At least `1s` when idle mode is on. |

### Live page refresh

| Variable | Default | Read by | Description |
| --- | --- | --- | --- |
| `HM_FOCUS_INTERVAL` | `5s` | `harbormaster` | How often a page open in a visible tab is read from AWS. Minimum `2s`. |
| `HM_FOCUS_TTL` | `30s` | `harbormaster` | How long an open page stays registered without a heartbeat. Must be longer than `12s`. |
| `HM_FOCUS_MAX_OBJECTS` | `50` | `harbormaster` | Most clusters and services read at the focus interval across the install. At least 1. |
| `HM_FOCUS_MAX_PER_USER` | `5` | `harbormaster` | Most pages one user can have read at the focus interval at once. At least 1. |
| `HM_FOCUS_MAX_CALLS_PER_SEC` | `5` | `harbormaster` | AWS calls a slice's focus reads may make per second in each rate category. At least 1. |

## Retention

Retention applies to the coordinator's cleanup job. See [Data retention](../operations/data-retention.md).

| Variable | Default | Read by | Description |
| --- | --- | --- | --- |
| `HM_RETENTION_STOPPED_TASKS` | `168h` | `harbormaster` | How long stopped tasks are kept. |
| `HM_RETENTION_SERVICE_EVENTS` | `720h` | `harbormaster` | How long service events are kept. |
| `HM_RETENTION_COUNTS_HISTORY` | `168h` | `harbormaster` | How long task-count history is kept. |
| `HM_RETENTION_OPERATION_LOG` | `2160h` | `harbormaster` | How long the operations log is kept. |
| `HM_RETENTION_DEPLOYMENT_HISTORY` | `2160h` | `harbormaster` | How long deployment history is kept. |
| `HM_RETENTION_AUDIT_LOG` | `8760h` | `harbormaster` | How long the audit log is kept. |
| `HM_RETENTION_ORPHAN_TAGS` | `720h` | `harbormaster` | How long tags whose resource no longer exists are kept. |
| `HM_RETENTION_AVAILABILITY_DAILY` | `8760h` | `harbormaster` | How long daily service availability rollups are kept. |
| `HM_RETENTION_CAPACITY_DAILY` | `8760h` | `harbormaster` | How long daily cluster capacity rollups are kept. |
| `HM_RETENTION_UTIL_DAILY` | `2160h` | `harbormaster` | How long daily service utilisation rollups are kept. |
| `HM_RETENTION_SCAN_IMAGES` | `168h` | `harbormaster` | How long a scan result is kept after the image was last requested. |
| `HM_RETENTION_SCAN_RUNS` | `720h` | `harbormaster` | How long image scan run records are kept. |
| `HM_RETENTION_NOTIFICATION_LOG` | `720h` | `harbormaster` | How long the notification delivery log and resolved alert incidents are kept. |
| `HM_RETENTION_AGENT_CONVERSATIONS` | `720h` | `harbormaster` | How long an AI chat conversation is kept after its last activity. Its runs and usage stay. |
| `HM_RETENTION_AGENT_RUNS` | `2160h` | `harbormaster` | How long finished AI investigations and review results (summaries and stored evidence) and finished agent run records are kept. `0` keeps everything. A non-zero value must be at least `48h`; a shorter or negative one stops startup. Investigations and review results that are waiting, running or younger than 48 hours, the newest scheduled result of each review, and every run of the current UTC month, are never deleted, whatever the value. |

## Image security

The scanner is off until `HM_SCAN_ENABLED=true`. With it on and `HM_FAKE_AWS` off, a task that coordinates (`all` or `coordinator`) requires the five launch settings and names the first missing one at start. See [Image security](../guides/image-security.md).

### Server settings

| Variable | Default | Read by | Description |
| --- | --- | --- | --- |
| `HM_SCAN_ENABLED` | `false` | `harbormaster` | Turns image scanning on. |
| `HM_SCAN_CLUSTER_ARN` | none | `harbormaster` | ECS cluster that runs the scanner task. Required when scanning is on. |
| `HM_SCAN_TASK_DEFINITION` | none | `harbormaster` | ECS task definition of the scanner task. Required when scanning is on. |
| `HM_SCAN_SUBNETS` | none | `harbormaster` | Comma-separated subnet IDs for the scanner task. Required when scanning is on. |
| `HM_SCAN_SECURITY_GROUP` | none | `harbormaster` | Security group for the scanner task. Required when scanning is on. |
| `HM_SCAN_BUCKET` | none | `harbormaster`, `scanner` | S3 bucket for job manifests, results and the vulnerability database. Required when scanning is on. The scanner also needs it (see below). |
| `HM_SCAN_ROLE_NAME` | `harbormaster-scan` | `harbormaster` | Name of the per-account role the scanner task assumes to read ECR. |
| `HM_SCAN_INTERVAL` | `15s` | `harbormaster` | How often the coordinator checks for requested scans. At least `1s`. |
| `HM_SCAN_TASK_TIMEOUT` | `30m` | `harbormaster` | Maximum run time of one scanner task. At least `5m`. |
| `HM_SCAN_DB_REFRESH` | `24h` | `harbormaster` | Refresh the vulnerability database before a scan when it is older than this. `0` never refreshes. Otherwise at least `1h`. |
| `HM_SCAN_DB_MAX_AGE` | `120h` | `harbormaster` | Database age past which the scanner refuses to match and reports `db_stale`. At least `1h`. |
| `HM_SCAN_MAX_TASKS` | `1` | `harbormaster` | Scanner tasks that may run at once. 1 to 4. |
| `HM_SCAN_BATCH_SIZE` | `25` | `harbormaster` | Images per scanner task. 1 to 100. |
| `HM_SCAN_MAX_IMAGE_MB` | `4096` | `harbormaster` | Largest image the scanner will pull, in MB. At least 1. |

### Scanner task settings

The server sets `HM_SCAN_RUN_ID` and `HM_SCAN_MODE` on each scanner task it launches. The rest come from the scanner task definition (the Terraform module sets them). Run `harbormaster-scanner run` by hand only for debugging.

| Variable | Default | Read by | Description |
| --- | --- | --- | --- |
| `HM_SCAN_RUN_ID` | none | `scanner` | Id of the scan run to execute. Required: a positive number. |
| `HM_SCAN_MODE` | none | `scanner` | Job mode. The only accepted value is `scan`; anything else exits 1. |
| `HM_SCAN_BUCKET` | none | `scanner` | S3 bucket that holds the job. Required. |
| `HM_SCAN_IMAGE_DIGEST` | none | `scanner` | Digest of the scanner image itself, recorded with results. |
| `HM_SCAN_WORK_DIR` | `/work` | `scanner` | Scratch directory. |
| `HM_SCAN_SYFT` | `/usr/local/bin/syft` | `scanner` | Path to the Syft binary. Also checked by `harbormaster-scanner healthcheck`. |
| `HM_SCAN_GRYPE` | `/usr/local/bin/grype` | `scanner` | Path to the Grype binary. Also checked by `harbormaster-scanner healthcheck`. |

The scanner also loads standard AWS SDK settings (credentials, `AWS_REGION`) from its environment.

## ARM64 advisor

| Variable | Default | Read by | Description |
| --- | --- | --- | --- |
| `HM_ARM64_CHECK` | `true` | `harbormaster` | Turns the image platform check job on (coordinator). |
| `HM_ARM64_CHECK_INTERVAL` | `15m` | `harbormaster` | How often a cycle looks for new and due images. At least `1s`. |
| `HM_ARM64_RECHECK` | `24h` | `harbormaster` | How often a known image is resolved again. Must be positive. |
| `HM_ARM64_PUBLIC_REGISTRIES` | `true` | `harbormaster` | Lets the job read Docker Hub, public ECR and GHCR anonymously. |

## Analytics

| Variable | Default | Read by | Description |
| --- | --- | --- | --- |
| `HM_ANALYTICS_UTIL` | `true` | `harbormaster` | Daily CloudWatch utilisation rollup behind the Capacity and Cost tabs. One `GetMetricData` batch per account and region a day. |

## Limits

| Variable | Default | Read by | Description |
| --- | --- | --- | --- |
| `HM_PAGE_SIZE_DEFAULT` | `100` | `harbormaster` | Default API page size. |
| `HM_PAGE_SIZE_MAX` | `500` | `harbormaster` | Maximum API page size. |
| `HM_MAX_DESIRED_COUNT` | `50` | `harbormaster` | Highest desired count Harbormaster will set on a service. At least 1. |
| `HM_METRICS_CACHE_TTL` | `60s` | `harbormaster` | How long metric query results are reused before AWS is asked again. |
| `HM_LOG_TAIL_IDLE` | `30m` | `harbormaster` | Closes a live log tail after this long without activity. |
| `HM_LOG_TAILS_PER_USER` | `2` | `harbormaster` | Concurrent live log tails per user (across tasks when `HM_KV_URL` is set, otherwise per task). |
| `HM_LOG_HISTORY_LIMIT` | `500` | `harbormaster` | Maximum log events returned by one history query. |
| `HM_LOG_SEARCH_TIMEOUT` | `60s` | `harbormaster` | How long one Logs Insights search may run. Must be positive. |
| `HM_LOG_SEARCH_MAX_SERVICES` | `20` | `harbormaster` | Maximum services one log search may cover. At least 1. |
| `HM_LOG_SEARCH_MAX_RANGE` | `168h` | `harbormaster` | Longest time range one log search may cover. Must be positive. |
| `HM_LOG_SEARCH_MAX_GB` | `10` | `harbormaster` | Most GiB one log search may scan before its queries are stopped. `0` removes the cap. Negative values are rejected. |
| `HM_EXEC_IDLE_TIMEOUT` | `20m` | `harbormaster` | Closes a terminal session after this long without keystrokes or resizes. |
| `HM_EXEC_MAX_DURATION` | `4h` | `harbormaster` | Closes a terminal session this long after it opened. |
| `HM_EXEC_MAX_SESSIONS_PER_USER` | `3` | `harbormaster` | Concurrent terminal sessions per user (across tasks when `HM_KV_URL` is set, otherwise per task). |

## Access tokens and OAuth

Tokens are off until `HM_API_TOKENS=true`. These settings are validated even while tokens are off, so a bad value is caught before anyone turns them on. See [Access tokens](../guides/access-tokens.md).

| Variable | Default | Read by | Description |
| --- | --- | --- | --- |
| `HM_API_TOKENS` | `false` | `harbormaster` | Turns on bearer authentication and the token routes. Off, token routes answer 404 and an `Authorization` header is ignored. |
| `HM_TOKEN_MAX_TTL` | `2160h` | `harbormaster` | Longest lifetime of a new token (90 days). Must be positive. |
| `HM_TOKEN_MAX_TTL_SSO` | `168h` | `harbormaster` | Longest lifetime for an SSO-linked user and for global admins (7 days). Positive and not above `HM_TOKEN_MAX_TTL`. |
| `HM_TOKEN_MAX_PER_USER` | `10` | `harbormaster` | Unexpired tokens one user may hold. At least 1. |
| `HM_TOKEN_LOG_SEARCH_MAX_GB` | `2` | `harbormaster` | Most GiB one token-initiated log search may scan. `0` or more. |
| `HM_TOKEN_LOG_SEARCH_DAILY_GB` | `20` | `harbormaster` | GiB one user's tokens may scan per UTC day. `0` turns token log search off. |
| `HM_TOKEN_REDACT_ENV` | `false` | `harbormaster` | Replaces environment values with `[redacted]` in task definition answers to tokens. |
| `HM_MCP_PUBLIC_URL` | none | `harbormaster`, `mcp` | Public base URL of the MCP gateway. On the server it is shown in the connect instructions and is the OAuth issuer; it must be `https://`, with no path, query or trailing slash. On the gateway it is required (see below). |
| `HM_MCP_GATEWAY_CIDRS` | none | `harbormaster` | Comma-separated CIDRs of the gateway's subnets. Behind one, the client IP is the `X-Forwarded-For` entry to its left. Needs `HM_TRUSTED_PROXY`. `0.0.0.0/0` and `::/0` are refused. |
| `HM_OAUTH` | `false` | `harbormaster` | OAuth for MCP connectors. Needs `HM_API_TOKENS=true` and `HM_MCP_PUBLIC_URL`. Off, the OAuth routes answer 404. |
| `HM_OAUTH_ACCESS_TTL` | `1h` | `harbormaster` | Lifetime of an OAuth access token. 5 minutes to 24 hours. |
| `HM_OAUTH_CLIENT_HOSTS` | `claude.ai` | `harbormaster` | Comma-separated hosts whose client metadata documents are accepted as client ids. Lowercase host names only. Replaces the default. |
| `HM_OAUTH_REDIRECT_URIS` | Claude's callbacks | `harbormaster` | Comma-separated redirect URIs a client may use. Replaces the whole default list: `https://claude.ai/api/mcp/auth_callback`, `https://claude.com/api/mcp/auth_callback`, `http://localhost/callback`, `http://127.0.0.1/callback`. An `http` entry must be on `localhost` or `127.0.0.1` with no port and then matches any port. |

## MCP gateway

These are the `harbormaster-mcp` container's variables. The gateway holds no secrets. See [Install the MCP gateway](../install/mcp-gateway.md).

| Variable | Default | Read by | Description |
| --- | --- | --- | --- |
| `HM_MCP_UPSTREAM_URL` | required | `mcp` | Harbormaster's internal base URL. `https://`, or `http://` on a loopback host only; no path, query, fragment or user info. |
| `HM_MCP_PUBLIC_URL` | required | `mcp` | The gateway's public base URL, same format as above. |
| `HM_MCP_LISTEN` | `:8481` | `mcp` | Listen address (`host:port`). `harbormaster-mcp healthcheck` uses its port. |
| `HM_MCP_TRUSTED_PROXY` | `false` | `mcp` | Set `true` only behind the public load balancer: the client IP is then the `X-Forwarded-For` entry it appended. With `false` the gateway uses its direct peer. |
| `HM_MCP_ALLOWED_ORIGINS` | none | `mcp` | Comma-separated browser origins allowed to call `/mcp`. Other origins get 403. |
| `HM_MCP_MAX_RESULT_BYTES` | `100000` | `mcp` | Largest tool result. 10000 to 140000. |
| `HM_MCP_UPSTREAM_TIMEOUT` | `30s` | `mcp` | Per call to Harbormaster. `1s` to `120s`. |
| `HM_MCP_UPSTREAM_CA_PEM` | none | `mcp` | PEM text of extra CA certificates, added to the system roots for the upstream connection only. Invalid PEM stops startup (exit 2). |
| `HM_MCP_AUTHORIZE_URL` | none | `mcp` | Harbormaster's private base URL. Setting it turns OAuth on: the gateway serves the well-known metadata documents and relays `POST /oauth/token`. |
| `HM_MCP_AUTHORIZE_VIA_GATEWAY` | `false` | `mcp` | Advertise the gateway's own `/oauth/authorize` (a redirect to `HM_MCP_AUTHORIZE_URL`). Needs `HM_MCP_AUTHORIZE_URL`. |

## AI agent

The agent is off unless `HM_AGENT_URL` is set. Variables marked `harbormaster` go on the Harbormaster tasks (web and coordinator, or all); variables marked `agent` go on the `harbormaster-agent` container. The agent's variables are all listed here, and the `harbormaster-agent` Terraform module sets most of them for you. See [Install the AI agent](../install/ai-agent.md) and [Terraform module: harbormaster-agent](terraform-agent.md).

### Harbormaster settings

| Variable | Default | Read by | Description |
| --- | --- | --- | --- |
| `HM_AGENT_URL` | none | `harbormaster` | The agent's private base URL. Setting it turns the AI agent on: the agent routes, the settings tab, the chat button and run tokens exist only then. `https://`, or `http://` on a loopback host only; no path, query, fragment or user info. Needs an internal load balancer. Web and coordinator tasks both need it. |
| `HM_AGENT_CA_PEM` | none | `harbormaster` | PEM text of an extra CA that signs the agent's certificate, for the connection to the agent only. Invalid PEM stops startup. |
| `HM_AGENT_RUN_TOKEN_TTL` | `15m` | `harbormaster` | Lifetime of a run token. `1m` to `1h`. A shorter value than the run timeout plus a minute is logged once, because long runs may then lose their tools. |
| `HM_AGENT_INVESTIGATION_WINDOW` | `15m` | `harbormaster` | Alerts for the same service, or the same cluster or account entry, that open within this time share one investigation. The Opened message of an alert must also have been planned within it for the alert to be investigated. `1m` to `24h`. See [Automatic AI investigations](../guides/ai-agent-investigator.md). |
| `HM_AGENT_INVESTIGATIONS_CONCURRENT` | `2` | `harbormaster` | Investigations that run at the same time. `1` to `8`. |
| `HM_AGENT_INVESTIGATIONS_PER_HOUR` | `20` | `harbormaster` | Investigations that may start in an hour. A start counts when its run begins; the cap can overshoot by up to the number of parallel runs. The cap is checked once, before the first start; a retry is not counted again. `0` turns the investigator off: nothing starts, no new request is recorded, no Diagnosis message is planned or sent, and requests still waiting end as `rate_limited`. Not negative. |
| `HM_AGENT_INVESTIGATION_MAX_WAIT` | `15m` | `harbormaster` | A request that has waited longer than this before its first start is skipped as `expired`. `1m` to `24h`. |
| `HM_AGENT_REVIEWS_PER_DAY` | `24` | `harbormaster` | Scheduled AI reviews that may start in 24 hours. `0` to `200`. `0` turns reviews off: none is recorded or run. A review past the cap is skipped as `rate_limited`. The cap can overshoot by one when two coordinator tasks overlap. See [Scheduled AI reviews](../guides/ai-agent-reviews.md). |
| `HM_AGENT_REVIEW_MAX_LATE` | `6h` | `harbormaster` | How late a review may start. A run that is due and is older than this is recorded as `missed` and does not run. `15m` to `12h`. |
| `HM_AGENT_IDENTITY_RATE_PER_SECOND` | `5` | `harbormaster` | Requests per second the agent identity may make to Harbormaster, with a burst of 60 times that. `1` to `50`. |
| `HM_AGENT_IDENTITY_LOG_SEARCH_MAX_GB` | `2` | `harbormaster` | Most GiB one log search by the agent identity may scan. `0` sets no cap of its own; `HM_LOG_SEARCH_MAX_GB` still applies when set. At most `1000000`. |
| `HM_AGENT_IDENTITY_LOG_SEARCH_DAILY_GB` | `20` | `harbormaster` | GiB the agent identity may scan per UTC day. `0` turns its log search off. At most `1000000`. |

Redaction of environment values in the agent's tool results: an admin can lift it per provider in **Settings > AI agent**, but `HM_TOKEN_REDACT_ENV=true` (see [Access tokens and OAuth](#access-tokens-and-oauth)) overrides every lift and redacts all of them. Secret-looking values are redacted in either case.

### Agent settings

| Variable | Default | Read by | Description |
| --- | --- | --- | --- |
| `HM_AGENT_LISTEN` | `:8484` | `agent` | Listen address (`host:port`). `harbormaster-agent healthcheck` uses its port. |
| `HM_AGENT_UPSTREAM_URL` | required | `agent` | Harbormaster's internal base URL. `https://`, or `http://` on a loopback host only; no path, query, fragment or user info. |
| `HM_AGENT_UPSTREAM_CA_PEM` | none | `agent` | PEM text of extra CA certificates for the connection to Harbormaster only. Invalid PEM stops startup. |
| `HM_AGENT_TRUSTED_PROXY` | `false` | `agent` | Set `true` only behind exactly one trusted proxy, the agent's internal load balancer: the client address is then the `X-Forwarded-For` entry it appended. The Terraform module sets `true`. |
| `HM_AGENT_PROVIDERS` | required | `agent` | The model catalogue as JSON: a list of `{"provider": "<id>", "models": [{"id", "input_per_mtok_usd", "output_per_mtok_usd", "context_tokens"}]}`. Providers are `anthropic`, `openai`, `google` and `bedrock`, each at most once with at least one model. Prices are decimal strings in US dollars per million tokens, such as `"3.00"`. Required unless `HM_AGENT_FAKE_PROVIDER=true`. |
| `HM_AGENT_ANTHROPIC_API_KEY` | none | `agent` | Anthropic API key. Needed exactly when the catalogue lists `anthropic`. A secret. |
| `HM_AGENT_OPENAI_API_KEY` | none | `agent` | OpenAI API key. Needed exactly when the catalogue lists `openai`. A secret. |
| `HM_AGENT_GOOGLE_API_KEY` | none | `agent` | Google API key. Needed exactly when the catalogue lists `google`. A secret. |
| `HM_AGENT_BEDROCK_REGION` | none | `agent` | Region of the Bedrock endpoint, such as `eu-west-1`. Needed exactly when the catalogue lists `bedrock`. Credentials come from the task role. |
| `HM_AGENT_FAKE_PROVIDER` | `false` | `agent` | Adds a canned `fake` provider with one model. Refused unless `HM_AGENT_UPSTREAM_URL` is a loopback address. For local development only. |
| `HM_AGENT_MAX_STEPS` | `20` | `agent` | Most steps in one run. 1 to 100. |
| `HM_AGENT_MAX_RUN_TOKENS` | `200000` | `agent` | Token allowance of one run. 1000 to 2000000. A run always keeps room for a final answer. The worst case at the model's higher price is what a question reserves from the monthly allowance. |
| `HM_AGENT_RUN_TIMEOUT` | `5m` | `agent` | Longest a run lasts. `5s` to `30m`. The Terraform module accepts at most `5m`, because its load balancer waits 600 seconds. |
| `HM_AGENT_MAX_CONCURRENT_RUNS` | `8` | `agent` | Runs at once per agent task. 1 to 256. |
| `HM_AGENT_MAX_RESULT_BYTES` | `100000` | `agent` | Largest tool result sent to the model. 10000 to 140000. |

A key and a catalogue entry go together: a listed provider with no key, or a key with no listed models, stops startup. Errors name the variable and never repeat a key.

## Single sign-on (SAML)

SAML is configured in the web app by a global admin and stored in the database. Only one environment variable applies; `HM_SESSION_KEY` is also required. See [Single sign-on](../install/single-sign-on.md).

| Variable | Default | Read by | Description |
| --- | --- | --- | --- |
| `HM_SAML_REQUEST_TTL` | `10m` | `harbormaster` | How long a started SSO sign-in may take before it must be restarted. Between `1m` and `30m`. |

## Alert notifications and off-hours

| Variable | Default | Read by | Description |
| --- | --- | --- | --- |
| `HM_NOTIFY_INTERVAL` | `10s` | `harbormaster` | How often the coordinator plans and sends notifications. At least `1s`. |
| `HM_NOTIFY_SETTLE` | `3m` | `harbormaster` | How long a service must stay failing or degraded before it is notified or counted. `0` disables the wait. At most `30m`. |
| `HM_NOTIFY_ALLOWED_PRIVATE_CIDRS` | none | `harbormaster` | Comma-separated private ranges (inside `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16` or `fc00::/7`) that notification destinations may reach. Anything outside those ranges stops startup. |
| `HM_SMTP_HOST` | none | `harbormaster` | Mail server. Email routes are unavailable until it is set. |
| `HM_SMTP_PORT` | `587` | `harbormaster` | Mail server port, 1 to 65535. |
| `HM_SMTP_TLS` | `starttls` | `harbormaster` | `starttls` or `implicit`. |
| `HM_SMTP_USERNAME` | none | `harbormaster` | Mail login. Set with `HM_SMTP_PASSWORD` or neither. |
| `HM_SMTP_PASSWORD` | none | `harbormaster` | Mail password. |
| `HM_SMTP_FROM` | none | `harbormaster` | Sender address, for example `Harbormaster <harbormaster@example.com>`. Required when `HM_SMTP_HOST` is set. |
| `HM_OFFHOURS_INTERVAL` | `60s` | `harbormaster` | How often the coordinator reconciles off-hours schedules. At least `1s`. |

## Demo and test variables

These matter only with `--fake-aws` or in development.

| Variable | Default | Read by | Description |
| --- | --- | --- | --- |
| `HM_FAKE_NOTIFY_DEMO` | `true` | `harbormaster` | Fake AWS mode only: seed demo notification routes that deliver to the built-in fake receiver. |
| `HM_FAKE_OFFHOURS_DEMO` | `true` | `harbormaster` | Fake AWS mode only: whether the seeded demo off-hours schedule is enabled. Applies only when the demo schedule is first seeded. |
| `HM_FAKE_FAIL` | none | `harbormaster` | Fake AWS mode only: comma-separated operations to make fail on every call: `scheduled_actions`, `scaling_tier`, `target_health`. Unknown names are ignored. |

Variables used only by tests and tools (`HM_TEST_DATABASE_URL`, `HM_TEST_KV_URLS`, `HM_E2E_SCHEMA`, `HM_IT_*`, `HM_SOAK`) are listed in [Testing](../development/testing.md).

## Standard AWS variables

The AWS SDK reads its usual variables (`AWS_REGION`, `AWS_PROFILE`, container credentials and so on) for the server's own AWS access, in `harbormaster` outside fake mode and in `scanner`. Harbormaster does not define them. Access to connected accounts goes through assumed roles, not these credentials. See [IAM and AWS access](../security/iam-and-aws-access.md).

## Validation summary

Startup fails when any of these hold:

- Neither `HM_DATABASE_URL` nor `HM_DATABASE_SECRET_ARN` is set.
- A split role (`web`, `poller`, `coordinator`) runs without `HM_KV_URL`.
- `HM_OAUTH=true` without `HM_API_TOKENS=true` or `HM_MCP_PUBLIC_URL`.
- `HM_SMTP_HOST` is set without a valid `HM_SMTP_FROM`, or only one of `HM_SMTP_USERNAME` and `HM_SMTP_PASSWORD` is set.
- `HM_AGENT_URL` is not a valid base URL, `HM_AGENT_CA_PEM` holds no certificate, or `HM_AGENT_RUN_TOKEN_TTL` is outside 1 minute to 1 hour.
- `HM_AGENT_INVESTIGATION_WINDOW` or `HM_AGENT_INVESTIGATION_MAX_WAIT` is outside 1 minute to 24 hours, `HM_AGENT_INVESTIGATIONS_CONCURRENT` is outside 1 to 8, `HM_AGENT_INVESTIGATIONS_PER_HOUR` is negative, `HM_AGENT_IDENTITY_RATE_PER_SECOND` is outside 1 to 50, an `HM_AGENT_IDENTITY_LOG_SEARCH_*` value is negative or above 1000000, `HM_AGENT_REVIEWS_PER_DAY` is outside 0 to 200, or `HM_AGENT_REVIEW_MAX_LATE` is outside 15 minutes to 12 hours. These are checked whether or not `HM_AGENT_URL` is set.
- `HM_RETENTION_AGENT_RUNS` is negative, or non-zero and below 48 hours.
- The agent has no `HM_AGENT_PROVIDERS`, a provider without its key or region (or the reverse), or `HM_AGENT_FAKE_PROVIDER=true` against a non-loopback upstream.
- `HM_SCAN_ENABLED=true` (outside fake mode) on a coordinating task with any of the five launch settings missing.
