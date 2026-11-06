# Harbormaster UI redesign v4: implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development. One lane = one subagent in one git worktree (`git worktree add ../harbormaster-wt/v4-<lane> -b lane/v4-<lane> feat/ui-redesign-v4`). Steps use `- [ ]` syntax. Read `docs/superpowers/plans/2026-10-31-00-conventions.md` first; every line of its Global constraints applies.

**Goal:** Rebuild the SPA to the approved v4 mockup (tokens, icon set, grouped shell with one scope control, per-tier data freshness, partial-failure panels, full-window layouts, a new `/services` page, task drawer, regrouped service tabs, two-column Deploy change set) without changing any permission, lock, dialog, toast or validation rule in `docs/redesign-v4/INVENTORY.md`, and verify every screen against floci through the real AWS code path.

**Architecture:** Wave 0 lands everything shared: `tokens.css` with the `hm.css` values (old names kept as aliases so untouched CSS modules keep working), shared primitives with fixed prop signatures, an inline SVG `Icon`, the API contracts as Go DTOs + TS types + MSW fixtures + regenerated contract golden, and a floci harness (`tools/floci`, `make floci-*`, `web/e2e-floci`). Wave 1 runs twelve lanes with disjoint file ownership: three backend (per-tier poller status, partial deploy settings, services list and attention context) and nine frontend, each frontend lane coding against the wave-0 fixtures. Wave 2 writes the cross-lane e2e specs that need both halves, deletes the legacy components, and finalises the floci allowlist. The lead runs the gate after each wave; a Fable review closes.

**Tech Stack:** Go 1.26, pgx v5, `net/http` ServeMux, AWS SDK v2; React 19, TypeScript, Vite, react-router, TanStack Query v5, CSS modules, Vitest + Testing Library + MSW, Playwright 1.64; floci `floci/floci:2.4.0`, `postgres:16.4-alpine`.

**Spec:** `docs/redesign-v4/README.md` (binding), `docs/redesign-v4/INVENTORY.md` (every control and message must survive), mockup `docs/redesign-v4/mockup/src/` (design reference, not code). Conventions: `docs/superpowers/plans/2026-10-31-00-conventions.md`.

## Global Constraints

- Test-first for every behaviour change: write the failing test, run it, confirm it fails for the expected reason (undefined symbol, wrong text, missing role), then implement. Pure restyles need the existing test updated, not a new one.
- Per lane before hand-off: `make lint`, the affected unit suites, and only the Playwright specs the lane owns (see `docs/redesign-v4/LANE-BRIEF.md`). The full `make e2e` runs at each wave gate, because a dozen concurrent browser suites on one machine would be flaky. Never commit `internal/web/dist`.
- No new npm dependencies. No new icons beyond `mockup/src/icons.json`. No `max-width` on any page container. Every text at least 4.5:1, 12 px minimum, body 13 px, no uppercase tracked micro-labels.
- Every string in INVENTORY.md stays verbatim unless this plan renames it (the renames are listed under Decisions).
- URLs stay: `/services/:id/:tab` with the existing tab keys (`operations` is the History tab); `/analytics/:tab`; `/settings/:tab`; old `/admin/*` and `/networks/:acct/:region[/vpc]` redirects stay.
- Shared files are owned by exactly one lane per wave (table below). A lane that needs a change in a file it does not own stops and reports; it does not edit it.
- Contract golden: whoever changes a DTO regenerates with `HM_UPDATE_CONTRACT=1 go test ./internal/api -run TestContract` and updates `web/src/test/fixtures.ts` + `contract.test.ts` in the same commit (wave 0 only; wave 1 backend lanes do not change DTO shapes).
- Commits: conventional commits, body says why. Every commit message ends with exactly these two lines:
  `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`
  `Claude-Session: https://claude.ai/code/session_01G6atqei8utLsUdYaVb2i6f`
- Model tiers: Haiku = mechanical restyle with fixed strings; Sonnet = behaviour, state, backend. No lane needs Opus; the final review is Fable.

## Review Focus (five failure modes, each pinned by a test)

1. A failing non-core tier (scaling, target_health, network, storage) must not mark the target's clusters, services or tasks stale, and the freshness control must not count that region as not current for core data → Lane 1B `TestPollerStatus_NonCoreTierFailureKeepsCoreCurrent`; Lane 1A `FreshnessControl.test.tsx "names only the failed data type"`.
2. Deploy settings with unreadable scheduled actions must return every other part, block only scheduled-action edits, and a change set that does not touch scheduled actions must apply without a version conflict → Lane 1B `TestDeploySettings_PartialScheduledActions` and `TestChangeSet_AppliesWhenScheduledActionsUnreadable`; Lane 1I `DeployTab.partial.test.tsx "locks only the scheduled actions editor"`.
3. "What changed just before" must never be invented: omitted when no Harbormaster operation or deployment precedes the alert inside the window → Lane 1C `TestAlerts_JustBeforeOmittedWithoutChange`; Lane 1G `NeedsAttention.test.tsx "shows no Just before line when the API omits it"`.
4. A saved scope or stored preference must never widen what a user sees: tag ids the user lost View on are dropped when a saved scope is applied → Lane 1A `scopes.test.ts "applying a saved scope drops tag ids outside visibleTagIds"`.
5. The page must never scroll sideways down to 390 px and the sticky action column must keep row actions reachable → Lane 0A `DataTable.test.tsx "sticky actions column keeps position: sticky and the name column stays first"`; Lane 0C `routes.spec.ts` overflow assertion at 390 for every route.

## Flags: spec items judged infeasible or wrong (not silently changed)

- F1. Services page CPU and Memory columns (mockup `Services.js` COLS cpu/mem): no per-service batch metrics endpoint exists and the per-service metrics call is one CloudWatch request each. This plan ships the Services page without CPU/Memory columns and records them as a follow-up (a `GET /api/services/metrics?ids=` endpoint). Confirm or add a lane.
- F2. README item 3 says raw AWS errors appear "only in the popover detail and AWS accounts". The Clusters page "Busiest clusters" banner and the Metrics tab error banner also show CloudWatch error text today; this plan keeps those (they are page-local data problems, README item 3 last sentence) but strips the raw text to one sentence with the detail behind a "Show AWS error" disclosure. Flagging the reading.
- F3. `--dim` in the current `tokens.css` (dark `#5f6b7a` on `#11161c`) fails 4.5:1. `hm.css` has no `--dim`; this plan aliases `--dim` to `--muted`.
- F4. The mockup's Tasks "Saved view" select (`tk-view`) is mock-only (not in README item 8); not built. Saved views are built only where README item 7 requires them (Services).
- F5. Keyboard `j`/`k`/`↵` row navigation appears in the mockup help dialog but not in README item 2; not built. The help dialog lists only shortcuts that exist.
- F6. Floci limits that stay expected failures: `DescribeScheduledActions` (scaling tier failing, deploy settings partial), `StartLiveTail` (logs tail WebSocket fails; the Logs tab shows "Live tail disconnected." + Resume), SSM sessions (Terminal cannot connect; layout only), ECR (images are not in ECR, free-text tag). CloudTrail-sourced history rows do not exist.
- F7. The activity drawer's "Changes" segment and the History tab depend on Harbormaster-made operations; on a fresh floci estate they are empty by design. The seed performs one restart and one desired-count change through the API so they are not empty.

## Decisions (rulings implementers do not re-decide)

1. **Tokens:** `web/src/styles/tokens.css` gets the `hm.css` dark and light blocks verbatim under `:root[data-theme="dark"]`, `:root[data-theme="light"]` and the `prefers-color-scheme` fallback, plus aliases: `--success: var(--ok)`, `--warning: var(--warn)`, `--danger: var(--bad)`, `--expected: var(--exp)`, `--series-N: var(--sN)`, `--dim: var(--muted)`, `--hover-strong: var(--active)`, `--accent-soft-border: var(--accent-line)`, `--radius: var(--r)`, `--radius-sm: var(--r-s)`, `--shadow-pop: var(--pop)`, `--overlay: var(--scrim)`, `--term-bg: var(--term)`, `--healthy-cell: var(--cell-ok)`, and the `--tag-*-text/border` pairs re-derived from the new `--bad/--warn/--accent/--ok/--info/--muted`. Density: `:root[data-density="comfortable"] { --row: 44px; --pad: 16px }`, default `--row: 36px; --pad: 12px`.
2. **Icon:** `web/src/components/icons.ts` is a `const ICONS = { overview: 'M…', … } as const` copied from `icons.json` (45 names); `IconName = keyof typeof ICONS`. `Icon` renders a 24-viewBox stroke SVG, `aria-hidden` unless `label` is given.
3. **Status shape:** `StatusDot` renders `.st.st-<kind>` with the `hm.css` `::before` shapes (circle ok, triangle warn, diamond bad, half-disc info, ring exp, dash mute). Kind mapping stays in `components/status.ts` (`statusKind`, `clusterStatusKind`).
4. **Per-tier poller status:** the API keeps the existing `PollerItemDTO` fields and adds `tiers`. `failing`/`throttled`/`last_error`/`last_ok_at` now aggregate only the core tiers (`clusters`, `serviceList`, `services`, `runningTasks`, `stoppedTasks`, `fullTasks`). Non-core tiers appear only in `tiers`. Labels (server-side, `PollerTierDTO.label`): clusters|serviceList|services → "Clusters and services"; runningTasks|stoppedTasks|fullTasks → "Tasks"; target_health → "Target health"; scaling → "Auto scaling"; alerts → "Alerts"; network → "Networks"; storage → "Storage". "Current" (client) = no core tier failing and none throttled (corrected: a non-core failure must not label a region stale; the control shows it as a secondary note, e.g. "auto scaling stale in 3 regions"). Pages declare which labels they depend on (`<DataBanner tiers={['Auto scaling']}>`).
5. **Partial deploy settings:** `DeploySettingsResponse` gains `partial: PartialErrorDTO[]` (empty array when complete). Parts: `task_role`, `target_groups`, `scaling_target`, `scaling_policies`, `scheduled_actions`. A failed part leaves its field at the zero value (`scheduled_actions: []`, `has_target: false` only if `scaling_target` failed). `version` is computed over readable parts only; `applyChangeSet` recomputes the same way and refuses (422 `invalid`, message "Scheduled actions could not be read; retry before changing them.") only a change set that edits a part listed in the current `partial`. `liveService` and "no primary deployment" stay fatal (502/404).
6. **Services list:** `GET /api/services` returns every visible service (same scope/tag rules as `/api/clusters`), capped at 2000 with `truncated`. Filtering, saved views, sorting and 25-per-page paging are client-side. Saved views: All, Problems (status ≠ steady), My pins (localStorage pins), one per visible tag. Column prefs in localStorage `harbormaster-columns:services`.
7. **Attention context:** `AlertDTO` gains `cause` (the existing `text`, kept as `text` too for compatibility), `next_step: {label, href}` (server-built from kind: service_failing → Logs tab; restart_loop → Tasks tab; deployment_failed → Deploy tab; unhealthy targets → Tasks tab; account/data problems → `/settings/accounts`; others → service overview) and `just_before: {at, text, source} | null` from the newest `operation_log` (source harbormaster) or `deployment_history` row for the service with `at < alert.at` and `alert.at − at ≤ 24h`; null otherwise. Never synthesised from ECS events.
8. **Task drawer logs:** `GET /api/services/{id}/logs` accepts `task=<short id, 8+ chars>`; the server restricts reads to streams whose name ends with `/<taskId>` for the selected containers (`logStreamNamePrefix` per container). Standalone tasks have no service; the drawer shows "No service, so no logs here." for them.
9. **Shell state storage keys (per user where noted):** theme `harbormaster-theme`; density `harbormaster-density`; sidebar `harbormaster-sidebar`; scopes `harbormaster-scopes:<user>` as `{name, account?, region?, tag?: number[]}[]`; column prefs `harbormaster-columns:<table>`; overview layout and pins unchanged.
10. **Keyboard:** `g` then `o|s|c|t|n|a` within 1 s navigates to `/`, `/services`, `/clusters`, `/tasks`, `/networks`, `/analytics/delivery`; `/` focuses the page's primary filter (`[data-hm-filter]`, first in DOM); `?` opens the shortcuts dialog; all ignored while typing, in a dialog, or with the palette open; `[`, Cmd/Ctrl+K, Esc unchanged. Every page lane marks its main filter input `data-hm-filter`.
11. **Service tabs:** `TABS` order becomes overview, tasks, logs, metrics, terminal, (gap, label "Configure") deploy, task-definition, scaling, (gap) operations labelled "History". Keys unchanged. The header gains a primary "Deploy" link to `/services/:id/deploy` (hidden when the Deploy tab is locked, with the same lock tooltip on a disabled button).
12. **Deploy layout:** `.hm-split-sticky` grid (form `minmax(0,1fr)`, aside `340px` sticky `top: 0`) from 1180 px; aside stacks below the form under 1180 px; `wide-form` two CSS columns from 2200 px. The Pending changes aside lists each edited field as "label: before → after" from `changeSummary.ts` (`serviceRows`, `scalingLines`, container diffs).
13. **Donuts → stacked bars:** `StackBar` replaces `Donut` on the service Overview (target/task health), analytics Capacity (vCPU by account, tasks by capacity provider). `Donut.tsx` is deleted in wave 2 after both users migrate.
14. **Analytics retention:** `retentionText('2160h')` → "90 days"; `'168h'` → "7 days"; `'8760h'` → "1 year"; `'720h'` → "30 days"; other values → "N days" rounded.
15. **Permission matrix cells:** one button "Preset VLODET" (`aria-label "<user> on <tag>: <preset>"`) opening `Popover` with the preset select and six `Toggle`s; the implied-View rule and the PUT per cell are unchanged (`PermCell.tsx` keeps its mutation).
16. **SSO checklist:** panel "Set up single sign-on" with five steps (Identity provider, Service provider, Sign-in options, Test sign-in, Turn on) each `done|todo|blocked` derived from `SSOConfigDTO` only; linking by anchor to the existing five panels; no new API.
17. **Fake failure injection for e2e:** `HM_FAKE_FAIL` (comma list) read by `awsx/fake`; supported values `scheduled_actions` (DescribeScheduledActions returns an `aws_error`), `scaling_tier` (same, so the scaling tier fails), `target_health` (DescribeTargetHealth fails). Set per Playwright project via `webServer.env`; default unset.

## Waves, lanes, ownership, merge order

| Wave | Lane | Tier | Owns (exclusive) | Depends on |
|---|---|---|---|---|
| 0 | 0A tokens, primitives, icon, router stub | Sonnet | `web/src/styles/*`, `web/src/components/*` (+ tests), `web/src/app/density.ts`, `web/src/app/router.tsx`, `web/src/app/services/list/ServicesPage.tsx` (stub), `web/playwright.config.ts`, `web/src/main.tsx` | — |
| 0 | 0B API contracts + services list handler | Sonnet | `internal/api/dto.go`, `internal/api/dto_v4.go`, `internal/api/services_list.go` (+test), `internal/api/server.go`, `internal/api/contract_test.go`, `web/src/test/contract/shapes.json`, `web/src/api/types.ts`, `web/src/api/hooks.ts`, `web/src/api/deploy.ts`, `web/src/api/observe.ts`, `web/src/test/fixtures.ts`, `web/src/test/msw.ts`, `web/src/test/contract/contract.test.ts` | — |
| 0 | 0C floci harness + check suite | Sonnet | `tools/floci/**`, `Makefile`, `web/playwright.floci.config.ts`, `web/e2e-floci/**`, `web/.gitignore` (`.auth-floci/`), `docs/redesign-v4/floci-review/README.md` | — |
| 1 | 1A shell | Sonnet | `web/src/app/shell/**`, `web/src/app/theme.ts`, `web/e2e/shell.spec.ts` (new), `web/e2e/layout.spec.ts`, `web/e2e/smoke.spec.ts`, `web/e2e/tag-filter.spec.ts` | 0A 0B |
| 1 | 1B backend: tiers, partial deploy, task logs, fake failures | Sonnet | `internal/api/poller.go`, `internal/api/observe_status_test.go`, `internal/api/deploy_settings.go`, `internal/api/deploy_changeset.go` (+test), `internal/api/observe_logs.go` (+test), `internal/deploy/settings.go` (+test), `internal/deploy/changeset*.go`, `internal/poller/scaling.go` (+test), `internal/awsx/fake/**`, `internal/logs/**` | 0B |
| 1 | 1C backend: attention context | Sonnet | `internal/api/observe_alerts.go` (+test), `internal/store/attention.go` (+test) | 0B |
| 1 | 1D services page | Sonnet | `web/src/app/services/list/**`, `web/e2e/services.spec.ts` (new) | 0A 0B |
| 1 | 1E tasks page, task drawer, TaskTable | Sonnet | `web/src/app/tasks/**`, `web/src/components/TaskTable.*` (transferred from 0A after wave 0), `web/src/app/services/TasksTab.*`, `web/e2e/tasks.spec.ts`, `web/e2e/service-observe.spec.ts` | 0A 0B |
| 1 | 1F clusters list + detail | Haiku | `web/src/app/clusters/**`, `web/e2e/clusters.spec.ts`, `web/e2e/clusters-filters.spec.ts`, `web/e2e/cluster.spec.ts` | 0A |
| 1 | 1G overview | Sonnet | `web/src/app/overview/**`, `web/e2e/overview.spec.ts`, `web/e2e/overview-noaccess.spec.ts`, `web/e2e/observe.spec.ts` | 0A 0B |
| 1 | 1H service page frame + non-deploy tabs | Sonnet | `web/src/app/services/*.tsx|css` except `list/`, `deploy/`, `TasksTab.*`; `web/src/app/services/{overview,metrics,scaling,task-definition,logs,terminal}/**`, `web/src/app/observe/**`, `web/src/charts/**`, `web/e2e/service.spec.ts`, `web/e2e/terminal.spec.ts` | 0A 0B |
| 1 | 1I deploy change set | Sonnet | `web/src/app/services/deploy/**`, `web/e2e/deploy.spec.ts`, `web/e2e/change.spec.ts` | 0A 0B |
| 1 | 1J analytics | Haiku | `web/src/app/analytics/**`, `web/e2e/analytics.spec.ts` | 0A |
| 1 | 1K networks, storage, auth, my access | Haiku | `web/src/app/{networks,storage,auth,me}/**`, `web/e2e/{networks,storage,me,security}.spec.ts` | 0A |
| 1 | 1L settings | Sonnet | `web/src/app/settings/**`, `web/src/app/admin/**`, `web/e2e/{settings,admin,sso}.spec.ts` | 0A 0B |
| 2 | 2A integration e2e: freshness + partial deploy | Sonnet | `web/e2e/freshness.spec.ts`, `web/e2e/deploy-partial.spec.ts` (new), `web/playwright.config.ts` (adds a `chromium-degraded` project) | all of 1 |
| 2 | 2B integration e2e: services, attention, drawer logs | Sonnet | `web/e2e/services-live.spec.ts`, `web/e2e/attention.spec.ts`, `web/e2e/task-drawer.spec.ts` (new) | all of 1 |
| 2 | 2C legacy removal + floci allowlist | Haiku | `web/src/components/{Card,Tile,Donut,Pill-in-tables usages}`, `web/src/charts/Donut.tsx`, `web/src/app/shell/PollerBanners.tsx` (delete), `web/e2e-floci/allowlist.ts` | all of 1 |

Merge order. Wave 0: 0B, 0A, 0C (no overlapping files; 0A's router stub and 0B's hooks are independent). Wave 1: 1B, 1C (backend first so `make e2e` on the merged branch exercises real handlers), then 1A, 1E, 1H, 1I, 1D, 1F, 1G, 1J, 1K, 1L. Wave 2: 2A, 2B, 2C. Each lane rebases on the integration branch before its PR; conflicts are impossible by ownership, so a conflict means an ownership breach: stop and report.

Hand-off of `TaskTable.*`: 0A may restyle it (sticky actions, density) in wave 0; from wave 1 it belongs to 1E.

## API contracts (fixed in wave 0; backend lanes implement, frontend lanes consume)

`GET /api/poller` item (existing fields unchanged in meaning for core tiers, `tiers` new):

```json
{"account_id":"000000000000","account_name":"floci","region":"eu-west-1",
 "throttled":false,"failing":false,"last_error":"","last_ok_at":"2026-11-06T10:00:05Z",
 "tiers":[
  {"tier":"services","label":"Clusters and services","core":true,"failing":false,"throttled":false,"last_ok_at":"…","last_error":"","interval_ms":5000},
  {"tier":"scaling","label":"Auto scaling","core":false,"failing":true,"throttled":false,"last_ok_at":null,"last_error":"DescribeScheduledActions: UnsupportedOperation …","interval_ms":60000}]}
```
Go: `PollerTierDTO{Tier, Label string; Core, Failing, Throttled bool; LastOKAt *time.Time; LastError string; IntervalMs int64}`; `PollerItemDTO.Tiers []PollerTierDTO`. Non-admins get `last_error` only on failing tiers (existing rule, applied per tier). Tiers sharing a label are merged into one entry (worst state wins, oldest `last_ok_at`).

`GET /api/services/{id}/deploy-settings`: existing body plus
```json
"partial":[{"part":"scheduled_actions","code":"aws_error","message":"Scheduled actions could not be read: UnsupportedOperation …"}]
```
Go: `PartialErrorDTO{Part, Code, Message string}`; `DeploySettingsResponse.Partial []PartialErrorDTO` (never null). `POST …/changeset` 422 `invalid` when the request edits a part in `partial` (Decision 5).

`GET /api/services` (scope params `account`, `region`, `tag` as elsewhere):
```json
{"items":[{"id":"…","name":"payments-api","cluster":{"id":"…","name":"core-services"},"account_id":"…","account_name":"homelab-admin","region":"us-east-1",
  "tag":{"id":3,"name":"Prod","color":"red"},"tag_inherited":true,"status":"failing","attention":"2 tasks stopped with an error in the last hour.",
  "family":"payments-api","revision":42,"image_tag":"1.18.2","running":3,"desired":3,"cpu":"512","memory":"1024",
  "latest_registered_revision":43,"last_change_at":"…","rollout":"in_progress|failed|completed|null",
  "can":{"operate":true,"logs":true,"terminal":false}}],
 "total":15,"truncated":false,"polled_at":"…"}
```
Go: `ServiceListItemDTO` (new, `dto_v4.go`), `ServiceListDTO{Items []ServiceListItemDTO; Total int; Truncated bool; PolledAt time.Time}`. `attention` is the newest open alert text for the service or `""`.

`GET /api/alerts` item adds:
```json
"cause":"2 tasks stopped with an error in the last hour.",
"next_step":{"label":"Open logs","href":"/services/<id>/logs"},
"just_before":{"at":"2026-11-06T09:48:00Z","text":"Deployed :43 (dana)","source":"harbormaster|deployment"}
```
`just_before` is `null` when unknown. Go: `AlertStepDTO{Label, Href string}`, `AlertChangeDTO{At time.Time; Text, Source string}`.

`GET /api/services/{id}/logs?task=<id>&limit=20` → existing `LogsHistoryDTO`, lines restricted to that task's streams.

TS (`web/src/api/types.ts`, `deploy.ts`, `observe.ts`): `PollerTierDTO`, `PollerEntryDTO.tiers`, `PartialErrorDTO`, `DeploySettingsResponse.partial`, `ServiceListItemDTO`, `ServiceListDTO`, `AlertDTO.cause|next_step|just_before`. Hooks (`hooks.ts`/`observe.ts`): `useServicesList(filters: Filters)` key `['services', filters]` live; `useDataProblems(labels?: string[]): {items: PollerEntryDTO[]; affected: PollerTierDTO[]; current: number; total: number}` (derived from `usePollerStatus`, memoised); `useTaskLogs(serviceId: string, taskId: string, enabled: boolean)` key `['logs', serviceId, 'task', taskId]`.

## Wave 0

### Lane 0A: tokens, base, primitives, Icon, router stub — *Sonnet*

Files: owns per table. Read-only: `docs/redesign-v4/mockup/src/hm.css`, `icons.json`, every `*.module.css` (to learn which token names are in use: `grep -rho 'var(--[a-z0-9-]*)' web/src | sort | uniq -c`).

Produces (exact signatures):
- `Icon({ name: IconName; size?: 12 | 14 | 16 | 18; label?: string; className?: string })`
- `StatusDot({ status?, clusterStatus?, kind?, bare?: boolean, children?, title? })` (adds `bare`; shape per Decision 3)
- `Panel({ title, sub?, toolbar?, actions?, children, flush?, legend?, footer?, label?: string, loading?: boolean, error?: { message: string; detail?: string; onRetry?: () => void } })`; `error` renders `PanelError` inside the body in place of children; `loading` renders `Skeleton`
- `PanelError({ message, detail?, onRetry? })` (role="alert", button "Retry", `detail` behind `<details>` "Show error")
- `Skeleton({ lines?: number; height?: number; label?: string })` (role="status")
- `Popover({ open, onClose, label, align?: 'left' | 'right', width?: number, children, role?: 'dialog' | 'menu' | 'group' })` (Esc and outside click close; focus moves in and returns)
- `Drawer({ open, onClose, title: ReactNode, sub?, wide?, footer?, children })` (role="dialog" aria-modal, Esc closes, scrim)
- `StackBar({ parts: { n: number; color: string; label: string }[]; total?: number; label: string; legend?: boolean })` (role="img" with the counts in `aria-label`)
- `ColumnPicker({ columns: { key: string; label: string; locked?: boolean }[]; visible: string[]; onChange: (keys: string[]) => void })` + `useColumnPrefs(storageKey: string, defaults: string[]): [string[], (keys: string[]) => void]`
- `DataTable` props add `stickyActions?: boolean` (last column `position: sticky; right: 0`), `visibleColumns?: string[]`, `rowAction?: (row) => ReactNode`; row height from `var(--row)`
- `KpiItem` adds `href?: string`, `tone?: 'ok' | 'warn' | 'bad' | 'exp'`
- `Banner` adds `action?: ReactNode`, `compact?: boolean`, `data-hm-banner` attribute
- `DataBanner({ tiers: string[] })`: moved to lane 0D, because it consumes 0B's `useDataProblems` and 0A's `Banner compact`.
- `web/src/app/density.ts`: `readDensity(): 'compact' | 'comfortable'`, `applyStoredDensity()`, `toggleDensity()`, `useDensity()` mirroring `theme.ts`
- `web/src/styles/layout.css` global utilities: `.hm-wide-split`, `.hm-split-sticky`, `.hm-fill`, `.hm-fill-col`, `.hm-fill-scroll`, `.hm-fields`, `.hm-sr` (from `hm.css` lines 153–164, 77)
- Router: `<Route path="services" element={<ServicesPage />} />` before `services/:serviceId`; stub `ServicesPage` renders `<h1>Services</h1>` + `EmptyState` "Loading services."; `playwright.config.ts` regexes pre-register `services|shell|freshness|attention|task-drawer|services-live|deploy-partial` (observe set: `services`, `shell`, `attention`, `services-live`; flows: `freshness`, `task-drawer`, `deploy-partial`).

Tasks (each test-first):
- [ ] `tokens.test.ts` (new, reads `tokens.css` as text): "dark and light define --ok --warn --bad --info --exp --mute --s1..--s6 --row --pad" and "legacy names are aliases of the new ones". Then rewrite `tokens.css` (Decision 1) and `base.css` (body `13px/1.5`, `font-variant-numeric: tabular-nums`, `:focus-visible` ring, `h1–h4,p { margin: 0 }`).
- [ ] `Icon.test.tsx`: renders a `path` with the `d` of `ICONS.search`; `aria-hidden` without label; `role="img"` + name with label; unknown name is a type error (compile).
- [ ] `StatusDot.test.tsx`: class `st-ok|warn|bad|info|exp|mute` per status; `bare` omits the text; text still present otherwise (shape + colour + text).
- [ ] `Panel.test.tsx`: `error` shows message, Retry calls `onRetry`, children not rendered; `loading` renders a status skeleton; `label` sets `aria-label` on the `section`.
- [ ] `Popover.test.tsx`, `Drawer.test.tsx`: open/close via Esc and scrim, focus returns to the opener, `aria-modal` on Drawer.
- [ ] `StackBar.test.tsx`: `aria-label` "Healthy 2, Unhealthy 1, Missing 0 of 3"; zero parts render `min-width` segments only for n>0.
- [ ] `ColumnPicker.test.tsx`: locked columns cannot be toggled; "Reset to default"; prefs persist under the storage key.
- [ ] `DataTable.test.tsx`: "sticky actions column keeps position: sticky and the name column stays first"; `visibleColumns` hides columns; row height follows `data-density`.
- [ ] `KpiStrip.test.tsx`: `href` renders the cell as a link; tone class applied.
- [ ] `density.test.ts`: toggle writes `harbormaster-density` and `data-density`.
- [ ] Router test (`router.test.tsx` or extend `Shell.test.tsx`): `/services` renders the stub heading.
- [ ] Remove the two-letter glyph CSS from `components` (shell's own glyph CSS is lane 1A's). Commit per primitive: `feat(ui): …` with the why.

Acceptance: `make lint`, `make test` green; existing component tests updated to the new markup; no page behaviour changed; `git grep -n "font-size: 1[01]px" web/src` returns nothing.

### Lane 0B: API contracts, services list endpoint, fixtures — *Sonnet*

Read-only: `internal/api/clusters.go` (how `ClusterServiceDTO` rows are built), `internal/store/snapshots_read.go` (`ListServices`, `Visibility`), `internal/api/apitest_test.go` helpers, `internal/api/tagfilter_test.go` (scope tests pattern), `web/src/test/contract/contract.test.ts`.

Tasks:
- [ ] Go DTOs in `dto_v4.go` (`PollerTierDTO`, `PartialErrorDTO`, `ServiceListItemDTO`, `ServiceListDTO`, `AlertStepDTO`, `AlertChangeDTO`); add fields `PollerItemDTO.Tiers` (`dto.go`), `DeploySettingsResponse.Partial` (type lives in `deploy_settings.go`, 1B's file: 0B adds only the one field line and the `make([]PartialErrorDTO,0)` initialisation, noted in the commit; 1B owns the file from wave 1), `AlertDTO.Cause|NextStep|JustBefore` (`observe_alerts.go`: same one-field rule, `JustBefore *AlertChangeDTO`). Existing handlers emit empty values so nothing else changes.
- [ ] `services_list_test.go` (test-first, helpers `newTestServer`, `seedMockupTags`, `pollAll`, `login`): `TestServicesList_AdminSeesEstate` (15 items, `truncated=false`, `payments-api` has `tag_inherited=true`, `artifact-proxy` has `tag.name=="Prod"` and `tag_inherited=false`); `TestServicesList_ScopeAndTag` (`?tag=<Prod>` → 5; `?account=731022849906` → 2; `?tag=abc` → 422); `TestServicesList_NonAdminNeverWidens` (omer: no Prod items; `can.terminal=false` everywhere); `TestServicesList_AttentionText` (image-resizer `attention != ""` after the alerts tier ran, `rollout=="failed"`); `TestServicesList_Truncated` (limit injected via an unexported `servicesListCap` var set to 3 in the test). Implement `listServicesFlat` + route `GET /api/services` in `server.go`.
- [ ] `contract_test.go`: register the new DTOs; regenerate `shapes.json`; TS types; `fixtures.ts` adds `POLLER_TIERS` (one target with scaling failing, core current), `SERVICES_LIST`, `DEPLOY_SETTINGS_PARTIAL` (scheduled_actions failed), `ALERTS` items with `next_step` and `just_before` (one null); `msw.ts` handlers for `/api/services`, `/api/services/:id/logs` with `task` (returns 3 lines), updated `/api/poller`; `contract.test.ts` entries.
- [ ] Hooks (`hooks.test.ts` / `observe.test.ts`): `useServicesList` builds `?account&region&tag`; `useDataProblems(['Auto scaling'])` returns `affected` of length 1 and `current=0,total=1` for the fixture; `useTaskLogs` disabled when `enabled=false`.
- [ ] Commit: `feat(api): v4 contracts — per-tier poller status, partial deploy settings, flat services list`.

Acceptance: `go test ./internal/api` green; contract test green; `npm test` green; no handler behaviour changed except the new route.

### Lane 0C: floci harness and check suite — *Sonnet*

Read-only: `docs/redesign-v4/floci-review/*.sh`, `cmd/harbormaster/serve.go` (`LoadDefaultConfig`), `internal/metrics/builders.go` (metric names, dimensions), `tools/e2eschema/main.go`, `web/e2e/fixtures/test.ts` (CSP guard, reuse), `web/e2e/helpers.ts` (`login`, `saveScreenshot`).

Produces:
- `tools/floci/docker-compose.yml`: `floci` (`floci/floci:2.4.0`, `FLOCI_SERVICES_ECS_MOCK=true`, port `4566`, healthcheck) and `pg` (`postgres:16.4-alpine`, port `55433`, password `hm`). No Docker socket mount.
- `tools/floci/aws-config` (`[profile floci]` region `eu-west-1`, `endpoint_url = http://127.0.0.1:4566`, `output = json`) and `tools/floci/aws-credentials` (`floci`/`floci` dummy keys). `tools/floci/env.sh` exports `AWS_CONFIG_FILE`, `AWS_SHARED_CREDENTIALS_FILE`, `AWS_PROFILE=floci`, `AWS_ENDPOINT_URL=http://127.0.0.1:4566`, `AWS_EC2_METADATA_DISABLED=true`, `AWS_SDK_LOAD_CONFIG=1` and refuses (exit 2) when `AWS_ENDPOINT_URL` host is not `127.0.0.1`/`localhost` or when `AWS_PROFILE != floci`.
- `tools/floci/seed.sh` (idempotent; refuses like `seed-floci.sh` line 6): IAM role `harbormaster-access`; three regions (`eu-west-1` 10.10, `us-east-1` 10.20, `eu-central-1` 10.30) each with VPC, three subnets, IGW, NAT, SG, EFS; clusters/services as in `seed-floci.sh` plus `report-worker` mounting the EFS volume, `payments-api` with a second container `otel-collector`, environment variables and a container health check; scalable target + CPU target-tracking policy on `payments-api`; stop every `image-resizer` task with the OOM reason and one `catalog-api` task with "Your Spot Task was interrupted."; a standalone `run-task` `release-build` in `ci-tools`-equivalent cluster; log groups `/ecs/<svc>` with streams `ecs/<container>/<taskId>` containing INFO/WARN/ERROR lines for the running tasks; `cloudwatch put-metric-data` into `ECS/ContainerInsights` for `CpuUtilized`, `MemoryUtilized`, `CpuReserved`, `MemoryReserved`, `RunningTaskCount` with `ClusterName`+`ServiceName` and cluster-only dimensions, 24 points at 5-minute spacing, for every service except those in `edge-proxy` (Insights off). Prints a summary.
- `tools/floci/prepare.sh`: `harbormaster migrate`; `create-admin --username admin --password-stdin` (ignores "already exists"); starts nothing.
- `tools/floci/connect.sh`: signs in (`POST /api/auth/login`), completes the forced password change (`POST /api/auth/password`), reads `csrf_token` from `/api/me`, external id from `/api/accounts/external-id`, `POST /api/accounts` `{name:"floci", role_arn:"arn:aws:iam::000000000000:role/harbormaster-access", external_id, regions:[eu-west-1,us-east-1,eu-central-1]}` (skips if an account named floci exists), tags `core-services` Prod via `/api/tag-assignments`, creates user `omer` (Operator on Dev) and performs one restart and one desired-count change on `notifications` through the API (F7). Polls `/api/clusters` until ≥ 4 clusters.
- Makefile targets: `floci-up` (compose up + wait), `floci-seed`, `floci-serve` (prepare + serve in foreground with `HM_LISTEN=127.0.0.1:${HM_FLOCI_PORT:-8482}`, `HM_DATABASE_URL=postgres://postgres:hm@127.0.0.1:55433/postgres?sslmode=disable`, `HM_COOKIE_SECURE=false`, `HM_SESSION_KEY=floci`, `HM_POLL_SCALING=10s`, `HM_POLL_TARGET_HEALTH=10s`, `HM_POLL_NETWORK=20s`, `HM_POLL_STORAGE=20s`, sourcing `env.sh`), `floci-check` (`make build`, prepare, then `cd web && npx playwright test -c playwright.floci.config.ts`, then restores `internal/web/dist/index.html`; the Playwright `webServer` starts `../bin/harbormaster serve` with the same env), `floci-down`. All refuse when `tools/floci/env.sh` refuses.
- `web/playwright.floci.config.ts`: `testDir ./e2e-floci`, `workers 1`, `HM_FLOCI_PORT`, `screenshot: 'only-on-failure'`, `HM_E2E_SCREENSHOT_DIR` honoured, projects `setup` (`floci.setup.ts`: runs `connect.sh` logic in TS via `request`, stores `.auth-floci/admin.json`), `routes`, `focused`.
- `web/e2e-floci/allowlist.ts`: `export const EXPECTED_FAILURES: { method: string; path: RegExp; status?: number; until?: 'wave2' }[]` — initial: `GET /api/services/*/deploy-settings` 502 (until wave 2), `GET /api/services/*/logs/tail` (any), `GET /api/tasks/*/exec` (any), `GET /api/accounts/test`? none; everything else fails the test.
- `web/e2e-floci/routes.spec.ts`: builds the route list at runtime from `/api/clusters` and `/api/services` (first cluster id, first service id, first standalone task short id); for each route × `[1440, 2560, 3840, 390]` × `['dark','light']` (theme via `addInitScript` setting `localStorage harbormaster-theme`): `goto`, wait for `main` and `networkidle`, assert no `pageerror`, no console `error`, `document.documentElement.scrollWidth <= innerWidth` and `main.scrollWidth <= main.clientWidth`, every response with status ≥ 400 matches the allowlist, at most one `[data-hm-banner]` whose text starts with "Data for" (no stacked poller banners), screenshot named `<route>-<w>-<theme>.png` when `HM_E2E_SCREENSHOT_DIR` is set.
- `web/e2e-floci/focused.spec.ts`: "terminal fills to the window bottom" (`[data-testid=terminal-host]` bottom within 24 px of `innerHeight` at 1440×900 and 3840×2160); "logs fill to the window bottom"; "only the scheduled actions panel degrades on Deploy" (Containers, Task size, Service and the Policies table render; exactly one `role=alert` inside the Auto scaling panel, text contains "Scheduled actions could not be read", Retry present; Image tag combobox enabled); "freshness control counts core data" (control reads `/^\d+ of \d+ regions current$/`; popover lists "Auto scaling" as stale for each region and "Clusters and services" as current; Overview shows no `[data-hm-banner]`); "overview and deploy use two columns at 2560" (`.hm-wide-split` children side by side); "no page error on 390".

Tasks:
- [ ] `tools/floci/env_test.sh` (bats-free: plain sh test run by `make floci-selfcheck`): env.sh refuses a non-local endpoint; seed.sh refuses without profile floci.
- [ ] Bring up floci, seed, connect; record the exact failing calls into the allowlist with a comment per entry naming the floci limitation.
- [ ] Write `routes.spec.ts` and `focused.spec.ts`; `focused` tests that cannot pass yet are written now and `test.fixme`'d with the owning lane in the reason (2A removes the fixmes).
- [ ] `docs/redesign-v4/floci-review/README.md`: how to run, what fails and why.
- [ ] Commit: `feat(tooling): floci harness and Playwright floci check suite`.

Acceptance: `make floci-up floci-seed floci-check` runs end to end on the wave-0 branch; `routes` passes except the allowlisted 502; `focused` has 4 fixmes; `make floci-down` leaves no container.

## Wave 0 follow-up

### Lane 0D: DataBanner — *Sonnet*

Runs after 0A, 0B and 0C are merged. Owns `web/src/components/DataBanner.*`. `DataBanner({ tiers: string[] })` uses `useDataProblems(tiers)` and renders one compact amber Banner "Some data on this page is stale: <labels>", or nothing. `DataBanner.test.tsx` (MSW `POLLER` fixture with a failing scaling tier): renders for `['Auto scaling']`, nothing for `['Tasks']`.

## Wave 1

Common to every frontend lane: use only wave-0 primitives and tokens; delete page-local reimplementations (own `.pill`, own tables, own stripes); every panel that owns a request renders `Panel error={…}` with Retry (`query.refetch`) and `loading` skeletons, never a blank; main filter input gets `data-hm-filter`; chart containers `max-height: 340px`; update the lane's e2e specs to the new markup in the same commit as the markup change; no e2e assertion on a backend change that is not merged (those go to wave 2).

### Lane 1A: shell — *Sonnet*

Consumes: `Icon`, `Popover`, `Drawer`, `Dialog`, `StatusDot`, `useDataProblems`, `density.ts`, `visibleTagIds`.
Produces: `Sidebar` (groups Overview/Services/Clusters/Tasks; "Infrastructure" Networks, Storage; "Insights" Analytics; "Admin" Settings or "Account" My access; icons from `ICONS`; account menu at the foot via `Popover` with "My access", "Change password" (has_password), "Link SSO account" (rule unchanged), "Sign out"; Services nav active for `/services*`), `TopBar` (`ScopeControl`, wrapping crumbs with `min-width: 0`, search `flex: 0 1 280px; min-width: 160px`, `FreshnessControl`, Activity button with count, density, theme, help), `ScopeControl` (one button "<account> · <regions> · <tags>"; popover with the three existing selects/multiselect from `Filters` and "Saved scopes" chips + "Save current" prompting a name; `scopes.ts` with `loadScopes(user)`, `saveScope`, `removeScope`, `applyScope(scope, me): Filters` dropping invisible tag ids), `FreshnessControl` ("N of M regions current", `StatusDot` kind by worst state; popover per account/region: stale labels with age "Auto scaling is 4 min old", current labels, raw error in `mono` only under "Show AWS error"; link "Open AWS accounts" for admins), `ActivityRail` → `Drawer` (never open by default; `railOpen` default false, no docking), `KeyboardHelp` dialog, `shortcuts.ts` (`useShortcuts(navigate)` implementing Decision 10), `PollerBanners` left in place but unmounted from `Shell` (2C deletes the file).

Tests (test-first): `Sidebar.test.tsx` "groups and icons; Services active on a service page; account menu lists the three rules"; `ScopeControl.test.tsx` "label summarises account, region count and tags"; `scopes.test.ts` "applying a saved scope drops tag ids outside visibleTagIds", "scopes are stored per user"; `FreshnessControl.test.tsx` "names only the failed data type", "2 of 3 regions current from the fixture", "non-admin sees no raw error for current tiers"; `shortcuts.test.ts` "g o navigates", "g then timeout then o does nothing", "g o is ignored inside inputs and dialogs", "/ focuses [data-hm-filter]", "? opens help"; `Shell.test.tsx` "activity drawer closed by default; no PollerBanners rendered"; `layout.test.tsx` updated (no rail docking); `CommandPalette.test.tsx` adds "Go to" rows Services and Analytics.
e2e: `shell.spec.ts` (new: scope save/apply, freshness control text on the healthy estate "N of N regions current", density toggle persists, `g s` lands on /services, `?` dialog); `smoke.spec.ts` ("throttle and stale banners absent" becomes "freshness control reads all current"); `layout.spec.ts` (rail no longer docked: main width expectation = 2560 − 212); `tag-filter.spec.ts` (selectors through the scope popover).

Acceptance: INVENTORY Shell section fully covered (tooltips, sign-out failure toast, collapsed account menu aria name, auto-collapse < 1100, palette groups); crumbs never overlap search at 1024 (unit test on class names + e2e in `shell.spec.ts` measuring bounding boxes).

### Lane 1B: backend — per-tier status, partial deploy settings, task logs, fake failures — *Sonnet*

Tests first: `observe_status_test.go` `TestPollerStatus_TiersListed`, `TestPollerStatus_NonCoreTierFailureKeepsCoreCurrent` (upsert a failing `scaling` loop: item `failing=false`, tier "Auto scaling" `failing=true`), `TestPollerStatus_TierLabelsMerge` (services + clusters loops → one "Clusters and services" entry with the older `last_ok_at`), `TestPollerStatus_NonAdminTierErrorHidden`; `internal/deploy/settings_test.go` `TestSettingsFrom_PartialScheduledActions` (fake AutoScaling returning an error from DescribeScheduledActions: `Partial` has one entry, policies present, `Version` stable across two calls), `TestSettingsFrom_FatalErrorsStayFatal`; `internal/poller/scaling_test.go` `TestCollectServiceScalingConfigPartial`; `internal/api/deploy_changeset_test.go` `TestChangeSet_AppliesWhenScheduledActionsUnreadable` (env change applies), `TestChangeSet_RefusesScheduledActionEditWhenUnreadable` (422 with the Decision 5 message); `observe_logs_test.go` `TestLogs_TaskParamRestrictsStreams`, `TestLogs_TaskParamUnknownIs404`; `internal/awsx/fake` `TestFake_FailInjection`.
Produces: `poller.CollectServiceScalingConfigPartial(ctx, api, key) (model.Scaling, []PartError)` with `PartError{Part string; Err error}`; `deploy.DeploySettings.Partial []deploy.PartError`; `(DeploySettings).version()` over readable parts; `HM_FAKE_FAIL` parsing in `awsx/fake` (Decision 17); the scaling poll tier unchanged (still fails as a tier when scheduled actions fail, which is what per-tier status now reports correctly).
Acceptance: `go test ./...` green; `make e2e` green (no DTO shape change); on floci `GET …/deploy-settings` is 200 with `partial=[scheduled_actions]`.

### Lane 1C: backend — attention context — *Sonnet*

Tests first: `internal/store/attention_test.go` `TestLastChangeBefore_PrefersNewestOfOperationOrDeployment`, `TestLastChangeBefore_IgnoresECSEventsAndOutsideWindow`; `observe_alerts_test.go` `TestAlerts_NextStepPerKind`, `TestAlerts_JustBeforeFromOperationLog` (append a harbormaster operation 10 min before a seeded alert → `just_before.text` names it), `TestAlerts_JustBeforeOmittedWithoutChange` (null), `TestAlerts_AccountProblemNextStepIsAccounts`.
Produces: `(*Store).LastChangeBefore(ctx, key model.ServiceKey, before time.Time, window time.Duration) (*ChangeRef, error)` with `ChangeRef{At time.Time; Text, Source string}`; `alertDTO` filling `Cause`, `NextStep`, `JustBefore` (one batched query per response, not per alert: `LastChangesBefore(ctx, keys []model.ServiceKey, …)`).
Acceptance: `go test ./internal/api ./internal/store` green; `/api/alerts` latency unchanged within noise on the fake estate (existing benchmark or a `-bench` note in the commit).

### Lane 1D: services page — *Sonnet*

Consumes: `useServicesList`, `DataTable` (sticky actions, `visibleColumns`), `ColumnPicker`, `useColumnPrefs`, `Seg`, `Drawer`, `StatusDot`, `TagChip`, `RestartDialog` (read-only import from `../RestartDialog`), `usePins`.
Produces: `ServicesPage.tsx`, `ServicesTable.tsx`, `ServiceDrawer.tsx`, `filterServices.ts` (`applyView`, `applyStatus`, `applyQuery`, `sortServices` pure), `useServicesState.ts` (URL params `view`, `status`, `q`, `sort`, `dir`, `page`).
Tests: `filterServices.test.ts` (view/status/query/sort cases incl. `running < desired` tone, tie-break by name); `ServicesPage.test.tsx` "saved view tabs with counts from the list", "status segment counts", "column picker hides Image tag and persists", "25 per page pager", "row Restart disabled with the rollout tooltip when rollout is in_progress", "Details opens the drawer with the KV rows and six links", "empty state has Clear filters", "truncated note"; crumbs "Services".
e2e `services.spec.ts`: pavel sees 15, Problems view shows image-resizer first, My pins after pinning, omer never sees Prod, drawer opens and Esc closes.
Acceptance: INVENTORY has no prior Services page; README item 7 fully met except F1.

### Lane 1E: tasks page, task drawer, TaskTable — *Sonnet*

Consumes: `DataTable`, `ColumnPicker`, `Drawer`, `useTaskLogs`, `StopTaskDialog`, `stopStatus.tsx`.
Produces: `TaskDrawer({ task: TaskDTO; onClose; onStop })` (identity KV, status with shape, friendly + raw stop reason, containers from `exec-targets` when running, "Recent log lines" via `useTaskLogs` (20 lines, link "Open logs"), actions Logs/Terminal/Stop with the existing permission rules); `TaskTable` with `onOpen(task)`, sticky actions, visible columns ("Task, Service, Status always show"); Tasks page with column picker, `data-hm-filter`.
Tests: `TaskDrawer.test.tsx` "renders friendly and raw reason", "standalone task shows the no-logs line", "Stop hidden without operate"; `TaskTable.test.tsx` updated; `Tasks.test.tsx` "column picker persists", "clicking a task id opens the drawer", "Group repeats unchanged"; `TasksTab.test.tsx` same drawer on the service tab.
e2e: `tasks.spec.ts`, `service-observe.spec.ts` updated to the new selectors (drawer open, stop via drawer).

### Lane 1F: clusters list and detail — *Haiku*

Changes: `ClustersTable`/`ServicesTable` on `DataTable` with sticky actions + `ColumnPicker` (`harbormaster-columns:clusters`), stripes and `StatusDot` shapes, Tasks-by-service bars as `StackBar`, "A dash in CPU…" note kept, KPI strip links; `data-hm-filter` on the name search; `DataBanner tiers={['Clusters and services','Tasks']}` under the header. Tests: `Clusters.test.tsx` "column picker", `Cluster.test.tsx` "stacked bars aria label"; e2e `clusters*.spec.ts`, `cluster.spec.ts` selectors.

### Lane 1G: overview — *Sonnet*

Consumes: `AlertDTO.cause|next_step|just_before` (fixtures), `KpiItem.href`, `StackBar`, `.hm-wide-split`.
Produces: `NeedsAttention` rows: severity `StatusDot`, service/cluster links or account link, age, `cause`, "Next step" link, "Just before: …" only when present, Logs/Tasks/Restart (rules unchanged), account rows with "Open accounts"; KPI links (Clusters → `/clusters`, Services → `/services?view=problems`, Tasks → `/tasks`, Error stops → `/analytics/reliability`) and a `counts.test` asserting the tile numbers equal the list counts from the same fixture; Triage layout in `.hm-wide-split` (attention | map+charts); stacked bar chart and reason bars via `StackBar`/`Meter`; `DataBanner tiers={['Clusters and services','Tasks','Alerts']}`.
Tests: `NeedsAttention.test.tsx` "shows cause, next step and just before", "shows no Just before line when the API omits it", "account problem row links to settings"; `Overview.test.tsx` "KPI tiles link and agree with the lists", "two columns class present"; `EstateView` unchanged behaviour test retained.
e2e: `overview.spec.ts`, `observe.spec.ts`, `overview-noaccess.spec.ts` selectors; the "just before" live assertion is wave 2 (`attention.spec.ts`).

### Lane 1H: service page frame and non-deploy tabs — *Sonnet*

Changes: `tabs.ts` per Decision 11 (`TABS` gains `group?: 'configure'`, label "History"; `Tabs` renders the gap + "Configure" label — `Tabs.tsx` is 0A's component: 0A adds `TabItem.group?: string` and renders a `.tabgap` + muted label before the first item of a new group; 1H only supplies data); `ServiceHeader` primary Deploy link; `OverviewTab` `StackBar` for target/task health; Metrics/Scaling/Task definition/Logs/Terminal/History restyle on `Panel` with `error`/`loading`; logs and terminal `.hm-fill-col`/`.hm-fill-scroll` so they fill to the window bottom at every height; `DataBanner` on Scaling (`['Auto scaling']`) and Overview tab (`['Target health','Tasks']`); charts cap 340 px.
Tests: `tabs.test.ts` order and History label; `ServicePage.test.tsx` "Deploy link hidden when locked"; `OverviewTab`/`MetricPanels` tests for StackBar aria; `LogsTab.test.tsx`, `TerminalTab.test.tsx` unchanged behaviour; `charts.test.tsx` max-height.
e2e: `service.spec.ts` ("target health donut" → stacked bar), `terminal.spec.ts`, and `layout.spec.ts` "logs and terminal fill the viewport height" tightened to `innerHeight − bottom ≤ 24` (that test lives in 1A's `layout.spec.ts`: 1H reports the number, 1A sets it; or 1H adds the assertion to `service.spec.ts` instead — do the latter).

### Lane 1I: deploy change set — *Sonnet*

Consumes: `DeploySettingsResponse.partial` fixture, `.hm-split-sticky`, `.hm-wide-form`, `Panel error`.
Produces: `PendingChangesAside` (sticky; "N changes"; per-field "label: before → after"; problems list; loaded-revision chip + Dismiss; Discard/Review…); `ScalingPanel` reading `partial`: scheduled-actions section shows `PanelError` with the message and Retry (`refetch`), its Add/Edit/Remove disabled with tooltip "Scheduled actions could not be read", policies and target editable; `changeSummary.ts` gains `pendingRows(draft, settings): PendingRow[]`; OOM hint only when `stoppedTasks` has an OOM stop (already so; add the test that pins it); Containers panel disabled tooltip unchanged; `ReviewChangesDialog` untouched except tokens.
Tests: `DeployTab.partial.test.tsx` "locks only the scheduled actions editor", "Retry refetches deploy settings", "Review is enabled with partial when the draft does not touch scheduled actions"; `PendingChangesAside.test.tsx` before/after rows for env, image tag, desired count, circuit breaker, policy; `TaskSizePanel.test.tsx` "no OOM hint without an OOM stop"; existing deploy tests updated.
e2e: `deploy.spec.ts`, `change.spec.ts` selectors (pending aside instead of the bottom bar). Degraded-mode e2e is wave 2.

### Lane 1J: analytics — *Haiku*

Changes: KPI captions wrap (`overflow-wrap: anywhere`, no `white-space: nowrap`), `retentionText` per Decision 14 (test cases 2160h/168h/8760h/720h/0), Capacity donuts → `StackBar`, panels on `Panel` with `error`, `DataBanner` not needed (analytics reads its own tables). e2e `analytics.spec.ts` selectors.

### Lane 1K: networks, storage, auth, my access — *Haiku*

Changes: tokens/primitives restyle; Networks list + detail on `.hm-fill`; `DataBanner tiers={['Networks']}` / `['Storage']`; Storage table `DataTable` sticky actions; auth cards unchanged copy; My access table. Tests updated; e2e `networks`, `storage`, `me`, `security` selectors.

### Lane 1L: settings — *Sonnet*

Changes: permission matrix cells per Decision 15 (`PermCell` → button + `Popover`; `PermCell.test.tsx` "preset Operator sets V L O", "turning off View clears the cell", "popover closes on Esc"); users table row "Actions" `Popover role="menu"` with the existing buttons and their disabled reasons; accounts table status cell with `<details>` "Show AWS error" and per-region test results (`Accounts.test.tsx` "error expands"); SSO checklist per Decision 16 (`SSOPage.test.tsx` "steps derive from config", "Turn on blocked without break-glass keeps the tooltip"); tags, audit, general restyle; `HeaderAction` slot kept. e2e `settings`, `admin`, `sso` selectors.

## Wave 2

### Lane 2A: integration e2e — freshness and partial deploy — *Sonnet*

`playwright.config.ts` adds project `chromium-degraded` (`HM_FAKE_FAIL=scheduled_actions,scaling_tier`, own port offset `+1`, `testMatch /(freshness|deploy-partial)\.spec\.ts/`, after `chromium-flows`). `freshness.spec.ts`: control reads "0 of 4 regions current" with popover "Auto scaling" stale per region and "Clusters and services" current; Overview shows no page banner; Scaling tab shows one compact `DataBanner`; Clusters page none. `deploy-partial.spec.ts`: Deploy tab renders all panels; scheduled actions degraded with Retry; env change applies (toast "Applied the changes to …"); adding a scheduled action is refused with the 422 message. Removes the four `test.fixme` in `web/e2e-floci/focused.spec.ts` (owned by 2A in this wave).

### Lane 2B: integration e2e — services, attention, drawer logs — *Sonnet*

`services-live.spec.ts` (counts agree with `/api/services`, Problems view = alerts count, omer scope), `attention.spec.ts` (deploy a revision to `web-frontend`, then assert its later alert row shows "Just before: Deployed …"; if no alert follows, assert the row for `image-resizer` shows no Just before line), `task-drawer.spec.ts` (drawer shows 20 payments-api log lines for a running task; stop via drawer).

### Lane 2C: legacy removal and floci allowlist — *Haiku*

Delete `PollerBanners.tsx`, `charts/Donut.tsx`, `components/Card.*`, `components/Tile.*` after `git grep` shows no importers; remove dead CSS (`.glyph`, old `.pill` table usages); `web/e2e-floci/allowlist.ts` drops the `deploy-settings` entry; `docs/redesign-v4/floci-review/README.md` updated.

## Integration gates (lead)

After each wave, on `feat/ui-redesign-v4` after merging in the listed order (`git merge --no-ff lane/v4-<lane>`):

```
make lint
make test
HM_TEST_DATABASE_URL=… make e2e            # restore: git checkout -- internal/web/dist/index.html
make floci-up && make floci-seed && make floci-check && make floci-down
```

Passes when: lint clean; `go test ./...` and `vitest --run` green; Playwright `setup`, `chromium`, `chromium-observe`, `chromium-flows` (and `chromium-degraded` from wave 2) green; floci `routes` green with only allowlisted failures; floci `focused` green except the fixmes listed for that wave.

Expected failures that are floci limitations (every wave): logs live tail WebSocket, terminal connect, ECR tag listing, CloudTrail history rows, Container Insights for `edge-proxy` (seeded off on purpose). Wave 0 and 1 only: `deploy-settings` 502 (wave 0) / partial (wave 1 onward, no longer an HTTP failure after 1B merges); `focused` fixmes: wave 0 all four; wave 1 none expected to fail once 1A, 1B, 1H, 1I are merged (lead un-fixmes them locally to check, 2A removes them).

Wave 1 merge note: after 1B and 1C merge, re-run `HM_UPDATE_CONTRACT=1 go test ./internal/api -run TestContract` and confirm `shapes.json` is unchanged (DTO shapes were fixed in wave 0; a diff means a contract drift to reject).

## Final review stage (Fable, on the merged branch)

1. Spec coverage: walk `INVENTORY.md` top to bottom; for each section confirm each control, dialog, toast, lock text and empty/error string exists in the merged `web/src` (`git grep -F` per quoted string) and in an e2e or unit test. Produce a table of misses.
2. README items 1–11: one paragraph each with the evidence file and test name.
3. Review Focus 1–5: run each named test individually and read its assertions against the failure mode.
4. Floci screenshots: run `HM_E2E_SCREENSHOT_DIR=… make floci-check`, inspect every route at 390 and 3840 in both themes for overflow, legibility (12 px floor, contrast), stacked banners, blank panels.
5. Flags F1–F7: confirm each is still true or resolved, and list the follow-ups.
6. Commit hygiene: every commit on the branch ends with the two attribution lines; no `internal/web/dist` changes; conventional types.

### Critical Files for Implementation
- /Users/pavelratnitsky/GolandProjects/harbormaster/web/src/styles/tokens.css
- /Users/pavelratnitsky/GolandProjects/harbormaster/web/src/api/types.ts
- /Users/pavelratnitsky/GolandProjects/harbormaster/internal/api/poller.go
- /Users/pavelratnitsky/GolandProjects/harbormaster/internal/deploy/settings.go
- /Users/pavelratnitsky/GolandProjects/harbormaster/web/src/app/shell/TopBar.tsx
