# HTTP API reference

An overview of the Harbormaster HTTP API: how to authenticate, the error format, rate limits and every route group with its required permission. It is for engineers who script against Harbormaster or build tools on top of it.

!!! warning "Not a stable public API"
    This is the API that the web app uses. Nothing in the code or the README promises versioning or backward compatibility: paths have no version prefix, and response shapes change with the web app. Pin the Harbormaster version you script against, and prefer the read-only routes that [access tokens](../guides/access-tokens.md) and the MCP gateway use.

## Basics

- **Base path:** `/api`. Every route below is relative to the server's `HM_BASE_URL`. Unknown `/api/` paths return a JSON 404, never the web app.
- **Format:** JSON request and response bodies. Request bodies are limited to 1 MiB.
- **Responses:** `GET` responses that return 200 carry an `ETag`. Send it back in `If-None-Match` to get `304 Not Modified`.
- **Ids:** clusters, services and tasks use opaque string ids returned by the list routes. Use them in paths as returned.
- **Tag filter:** list routes accept a repeatable `?tag=<tag id>` query parameter that narrows results to those tags. It never widens what you can see.
- **Security headers:** responses carry a content security policy, HSTS and `X-Frame-Options: DENY`.

## Authentication

There are two ways to authenticate.

### Session cookie

Sign in with `POST /api/auth/login` (or single sign-on). The server sets the `hm_session` cookie and returns the user, including a `csrf_token`. `GET /api/me` returns the same token for an existing session.

- Send the cookie on every request.
- On every method except `GET`, `HEAD` and `OPTIONS`, send the token in the `X-CSRF-Token` header. A missing or wrong token returns `403 csrf`.
- A user who must change their password gets `403 password_change_required` on every route except `GET /api/me`, `POST /api/auth/password` and `POST /api/auth/logout`.

Sessions end after `HM_SESSION_IDLE` without use or `HM_SESSION_ABSOLUTE` in total. Login attempts are rate limited per user and per IP (`HM_LOGIN_MAX_PER_USER`, `HM_LOGIN_MAX_PER_IP`, `HM_LOGIN_WINDOW`).

### Bearer access token

When `HM_API_TOKENS=true`, send `Authorization: Bearer hmp_...`. A token is created by a signed-in user on the My access page.

- A token acts as its user, read-only. It sees the same tags, and Logs only where the user has Logs. An admin's token is not more powerful.
- It can call only the routes marked **Token** in the tables below. Every other route returns `403 token_not_allowed`, writes and admin routes included.
- A token that does not resolve returns `401` on every route. With tokens off, the header is ignored.
- Tokens skip the CSRF check because they carry no cookie.

See [Access tokens](../guides/access-tokens.md) for lifetimes and revocation.

## Errors

Errors use one JSON shape:

```json
{"error": {"code": "forbidden", "message": "You need Deploy on Prod"}}
```

| Status | Code | Meaning |
| --- | --- | --- |
| 401 | `unauthenticated` | No valid session or token, or wrong credentials. |
| 403 | `forbidden` | You can see the resource but lack the action, or the route is admin-only. |
| 403 | `csrf` | Missing or invalid CSRF token. |
| 403 | `token_not_allowed` | The route is not available to access tokens. |
| 403 | `password_change_required` | Change your password first. |
| 403 | `sso_required` | The user must sign in through single sign-on. |
| 404 | `not_found` | The resource does not exist, **or you cannot see it**. Invisible resources never return 403. |
| 405 | `method_not_allowed` | A POST-only route was called with GET. |
| 409 | `conflict` | The change conflicts with existing data, a running rollout, an off-hours schedule or a stale base revision. |
| 422 | `invalid` | Validation failed. The message says why. |
| 429 | `rate_limited` | A rate limit was hit. `Retry-After` is set for token requests. |
| 429 | `log_search_budget_exhausted` | The daily token log-search budget is used up. |
| 502 | `aws_error` | AWS rejected the call. The message carries AWS's text. |
| 503 | `throttled` | AWS is throttling Harbormaster. Retry shortly. |
| 500 | `internal` | Unexpected failure. Details are in the server log. |

## Rate limits

| Limit | Value | Scope |
| --- | --- | --- |
| Requests with an access token | burst 120, then 2 per second | per user, shared across web tasks |
| Log search runs | burst 6, then 1 per 10 seconds; one running search at a time (409 when exceeded) | per user |
| Notification test sends | burst 5, then 1 per 10 seconds | per user |
| Opening a live page (poll-now signal) | burst 10, then 2 per second | per user |
| SSO sign-in start | burst 60, then 1 per second | per client IP |
| OAuth token endpoint | burst 30, then 1 per second | per client IP |
| Login attempts | `HM_LOGIN_MAX_PER_USER` and `HM_LOGIN_MAX_PER_IP` per `HM_LOGIN_WINDOW` | per user and per IP |
| Terminal sessions, live log tails | `HM_EXEC_MAX_SESSIONS_PER_USER`, `HM_LOG_TAILS_PER_USER` | per user |

Counted across tasks when `HM_KV_URL` is set, otherwise per task. See [Throttling and limits](../operations/throttling-and-limits.md).

## Permissions in the tables

The **Needs** column uses these terms. Permission names are defined in [Permissions](permissions.md).

| Term | Meaning |
| --- | --- |
| Public | No session needed. |
| Signed in | Any signed-in user. Results are filtered to what the user can see (View on the resource's tag). |
| View | View on the tag of the cluster, service or task in the path. Without it the answer is 404. |
| Logs, Operate, Deploy, Edit, Terminal, Accept risk | That action on the resource's tag, on top of View. |
| Admin | Global admin only. |
| Token | The route is also available to read-only access tokens. |

## Health and metrics

| Method and path | Needs | Purpose |
| --- | --- | --- |
| `GET /healthz` | Public | Liveness. Returns `ok` (200) when the database answers, `unavailable` (503) otherwise. Served by every role. A poller also returns 503 when its lease loop has not finished a tick for 60 seconds. |
| `GET /metrics` | Metrics token | Prometheus text format. Send `Authorization: Bearer <HM_METRICS_TOKEN>`. 404 when `HM_METRICS_TOKEN` is empty, 401 for a wrong token. See [Metrics](metrics.md). |

The `poller` and `coordinator` roles serve only these two paths. The `web` and `all` roles serve the full API.

## Authentication and the current user

| Method and path | Needs | Purpose |
| --- | --- | --- |
| `POST /api/auth/login` | Public | Sign in with username and password. Sets the session cookie. |
| `GET /api/auth/setup` | Public | Whether first-run setup is needed. |
| `POST /api/auth/setup` | Public | Create the first admin with the one-time setup token. |
| `GET /api/auth/options` | Public | Which sign-in methods are on (password, SSO). |
| `GET /api/auth/saml/login` | Public | Start SAML sign-in. |
| `POST /api/auth/saml/acs` | Public | SAML assertion consumer service. Exempt from CSRF; it never reads the session. |
| `GET /api/auth/saml/metadata` | Public | Service provider metadata. |
| `POST /api/auth/saml/link` | Signed in | Start linking the account to an SSO identity. |
| `POST /api/auth/logout` | Signed in | End the session. |
| `POST /api/auth/password` | Signed in | Change your password. |
| `GET /api/me` | Signed in | The current user: admin flag, per-tag permissions, visible accounts and regions, CSRF token. |
| `GET /api/poller` | Signed in | Polling status and data age per account and region. |
| `GET /api/settings` | Signed in | Instance settings (the stopped-tasks window). |
| `PUT /api/settings` | Admin | Change instance settings. |

## Estate: overview, clusters, services, tasks

| Method and path | Needs | Purpose |
| --- | --- | --- |
| `GET /api/overview` | Signed in, Token | Estate summary. |
| `GET /api/clusters` | Signed in, Token | List clusters. |
| `GET /api/clusters/{clusterId}` | View, Token | One cluster. |
| `GET /api/services` | Signed in, Token | List services. |
| `GET /api/services/{serviceId}` | View, Token | One service. |
| `GET /api/services/{serviceId}/tasks` | View, Token | Tasks of a service. |
| `GET /api/services/{serviceId}/operations` | View, Token | Operations log of a service. |
| `GET /api/tasks` | Signed in, Token | List tasks. |
| `GET /api/search` | Signed in, Token | Search clusters, services and tasks by name. |
| `GET /api/config-search` | Signed in, Token | Search stored task definitions (`?q=`). Names only, never values. |
| `GET /api/alerts` | Signed in, Token | Current alerts. |
| `GET /api/networks` | Signed in, Token | Network accounts and regions. |
| `GET /api/networks/{account}/{region}` | Signed in, Token | VPCs in a region. |
| `GET /api/vpcs/{vpcId}/tree` | Signed in, Token | VPC tree: subnets, gateways, endpoints, security groups. |
| `GET /api/storage` | Signed in, Token | EFS file systems. |
| `POST /api/focus` | Signed in | Heartbeat from a page open in a visible tab (live refresh). |
| `DELETE /api/focus` | Signed in | Release a live page. |

## Metrics, logs and timeline

| Method and path | Needs | Purpose |
| --- | --- | --- |
| `GET /api/overview/metrics` | Signed in | CloudWatch metrics for the overview. |
| `GET /api/clusters/metrics` | Signed in | Metrics for the cluster list. |
| `GET /api/clusters/{clusterId}/metrics` | View | Metrics of one cluster. |
| `GET /api/services/metrics` | Signed in | Metrics for the service list. |
| `GET /api/services/{serviceId}/metrics` | View, Token | Metrics of one service. |
| `GET /api/services/{serviceId}/scaling` | View, Token | Auto scaling configuration and activity. |
| `GET /api/services/{serviceId}/target-health` | View, Token | Load balancer target health. |
| `GET /api/services/{serviceId}/logs` | Logs, Token | Log history. |
| `GET /api/services/{serviceId}/logs/tail` | Logs | Live log tail. **WebSocket.** |
| `GET /api/services/{serviceId}/timeline` | View, Token | Service timeline. |
| `GET /api/services/{serviceId}/timeline/tasks/{taskId}` | View, Token | One task on the timeline. |
| `GET /api/services/{serviceId}/timeline/log-errors` | Logs | Error counts from logs per bucket. |
| `GET /api/log-search/services` | Signed in, Token | Services you can search (those with Logs). |
| `POST /api/log-search` | Logs, Token | Run a Logs Insights search. |
| `POST /api/log-search/estimate` | Logs, Token | Estimate what a search would scan. |
| `GET /api/log-search/saved` | Signed in | Your saved searches. |
| `POST /api/log-search/saved` | Logs | Save a search. |
| `DELETE /api/log-search/saved/{id}` | Signed in | Delete one of your saved searches. |

## Analytics

All are `GET`, need Signed in and are available to tokens.

| Path | Purpose |
| --- | --- |
| `/api/analytics/delivery` | Deployment frequency and outcomes. |
| `/api/analytics/reliability` | Availability. |
| `/api/analytics/capacity` | Cluster capacity. |
| `/api/analytics/operations` | Operations activity. |
| `/api/analytics/cost` | Estimated Fargate run rate and advisors. |

## Changes: deploy, edit, scale, restart

| Method and path | Needs | Purpose |
| --- | --- | --- |
| `GET /api/services/{serviceId}/task-definition` | View, Token | The deployed task definition. |
| `GET /api/services/{serviceId}/revisions` | View, Token | Task definition revisions. |
| `GET /api/services/{serviceId}/revisions/{revision}` | View, Token | One revision. |
| `GET /api/services/{serviceId}/image-tags` | View | Tags available for an image. |
| `GET /api/services/{serviceId}/desired-count` | View, Token | Desired count and limits. |
| `GET /api/services/{serviceId}/deploy-settings` | View | What the caller may change on the Deploy tab. |
| `GET /api/services/{serviceId}/arm64-readiness` | View, Token | ARM64 advisor result. |
| `POST /api/services/{serviceId}/revisions` | Edit | Register a new task definition revision. |
| `POST /api/services/{serviceId}/deploy` | Deploy | Deploy a revision. |
| `POST /api/services/{serviceId}/changeset` | Per change (below) | Apply several changes in one request. |
| `POST /api/services/{serviceId}/restart` | Operate | Force a new deployment. |
| `POST /api/services/{serviceId}/desired-count` | Operate | Set the desired count (up to `HM_MAX_DESIRED_COUNT`). |
| `POST /api/tasks/{taskId}/stop` | Operate | Stop a task. |

A change set needs, for each part it contains, Edit (a new task definition body), Deploy (a revision, ECS Exec, deployment settings, capacity provider strategy, or a task definition change combined with service or scaling changes) and Operate (desired count, forced deployment, scaling). The whole request is refused before any write if one is missing.

## Terminal

| Method and path | Needs | Purpose |
| --- | --- | --- |
| `GET /api/tasks/{taskId}/exec-targets` | View | Containers and commands available in a running task; whether the caller has Terminal. |
| `GET /api/tasks/{taskId}/exec` | Terminal | Interactive shell through ECS Exec. **WebSocket.** |

## Image security

Available only when scanning is on (`HM_SCAN_ENABLED`).

| Method and path | Needs | Purpose |
| --- | --- | --- |
| `GET /api/security` | Signed in, Token | Scan results across visible services. |
| `GET /api/security/images/{digest}` | Signed in, Token | One image's findings. |
| `GET /api/security/services/{serviceId}` | View, Token | A service's images and gate setting. |
| `PUT /api/security/services/{serviceId}/setting` | Deploy | Turn the pre-deploy scan gate on or off. |
| `POST /api/security/services/{serviceId}/scans` | Deploy | Request a scan. |
| `GET /api/security/services/{serviceId}/scans` | View | Scan request status. |
| `POST /api/security/images/{digest}/rescan` | Deploy | Rescan an image (on a service that requested it). |
| `PUT /api/security/images/{digest}/base-image` | Deploy | Set or clear the repository's base image. |
| `POST /api/security/images/{digest}/acceptances` | Accept risk | Accept a vulnerability on an image. |
| `POST /api/security/images/{digest}/package-acceptances` | Accept risk | Accept a package's findings. |
| `DELETE /api/security/acceptances/{id}` | Accept risk | Remove an acceptance. |

## Off-hours

| Method and path | Needs | Purpose |
| --- | --- | --- |
| `GET /api/services/{serviceId}/offhours` | View, Token | A service's schedule state. |
| `POST /api/services/{serviceId}/offhours/wake` | Operate | Wake a sleeping service for two hours. |
| `POST /api/services/{serviceId}/offhours/sleep` | Operate | Put a woken service back to sleep. |
| `GET /api/offhours/schedules` | Admin | List schedules. |
| `GET /api/offhours/schedules/{id}` | Admin | One schedule. |
| `POST /api/offhours/schedules` | Admin | Create a schedule. |
| `PUT /api/offhours/schedules/{id}` | Admin | Update a schedule. |
| `DELETE /api/offhours/schedules/{id}` | Admin | Delete a schedule. |
| `POST /api/offhours/preview` | Admin | Preview the services a tag matches. |
| `GET /api/offhours/tags` | Admin | AWS tags available to schedule on. |

## Notifications

All are Admin-only.

| Method and path | Purpose |
| --- | --- |
| `GET /api/notifications/routes` | List routes. |
| `POST /api/notifications/routes` | Create a route. |
| `PUT /api/notifications/routes/{id}` | Update a route. |
| `DELETE /api/notifications/routes/{id}` | Delete a route. |
| `POST /api/notifications/routes/{id}/test` | Send a test message. |
| `POST /api/notifications/preview` | Preview what a scope matches. |
| `GET /api/notifications/targets` | Clusters, accounts and tags a route can target. |
| `GET /api/notifications/deliveries` | The delivery log. |
| `POST /api/notifications/deliveries/{id}/retry` | Retry a failed delivery. |
| `GET /api/notifications/summary` | Delivery summary. |

## Users, tags, permissions, accounts, audit, SSO

All are Admin-only.

| Method and path | Purpose |
| --- | --- |
| `GET /api/users`, `POST /api/users` | List and create users. |
| `DELETE /api/users/{id}` | Delete a user. |
| `POST /api/users/{id}/reset-password` | Reset a password. |
| `POST /api/users/{id}/admin` | Grant or remove global admin. |
| `POST /api/users/{id}/break-glass` | Flag or unflag a break-glass admin. |
| `PUT` and `DELETE /api/users/{id}/sso-link` | Set or clear a pending SSO link. |
| `POST /api/users/{id}/sso-unlink` | Unlink an SSO identity. |
| `GET /api/permissions` | The permission matrix (users by tags). |
| `PUT /api/permissions/{userId}/{tagId}` | Set a user's actions on a tag (body `{"actions": <bitmask>}`). |
| `GET /api/tags`, `POST /api/tags` | List and create tags. |
| `PATCH /api/tags/{id}`, `DELETE /api/tags/{id}` | Edit or delete a tag. The built-in Dev tag cannot be deleted. |
| `GET /api/tags/{id}/impact` | What deleting or changing a tag affects. |
| `GET /api/tag-assignments` | Tag assignments. |
| `PUT /api/tag-assignments/clusters/{clusterId}` | Assign a tag to a cluster. |
| `PUT /api/tag-assignments/services/{serviceId}` | Assign a tag to a service. |
| `GET /api/accounts`, `POST /api/accounts` | List and connect AWS accounts. |
| `GET /api/accounts/external-id` | Generate an external ID. |
| `POST /api/accounts/test` | Test a role. |
| `PATCH /api/accounts/{id}`, `DELETE /api/accounts/{id}` | Edit, enable, disable or remove an account. |
| `GET /api/audit` | The audit log. |
| `GET /api/sso`, `PUT /api/sso` | Read and change SSO configuration. |
| `POST /api/sso/metadata` | Import IdP metadata. |
| `PUT /api/sso/enabled` | Turn SSO on or off. |
| `POST /api/sso/key` | Rotate the SP signing key. |
| `GET /api/sso/tests/{id}` | Result of a test sign-in. |

## Access tokens and OAuth

The token routes exist only when `HM_API_TOKENS=true`; the OAuth routes only when `HM_OAUTH=true`. Otherwise they return 404.

| Method and path | Needs | Purpose |
| --- | --- | --- |
| `GET /api/tokens` | Signed in (session) | Your tokens. |
| `POST /api/tokens` | Signed in (session) | Create a token. The secret is shown once. |
| `DELETE /api/tokens/{id}` | Signed in (session) | Revoke your token. |
| `GET /api/tokens/current` | Token | Who the token is, the tags it sees and its remaining log-search budget. |
| `GET /api/users/{id}/tokens` | Admin | A user's tokens. |
| `DELETE /api/users/{id}/tokens/{tokenId}` | Admin | Revoke a user's token. |
| `DELETE /api/oauth/grants/{id}` | Signed in (session) | Revoke one of your connected apps. |
| `DELETE /api/users/{id}/oauth-grants/{grantId}` | Admin | Revoke a user's connected app. |
| `GET /api/oauth/authorize` | Signed in (session) | Validate an authorization request. |
| `POST /api/oauth/authorize` | Signed in (session) | Approve or deny it. |
| `POST /api/oauth/token` | Public | OAuth token endpoint (code and refresh grants). |

Routes with no **Token** mark refuse access tokens: a token cannot create, list or revoke tokens.

## WebSocket endpoints

Two routes upgrade to WebSocket. Both use the session cookie and check permissions on the server.

| Path | Needs | Notes |
| --- | --- | --- |
| `/api/services/{serviceId}/logs/tail` | Logs | The first frame from the client carries the CSRF token. The server closes with codes in the 4xxx range: 4400 bad start, 4401 CSRF invalid, 4403 forbidden, 4404 not found, 4408 idle, 4409 too many tails, 4422 invalid, 4502 AWS error. Idle tails close after `HM_LOG_TAIL_IDLE`. |
| `/api/tasks/{taskId}/exec` | Terminal | Sessions close after `HM_EXEC_IDLE_TIMEOUT` of inactivity or `HM_EXEC_MAX_DURATION`. At most `HM_EXEC_MAX_SESSIONS_PER_USER` per user. The task must be running. |

Both check the `Origin` header and accept only the same host or the host of `HM_BASE_URL`. Access tokens cannot open them.

## Not part of `/api`

| Path | Purpose |
| --- | --- |
| `/` | The embedded web app. |
| `/fake-idp/`, `/fake-notify/` | Demo identity provider and notification receiver. Mounted only in fake AWS mode. |

The MCP gateway has its own small surface (`GET /healthz`, `POST /mcp`, OAuth metadata and `POST /oauth/token`). See [AI access](../guides/ai-access-mcp.md).
