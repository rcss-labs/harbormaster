# Prompt for Claude Code

Copy this folder to `docs/design/analytics/` in `rcss-labs/harbormaster`, then paste the prompt below.

---

Add an Analytics section to Harbormaster as described in `docs/design/analytics/README.md`. Open `docs/design/analytics/Harbormaster Analytics.dc.html` in a browser to see it; it is a design reference, so do not copy its HTML. Build it in `web/src` with our React, TanStack Query and CSS-module patterns and the redesigned tokens and shell. Analytics covers clusters and services only: no sign-ins and no admin changes.

Ship it in two phases. Run `make lint` and `make test` after each step and add Go and Vitest tests next to what you change.

**Phase 1: no schema changes**
1. **Backend:** add read-only endpoints under `/api/analytics/{delivery,reliability,capacity,operations}`.
   - They take `tag`, `range` and the current scope (account and region), and return KPIs plus bucketed series (24h → 1h, 7d → 4h, 30d → 1d, 90d → 2d).
   - Use SQL aggregation over the existing tables only: `deployment_history`, `tasks`, `service_counts_history`, `service_events`, `services`, `task_definitions`, `scaling`, `subnets`, `operation_log`, `revision_records`.
   - Clamp each query to its table's configured retention and return the window actually used, so the UI can show the notice.
   - Apply the user's tag permissions exactly as the existing list endpoints do.
2. **Frontend:** add an Analytics item to the sidebar between Storage and Settings, with the route `/analytics/:tab?tag=&range=`.
   - Build shared chart components with no new dependencies: line/area, stacked area, donut, and a hover tooltip. Match the README's chart spec.
   - Use the existing DataTable for the tables.
3. Build the four tabs with their non-proposed panels, the source footers and the "Data used on this page" table.

**Phase 2: the four small additions** (one migration, `0080_analytics.up.sql`)
4. Add `service_availability_daily`, `cluster_capacity_daily` and `service_util_daily`, and the `deployment_history.failure_reason` column. The README has the columns and sizes.
   - Nightly rollups write one row per service or cluster per day, and must run before retention deletes the source rows.
   - The utilisation rollup makes one CloudWatch GetMetricData batch per account and region a day, behind `HM_ANALYTICS_UTIL`.
   - The OutcomeLogger sets `failure_reason`.
5. Add the `HM_RETENTION_*_DAILY` settings to config, `RunRetention`, the README env table and the tests.
6. Turn on the panels marked Proposed, removing the badge and dashed style once their data exists. Until a rollup has data, show an empty state saying when data started.

Rules:
- No new npm or Go dependencies.
- No per-task or per-minute history beyond what exists today.
- Every query must stay fast with 100 clusters, 600 services and 1,000 tasks. Add indexes only where a query plan needs them.
