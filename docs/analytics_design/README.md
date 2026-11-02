# Handoff: Harbormaster Analytics

## Overview
A new **Analytics** section for clusters and services. Most panels read only tables Harbormaster already stores, within their current retention. Dashed "Proposed" panels need one of four small additions (about 48 MB after a year in total) listed below.

Sign-ins and admin changes are out of scope on purpose.

## About the design files
`Harbormaster Analytics.dc.html` is a **design reference built in HTML**. Open it in a browser with `support.js` and `web/` beside it. Rebuild it in `web/src` with React, react-router, TanStack Query, CSS modules and `tokens.css`, following the shell and tokens from the main redesign handoff (`design_handoff_harbormaster_redesign`). All numbers in the prototype are sample data.

## Fidelity
High fidelity: colours, type, spacing, chart style and interactions are final.

## Navigation
- Sidebar order: Overview, Clusters, Tasks, Networks, Storage, **Analytics (AN)**, Settings.
- Route: `/analytics/:tab` with `?tag=&range=`.
- Tabs: Delivery, Reliability, Capacity, Operations. Each tab label shows its data window (90d, 7d, "live + 7d", 90d) in dim mono.
- Header filters:
  - **Tag:** All / Prod / Staging / Dev.
  - **Range:** 24h / 7d / 30d / 90d.
  - Range is dimmed on Capacity, where it doesn't apply.
- **Notice bar** (amber):
  - On Reliability, a range longer than the 7-day retention is clamped to 7 days and the bar says so.
  - Capacity always shows a bar explaining that it uses the latest poll.

## Panel anatomy
- **Header (38px):** optional "PROPOSED" badge (violet `#a78bfa`, border `#33295a`), title, sub, legend on the right (3px colour bar, label, mono value).
- **Footer:**
  - `source <table> · kept N` (mono 10.5px, `#0e1318` background), shown when the "show sources" setting is on.
  - Proposed panels instead show `needs new data <table> · size · how`.
- **Proposed panels:** dashed border `#3d3466`, background `#100f18`, chart opacity reduced.
- Each tab ends with a "Data used on this page" table listing table, what is read, retention, and the setting that controls it.

## Charts (no bar charts)
- **Line / area:** 150px plot, 5 horizontal gridlines (`#1a2028`, bottom `#2a333e`), y labels at the max and the midpoint. Strokes are 1.6px; area fill opacity is 0.12.
  - Optional dashed reference line (for example 5% failure rate, 99.5% availability) labelled at the right.
- **Stacked area:** fill opacity 0.42 with a 1.25px top stroke per series. Grey series mark expected events.
- **Hover:** crosshair line and point dots. Tooltip with the time ("3h ago"), each series value and, when stacked, the total. It flips side past 60% of the width.
- **Donut:** 132px, 16px ring, total in the centre, with a legend table beside it (value and %).
- **X axis:** three mono labels (start, middle, now).
- Buckets: 24h → hourly, 7d → 4h, 30d → daily, 90d → 2 days.
- Tables use the redesign's DataTable with in-cell mini bars (56×6px).

## Tabs and the queries behind them
### Delivery (`deployment_history`, 90 days)
- KPIs:
  - Rollouts and rollouts per day.
  - Change failure rate: failed ÷ (completed + failed).
  - Median rollout time (`finished_at − started_at`) and p90.
  - Time to recover: median from a failed rollout to the next completed rollout of the same service.
  - Rollbacks: deploys to a lower revision.
  - Superseded rollouts.
- Charts:
  - Rollouts stacked by outcome.
  - Change failure rate line (5% reference).
  - Median and p90 duration lines.
- Table: services by change failure rate.
- *Proposed (failure_reason):* "Why rollouts failed" stacked area, and recent failed rollouts with their reason.

### Reliability (stopped `tasks` 7 days, `service_counts_history` 7 days, `service_events` 30 days)
- KPIs:
  - Availability: share of 5-minute samples with running ≥ desired.
  - Time below desired.
  - Task stops and unexpected stops (anything other than deployment or scale-in).
  - Out of memory.
  - Placement failures: service events containing "unable to place".
- Charts:
  - Availability line (99.5% reference).
  - Services below desired.
  - Task stops by `stop_reason_group`, stacked.
  - Unexpected stops by `availability_zone`.
  - Container exit codes, stacked.
- Table: least available services.
- *Proposed (service_availability_daily):*
  - Availability over 90 days.
  - Time below desired over 90 days.
  - Services getting less available (last 30 days compared with the 30 before).

### Capacity (latest poll and `service_counts_history`)
- KPIs:
  - Reserved vCPU and memory: sum of running task `cpu` / `memory`.
  - Running tasks.
  - Spot share.
  - Services at their scaling max: `desired = max_capacity`.
  - Subnets with less than 10% of IPs free.
- Chart: reserved vCPU over the last 7 days, stacked by tag. It is estimated as counts history × each service's current task definition cpu.
- Donuts: reserved vCPU by account, tasks by capacity provider.
- Tables:
  - Clusters by reserved vCPU.
  - Services at their auto-scaling max, with how long (from counts history).
  - Subnets low on IPs.
- *Proposed:*
  - Reserved vs used vCPU over 90 days, and Spot share over 90 days (cluster_capacity_daily).
  - Memory reserved vs used p95, and most over-reserved services (service_util_daily).

### Operations (`operation_log` with source = harbormaster, 90 days; `revision_records`)
- KPIs: operations, revisions registered, scale changes, restarts, tasks stopped by hand, terminal sessions.
- Charts:
  - Operations stacked by type.
  - Hands-on operations: restart, stop and terminal lines.
  - Restarts per 100 deploys.
- Tables: who operates, most restarted services.

## Proposed data (keep it small: daily rollups only)
| # | Addition | Built from | Rows/day | Kept | Size |
| --- | --- | --- | --- | --- | --- |
| 1 | `service_availability_daily` (samples, samples_ok, minutes_below, unexpected_stops) | nightly rollup of service_counts_history + stopped tasks, run before retention deletes them | ~600 | 365 days | ~35 MB |
| 2 | `cluster_capacity_daily` (vcpu avg/max, mem avg/max, tasks avg, spot avg) | running tasks sampled on the counts-history poll | ~100 | 365 days | ~5 MB |
| 3 | `service_util_daily` (cpu/mem avg and p95) | one CloudWatch GetMetricData batch per account and region a day | ~600 | 90 days | ~8 MB |
| 4 | `deployment_history.failure_reason` | OutcomeLogger: circuit breaker event or the last stopped task's reason | ~20 | 90 days (existing) | < 1 MB |

New settings: `HM_RETENTION_AVAILABILITY_DAILY` (8760h), `HM_RETENTION_CAPACITY_DAILY` (8760h), `HM_RETENTION_UTIL_DAILY` (2160h), and `HM_ANALYTICS_UTIL` (default true; turns off #3).

For scale: `service_counts_history` already holds about 1.2M rows. These additions together add about 1,320 rows a day.

## Settings in the prototype
- `showSources`: footers and the "Data used on this page" table.
- `showProposed`: hides every proposed panel, which shows what ships with no schema change.

## Files
- `Harbormaster Analytics.dc.html`: the prototype.
- `support.js` and `web/public/brand/…`: needed only to open it.
- `PROMPT.md`: a prompt for Claude Code.
