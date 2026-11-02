# Handoff: Harbormaster UI redesign (dark, scale-ready)

## Overview
A redesign of the Harbormaster web UI (`web/src`, React + CSS modules) for DevOps teams running Harbormaster against large estates: **100 clusters, ~600 services, ~1,000 running tasks**. It keeps the brand (IBM Plex Sans, Geist Mono, the cyan symbol), raises information density, fixes wayfinding, and adds a command palette, inline quick actions, a live activity rail, and patterns that stay usable at scale (problems-first views, filtering, sorting, paging, grouping of repeated failures).

## About the design files
The files in this bundle are **design references built in HTML**: prototypes that show the intended look and behaviour. They are not production code to copy. The task is to **recreate these designs in the existing Harbormaster web app** (`web/src`: React, react-router, TanStack Query, CSS modules, `src/styles/tokens.css`) using its established patterns, components and API hooks. Replace all sample data with the real API data (`useOverview`, `useClusters`, `useTasks`, `useAlerts`, etc.).

Open `Harbormaster Redesign v3.dc.html` in a browser (keep `support.js` and `web/public/brand/` next to it). Every screen is reachable from the sidebar; service pages open from any service name. `Current Overview (reference).dc.html` is a rebuild of today's Overview, for comparison.

## Fidelity
**High fidelity.** Colours, type, spacing, row heights and interactions are final. Recreate them closely using CSS modules and tokens. Icons are not part of the design: the collapsed sidebar uses two-letter mono glyphs (OV, CL, TK, NW, ST, SE) on purpose.

## Design tokens (dark theme, refreshes `tokens.css` dark values)
| Token | Value | Use |
| --- | --- | --- |
| `--bg` | `#0b0f13` | page |
| `--chrome` | `#0e1318` | sidebar, top bar, rail, group-header rows |
| `--surface` | `#11161c` | panels |
| `--raised` | `#121920` | inputs, buttons, popovers |
| `--hover` | `#141b22` | row hover; `#1b232c` button hover |
| `--border` | `#1f262e` | panel borders, header dividers |
| `--row-border` | `#1a2028` | row dividers |
| `--border-strong` | `#2a333e` | controls |
| `--text` | `#e5eaf0` | |
| `--text-2` | `#aab4c1` | secondary values |
| `--muted` | `#8c97a7` | |
| `--dim` | `#5f6b7a` | column headers, mono labels |
| `--accent` | `#45b5e3` (brand symbol) | links, active, primary buttons (ink `#04202c`) |
| `--accent-soft` | `rgba(69,181,227,0.12)`, border `rgba(69,181,227,0.45)` | active nav / segments |
| `--success` `--warning` `--danger` `--info` | `#34d399` `#fbbf24` `#f87171` `#a78bfa` | status |
| danger control border / hover | `#4a2a2e` / `#2a1619` | Stop, Delete |
| series | CPU `#45b5e3`, memory `#f59e4b`, requests `#34d399`, p95 `#a78bfa` | charts |
| healthy map cell | `#1f5c48` | muted so problems stand out |
| overlay / pop shadow | `rgba(4,7,10,0.6)` / `0 18px 50px rgba(0,0,0,0.55)` | |

Tag chips (text / border): Prod `#f87171/#4a2a2e`, Staging `#fbbf24/#4a3a1a`, Dev `#8c97a7/#2a333e`, payments `#45b5e3/#1c3a48`, data `#34d399/#1a3d31`, ml `#a78bfa/#33295a`. Inherited tags use a dashed border.

**Type:** IBM Plex Sans, 12.5px/1.45 body, tabular numerals. Geist Mono for IDs, revisions, numbers, IPs, ARNs, column headers and KPI values. Page title 18px/600, -0.015em. Panel title 12.5px/600. Column headers: Geist Mono 10px/500, uppercase, +0.06em, `--dim`. KPI label: mono 10px, +0.08em. KPI value: mono 20px/600, -0.03em.

**Shape and spacing:** radius 6px for panels and controls, 5px for small buttons, 4px for chips. No panel shadows. Panel header 34px (38–40px when it holds a toolbar). Row heights: 30–34px for list rows, 44px for rows with two text lines. Page padding 16px 20px 32px; gap 14px between panels.

**Status:** a 6px dot plus coloured text (no pill background), and a 3px left stripe on rows that are not healthy.

## Shell
- **Grid:** sidebar | main | activity rail (`212px | 1fr | 300px`).
- **Sidebar:**
  - Collapsible to 52px with the `[` key or its footer button. It auto-collapses when the viewport is under 1100px.
  - Each item has a 24×22 glyph box with a mono label. Counts go right of the label; collapsed, they sit as a corner badge.
  - Items: Overview (failing count, red), Clusters (count), Tasks (problem count, red), Networks, Storage, Settings (account issues, amber).
  - A **Pinned** services list sits under the nav, and the user block is in the footer.
- **Top bar (48px):**
  - A **Scope** chip (account / region filter) and the breadcrumbs.
  - A "live · polled 6s ago" pulse, hidden when narrow.
  - A **⌘K** search button (flex 1, max 300px).
  - An **Activity** toggle with a count.
- **Activity rail:**
  - Docked at 1280px and wider, an overlay below that.
  - Filters: Problems / Changes / All. Repeated events collapse into one row with a ×N badge.
- **Command palette (⌘K / Ctrl+K, Esc to close):**
  - Groups: Actions, Services (problems first when the query is empty), Clusters, Tasks (once the query is 3+ characters), Go to.
  - Up to 4 items per group when empty, 6 when filtering. The first item is highlighted.
- **Confirm dialog + toast** for every change: restart, stop, desired count, roll back, register, admin changes, SSO.

## Screens
1. **Overview**, with a toggle between two directions:
   - **Triage:**
     - KPI strip of 6 cells, each with a sparkline.
     - **Needs attention:** one row per service, worst first. Severity filter chips, inline Logs / Tasks / Restart, capped at 6 rows with "Show all N".
     - **Estate map:** one row per account, a 14px cell per cluster coloured by status. Clicking a cell opens the cluster.
     - **Busiest clusters** by CPU, task stops per hour, and why tasks stopped.
   - **Estate:**
     - Every service grouped by Cluster or Account.
     - **Problems only** is on by default: it hides healthy groups and steady services and says how many are hidden.
     - Groups collapse, a group shows at most 12 rows, and there is a text filter.
2. **Clusters:**
   - A dense table filtered by environment (All / Prod / Staging / Dev) and status (Any / Problems / Deploying / Healthy) with a text filter.
   - Sortable columns (default: status, worst first), 25 rows per page.
   - The Services column previews 3 services and shows "+N".
3. **Cluster detail:**
   - Header with account (ID), region and VPC link.
   - KPI strip, CPU and memory chart, tasks by service (bar = running, tick = desired).
   - Services table, problems first.
4. **Tasks:**
   - KPI strip and segments: Problems / Running / Stopped / All.
   - Filter by ID, service, cluster or IP.
   - **Group repeats** is on by default: identical failures (same service + reason) collapse into "×6 tasks" rows that expand. 50 rows per page.
5. **Service page:**
   - Sticky header with name, tag, status and meta. Actions: an inline **desired-count stepper** with Apply, Restart, AWS console.
   - Sticky tabs:
     - **Overview:** KPIs, CPU and memory chart with a deploy marker, target health and last rollout, running tasks with Shell / Logs / Stop, deployments.
     - **Metrics:** a 1h / 6h / 24h / 72h range and 6 charts in 2 columns.
     - **Task definition:** quick edit, JSON with a diff gutter, "Review and register :24", revisions with roll back.
     - **Tasks:** this service's running and stopped tasks.
     - **Scaling:** stepper plus a range bar showing the auto-scaling min and max, policy details, activity.
     - **Logs:** full-height viewer with level filter (All / Warn+ / Errors), search, and Live / Paused. Single-line rows with level-tinted backgrounds.
     - **Terminal:** task / container / command pickers, session panel (limits, not recorded).
     - **Operations log:** "Show ECS service events" toggle.
6. **Networks:**
   - VPC list grouped by account with a filter (stacks to 260px high when narrow).
   - The selected VPC shows a KPI strip and a Subnets table with IP use.
   - **Placement** outline: zone ▸ subnet ▸ cluster ▸ service. Clusters are collapsed by default; "Expand all" opens them.
   - Gateways and endpoints.
7. **Storage:**
   - KPI strip (with the not-mounted, still-billed size) and an All / Mounted / Not mounted filter.
   - Table sorted largest first. Unmounted file systems get an amber stripe.
8. **Settings** (admin; sticky tabs; the primary action sits in the header for the active tab):
   - General: stopped-tasks window.
   - AWS accounts: status per account, including "Slowed by AWS throttling" and "Data is N min old" with the error; Test now.
   - Users & permissions:
     - A V / L / O / D / E / T matrix per user × tag.
     - Rule: turning on any action also turns on View; turning off View clears the tag.
     - The preset name (Full / Deployer / Operator / Viewer / Custom / None) is derived from the letters.
   - Tags: summary table, then assignments paged 15 clusters at a time, each expandable to its services.
   - Single sign-on: Status, IdP, SP, Sign-in options, Test sign-in.
   - Audit log: type filter, text filter, Load more.

## Responsive behaviour
- When the main column is under 1100px, two-column grids stack.
- Wide tables scroll horizontally inside their panel (min-widths of 960–1240px).
- The Needs attention row drops its detail column when narrow.

## State (per screen, URL-backed where it matters)
- Overview: layout (triage / estate), severity filter, Show all, estate grouping, Problems only, per-group open state, estate filter.
- Clusters: environment, status, query, sort {key, direction}, page.
- Tasks: segment, query, Group repeats, page, open groups.
- Service: active tab, metrics range, log level, live tail, draft desired count.
- Networks: query, selected VPC, open placement rows.
- Storage: segment, query.
- Settings: tab, permission edits, tag overrides, assignments page and open rows, audit filter and count.
- Shell: sidebar collapsed, rail open, palette open and query.

## Assets
- `web/public/brand/harbormaster-symbol-dark.svg` (already in the repo).
- No other images and no icon set.

## Files
- `Harbormaster Redesign v3.dc.html`: the full prototype. The markup is inline-styled; data and behaviour are in the `class Component` script at the bottom.
- `support.js`: runtime needed only to open the prototype.
- `Current Overview (reference).dc.html`: today's Overview, rebuilt for comparison.
- `PROMPT.md`: a ready-to-paste prompt for Claude Code.
