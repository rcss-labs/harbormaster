# Prompt for Claude Code

Paste this into Claude Code from the root of the `rcss-labs/harbormaster` repo, with this handoff folder copied to `docs/design/redesign/`.

---

Implement the Harbormaster UI redesign described in `docs/design/redesign/README.md`. The prototype `docs/design/redesign/Harbormaster Redesign v3.dc.html` is a design reference: open it in a browser to see every screen and interaction. Do not copy its HTML or inline styles. Rebuild it in our existing web app (`web/src`) with React, react-router, TanStack Query, CSS modules and `src/styles/tokens.css`, using real data from our API hooks.

Work in this order and keep each step shippable. After each step, run `make lint` and `make test`, and update or add Vitest tests next to the components you change:

1. **Tokens and base.** Update the dark values in `src/styles/tokens.css` to the README's token table and add the new tokens (`--chrome`, `--text-2`, `--dim`, `--row-border`). Keep the light theme working; derive its new tokens from the existing light palette. Body text becomes 12.5px.
2. **Shared primitives** in `src/components`:
   - `Panel` (34px header, an optional toolbar slot).
   - `KpiStrip` (cells with a mono label, value and sparkline).
   - `StatusDot` (dot plus coloured label; replaces `Pill` in tables).
   - `DataTable` (CSS-grid rows, sticky header, the 3px status stripe, horizontal scroll with min-width, sortable headers, paging).
   - `SegmentedFilter` (with counts).
   - `Checkbox toggle`, `TagChip` (solid or dashed), `Pager`.
3. **Shell:**
   - Collapsible sidebar: 212 / 52px, the `[` shortcut, auto-collapse under 1100px, two-letter glyphs, pinned services.
   - Top bar: Scope chip for the account and region filters, breadcrumbs, live poll indicator, ⌘K button, Activity toggle.
   - Activity rail: docked at 1280px and wider, overlay below; Problems / Changes / All; repeated events collapsed into ×N.
   - Command palette: ⌘K / Ctrl+K, grouped results from our search endpoint plus actions, keyboard navigation.
4. **Overview:** the Triage and Estate layouts with a persisted toggle; Needs attention with severity filters and inline Logs / Tasks / Restart; the estate map; Estate grouping by cluster or account with Problems only on by default.
5. **Clusters list and cluster detail:** filters, sorting, paging (25 per page), the services preview.
6. **Tasks:** segments, query, Group repeats (group by service + stop reason or health), paging (50 per page). The URL holds the filters.
7. **Service page:**
   - Sticky header with the inline desired-count stepper (reuse our existing dialog logic and validation: `HM_MAX_DESIRED_COUNT`, the auto-scaling range warning) and sticky tabs.
   - Restyle every tab to the prototype. The Logs tab rows must not wrap the stream column.
8. **Networks:** VPC list plus detail with the Placement outline (collapsed to clusters by default).
9. **Storage:** table view with the Not mounted filter and stripe.
10. **Settings:** restyle all six tabs. Keep every existing rule and guard (break-glass, last admin, SSO order). The permission matrix keeps the "any action turns on View / View off clears" rule.

Rules:
- Every change still goes through our confirm dialogs and toasts. Permission checks and disabled-state tooltips stay as they are.
- Do not add npm dependencies. Do not draw new icons.
- Test at 920px and 1440px wide with the fake estate from `make run`. Each list must stay usable with 100 clusters and 1,000 tasks: no unbounded lists, and every long list is filtered, grouped or paged.
- When unsure about a value, read it from the prototype.
