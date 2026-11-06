# Harbormaster UI redesign v4: spec

Approved clickable mockup: https://claude.ai/artifact/QQtjnwPuSLFaVHvo6kfKLJ
Source of every screen: `mockup/src/screens/<Name>.html` + `.js`; tokens and components: `mockup/src/hm.css`; shell: `mockup/src/shell.html`; mock data: `mockup/src/data.js`. `INVENTORY.md` lists every section, column, dialog and message of the current app.

The mockup is a design reference, not code to copy. Rebuild it in `web/src` (React, react-router, TanStack Query, CSS modules, `src/styles/tokens.css`) using real API data. Its "Show state", "Preview as", "View as" and "Rollout state" controls are mockup-only and are not built.

## Behaviour that must not change
Every permission check, lock, disabled reason, confirm dialog, toast, validation rule and guard in `INVENTORY.md` stays. Break-glass, last-admin and SSO ordering rules stay. Existing URLs keep working.

## Design changes to build
1. **Tokens and legibility.** Values from `hm.css` for dark and light. All text at least 4.5:1; 12 px minimum; no uppercase tracked micro-labels; body 13 px. Chart series order and values from `hm.css` (they pass the colour-blind validator). Status is shape, colour and text (`.st-*` shapes). Healthy map cells at least 3:1. Remove the two-letter glyphs for an inline stroke SVG icon set from `mockup/src/icons.json`. No new npm dependencies.
2. **Shell.** Nav grouped: Overview, Services (new), Clusters, Tasks; Infrastructure: Networks, Storage; Insights: Analytics; Admin: Settings. Account menu at the foot of the sidebar. One scope control with saved scopes (stored per user in the browser). Search keeps a minimum width; the breadcrumb can wrap and never overlaps. Activity is a closed drawer, never open by default. Density toggle (compact 36 px, comfortable 44 px row) and theme toggle, remembered. Keyboard: existing `[`, Cmd/Ctrl+K, Esc, plus `g o|s|c|t|n|a`, `/`, `?` help dialog, ignored while typing.
3. **Data freshness.** Remove stacked `PollerBanners`. One top-bar control: "N of M regions current", popover names what is stale per account and region, which data type, and what is still current. Raw AWS errors only in the popover detail and AWS accounts. A failed poll tier must not label the whole region stale: this needs per-tier status from the API. A page shows a compact banner only when its own data is affected.
4. **Partial failure.** One failed sub-request degrades only its panel with cause and Retry; no blank tabs; loading uses skeletons. `GET /api/services/{id}/deploy-settings` must return what it could read plus per-part errors, so Deploy stays usable when for example scheduled actions cannot be read. The affected editor shows the error and blocks only edits to that part.
5. **Full window.** No max page width: all pages fill 1440, 2560 and 3840 px wide. Terminal and logs fill the window height with inner scrolling. Chart height capped at 340 px. At 2200 px and wider Overview and Deploy flow into two columns. Wide tables scroll inside their panel, with names and row actions always visible via a sticky action column. Page never scrolls sideways, down to 390 px.
6. **Overview.** Needs attention rows show cause, suggested next step link, and what changed just before when known (from deployment and operations history; omitted when unknown, never invented). Include account and data problems. Counts link to their source and agree.
7. **Services page** (`/services`). Flat list with saved views (All, Problems, My pins, per tag), filter, status segment, column picker, sortable columns, row actions, detail drawer. 25 per page.
8. **Tables.** Column picker and density on Services, Clusters, Tasks. Task click opens a side drawer: identity, status, friendly and raw stop reason, containers, recent log lines, actions.
9. **Service page.** Tabs: Overview, Tasks, Logs, Metrics, Terminal | Configure (sub-tabs Deploy, Task definition, Scaling) | History. Same content. URLs stay `/services/:id/:tab`; `operations` is the History tab. Header gains a primary Deploy link.
10. **Deploy change set.** Form left, sticky Pending changes right with live before and after for each edited field. All existing review, partial-failure and retry behaviour stays. OOM hint only when the service actually stopped with OOM.
11. **Other pages.** Target-health and capacity donuts become stacked bars. Analytics KPI captions wrap; retention reads "90 days", not "2160h". Permission matrix cells show a preset button that opens a popover with V L O D E T, keeping the implied-View rule. SSO becomes a guided checklist above the same five panels. Account errors are expandable.

## Decisions not to build now
UI scale setting (asked, not answered). Mock-only totals and "Show state" controls.

## Verification
Unit and e2e suites stay green with specs updated to the new markup. Every screen is exercised through the real AWS code path against floci (`floci/floci:2.4.0`, ECS mock mode), at 1440, 2560, 3840 and 390 px wide, in dark and light. Floci gaps are real test cases: it lacks `DescribeScheduledActions`, which must degrade only that panel, and it serves no Container Insights data unless metrics are seeded.
