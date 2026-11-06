# Harbormaster redesign mockup: screen author brief

You author screens for a high-fidelity, clickable mockup of Harbormaster, an AWS ECS control center for DevOps teams. Mock data only. It is a professional tool: dense, calm, fast to scan, modern without decoration. Colour is spent on state.

## Files
Root: `design/` (absolute path given in your task).
- `src/screens/<Name>.html` + `src/screens/<Name>.js`: **write only your own screens here.** Never edit `src/hm.css`, `helpers.js`, `data.js`, `shell.*`, `build.mjs`, or another agent's screens. If you need a missing class, use inline `style` with the CSS tokens. Report anything shared you wished existed.
- `src/screens/Storage.html/.js`: the reference screen. **Read it first and copy its patterns.**
- Read `src/hm.css` for the classes, `src/data.js` for the shared mock estate (`D.*`; use it so numbers agree across screens; add screen-local mock inside your .js), `src/helpers.js` for `H.*`.
- `INVENTORY.md`: exact sections, columns, labels, dialogs and states for every page of the current app. **Keep every section, column, action, dialog, state and wording in it for your screens**, except where this brief says the redesign changes them. Coverage is the point.
- Lint your screens: `node build.mjs Name1 Name2` from `design/`. Fix until it prints ✓. **Do not render, screenshot or open anything.** The lead does a single render pass.

## Screen format
`<Name>.html` starts with `<!--meta title="Clusters" nav="clusters" crumbs="Clusters" h="900"-->`, then body markup only (the shell is added for you). `crumbs` for nesting: `Clusters|Clusters > core-services|ClusterDetail > payments-api` (`Label|ArtboardName`; last is current). `nav` is one of overview services clusters tasks networks storage analytics settings. `shell="none"` drops the shell (auth pages). `h` is a rough page height at 1440 px wide.

`<Name>.js` must define exactly:
`var Screen={state:{...},vals:function(c,s){ return {...template values...}; }};`
`c` is the component (`c.setState`), `s` is state. Classic JS, no imports. Don't name state keys starting `__`.

## Template rules (violations fail silently)
- `{{a.b}}` is a dotted lookup only; never an expression. Compute in `vals`.
- `<sc-for list="{{xs}}" as="x" hint-placeholder-count="3">…</sc-for>`, `<sc-if value="{{cond}}" hint-placeholder-val="{{true}}">…</sc-if>` (no else: use two sc-if with a computed negative). `{{$index}}` is available.
- Events: `onClick="{{fn}}"`, `onChange="{{fn}}"` where fn comes from `vals`; per-row handlers go on each row object. Whole-value only.
- Attributes: `x="{{v}}"` raw, `class="st st-{{r.status}}"` interpolated. `style="{{s}}"` takes a CSS string computed in vals. Prefer classes; static inline style for one-offs.
- Real elements only: `<button type="button">`, `<a href>`, `<input id>` + `<label for>`; never onClick on div/span/tr. Every input/select/textarea has a stable unique id. Icon-only buttons get `aria-label`. Close all tags; quote all attributes; no self-closing except void/SVG shapes.
- Never put a hole inside svg `<text>` or `<title>`: it renders nothing. Use chart partials or HTML.
- No emoji, ever. No `innerHTML`, no global key handlers.
- Links between screens: `<a href="Clusters.dc.html">`. A `<button>` inside an `<a>` swallows the click: style the `<a>` as `class="btn"`.
- Artboards (exact names): Overview Services Clusters ClusterDetail Tasks TaskTerminal SvcOverview SvcTasks SvcLogs SvcMetrics SvcTerminal SvcDeploy SvcTaskDef SvcScaling SvcHistory Networks Storage AnDelivery AnReliability AnCapacity AnOperations SetGeneral SetAccounts SetUsers SetTags SetSso SetAudit MyAccess Login Setup ChangePassword States. Every service link goes to `SvcOverview.dc.html` (the mock's service is payments-api), every cluster link to `ClusterDetail.dc.html`.

## Partials and tokens
- `[[i:name]]` inserts an icon (see `src/icons.json`). Icons go in static markup only, not in sc-for data.
- `<!--@line k="chart" label="what the chart shows"-->`, `<!--@bars k="chart" label="…"-->`, `<!--@legend k="chart"-->`, `<!--@spark k="pathObj" color="var(--s1)"-->`. Values come from `H.chart({series:[{name,color:'s1',values,area:true}],x:['24 h ago','12 h','now'],marks:[{at:i,label:':42'}],unit:'%',max:100,w,h,tip:function(i){}})`, `H.bars({keys:[{name,color}],rows:[[a,b],…],x:[…],w,h})`, `H.spark(values)`, `H.series(seed,count,base,swing)`.
- `w` of a chart should approximate its rendered px width (half panel about 560, full about 1140), h 140-220.
- Service header + tabs: `<!--@svcheader tab="overview"-->` (overview tasks logs metrics terminal history deploy taskdef scaling). It renders name, status, desired stepper, Restart, Deploy, AWS console. Service screens use `crumbs="Clusters|Clusters > core-services|ClusterDetail > payments-api"`, nav clusters... use nav="services".
- Settings head + tabs: `<!--@settingsnav tab="users" action="&lt;button class=&quot;btn btn-p&quot; type=&quot;button&quot;&gt;Add user&lt;/button&gt;"-->` Simpler: omit `action` and put the primary action in your panel header. Analytics head + tabs: `<!--@analyticshead tab="delivery"-->`.
- Interactions: `H.seg(c,'key',[['id','Label',count]])` for segmented filters; `H.toggle(c,'key')`, `H.set(c,{...})`, `H.toast(c,'Saved.')`.

## Design language (use it, don't invent another)
- Status is `<span class="st st-{{x.status}}">{{x.statusL}}</span>` with status ok warn bad info exp mute: shape plus colour plus text. Row stripes via `stripe-bad|warn|info|exp` on `<tr>` for non-healthy rows. Tag chip `class="tag tag-Prod"` with `inh` when inherited (dashed). `pill pill-ok` only for discrete badges.
- Colour rules: accent is for links, active, the one primary action per view. Series `--s1..--s6`, in that order, entity keeps its colour. Status colours never mean a series. Text never wears series colour. One y-axis per chart; two measures with different scales means two charts. Legend for 2+ series. Avoid donuts: use `.stackbar` with a legend, or meters.
- Type: mono only for IDs, revisions, IPs, numbers, ARNs. Sans for names and labels. Minimum 12 px: no uppercase tracked micro-labels. Tabular numbers are automatic. Never truncate names silently: wrap or give the table `min-width` inside `.tw`.
- Density: table rows use `--row`; two-line cells get `class="two"` and `<small>`. Panels: `.panel > .panel-h(.panel-t) + .panel-b|.tw > .tbl`. Tables keep action columns visible using `.acts`. Summary before detail. KPI strip only where those figures earn it.
- Layout: `.ph`, `.kpis`, `.g2 .g3 .g21 .g12`, `.stack`, `.row`, `.toolbar`, `.field`, `.fields`, `.kv`, `.feed`, `.ban ban-warn`, `.empty`. Everything must reflow at phone width: wide tables stay in `.tw`, toolbars wrap.
- Overlays: `.scrim` (a button, onClick closes) then `.modal[.wide]` (`.modal-h .modal-b .modal-f`) or `.drawer[.wide]`. Destructive confirm uses `btn-dp`. Every dialog in INVENTORY for your screens must be reachable by clicking its trigger, with real fields and exact wording. Forms validate visibly: show at least one inline error state where INVENTORY lists one when the trigger is exercised.
- Writing: active voice, specific, plain. A control says exactly what happens. Errors state cause and fix. No em-dash asides.

## What the redesign deliberately changes (from reviewing the running app)
1. **Stale data**: stacked per-region red banners (260 px of raw errors, one failing poll tier marked whole regions stale) become one topbar freshness indicator with a popover that names what is stale and what is still current. Screens never render per-region poller banners, only a compact `ban-warn` when the data on that screen is itself affected.
2. **Activity** is a closed drawer in the shell, not a default-open 300 px rail that squeezed every table.
3. **Scope** is one control (done in shell). **Services** is a new top-level flat list.
4. **Needs attention** carries cause, the suggested next step, and what changed just before it.
5. **Tables** never hide names or actions: details open in a side drawer instead of leaving the page. Saved views, column picker and density are visible affordances.
6. **Service page**: 9 tabs become Overview, Tasks, Logs, Metrics, Terminal | Configure (Deploy, Task definition, Scaling), History. Same content, grouped by intent; Operations log is History.
7. **Partial failure**: one failed sub-request degrades only its own panel. No blank tabs; loading uses skeletons (`.sk`); a panel that failed says what failed and offers Retry while the rest of the page works.
8. **Contrast**: all text at least 4.5:1, status shape-coded, min 12 px, retention shown as "90 days" not "2160h", no truncated KPI captions.
