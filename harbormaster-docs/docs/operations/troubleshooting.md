# Troubleshooting

This page maps symptoms to causes and fixes. The messages are the ones Harbormaster shows in the web app or writes to its logs. It is for operators who run an install and need to find out why something is stale, denied or refused.

Read server logs from the CloudWatch log group of the module (`/ecs/harbormaster/harbormaster-server` for the single service). Use `--profile`, an explicit `--region` and a time window:

```sh
aws logs tail /ecs/harbormaster/harbormaster-server --since 30m --profile <profile> --region <region>
```

## Start-up and health

| Symptom | Cause | Fix |
| --- | --- | --- |
| Task exits at start with `HM_DATABASE_URL or HM_DATABASE_SECRET_ARN must be set` | No database setting reached the task | Set one. The Terraform module injects the secret `harbormaster/database-url` as `HM_DATABASE_URL` |
| Task exits with `read database secret: ...` | `HM_DATABASE_SECRET_ARN` points at a secret Harbormaster cannot read or parse. An RDS-managed master secret has no `host` and is rejected | Use a secret that holds a postgres URL, or JSON with `username`, `password`, `host`, `port` and `dbname`. Check the execution role can read it |
| Exit with `role <role> needs the shared store: set HM_KV_URL (only role all runs without it)` and usage text | A `web`, `poller` or `coordinator` role started without a store | Set `cache_mode` to `create` or `external`, or run `HM_ROLE=all`. See [High availability](high-availability.md) |
| Exit with `HM_KV_URL must use database 0` or `HM_KV_URL: scheme must be redis, rediss, valkey or valkeys` | Invalid store URL | Fix the URL in the secret |
| `/healthz` returns `503` with body `unavailable` | The database ping failed. On a poller, the lease manager also has not finished a tick for 60 seconds. The log has `healthz ping failed` or `healthz failed` | Check database reachability, the security group and RDS status. ECS replaces an unhealthy task |
| Load balancer shows no healthy targets | Tasks fail `/healthz`, or the target group cannot reach the task port | Check the task log, then the `server` or `web` security group rule from the ALB |
| Log shows `applied migrations` at start | Normal. Pending migrations ran | None |
| `/metrics` returns `404` | `HM_METRICS_TOKEN` is not set | Set `enable_metrics = true` on the module |
| `/metrics` returns `401` | Missing or wrong bearer token | Send `Authorization: Bearer <token>` with the value of the secret `harbormaster/metrics-token` |

## Stale data and throttling

| Symptom | Cause | Fix |
| --- | --- | --- |
| Banner `Data is N min old` for an account and region | The last poll failed. The AWS accounts page shows the error | Read the error and use the rows below |
| Banner `Data is unavailable` | No poll has succeeded yet for that account and region | Same. Check the role and the region first |
| Banner `Slowed by AWS throttling` | AWS throttles Harbormaster's calls in that account and region | It recovers on its own after five clean cycles. For a large estate, raise the `HM_POLL_*` intervals. See [Throttling and limits](throttling-and-limits.md) |
| Stale banner for a short time after a task was replaced | A crashed poller's leases take up to 35 seconds to move | Wait. See [High availability](high-availability.md#failover) |
| Stale banner on every account and `lease: ...` warnings in the log | Pollers cannot reach the database, or no poller runs | Check poller tasks and the database. `harbormaster_lease_slices_owned` should add up to your slice count |
| Log `poller: lease lost, stopping slice` | Another task took the slice, or this task was paused or lost the database | Occasional is normal during a deploy. If frequent, look at database load and CPU starvation. `harbormaster_fenced_writes_rejected_total` rising confirms it |
| Log `lease: renewals stopped, self-fencing` | A task could not renew for 20 seconds | Check database latency and connections |
| Log `lease: claim failed` or `lease: read leases` with an error | A database error during lease handling | Check the error text and the database |
| Pages say `Live refresh unavailable` | The shared store is down, or no poller holds the slice. Normal polling continues | Check `harbormaster_kv_up` and the cache. In a single task without a store this does not occur |
| Log `kv: store is down` | The store failed three commands or a ping | Check ElastiCache. Harbormaster falls back to Postgres and per-task limits. See [High availability](high-availability.md#when-the-store-is-down) |
| Pages are stale by up to 30 seconds after a poller died | The snapshot cache outlives its cluster by up to `HM_SNAPSHOT_CACHE_TTL` | Wait, or set `HM_SNAPSHOT_CACHE=false` to read Postgres directly |

## AssumeRole failures

A poll fails for one account with an AWS error on the AWS accounts page and in the log as `assume role in account <account-id>: ...`. Connecting the account also reports `assume role: ...` per region.

| Error text | Cause | Fix |
| --- | --- | --- |
| `AccessDenied` or `not authorized to perform sts:AssumeRole` | The access role does not trust the Harbormaster task role, the external ID does not match, or the task role's policy does not allow the role ARN | In the target account, check the trust policy: the principal is the web task role (and the legacy role while it exists), and `sts:ExternalId` equals the ID in the Connect account form. In the Harbormaster account, check `access_role_arns` allows the role ARN |
| `account not connected` (`look up role for account ...` or `account <id>: account not connected`) | The account row was removed or disabled | Connect the account again |
| Only some tasks fail, after an upgrade to split services | The poller or coordinator cannot assume `<role_name>-poller` or `<role_name>-coordinator` | Apply the access module in that account with `poller_task_role_arn` and `coordinator_task_role_arn` set. See [Terraform module: harbormaster-access](../reference/terraform-access.md). The role name must match in every account |
| Works for reads, `AccessDenied` on a deploy or restart | The access role was created with `read_only = true`, or `iam:PassRole` does not cover the task role | Set `read_only = false`, or widen `pass_role_arns` to include the task and execution roles |
| `AccessDenied` after you replaced the legacy task role | IAM trust policies match the role's unique ID, not its name. A re-created role with the same name is a different principal | Apply the access module again in each account |
| `ThrottlingException: Rate exceeded` | AWS throttling | See [Throttling and limits](throttling-and-limits.md) |

## Single sign-on

Sign-in failures show on the login page. Detail is in the audit log, not on the page.

| Symptom | Cause | Fix |
| --- | --- | --- |
| `Single sign-on failed. Try again, or ask a global admin to check the audit log.` | Any validation failure at the assertion consumer service. The audit log has an `sso_sign_in_failed` entry with a reason code, and the server log has `saml: sign-in refused` | Read the reason in the audit log and use the next rows |
| `Your identity provider declined the sign-in.` | The identity provider returned a failure status (reason `idp_status`) | Check the application assignment in the identity provider |
| `Single sign-on is turned off.` | SSO is not enabled (`sso_off`) | Turn SSO on, or sign in with a password |
| Reason `issuer`, `destination`, `recipient` or `audience` | The identity provider's entity ID or the service provider URLs do not match. `base_url` is part of the service provider identity | Set the ACS URL and entity ID in the identity provider to the values on the SSO settings page. Changing `base_url` changes both |
| Reason `expired` or `not_yet_valid` | Clock skew between the identity provider and Harbormaster | Fix the clock on the side that is wrong |
| Reason `invalid_signature` or `unsigned` | The identity provider certificate stored in Harbormaster does not match the one that signed the response | Upload the current identity provider metadata or certificate |
| Reason `no_nameid` or `transient_nameid` | The identity provider sends no stable NameID | Set a persistent NameID format in the identity provider |
| Reason `replay` or `unknown_request` | The sign-in took longer than `HM_SAML_REQUEST_TTL` (default 10 minutes), or the browser reused an old response | Start the sign-in again |
| Reason `decrypt_failed` or `sp_key_unavailable`, or the SSO page says `The service provider key cannot be decrypted. Check HM_SESSION_KEY or generate a new certificate.` | `HM_SESSION_KEY` changed since the key was stored | Restore the original key, or generate a new certificate and update the identity provider. See [Backup and restore](backup-and-restore.md#the-key-you-must-keep-hm_session_key) |
| Metadata URL fetch fails when you save SSO | The task fetches the URL itself. The identity provider needs a public `https` address | Use a public URL, or upload the metadata XML |
| Locked out after turning SSO on | No password administrator exists | Run `harbormaster create-admin --username <name> --password-stdin` in the server's environment. Break-glass administrators can still sign in with a password |

## Terminal

| Symptom | Cause | Fix |
| --- | --- | --- |
| `ECS Exec is off for <service>.` | `enable_execute_command` is off on the service | Turn it on in Terraform, or from the service's Deploy tab (this forces a new deployment). The Terminal tab also shows a banner |
| `The ExecuteCommandAgent is not running in container <name>.` | The task started before exec was enabled, or the agent is not running | Replace the task (redeploy) after enabling exec |
| `You already have N open terminal sessions.` | You reached `HM_EXEC_MAX_SESSIONS_PER_USER` (default 3) | Close a session. Idle sessions close after `HM_EXEC_IDLE_TIMEOUT` (20 minutes) |
| `Command must be /bin/sh or /bin/bash.` | The terminal only starts those shells | Use one of them |
| `Task not found.` or `Container not found.` | The task stopped, or you lack access to it | Pick a running task |
| An AWS message from `aws_error`, or `SSM session ended unexpectedly.` | The task role lacks the `ssmmessages:*` permissions, or the access role lacks `ecs:ExecuteCommand` and `ssm:TerminateSession` | Add `ssmmessages:CreateControlChannel`, `CreateDataChannel`, `OpenControlChannel` and `OpenDataChannel` to the task's own role. Apply the access module without `read_only` |
| Session closes after a while | Idle timeout, `HM_EXEC_MAX_DURATION` (4 hours), or the load balancer idle timeout | Raise the Harbormaster limits. The module sets `alb_idle_timeout` to 1800 seconds. See [Terminal](../guides/terminal.md) |
| `The CSRF token is not valid.` | The page's session expired | Reload and sign in again |

## Logs and log search

| Symptom | Cause | Fix |
| --- | --- | --- |
| `You already have 2 live tails open.` | `HM_LOG_TAILS_PER_USER` reached | Close a tail |
| `You already have a log search running.` | One search per user at a time | Wait for it or cancel it |
| `You are searching too fast. Try again in a few seconds.` | Per-user search rate limit | Wait a few seconds |
| Search returns a partial result | It reached `HM_LOG_SEARCH_MAX_GB` (default 10) or the timeout | Narrow the range or services. Raise the cap only if you accept the cost. See [AWS costs](aws-costs.md) |
| `429 log_search_budget_exhausted` | A token used its daily scan budget | Wait until 00:00 UTC, or use the browser, which is not charged |

## Access tokens and the MCP gateway

Access tokens are off until `HM_API_TOKENS=true`. With it unset, `/api/tokens` answers `404` and an `Authorization` header is ignored.

| Symptom | Cause | Fix |
| --- | --- | --- |
| `/api/tokens` returns `404` | `HM_API_TOKENS` is off | Set it in `extra_env` and redeploy |
| `403 token_not_allowed` with the message `This route is not available to access tokens.` | Tokens are read-only and limited to a list of routes, even for an administrator's token | Use a browser session for writes and admin screens |
| `401 unauthenticated` for a token | The token expired, was revoked or the secret was mistyped | Create a new token on My access |
| Harbormaster exits at start after you set `HM_OAUTH` | The log says `HM_OAUTH: needs HM_API_TOKENS=true` or `HM_OAUTH: needs HM_MCP_PUBLIC_URL (the OAuth issuer and resource)` | Set the missing variable. `HM_MCP_PUBLIC_URL` must be an `https://` URL with no path or trailing slash |
| Tokens are accepted on a public load balancer | `internal = false` on the `harbormaster` module while `HM_API_TOKENS` is on | Set `internal = true` and use the gateway for public access. The gateway's WAF and limiter do not protect a public Harbormaster ALB. See [Terraform module: harbormaster-mcp](../reference/terraform-mcp.md) |
| Gateway client IP shows the gateway, not the caller | `HM_MCP_GATEWAY_CIDRS` does not list the gateway's subnets | Set it to the CIDRs of the gateway's private subnets |
| `429` with `Retry-After` | Per-user token rate limit (burst 120, then 2 per second) | Slow the client |
| Gateway returns `502 upstream_unavailable` | The gateway cannot reach Harbormaster | See the [MCP gateway runbook](runbooks/mcp-gateway.md#7-what-a-gateway-outage-looks-like-and-what-to-check) |

## Notifications

| Symptom | Cause | Fix |
| --- | --- | --- |
| `Set HM_SESSION_KEY before adding notification routes.` | No session key, so route secrets cannot be sealed | Set `HM_SESSION_KEY`. The Terraform module does it |
| A route fails with `Enter the destination again.` | The sealed secret cannot be opened (the key changed), or the server has no key | Edit the route and enter the destination again |
| Email routes are unavailable | `HM_SMTP_HOST` is not set | Set the `HM_SMTP_*` variables. See [Alert notifications](../guides/alert-notifications.md) |
| An internal webhook is refused | Private destinations are blocked unless allowed | Add the range to `HM_NOTIFY_ALLOWED_PRIVATE_CIDRS`. Loopback, link-local and metadata addresses are never allowed |
| Log `notify: coordinator lease not confirmed, not sending` | The task lost the coordinator lease while it prepared a send | Normal during a handover. Another task sends |

## Still stuck

1. Check `harbormaster_*` metrics against [Health and metrics](health-and-metrics.md).
2. Read the audit log and the AWS accounts page in the web app.
3. Search the server log for `Warn` and `Error` lines around the time of the problem.
4. See [Support](../community/support.md).
