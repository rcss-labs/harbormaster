# Concepts

This page defines the terms used throughout the Harbormaster docs. Read it once before you install, or come back to it when a term is unfamiliar.

## Accounts and regions

An **account** is an AWS account that Harbormaster manages. A global admin connects each one under **AWS accounts**. Harbormaster reads and changes its resources by assuming an IAM role in that account (see [Connect AWS accounts](../install/connect-aws-accounts.md)).

An account is polled per **region**. The pair of account and region is called a **slice**. It is the unit that polling, leases and the "last poll" age are tracked by.

## Clusters, services and tasks

These map directly to ECS:

| Harbormaster | ECS |
| --- | --- |
| Cluster | ECS cluster |
| Service | ECS service |
| Task | ECS task and its containers |
| Task definition | One task definition family per service, with its revisions |
| Logs | CloudWatch Logs (`awslogs` driver) |
| Metrics | CloudWatch Container Insights and load balancer metrics |
| Terminal | ECS Exec |
| Storage | EFS file systems and access points |
| Networks | VPCs, subnets and task network interfaces |

A task that belongs to no service (a one-off `RunTask`, a scheduled task, a migration) uses its cluster's tag for every permission.

## Tags and permissions

A **tag** is a Harbormaster-internal environment label such as Dev, Staging or Prod. Tags are stored in Harbormaster only. They are not AWS tags.

- Every cluster has exactly one tag. An untagged cluster counts as Dev.
- A service either has its own tag or inherits its cluster's tag. Its own tag wins. This is the service's **effective tag**.

Users get permissions **per tag**. There are seven actions:

| Action | Letter | Allows |
| --- | --- | --- |
| View | V | See clusters, services, tasks, metrics, task definitions and the operations log |
| Logs | L | Read logs, history and live tail |
| Operate | O | Restart, change desired count, stop a task |
| Deploy | D | Deploy or roll back a registered revision |
| Edit task definition | E | Register a new task definition revision |
| Terminal | T | Open an ECS Exec session into a task |
| Accept risk | R | Accept an image vulnerability finding for a service |

Every action requires View on the same tag. Presets fill the boxes: **None**, **Viewer** (V), **Operator** (V, L, O), **Deployer** (V, L, O, D, E) and **Full** (V, L, O, D, E, T). Accept risk is in no preset. Anything else shows as Custom.

A **global admin** can do everything on everything: create tags, assign them, manage users, connect accounts and read the audit log.

A service a user cannot see is hidden everywhere: lists, counts, alerts, search and the network tree. The server checks permissions on every request, including the WebSocket endpoints for the terminal and live logs. See [Users, tags and permissions](../guides/users-tags-permissions.md) and the [permissions reference](../reference/permissions.md).

## Roles: web, poller, coordinator

One image runs in four **roles**, chosen with `serve --role=<role>` or `HM_ROLE`:

| Role | What it does |
| --- | --- |
| `all` (default) | Everything in one process. Needs no extra store. |
| `web` | The API, the web app and the WebSockets. No polling, no background jobs. |
| `poller` | Polls AWS for the slices it holds leases on. Serves only `/healthz` and `/metrics`. |
| `coordinator` | Runs background jobs: notifications, off-hours, image scans, the ARM64 check, rollups and retention. One task is active; the rest wait. |

The default install is one ECS service with role `all`. The split roles are for larger installs; see [Split services](../install/split-services.md).

## Snapshots

A **snapshot** is the last state of a slice that a poller read from AWS and wrote to Postgres. Pages read snapshots, not AWS, so any task can answer them. Logs, metrics, deploys and terminal sessions are different: they call AWS directly from whichever task serves the request.

Every response carries the time of its data, so the app can show "last poll 3 s ago" and a stale banner when polling falls behind.

## Leases

A **lease** is a row in Postgres that says which poller task polls which slice. Exactly one task holds each slice's lease at a time. A task claims, renews and gives back leases on a short tick. If a task stops without releasing, another task takes over after the lease expires. Every poller write checks that the lease is still current, so a paused task cannot overwrite the new owner's data.

## Focus

**Focus** is the live refresh of the page you have open. While you view a cluster or service page, a poller refreshes those objects faster than the normal polling tiers and the page shows it is live. In a single `all` task, focus works in memory. With split roles, the web task tells the poller which pages are open through the shared store (Valkey or Redis), so the store is required. See [Architecture](architecture.md).

## Change sets

A **change set** is a group of edits to a service that you review and apply together. On the service's Deploy tab you can change containers, task size, service settings and auto scaling. Harbormaster shows the diff, asks for a reason, applies the change, and shows the rollout. The change is written to the operations log and the audit log.

See [Deploy a change](../guides/deploy-a-change.md).

## Next steps

- [Architecture](architecture.md)
- [Glossary](../reference/glossary.md)
