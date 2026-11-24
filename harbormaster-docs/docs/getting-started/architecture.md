# Architecture

This page describes the Harbormaster components, how data flows between them, and what each depends on. It is for engineers who deploy or operate Harbormaster.

## How it runs

```text
              browsers
                 | HTTPS
                ALB  (targets: web only)
                 |
    +------------+-------------+    +------------------------+    +---------------------------+
    | web                      |    | poller                 |    | coordinator               |
    | API, web app, WebSockets |    | slice loops and alerts |    | one active, the rest wait |
    | deploys, terminal, logs  |    | focus loops            |    | notifications, off-hours, |
    | focus heartbeats         |    | one lease per account  |    | image scans, ARM64 check, |
    | snapshot cache reads     |    | and region             |    | rollups, retention        |
    +---+-----------+----------+    +---+-----------+--------+    +---+-----------+-----------+
        |           |                   |           |                 |           |
        |           +-- Valkey or Redis (optional) -+-----------------+           |
        |        open pages, poll-now and kick signals, shared limits, cache      |
        +------------------------------ Postgres ---------------------------------+
              snapshots, sessions, permissions, audit and the leases (the truth)
```

The default install is one ECS service that runs every part (`role=all`) and needs no store. With `split_services`, the module runs the three roles above as three ECS services of the same image, so polling capacity, API capacity and background jobs scale separately.

## Components

- **One Go binary with an embedded web app.** Harbormaster is one binary (`harbormaster`) that serves a React single-page app embedded in it. There is no separate front-end deployment.
- **Application Load Balancer.** The ALB terminates HTTPS and forwards to the web parts only. You choose internal or internet-facing with the required `internal` input; internal is recommended. See [Deploy with Terraform](../install/deploy-with-terraform.md).
- **PostgreSQL.** The module creates a private, encrypted RDS instance.
- **Valkey or Redis (optional).** A shared store for state that is cheap to lose. Required only for split services.
- **Scanner.** Image scans run in a separate ECS task that Harbormaster starts on demand, from its own image. Off by default.
- **MCP gateway.** An optional, separate, read-only service for AI tools. Off by default. See [MCP gateway](../install/mcp-gateway.md).

## Roles

One image runs in four roles, chosen with `serve --role=<role>` or `HM_ROLE`.

| Role | Serves | Runs |
| --- | --- | --- |
| `all` (default) | The API, the web app and the WebSockets, `/healthz`, `/metrics` | Everything below, in one process |
| `web` | The API, the web app and the WebSockets, `/healthz`, `/metrics` | The bootstrap admin step, manual wake and sleep, "Send test", delivery retry. No polling and no background job |
| `poller` | `/healthz` and `/metrics` only | The slice polling loops under leases, and the idle monitor |
| `coordinator` | `/healthz` and `/metrics` only | The jobs under the coordinator lease (notifications, off-hours, image scans, ARM64 check, outcome log, rollups, retention) and an idle monitor |

Every role migrates the database schema on start. See [Upgrading](../install/upgrading.md).

## Data flow

1. **Polling.** Each account and region (a slice) is polled by exactly one poller task: the one that holds its lease in Postgres. The poller calls AWS and writes snapshots to Postgres.
2. **Reads.** The browser polls `/api` about every 5 seconds. Any web task answers from the snapshots in Postgres. Harbormaster has no push channel.
3. **Direct calls.** Logs, metrics, deploys and terminal sessions call AWS directly from whichever task serves the request.
4. **Authorization.** What a user sees and does comes from their tags and permissions. The server checks them on every request, including the WebSocket endpoints for the terminal and live logs.
5. **Background jobs.** The task that holds the coordinator lease runs notifications, off-hours reconciliation, image scans, the ARM64 check, rollups and retention. A web task can ask the coordinator to run a job now (a saved schedule, a changed route, a scan request) over the shared store. Without a store, or if nobody hears it, the job runs at its own interval.

## Postgres is the truth

Postgres holds snapshots, sessions, users, permissions, tags, the audit log, alerts, the notification queue and the leases. Any web task can serve any API request because all of this is in the database. Every write a poller makes for a slice checks that its lease is still current.

The database is not a source of truth for ECS itself. AWS is. Snapshots can be rebuilt by polling again.

## Optional Valkey or Redis

The shared store (`HM_KV_URL`) holds only state that is cheap to lose:

- the set of open pages and when each was last read,
- poll-now and kick signals,
- shared limits (terminal, live-tail and log-search sessions, rate limits),
- with the snapshot cache on, generation tokens and cached read models.

Every key expires. The store never holds sessions, users, permissions, tags, snapshots, alerts, the notification queue or leases. No write waits on it.

If the store is down, polling, alerts, notifications, deploys and sign-in carry on unchanged. Page reads go straight to Postgres, live pages stop (the page says `Live refresh unavailable`), and limits are counted per task. Valkey 7.2 and Redis 7.0 or newer both work.

Only `role=all` runs without a store. The `web`, `poller` and `coordinator` roles refuse to start without `HM_KV_URL`.

## AWS access: AssumeRole with an external ID

Harbormaster's ECS task role may only call `sts:AssumeRole`, into the access roles you list. In each managed account, the `harbormaster-access` role trusts Harbormaster's task role and requires an external ID. The external ID is generated per account in the web app.

With split services, each managed account has three roles from the same role name: `harbormaster-access`, `harbormaster-access-poller` and `harbormaster-access-coordinator`. A poller assumes the `-poller` role and a coordinator the `-coordinator` role. Harbormaster derives both from the one ARN you enter. A `role=all` or `web` task uses the stored role itself.

The access role covers ECS, EC2, EFS, ECR, CloudWatch, Logs, Application Auto Scaling, ELBv2 and SSM. See [Connect AWS accounts](../install/connect-aws-accounts.md) and [IAM and AWS access](../security/iam-and-aws-access.md).

## Polling model

Harbormaster polls on tiers, with intervals you can set (for example services every 30 seconds and full task details every 5 minutes by default). It stays inside AWS API limits and backs off when throttled. After a period without any open page or token activity, polling slows to an idle interval. See [Throttling and limits](../operations/throttling-and-limits.md) and the [configuration reference](../reference/configuration.md).

Leases are spread by weight across the poller tasks. A task that stops releases its leases and another takes them within a tick. A task that dies is replaced within about 35 seconds, and the stale banner covers that gap.

## Embedded web app

The React app is built at image build time and embedded in the binary. Browsers get it from the web part of the same process that serves the API. Security headers (CSP, HSTS, `X-Frame-Options: DENY`) are set by the server. See [Frontend](../development/frontend.md) if you work on it.

## MCP gateway

`harbormaster-mcp` is a thin, read-only translator. It speaks the Model Context Protocol on `POST /mcp`. Each tool call becomes one read request to Harbormaster's own API, carrying the caller's own access token. It holds no credentials, no database or cache access and no AWS permissions. It is the only public piece: Harbormaster stays on an internal load balancer. See [MCP gateway](../install/mcp-gateway.md) and [AI access with MCP](../guides/ai-access-mcp.md).

## AI agent

The optional AI agent (`harbormaster-agent`) answers chat questions about the estate. It is a separate service on its own internal load balancer, off unless `HM_AGENT_URL` is set.

```text
 browser --> web (chat API, saves messages) --HTTPS--> agent LB --> agent tasks
                |                                                     |   |
                |  internal ALB  <---- read-only run token -----------+   +--> model provider
                +-- Postgres: conversations, runs, allowance            (Anthropic, OpenAI, Google,
                                                                         or Bedrock through the task role)
 coordinator: repairs interrupted runs and writes their audit rows
```

For each question, a web task reserves the worst-case cost from the monthly allowance, mints a read-only run token for the asking user, and streams the agent's answer to the browser. The agent loops between the model and the shared read-only tools. Each tool call is one read request to Harbormaster's internal load balancer, carrying the run token. The agent has no database, cache, SAML or AWS estate access. Provider keys live only in its environment. The coordinator repairs runs that were interrupted. See [AI chat](../guides/ai-agent-chat.md), [Install the AI agent](../install/ai-agent.md) and the [security model](../security/security-model.md#ai-agent).

## Scanner

Image scanning runs Syft and Grype in a separate ECS task that the coordinator starts on demand. The scanner has its own image, its own execution role, a private S3 bucket for jobs and results, and a scan role in each connected account that can only pull images from ECR. It has no database URL, session key or access role. See [Image security](../guides/image-security.md).

## Next steps

- [Prerequisites](../install/prerequisites.md)
- [Split services](../install/split-services.md)
- [High availability](../operations/high-availability.md)
