# Glossary

Terms used across the Harbormaster product and its documentation, in alphabetical order. Use it when a page uses a word you do not know.

## A

**Access role**
: The IAM role (`harbormaster-access`) that Harbormaster assumes in each managed AWS account. Created by the `harbormaster-access` Terraform module. With split services each account also has `-poller` and `-coordinator` variants. See [Connect AWS accounts](../install/connect-aws-accounts.md).

**Access token**
: A read-only secret that starts with `hmp_`, created by a user for scripts and AI tools. It sees what its user sees and can call only a list of read routes. See [Access tokens](../guides/access-tokens.md).

**Account**
: In Harbormaster, an AWS account that a global admin has connected. Not to be confused with a user account.

**Action**
: One of seven permission bits granted on a tag: View, Logs, Operate, Deploy, Edit task definition, Terminal, Accept risk. See [Permissions](permissions.md).

**Alert**
: A failure Harbormaster derives from polled data, such as a failing service, a restart loop or a failed deployment. Alerts show on the Overview and can be sent to notification routes.

**Audit log**
: The record of administrative and security-relevant actions (who, what, when). Kept for `HM_RETENTION_AUDIT_LOG` (one year by default).

## B

**Base image**
: The image a repository is built from, set per repository on the Security page. Harbormaster compares scanned layers against it. Config search can match it with `base:`.

**Bootstrap admin**
: The first `admin` user, created from a Secrets Manager secret when `HM_BOOTSTRAP_ADMIN_PASSWORD_SECRET_ARN` is set.

**Break-glass admin**
: A global admin who can sign in with a password while single sign-on is on. At least one is required before SSO can be turned on.

## C

**Change set**
: A reviewed group of changes to one service (task definition, service settings, scaling) applied in one request, with a reason. See [Deploy a change](../guides/deploy-a-change.md).

**Cluster**
: An ECS cluster. Each cluster has one effective tag.

**Coordinator**
: The role that runs background jobs: notifications, off-hours, image scans, the ARM64 check, rollups and retention. Many coordinator tasks can run; only the one holding the coordinator lease is active.

## D

**Data age**
: How old the displayed data is. A banner shows it when the last poll for an account and region failed.

**Deploy tab**
: The service page tab where task definition, service and scaling changes are drafted and reviewed.

**Dev tag**
: The built-in default tag. Clusters and services with no assigned tag fall back to it. It cannot be deleted or renamed.

## E

**ECS Exec**
: The AWS feature behind the Terminal tab. It opens a shell in a running task through SSM Session Manager. It must be enabled on the service.

**Effective tag**
: The tag that decides access to a cluster, service or task: its own, or the nearest one it inherits. See [Permissions](permissions.md#how-tags-match-clusters-services-and-tasks).

**External ID**
: A value Harbormaster generates for each connection. The access role's trust policy requires it, which protects against the confused-deputy problem.

## F

**Fake AWS mode**
: `serve --fake-aws`. Serves an in-memory ECS estate with demo data and needs no AWS credentials. For trying the product and for tests only.

**Fenced write**
: A poller database write that checks, in the same transaction, that its lease epoch is still current. A paused task that lost its lease finds its write rejected. Counted by `harbormaster_fenced_writes_rejected_total`.

**Focus**
: Live refresh. A page open in a visible tab is read from AWS every `HM_FOCUS_INTERVAL` (five seconds by default) instead of waiting for the normal poll.

## G

**Global admin**
: A user who holds every action on every tag and can manage users, tags, accounts, SSO, schedules and notification routes.

**Gate (pre-deploy scan gate)**
: An optional check that scans a new image before a deploy and blocks it according to the service's scan setting. Enforced on the server.

## I

**Idle mode**
: A slower polling pace used when nobody has used the UI for `HM_POLL_IDLE_AFTER`.

## K

**KV store**
: The optional Valkey or Redis store (`HM_KV_URL`). It holds only state that is cheap to lose: open pages, signals, shared limits and the snapshot cache. Required for split roles.

## L

**Lease**
: A row in Postgres that gives one poller task the right to poll one account and region (a slice), or gives one coordinator task the right to run the background jobs. Held for 30 seconds and renewed every 10.

**Log search**
: A CloudWatch Logs Insights query over up to 20 services at once. See [Log search](../guides/log-search.md).

## M

**MCP gateway**
: `harbormaster-mcp`, a small public, read-only service that speaks the Model Context Protocol and forwards each tool call to Harbormaster with the caller's own token. It holds no credentials.

**Member**
: A running Harbormaster process (a task) that heartbeats into the database. `harbormaster_poll_members` counts them by role.

## N

**Non-production tag**
: A tag with the Non-production flag. Only services under such tags are offered Fargate Spot suggestions.

**Notification route**
: One destination (Slack, Telegram, PagerDuty, webhook or email) with a scope of what to send. Managed by global admins.

## O

**Off-hours schedule**
: A weekly grid that puts ECS services carrying a given AWS tag to sleep (capacity zero) and wakes them. See [Scaling and off-hours](../guides/scaling-and-off-hours.md).

**Operations log**
: The per-service record of changes made through Harbormaster (deploys, restarts, scaling, terminal sessions).

## P

**Permission**
: The set of actions a user holds on a tag.

**Poller**
: The role that polls ECS and related services for the slices it holds leases on and writes snapshots to Postgres.

**Preset**
: A named bundle of actions: Viewer, Operator, Deployer, Full. See [Permissions](permissions.md#presets).

## R

**Read-only account**
: An account connected with a `read_only` access role. Harbormaster can inspect it but not change it.

**Role (Harbormaster)**
: Which parts a process runs: `all`, `web`, `poller` or `coordinator`. Chosen with `serve --role` or `HM_ROLE`. Not to be confused with an IAM role.

## S

**Scanner**
: The `harbormaster-scanner` task. It runs on ECS, pulls ECR images, builds an SBOM with Syft and matches vulnerabilities with Grype.

**Setup token**
: A one-time token logged on first start when no user exists. It lets the person who can read the log create the first admin.

**Slice**
: One account and region. The unit of polling and of leases.

**Snapshot**
: The latest polled state of the estate, stored in Postgres. The web app reads snapshots, not AWS, for list pages.

**Snapshot cache**
: Versioned read models of snapshots kept in the KV store to take load off Postgres. Off without a store.

**Split services**
: Running the web, poller and coordinator roles as separate ECS services so they scale independently. Needs the KV store.

**SSO**
: Single sign-on through SAML 2.0. See [Single sign-on](../install/single-sign-on.md).

## T

**Tag**
: A Harbormaster object that groups clusters and services for permissions. Separate from AWS resource tags, though off-hours schedules select services by AWS tag.

**Tier**
: One kind of polling work, for example `services` or `runningTasks`, each with its own interval.

## W

**Web (role)**
: The role that serves the API, the web app and the WebSockets.
