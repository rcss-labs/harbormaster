# AWS costs

Harbormaster is cheap to run, but it reads AWS constantly, and some of what it triggers is billed in the connected accounts, not in the Harbormaster account. This page lists the cost drivers, how to reduce them and how to measure them. Read it before you connect a large estate.

Prices below are the ones AWS published when this page was written. Check the current AWS pricing pages before you rely on them.

## What drives cost

### CloudTrail

Every poll is an AWS API call, and every call is a CloudTrail management event in the account and region that is polled. The first trail that copies management events to S3 is free. Each additional copy is billed per event ($2 per 100,000), as are events delivered to CloudWatch Logs (per GB ingested) and to CloudTrail Lake or Security Lake (per GB ingested). An organisation trail plus a per-account trail, or a trail that feeds CloudWatch Logs, can turn polling into a large bill.

- Harbormaster only reads. On any trail you pay for, record write-only management events: `ReadWriteType: WriteOnly` on the event selector, or `readOnly = false` in an advanced selector. That drops almost all of Harbormaster's events and keeps every change.
- Call volume grows with the number of clusters, services (10 per `DescribeServices` call), target groups (one `DescribeTargetHealth` each) and with every account and region you connect.

### Polling cadence

The defaults suit a UI that someone is looking at: 30 seconds for services and running tasks, 60 seconds for stopped tasks and target health, 5 minutes for the rest. When nobody has the UI open for `HM_POLL_IDLE_AFTER` (10 minutes), every tier slows to `HM_POLL_IDLE_INTERVAL` (5 minutes).

Only a visible browser tab counts as activity, because background tabs stop polling. A dashboard left open on a screen keeps Harbormaster at full speed. Raise the intervals or lower `HM_POLL_IDLE_AFTER` through the module's `extra_env` if the cost is still too high. The variables are in [Throttling and limits](throttling-and-limits.md#polling-intervals).

### Live pages

A cluster or service page left open in a visible tab is read every 5 seconds (`HM_FOCUS_INTERVAL`). That is about 3 to 4 AWS calls per cycle, about 75,000 read events a day, or about $1.50 a day per always-open page on a trail that bills management events. Pages in a hidden tab cost nothing. `HM_FOCUS_MAX_OBJECTS` and `HM_FOCUS_MAX_PER_USER` bound the total.

### Logs Insights

Log search is billed at $0.005 per GB scanned. Each search stops at `HM_LOG_SEARCH_MAX_GB` (10 GB, about $0.05) and starts on a 1 hour range.

### Live tail

CloudWatch Logs Live Tail is billed per session-minute. Each user can hold `HM_LOG_TAILS_PER_USER` (2) tails, and an unwatched tail closes after `HM_LOG_TAIL_IDLE` (30 minutes).

### Metrics

Charts call `GetMetricData`, billed per metric requested. Results are cached for `HM_METRICS_CACHE_TTL` (60 seconds) and shared by every viewer. The daily utilisation rollup (`HM_ANALYTICS_UTIL`, on by default) makes one batch per account and region a day.

### Infrastructure in the Harbormaster account

The Terraform module creates the following billable resources. Sizes are the defaults.

| Resource | Default | How to change |
| --- | --- | --- |
| ECS Fargate tasks | One task of 512 CPU units and 1024 MiB, or the web, poller and coordinator services | `cpu`, `memory`, `web_*`, `poller_*`, `coordinator_*` |
| RDS PostgreSQL | `db.t4g.micro`, 20 GiB gp3, single AZ, 7 days of backups | `db_instance_class`, `db_allocated_storage`, `db_multi_az` |
| Application Load Balancer | One | None |
| ElastiCache (optional) | `cache.t4g.micro`, no replicas | `cache_node_type`, `cache_replicas` |
| NAT gateway or VPC endpoints | Yours | Outside the module |
| Secrets Manager | Three or four secrets | None |
| CloudWatch Logs | 30 days of retention | `log_retention_days` |
| Image scanning (optional) | A scanner Fargate task per requested scan, an S3 bucket | `enable_image_scanning`, `HM_SCAN_*` |
| AI agent (optional) | Two Fargate tasks of 512 CPU units and 1024 MiB, an internal load balancer, a log group, and the model provider's charges | `desired_count`, `cpu`, `memory` in the [agent module](../reference/terraform-agent.md) |

### AI agent and model spend

The agent's infrastructure is small. The coordinator calls it too, for investigations. The model spend is the part to watch, and it is billed by your provider, not by AWS (unless you use Amazon Bedrock, where it appears on your AWS bill).

- A question costs the tokens of every model call it makes. The tool definitions alone are about 10,000 input tokens on each call, and a question can make several calls, so even short questions use real tokens.
- An admin sets a monthly allowance in **Settings > AI agent**. Harbormaster reserves a question's worst case (the run token limit at the model's higher price) before it starts and refuses the question when the allowance cannot cover it. The month is the UTC calendar month. An interrupted question can be charged more than it used, so the allowance can run ahead of the provider's bill.
- Prices come from the catalogue you give the agent. Harbormaster hardcodes none, so update `model_providers` when a provider changes its prices, or the allowance will be counted at the old ones.
- **Automatic investigations spend from the same allowance.** Each investigation is one run with the chat's per-run limits, at most two runs per investigation (the first and one retry), and the worst case of each is reserved before it starts. A run makes several model calls, and the tool definitions alone are about 10,000 input tokens on each one. Cost per investigation is listed in **Settings > AI agent**. Counts, not money, limit them: `HM_AGENT_INVESTIGATIONS_PER_HOUR` (20 by default) and `HM_AGENT_INVESTIGATIONS_CONCURRENT` (2). A storm of alerts can use up the month's allowance, and then the chat refuses questions as well. Set `HM_AGENT_INVESTIGATIONS_PER_HOUR` low or `0` if you cannot accept that. See the [runbook](runbooks/ai-agent.md#10-an-alert-storm).
- **Log search by the agent identity costs CloudWatch.** When the identity holds Logs, its log searches run CloudWatch Logs Insights in your accounts, billed by data scanned, up to `HM_AGENT_IDENTITY_LOG_SEARCH_MAX_GB` a search (2 GiB) and `HM_AGENT_IDENTITY_LOG_SEARCH_DAILY_GB` a UTC day (20 GiB), plus any global `HM_LOG_SEARCH_MAX_GB`. Set the daily value to `0` to stop it.
- Bedrock: charges appear under Amazon Bedrock in Cost Explorer for the region in `bedrock_region`.
- Lower `limits.max_run_tokens` and `limits.max_steps` to cap one question. Choose a smaller model for chat if answers are good enough.

### Cost tab estimates

The Cost tab in the web app estimates the monthly run rate of running Fargate tasks from the latest poll. It is an estimate, not a bill, and it leaves out load balancers, data transfer and storage above 20 GB. Harbormaster calls no AWS pricing API. Prices are tables in the source, so a price change needs a new release.

## Measure it

- `harbormaster_aws_api_calls_total` on `/metrics` counts every AWS call by account, region, service, operation, result and source. The `source` label is `tier`, `focus`, `request` or `job`, so you can see what each call was for.
- `harbormaster_poller_idle` is `1` while the poller is idle.
- In the billed account, open Cost Explorer, filter to CloudTrail and CloudWatch, and group by usage type. That shows which of the charges above you pay.

## Next steps

- [Throttling and limits](throttling-and-limits.md) for the intervals and caps.
- [Health and metrics](health-and-metrics.md) for scraping `/metrics`.
