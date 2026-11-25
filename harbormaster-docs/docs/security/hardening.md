# Hardening checklist

This checklist is for platform engineers taking Harbormaster to production. Each item names the setting and where it is documented.

## Network

- [ ] **Internal ALB.** Set `internal = true` and put the load balancer in private subnets. Needed for `HM_API_TOKENS`.
- [ ] **Allowed CIDRs.** Set `allowed_cidrs` to your VPN or office ranges. `0.0.0.0/0` exposes the login page to the internet.
- [ ] **Deletion protection.** Keep `alb_deletion_protection = true` (the default) and set `db_deletion_protection` for the database.
- [ ] **TLS.** Use an ACM certificate and keep `http_redirect = true`. Keep `HM_COOKIE_SECURE=true`.
- [ ] **MCP gateway.** If you use it, keep it in its own subnets, set `alarm_sns_topic_arns`, `deletion_protection` and `access_logs_bucket`, and tune `waf_rate_limit`. See [MCP gateway](../install/mcp-gateway.md).

## Identity

- [ ] **Single sign-on.** Turn on SAML SSO and keep at least one break-glass admin with a strong password. See [Single sign-on](../install/single-sign-on.md).
- [ ] **Few global admins.** Grant tag permissions instead. Give Terminal, Deploy and Accept risk only where needed.
- [ ] **Session lifetime.** Review `HM_SESSION_IDLE` and `HM_SESSION_ABSOLUTE` against your policy.
- [ ] **Token settings.** Leave `HM_API_TOKENS` off unless you need it. If on, shorten `HM_TOKEN_MAX_TTL` and `HM_TOKEN_MAX_TTL_SSO`, lower `HM_TOKEN_MAX_PER_USER`, and consider `HM_TOKEN_REDACT_ENV=true`.
- [ ] **Terminal limits.** Lower `HM_EXEC_IDLE_TIMEOUT`, `HM_EXEC_MAX_DURATION` and `HM_EXEC_MAX_SESSIONS_PER_USER` if the defaults are too generous. Enable Session Manager logging if you need transcripts.

## AWS access

- [ ] **Tighten `access_role_arns`.** List only the roles you connect.
- [ ] **Read-only where possible.** Use `read_only = true` in accounts that need no changes.
- [ ] **Narrow `pass_role_arns` and `scan_repository_arns`.** See [IAM and AWS access](iam-and-aws-access.md).
- [ ] **External ID.** Use a unique, random value per account.

## Secrets

- [ ] Keep the database password, `HM_SESSION_KEY` and `HM_SMTP_PASSWORD` in Secrets Manager or SSM, referenced by the task definition. Never put them in plain environment values.
- [ ] Use a long random `HM_SESSION_KEY`. Changing it makes sealed values (SSO signing key, notification route secrets) unreadable, so plan a rotation.
- [ ] Use `db_kms_key_id` for a customer-managed database key.
- [ ] Use `bootstrap_admin_password_secret_arn` instead of a setup token in the log if you automate first run, and change the password after first sign-in.

## Container and image

- [ ] **Pin image tags.** Deploy `harbormaster:<version>` built from a release tag. Do not use `latest`.
- [ ] **Non-root, read-only root filesystem.** The shipped images run as non-root. The Terraform modules set `readonlyRootFilesystem = true`. Check with `make check-image`.
- [ ] **Health check.** Keep the image `HEALTHCHECK`.
- [ ] **Vulnerabilities.** Run `make vulncheck` (`govulncheck`) before each release and rebuild when Go or base images get fixes.

## Logging and audit

- [ ] Set `log_retention_days` to meet your policy (default 30).
- [ ] Set `HM_RETENTION_AUDIT_LOG` to match your audit requirements (default one year).
- [ ] Alarm on the metrics in [Health and metrics](../operations/health-and-metrics.md). Protect `/metrics` with `HM_METRICS_TOKEN`.

## AI agent

Skip this section if you do not run the agent (`HM_AGENT_URL` unset).

- [ ] **Internal load balancers.** Harbormaster `internal = true` and the agent's load balancer in private subnets. The Terraform module refuses `HM_AGENT_URL` on a public Harbormaster load balancer; the application does not check, so keep that guard if you deploy without the module. Do not add a public listener to the agent.
- [ ] **Egress is fenced in the application only.** The agent's tasks may reach `0.0.0.0/0` on 443. Provider and Harbormaster calls go to fixed hosts; the AWS credential chain is separate and follows `AWS_ENDPOINT_URL_*` for STS and SSO. For a stricter fence, put your own filter in front of the NAT gateway that allows only your providers and Harbormaster, or use Bedrock only and replace the `task_to_internet` rule with VPC endpoints.
- [ ] **Keep redaction on for external providers.** Lift it only for a provider you accept receiving environment values, and only after you decide that. Secret-looking values are redacted either way. Setting `HM_TOKEN_REDACT_ENV=true` forces full redaction for every provider and overrides any lift. Log scrubbing is best effort: it can miss a secret with an unusual shape, a multi-line list under a secret name in text that is not valid JSON (it never goes past the end of the line), a value that comes before its name outside valid JSON, and a multi-line list under a secret name whose later lines start with `\`, `]` or `}`. See the [security model](security-model.md#what-is-redacted-and-what-leaves-the-network).
- [ ] **Few users.** Turn on AI chat only for users who need it. Review `user_ai_chat` audit entries.
- [ ] **Keys.** Keep provider API keys in Secrets Manager only, one secret per provider. Keys are kept out of logs, errors, audit rows and browser responses by design, with provider error text scrubbed best effort. Rotate them as in the [runbook](../operations/runbooks/ai-agent.md). Give the execution role `kms:Decrypt` if a customer-managed key encrypts them.
- [ ] **Allowance.** Set a monthly allowance you are happy to lose, and lower `limits.max_run_tokens` and `limits.max_steps` if the defaults are generous.
- [ ] **Bedrock.** Use ECS container credentials. Do not set `AWS_ENDPOINT_URL_*` in the agent's environment: the STS and SSO credential clients honour them.
- [ ] **Retention.** Review `HM_RETENTION_AGENT_CONVERSATIONS` (30 days by default) and `HM_RETENTION_AGENT_RUNS` (90 days), which also covers the stored evidence of investigations and reviews.
- [ ] **Agent identity.** Skip this and the next four items if no route has **Investigate with AI** on. Grant the agent identity View only on tags you want investigated, and Logs on fewer. It starts with no access. Review `agent_identity_update` audit entries.
- [ ] **Investigator model.** Choose a provider whose terms you accept for unattended runs over your alerts. Keep redaction on for it. Prefer Amazon Bedrock for sensitive estates.
- [ ] **Destinations.** A diagnosis puts a model's text into each route's destination. Switch the route on only for destinations that treat text as text (no automation that runs what a message says). Check that your webhook receiver accepts the `diagnosis` event.
- [ ] **Allowance and counts.** The investigator spends from the chat's allowance. Keep `HM_AGENT_INVESTIGATIONS_PER_HOUR` low enough that a storm cannot use the month's allowance, or set it to `0` to turn investigations off.
- [ ] **Identity limits.** Keep `HM_AGENT_IDENTITY_LOG_SEARCH_MAX_GB` and `HM_AGENT_IDENTITY_LOG_SEARCH_DAILY_GB` at values whose CloudWatch Logs Insights cost you accept, or set the daily one to `0` to stop its log search.
- [ ] **Coordinator reach.** In split mode, the coordinator's security group must be in `harbormaster_task_security_group_ids` on the agent module, and nothing else needs to be.
- [ ] **Reviews: access.** Skip this and the next three items if you have no scheduled AI review. A review reads what the agent identity may View, whatever its scope says: the scope is a focus, not a boundary. Keep the identity's View to the tags you accept a model provider reading. A review never reads logs, so it needs no Logs grant. Who may read a result depends on the same grant: a non-admin needs View on every tag the identity may View.
- [ ] **Reviewer model.** Choose a provider whose terms you accept for unattended runs over your estate's names, sizes, costs and image findings. Keep redaction on for it. Prefer Amazon Bedrock for sensitive estates.
- [ ] **Reviews: destinations.** A review message puts a model's text into each route's destination, every time it runs. Choose routes whose destinations treat text as text. Check that your webhook receiver accepts the `review` event.
- [ ] **Reviews: allowance and counts.** Reviews spend from the chat's allowance, and are skipped when it would not leave room for a chat run and an investigation. Keep the number of reviews and their cadence in line with the allowance (the editor shows the worst case for a month), and set `HM_AGENT_REVIEWS_PER_DAY=0` to turn reviews off.
- [ ] **Alarms.** Set `alarm_sns_topic_arns` on the agent module.

## Related pages

- [Security model](security-model.md)
- [IAM and AWS access](iam-and-aws-access.md)
- [Data retention](../operations/data-retention.md)
