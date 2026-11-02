# UX round 1: full-width layout, tag filter, quieter lists, Settings page

Date: 2026-11-01. Source: product owner's design and UX notes after using v0.1.0-rc.

## Why

Harbormaster is used on large, high-resolution monitors. The UI still feels like the mockup: fixed-size panels look stranded on big screens, and the lists show noise (healthy clusters, resolved alerts, failed tasks from hours ago) that hides the few things an operator needs to act on. Admin is four loose pages instead of one settings area. This round makes every screen show **what needs attention now**, and lets operators narrow the view by tag as well as account and region.

## Goals

1. **Full-width, full-height layout.** No screen caps width or uses fixed pixel heights for its main panels. On a 2560×1440 or 4K screen, content uses the available space: grids add columns or stretch, and long panels grow with the viewport.
2. **Tag filter, app-wide.** The top bar gets a tag filter next to account and region.
3. **Overview: Task health** shows only what is not fine.
4. **Overview: Needs attention** drops resolved issues quickly.
5. **Overview: "Why tasks stopped, last 3 days"** wraps reason text.
6. **Clusters: filter bar** that works in both card and table views.
7. **Tasks: recent-only stopped tasks.** Stopped and failed tasks older than a configurable window are hidden. The default window is 10 minutes.
8. **Settings page** replaces the separate admin pages, with tabs.

## Non-goals

- No new data sources or AWS calls.
- No change to the "why tasks stopped" statistics window. It stays at 3 days.
- No change to permissions semantics, the tag model (tags on clusters, inherited by services), or audit content.
- No redesign of the visual style (colours, typography, components). This round is about layout and behaviour.

## Anti-goals (a failure even if it technically works)

- An issue that has cleared stays on "Needs attention" for minutes or hours.
- Filters on the Clusters page disagree with the top-bar filters, for example the top bar says account A while the page shows account B.
- Hiding old stopped tasks also hides a task that is currently running, or skews the 3-day stop statistics.
- Wide screens show one narrow column with empty space on the right.

## Behaviour

### 1. Layout (whole app)

- Remove any width cap on the content area and its page containers, so pages fill the space beside the sidebar.
- Remove fixed pixel heights on main panels. The builder audits these; known ones are:
  - logs viewer: `height: 520px`
  - terminal: `height: 480px`
  - task-definition diff: `max-height: 380px`
  - the reason column in the task table: `max-width: 280px`
- Long panels such as logs and the terminal fill the remaining viewport height, with a sensible minimum. The terminal must still refit (xterm fit addon) when the window resizes.
- Grids like the Overview tiles, cluster cards and service cards use auto-fill or auto-fit columns with a minimum card width. Wide screens then get more columns instead of wider cards, and the existing narrow-screen breakpoints keep working.
- Small UI chrome keeps its fixed sizes: search box, toasts, dialogs and sidebar.

### 2. Tag filter (top bar)

- It is a multi-select of tags, placed after the account and region selects.
- **Tag model (as built):** a cluster has at most one tag. An untagged cluster counts as the built-in **Dev** tag. A service may override its cluster's tag; the override *replaces* the cluster's tag. So every service has exactly one effective tag.
- **Semantics: either (OR).** With T1 and T2 selected, a service matches when its effective tag is T1 or T2. *(Owner decision.)*
- **Cluster matching (owner decision):** a cluster appears when at least one of its services matches. Its cards, per-cluster bars, counts and sidebar numbers then count **only the matching services**. Example: cluster C is tagged `prod`, one service is overridden to `team-x`, and the filter is `team-x`. C appears, showing just that one service.
- **Dev (owner decision):** selecting Dev matches untagged clusters and their non-overridden services.
- Nothing selected means no tag filtering.
- The filter applies wherever account and region apply: Overview (all panels and counts), Clusters, Services, Tasks, Needs attention, search results, and sidebar counts.
- It behaves like the existing account/region filters (`web/src/app/shell/useFilters.ts`):
  - It is stored in the URL as repeated `tag=<id>` params.
  - It is remembered per user in localStorage.
  - The URL wins over the stored value.
  - Stored tag ids the user can no longer see, or that were deleted, are dropped.
- The backend accepts `tag` (repeatable) on every list and summary endpoint that already takes `account` and `region`, and applies it on top of tag-based visibility. A filter can only narrow what the user may see, never widen it.

### 3. Overview: Task health card

- The donut and its legend counts stay as they are.
- The per-cluster bars below the donut list only clusters that are not fully healthy, meaning any of:
  - unhealthy running tasks
  - a rollout in progress
  - running below desired (missing tasks)
- When every cluster is healthy, the list is not shown. A short line such as "All clusters healthy" may replace it.
- Bar colours show why each cluster is listed: unhealthy → warning, rolling out → info, missing → danger.
- `PerClusterDTO` has only `running` and `desired` today. Add `unhealthy`, `rolling_out` and `missing`, then update the contract goldens and the MSW fixtures.

### 4. Overview: Needs attention

- **Problem found:** the list refreshes every 5 s, and the alerts tier recomputes every 5 s. But two alert kinds in `internal/status/alerts.go` use a 24-hour window and stay visible after the service has recovered:
  - `restart_loop`: 3+ errored stops in 24 h
  - `deployment_failed`: the newest deployment FAILED within 24 h
- **New rule:** an alert shows only while its condition is current.
  - `deployment_failed` already clears when a newer deployment becomes the newest one. **Remaining stale case:** the service rolled back and is steady, but the failed deployment is still the newest. In that case, clear the alert once the service is steady (running = desired, a primary deployment is serving, and no rollout is in progress).
  - `restart_loop` *(owner decision)*: the window drops from 24 h to **1 hour**. The alert shows while the service had 3 or more errored stops in the last hour. It clears once fewer than 3 remain in that hour. This keeps a service that crashes every 30 minutes flagged, without letting the alert flap.
  - Every other alert kind keeps clearing on the next 5 s alerts-tier cycle.
- **Acceptance:** once an alert's condition stops holding, its alert disappears from the UI within about 10 s (one alerts cycle plus one frontend refetch).

### 5. Overview: "Why tasks stopped, last 3 days"

- Reason labels wrap onto multiple lines instead of being truncated or overflowing.
- Bars and counts stay aligned with their wrapped labels.

### 6. Clusters: filter bar

- **Filters:**
  - **Status:** multi-select of the cluster statuses already used by `clusterStatusKind`.
  - **Name search:** case-insensitive substring match.
  - **Account, region and tags:** the same filter state as the top bar (`useFilters`). Changing one here changes the top bar and the URL, and the other way round. There is never a second, page-local copy.
- **Placement:** one bar above the list. The card view and table view both render from the same filtered list, so switching views keeps the filters.
- **URL:** status and name search are page-local, but they live in the URL (`status=`, `q=`) so a filtered view can be shared and survives refresh.
- **Empty result:** shows an empty state with a "Clear filters" action.

### 7. Tasks: hide old stopped tasks

- **New setting:** "Show stopped tasks from the last X minutes".
  - It is global, and only admins can change it.
  - Default 10. Allowed range is 1–1440 minutes.
- **Task lists** apply the setting. These are the Tasks page, a cluster's tasks, and a service's Tasks tab.
  - **Shown:** all non-STOPPED tasks, plus STOPPED tasks whose `stopped_at` is within the last X minutes.
  - **Hidden:** STOPPED tasks older than that.
  - This applies to every task filter (problems, running, stopped, all).
- **Problems filter (owner decision):** an errored STOPPED task also drops out of "problems" X minutes after it stops. The service's own status and alerts still show that it is failing.
- **Not affected:**
  - The 72-hour `summary` (stop counts and reasons) in the tasks response, and the Overview's 3-day "why tasks stopped" statistics
  - Stops-per-hour charts
  - Alerts, which keep their own windows from section 4
  - Opening a specific task by id, for example from an event or a deep link
- **Enforcement:** the backend applies the filter so counts and lists agree. The UI may show a muted note such as "Stopped tasks older than 10 min are hidden", with a link to the setting for admins.

### 8. Settings page

- **Navigation:** a single sidebar entry, "Settings", replaces the separate Admin entries.
  - Route `/settings/:tab`, with tabs `general`, `accounts`, `users` and `audit`.
  - Old `/admin/*` URLs redirect to the matching tab.
- **Tabs:**
  - **General:** tag management (the current Tags page content) and the task visibility window from section 7, with save, validation and an audit entry on change.
  - **AWS accounts:** the current Accounts page.
  - **Users & permissions:** the current Users page and its permission editing.
  - **Audit log:** the current Audit page.
- **Access:**
  - The Settings entry and tabs are admin-only, as Admin is today. Non-admins hitting a settings URL get the existing admin-only treatment (`AdminOnly`).
  - Exception: if the visibility window is readable by everyone, as it needs to be for the hidden-tasks note, it is exposed read-only through an existing endpoint such as `/api/me` or a small `GET /api/settings`. Only admins can PUT.

## Constraints

- **Stack:** Go backend (`internal/api`, `internal/store`, `internal/poller`, `internal/status`), and a React 19 + TanStack Query SPA in `web/`. Keep the existing patterns:
  - `s.handle` route helper
  - strict JSON bodies
  - snake_case DTOs
  - the contract goldens in `web/src/test/contract/shapes.json`
  - the MSW fixtures
- **Migrations:** the next free number is `0060`. A settings table, or a key/value row, is expected to need one.
- **Tests:** test-first for every behaviour change, covering Go unit and store tests, Vitest for the screens, and Playwright e2e for:
  - the tag filter
  - the Clusters filters in both views
  - the hidden old tasks
  - the Settings tabs and redirects
- The full suite must pass: `go test ./...`, vitest, lint, typecheck, build, e2e.
- **Fixtures:** `fake.MockupEstate` is the source of truth for expected counts. Tags live in the database and are seeded by `internal/devseed/devseed.go`. The seed needs:
  - at least two tags on clusters, plus one service override, to exercise the cluster-matching rule
  - stopped tasks both inside and outside the 10-minute window, which go in the fake estate

## Left to the builder

- **Tag multi-select widget:** a native multi-select, or a small popover with checkboxes, matching existing controls.
- **Filter mechanics:** whether the tag filter extends the existing `Filters` type and `queryString`, or uses a parallel parameter.
- **Settings storage:** a generic `settings` key/value table or a typed single-row table.
- **Minimum card widths** for the auto-fill grids.
