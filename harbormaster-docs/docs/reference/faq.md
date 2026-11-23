# FAQ

Short answers to common questions, each grounded in how Harbormaster works today. Use it before you open an issue; links go to the full pages.

## Scope and requirements

### What does Harbormaster manage?

AWS ECS: clusters, services, tasks and task definitions. It also reads ECR images, EFS file systems, VPCs and CloudWatch metrics and logs. It manages ECS in every AWS account and region you connect. See [What is Harbormaster](../getting-started/what-is-harbormaster.md).

### Can it manage EKS or Kubernetes?

No. The code talks to ECS, ECR, EFS, EC2 networking, CloudWatch, Application Auto Scaling, ELBv2 and SSM. It has no EKS or Kubernetes support.

### Does it need a database?

Yes. Postgres is required (`HM_DATABASE_URL` or `HM_DATABASE_SECRET_ARN`). It holds snapshots, sessions, users, permissions, the audit log, notification queues and the leases. The Terraform module creates an RDS instance. See [Prerequisites](../install/prerequisites.md).

### Does it need Valkey or Redis (the store)?

No, not for the default install. A single `role=all` task runs without it, and limits are counted per task. The store is required for split services, and it adds estate-wide limits, live-refresh signals and the snapshot cache. If it goes down, polling, alerts, deploys and sign-in keep working. See [High availability](../operations/high-availability.md).

### Can I run more than one task?

Yes. Several `all` tasks are safe with or without a store, because leases decide which task polls which account and region. For separate web, poller and coordinator services, use split services, which need `HM_KV_URL`. See [Split services](../install/split-services.md).

### Does it work with Fargate and EC2 capacity?

It reads and changes ECS services of any launch type. The cost estimates cover Fargate (on-demand and Spot) only; tasks on EC2 or other capacity are counted as "not priced". See [AWS costs](../operations/aws-costs.md).

### Can I try it without AWS?

Yes. `harbormaster serve --fake-aws` (or `make run`) serves a fake estate with demo users and data. Use it only locally. See [Quickstart: local](../getting-started/quickstart-local.md).

## Access and security

### How do I create the first admin?

Three ways: read the one-time setup token from the server log, set a Secrets Manager secret for the bootstrap `admin` password, or run `harbormaster create-admin --username <name>`. See [First run](../install/first-run.md).

### I lost access. How do I get back in?

Run `harbormaster create-admin --username <name>` in the same environment as the server. It needs only the database settings and creates a break-glass admin that can sign in with a password even when single sign-on is on. See the [CLI reference](cli.md#create-admin).

### Why do I get 404 instead of 403 for something I know exists?

If you lack View on the tag of a resource, Harbormaster answers 404 so it does not reveal that the resource exists. A 403 means you can see it but lack the action. See [Permissions](permissions.md).

### Why does my access token get `403 token_not_allowed`?

Tokens are read-only and can call only an explicit list of read routes. Writes and all admin screens are refused, even for an admin's token. See the [API reference](api.md#bearer-access-token).

### How do I revoke every token after an incident?

Run `harbormaster revoke-tokens --all`, or `--user <name>` for one user. Turning `HM_API_TOKENS` off stops tokens working but deletes nothing. See the [CLI reference](cli.md#revoke-tokens).

### Does it support single sign-on?

Yes, SAML 2.0, configured by a global admin in the web app. It needs `HM_SESSION_KEY` set. Keep at least one break-glass admin. See [Single sign-on](../install/single-sign-on.md).

### Can I give someone read-only access to one team's services?

Yes. Create a tag, assign it to the team's clusters or services, and grant the user the Viewer preset (or Operator for restart and scaling) on that tag. See [Users, tags and permissions](../guides/users-tags-permissions.md).

### Can Harbormaster change my AWS resources if it is hacked or misused?

Its reach is limited by the IAM access role in each account. Set `read_only = true` on the `harbormaster-access` module to create a role that cannot register task definitions, update services, stop tasks, open exec sessions or pass roles. See [IAM and AWS access](../security/iam-and-aws-access.md).

## Operations

### Why is the data "N min old"?

The last poll for that account and region failed. The banner shows the age and the last error. Check the access role, AWS throttling and the poller's logs. See [Troubleshooting](../operations/troubleshooting.md).

### Will Harbormaster raise my AWS bill?

It reads AWS constantly, and each call is a CloudTrail management event in the polled account. Record write-only events on billed trails, lengthen intervals for large estates, and review Logs Insights and Live Tail use. See [AWS costs](../operations/aws-costs.md).

### How do I reduce polling load?

Raise the `HM_POLL_*` intervals, lower `HM_POLL_IDLE_AFTER`, and keep `HM_FOCUS_MAX_OBJECTS` low. Polling goes idle when nobody uses the UI, but an open dashboard keeps it at full speed. See [Throttling and limits](../operations/throttling-and-limits.md).

### How long is data kept?

Per type, by `HM_RETENTION_*` settings. Defaults: stopped tasks and task-count history 7 days, service events 30 days, the operations log and deployment history 90 days, the audit log and daily availability and capacity rollups one year. See the [configuration reference](configuration.md#retention) and [Data retention](../operations/data-retention.md).

### What do I back up?

Postgres. It is the source of truth. The store holds only state that is cheap to lose. See [Backup and restore](../operations/backup-and-restore.md).

### Does it export metrics?

Yes, Prometheus metrics at `/metrics` when `HM_METRICS_TOKEN` is set. See [Metrics](metrics.md).

### What happens if I change `HM_SESSION_KEY`?

Stored notification route secrets become unreadable (routes show "Enter the destination again") until you re-enter them. The SSO signing key is also encrypted under it. Treat it as a long-lived secret.

## Features

### Can I roll back a deploy?

Yes. A rollback deploys an earlier task definition revision from the service's revisions list, and it passes the image gate. See [Roll back](../guides/roll-back.md).

### How does the terminal work?

Through ECS Exec. The service needs ECS Exec on and the task role needs the `ssmmessages` permissions. Harbormaster does not record keystrokes; it records who opened a session, where, and for how long. See [Terminal](../guides/terminal.md).

### Can AI assistants use it?

Yes, read-only, through the MCP gateway or with an access token. The gateway has no tool that changes anything, and it acts as the token's user. See [AI access](../guides/ai-access-mcp.md).
