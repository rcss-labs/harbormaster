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

## Related pages

- [Security model](security-model.md)
- [IAM and AWS access](iam-and-aws-access.md)
- [Data retention](../operations/data-retention.md)
