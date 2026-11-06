# Harbormaster UI inventory (read-only pass; no tests/fixtures read)

Conventions: "Seg" = segmented control; "(n)" = live count badge on the option. Dialog buttons: Cancel/confirm unless stated. Toasts: 5.2 s auto-dismiss; kinds ok/bad/info/warn; "sticky" toasts have an × button. Relative times render "12s ago / 5 min ago / 3 h ago / 4 d ago"; hover shows the full timestamp. Tables are sortable DataTables. Page sizes: Tasks 50, Clusters 25, cluster Services 25, Assignments 15. Status pill kinds: ok (green), warn (amber), bad (red), info (blue), mute (grey), expected (teal).

## Permissions
Six bits, one letter each, in this order: **V** View, **L** Logs, **O** Operate, **D** Deploy, **E** Edit task definition, **T** Terminal.
- Any action implies View. Turning View off clears everything.
- Presets: None 0, Viewer V, Operator V+L+O, Deployer V+L+O+D+E, Full all six, and "Custom" when no preset matches.
- Global admin = everything on every tag, plus users, tags, AWS accounts, SSO, audit and settings.
- Locks read "You need **X** on **<Tag>**". The Deploy tab is unlocked by any of Deploy, Edit or Operate, and says "Edit, Deploy or Operate".
- Operate: restart, desired count, stop task, auto-scaling edit.
- Deploy: deploy, roll back.
- Edit: register a task definition, edit containers.
- Terminal: ECS Exec.
- A user sees a cluster if they have View on at least one of its services.
- The tag filter lists only tags the user can View.

## Shell (every signed-in page except /password)
Layout: Sidebar (212 px, 52 px collapsed), TopBar, main (PollerBanners first, then page), ActivityRail (300 px, docked at >=1280 px, otherwise an overlay with backdrop).

**Sidebar**
- Brand mark + "ECS control center". Tooltip: "Running on ECS · harbormaster/harbormaster-server".
- Nav items, each with a 2-letter glyph:
  - OV Overview: red count = danger alerts.
  - CL Clusters: count = clusters; also active on /services.
  - TK Tasks: amber badge "N unhealthy", red badge "N stopped with errors in the last X min".
  - NW Networks.
  - ST Storage.
  - AN Analytics: links to /analytics/delivery.
- Admins see a group "Admin" with SE Settings (amber badge "N account issues"). Non-admins see "Account" with MA My access.
- "Pinned" section: empty text "Pin a service or cluster from its page." Each pin is a status dot, name, and a second line (cluster for a service; "Account · id" for a cluster). The dot follows live status.
- Footer: avatar with 2-letter initials, "Signed in as **user** · Global admin|User". Buttons: "Change password" (only if the user has a password), "Link SSO account" (only if SSO is configured, the user has a password and is not yet linked), "Sign out".
  - Collapsed: the avatar opens these as a menu (aria "Account menu for X").
  - Sign-out failure toast: "Sign out failed on the server. You were signed out in this browser."
- Collapse button "« Collapse [" / "»".
- Shortcuts: `[` toggles the sidebar (ignored while typing, in a modal, or with the palette open). Cmd/Ctrl+K toggles the palette. Esc closes the palette or the overlay rail. Auto-collapses below 1100 px.

**TopBar** (left to right)
- "SCOPE" label with 3 filters: select "All accounts" (account names), select "All regions", multi-select "All tags" (checkbox list; shows the chosen names comma-joined).
- Breadcrumbs (links, "/" separators, last item plain).
- Live pulse "live · polled 6s ago" (turns "stale" on error; hidden if main width < 900 px).
- Palette button "Search or run a command…" + kbd "⌘K" (Mac) or "Ctrl K".
- "Activity" toggle with red problem count.
- Theme toggle "◐" (light/dark, remembered).
- The scope is stored in the URL (?account&region&tag) and in the browser per user.

**CommandPalette** (modal)
- Input placeholder "Jump to a service, cluster or task, or type an action"; "›" prefix, "esc" kbd.
- Groups: Actions, Services, Clusters, Tasks, Go to. Each row has a tag (ACT/SVC/CL/TASK/NAV), a label, and a sub.
  - Actions: "Show problem tasks", "Toggle theme", "Toggle sidebar" (sub "["), "Toggle activity".
  - Empty query: 4 rows per group; failing services first. Typing: 6 rows per group.
  - Server search at 2+ chars; tasks at 3+.
  - Go to: Overview, Clusters, Tasks, Networks, Storage, Settings (admin) or My access.
- Empty: "No match".
- Footer: "↑↓ select", "↵ open", "⌘K toggle", "Actions respect your permissions".

**ActivityRail**
- Header "Activity" + "×" (aria "Close activity").
- Seg "Problems (n) | Changes (n) | All (n)".
- Rows: severity dot, subject link, "×N" repeat badge, age, text, actor ("Harbormaster", a username, or "ECS event · cluster").
  - Problems = current danger/warning alerts. Changes = actions made via Harbormaster.
  - Max 40 rows; repeats collapse.
- Empty: "Nothing here."

**PollerBanners** (per account/region, above the page)
- Red: "Data for *Account / region* is N min old|unavailable: <error>".
- Amber: "Updates for *Account / region* are slowed by AWS throttling".

**LinkSSODialog**
- Title "Link your SSO account".
- Sub: "You will sign in at your identity provider; the account you sign in with there is attached to this Harbormaster user. Enter your password to continue."
- Field "Password". Buttons "Cancel", "Continue to identity provider" (disabled until a password is entered). Inline error.
- Return toasts:
  - ok "Your SSO account is linked."
  - bad "That SSO identity is already linked to another user."
  - bad "Linking failed. Ask a global admin to check the audit log."

**Other shell states**
- No-access user (not admin, no perms): empty state "You don't have access to anything yet. Ask a global admin." (My access still works.)
- Unknown route: "Page not found."
- Pins: a "Pin"/"Pinned" button beside the name on cluster and service headers; title "Pin to the sidebar" / "Remove from the sidebar". They are stored per user in the browser.

**Shared widgets**
- Tag chip: coloured pill; dashed = inherited from the cluster; tooltip "Tag set on this cluster|service" or "Inherited from cluster X". Admins can click it to open EditTagDialog.
- Tag colours: neutral, red, amber, blue, violet, green.
- Tabs: a locked tab shows " ⊘" and the tooltip "You need X on Tag".

## Auth (centered card: brand + "ECS control center", h1, optional sub/info, fields, red alert, full-width submit)
- **Login**, title "Sign in".
  - Password mode: fields "Username", "Password"; button "Sign in". Errors: "Wrong username or password." / "Could not sign in."
  - SSO mode (when enabled): a big link button labelled with the configured SSO label (default text not found), then "Sign in with a password instead". Expanding shows the note "Only break-glass accounts can sign in with a password." plus the Username/Password fields and a "Sign in" button.
  - Info after sign-out: "You are signed out of Harbormaster. To sign out of your identity provider, sign out there."
  - SSO error banners: "Single sign-on failed. Try again, or ask a global admin to check the audit log." / "Your identity provider declined the sign-in." / "Single sign-on is turned off."
  - A blank card with no form shows while options load.
- **Setup**, /setup on a fresh install. Title "Create the first global admin". Fields: "Setup token" (hint "Printed in the server log at start"), "Username", "Password", "Confirm". Button "Create admin". Errors: "Passwords do not match." / "That setup token is wrong."
- **ChangePassword**, /password. Title "Choose a new password". Sub: forced = "Your password is temporary. Set a new one to continue." else "Enter your current password and a new one." Fields: "Current password", "New password", "Confirm". Button "Change password". Errors: "Current password is wrong." / "Passwords do not match." SSO-only users are redirected away.
- Loading text: "Loading…".

## Overview (/)
- Header: h1 "Overview", meta "N accounts · N regions · polled from AWS every 5 s · last poll <ago>". Seg "Layout: Triage | Estate" (remembered).
- KPI strip, 6 tiles:
  - Clusters (N healthy).
  - Services (N need attention | all steady).
  - Tasks (running, "of desired · N unhealthy", 72 h sparkline).
  - Avg CPU (sparkline).
  - Avg memory (sparkline).
  - Error stops, 24 h (red sparkline).
- **Triage layout**
  1. Panel "Needs attention" ("N open"). Seg Severity "All | Failing | Warning | In progress" (n). One row per service: status dot (Failing/Warning/In progress), title link (admins: account issues link to /settings/accounts), cluster, text, age, links "Logs", "Tasks", and button "Restart" (disabled when blocked, with a tooltip; opens the Restart dialog). Cap 6, then "Show all N" / "Show fewer". Empty: "Nothing needs attention."
  2. Left: panel "Estate map". Legend Healthy/Deploying/Degraded/Needs attention. One row per account: name, "regions · count · N ✕", then coloured cells, one per cluster (link; tooltip "name, status, running/desired tasks, region"). Empty: "No clusters." / "Loading clusters."
  3. Right column:
     - "Busiest clusters, CPU" (link "All clusters →"): rows of name, "account · region", bar (amber >55%) and %. Stale note; amber banner "CloudWatch could not be read for <regions>; those clusters are missing from this list."; empty "Container Insights is off for every cluster you can see." or "No data yet".
     - "Task stops per hour, 24 h": stacked bar chart, legend Normal / Error exit, x labels 24 h ago, 12 h, now.
     - "Why tasks stopped, 3 days": horizontal bars per stop reason.
- **Estate layout**: one panel "Services".
  - Toolbar: "Group by" Seg "Cluster | Account"; toggle "Problems only" (default on); input "Filter services or clusters". Header right: "N of M services shown".
  - Collapsible group headers: ▾/▸, severity dot, name, tag chip (cluster grouping), "account · region", and "N failing / N unhealthy / N deploying / N svc".
  - Service rows: name link, status dot, links "Logs", "Tasks". 12 rows per group, then "Show more" (up to 60). Notes: "N more services · filter to narrow down", "N steady services hidden".
  - Paging: "Show 25 more clusters|accounts". Footer: "N healthy clusters hidden" + "Show all".
  - Empty: "No services have problems." / "No services match." / "Loading services."
- Error state: an empty-state message in place of the page.

## Clusters (/clusters)
- h1 "Clusters"; meta "Created by Terraform. The tag is stored only in Harbormaster."
- Filter bar:
  - Tag Seg "All | <tag names>" (n). With more than 5 tags it becomes a select "Name count".
  - Status Seg "Any status | Problems | Deploying | Healthy" (n).
  - Text input "Filter by cluster, account or region".
- Table columns:
  - Cluster (link; account · region beneath)
  - Env (tag chip)
  - Status
  - Services (preview links, "+N")
  - Tasks
  - CPU 24h (sparkline + %)
  - Memory 24h
  - vCPU
  - Spot
  - Last change
- Default sort is Status descending. Pager.
- Note: "A dash in CPU and memory means Container Insights is off for that cluster." (tooltip "Container Insights is off")
- Empty: "No clusters match these filters." + "Clear filters". Error: "The clusters could not be loaded."

## Cluster detail (/clusters/:id)
- No access (404): "No access / You have no permissions on any service in this cluster."
- Header:
  - h1 mono name, tag chip (admin-clickable), status dot, Pin button.
  - Sub: "Account (id) · region · VPC link · N Fargate · N Spot".
  - Right: link "Tasks in this cluster", link "Open in AWS console ↗".
- KPI strip: Services (N steady); Tasks (running/desired, "N missing" | "all running", sparkline); Reserved (vCPU · GiB); CPU now; Memory now (show "–" / "Insights off" when Insights is off).
- Two panels:
  - "Cluster CPU and memory, 24 h" (actions "Container Insights"): line chart, legend CPU, Memory, Deployment. Banners: "Container Insights is off for this cluster." / error. Empty "No data yet".
  - "Tasks by service" ("bar = running · tick = desired"): bars "running / desired", with a "N more in the table below" note. Beneath it: sub-chart "Task stops per hour".
- Panel "Services" ("N services · problems first"). Columns: Service, Label (tag), Status, Task definition, Image tag, Tasks, CPU, 24 h, Memory, Last change. Default sort is Status descending. Pager.
- Tag edit dialog: see Tags, EditTagDialog.

## Service page (/services/:id/:tab?)
- Errors: 404 gives "No access / You need View on this service's tag to open this service."; otherwise "The service could not be loaded."
- Sticky header:
  - Row 1: h1 name, tag chip (dashed if inherited), status dot, Pin.
  - Row 2: cluster link, account, region, "family:revision", launch type (Fargate).
  - Right (HeaderActions):
    - Desired stepper: label "DESIRED", "−", number input, "+", "Apply" (shown only when dirty). Disabled with the tooltip "You need Operate on <Tag>". Inline note when the value falls outside the auto-scaling range: "Auto scaling keeps this service between a and b. It will move N back into that range." + link "Auto scaling settings →".
    - "Restart". Disabled tooltips: "You need Operate on <Tag>" / "A rollout is already running" / the controller banner text / "Loading the service".
    - "AWS console ↗".
- Dialogs:
  - Desired count: title "Set <svc> to N tasks?" (or "a valid number of tasks"). Sub "Now X." plus "Every task stops. The service and its definition stay." at 0. Confirm "Update desired count". Toasts: "Desired count set to N."
  - Restart: title "Restart <svc>?". Sub "Starts a new deployment of :rev. Each task is replaced, new ones first." Confirm "Restart". Toast "Restart started."
- Tabs, in order: **Overview, Metrics, Task definition, Deploy** (bold), **Tasks** (badge = running count), **Scaling, Logs, Terminal, Operations log**.
  - Task definition badge ":N ready" when an undeployed revision exists.
  - Locks: Deploy, Logs, Terminal. A locked tab body is an amber banner "You need **X** on <TagChip> to use this. Ask a global admin to change your permissions."
- ControllerBanner (amber, shown when the service is not an ECS-controller service): free text from the server.
- PendingRevisionBanner (blue): "Revision **svc:N** is registered but not deployed. The service still runs :M." + button "Deploy :N" (opens the Deploy tab with ?revision=N). Disabled tooltip "You need Deploy on <Tag>" or the controller text.

### Tab: Overview
- KPI strip "Service summary":
  - Tasks (running/desired, "N pending" | "all running")
  - Revision (":N", image tag)
  - CPU (% "of N units")
  - Memory (% "of N MiB")
  - If the service has a load balancer: Req / min, p95 (ms), HTTP 5xx (today).
  - Insights off shows "–  Container Insights off".
- Panels:
  1. "CPU and memory, 24 h" with a deployment/restart marker legend (CPU, Memory, "Deployment or restart"). Banner "Container Insights is off for <cluster>, so there are no CPU and memory graphs."
  2. Behind a load balancer: "Target health" (donut Healthy targets / Unhealthy targets / Missing tasks, centre "n/desired") + the Rollout card. Otherwise: "Task health" (donut Running / Missing tasks, centre "n/m running") + the Rollout card.
  3. ALB panels: "Requests per minute, 24 h", "Response time p95, 24 h", "HTTP 5xx per hour, 24 h" ("N today").
  4. "Running tasks" (count; columns Task, Zone, CPU, Memory, action link) + "Running task count, 72 h" (running vs desired lines).
  5. "Deployments, 14 days" timeline; legend Deploy / Restart / "Failed, rolled back"; labels ":rev", "restart", ":rev failed".
  6. Two columns:
     - "Networking" (link "Network placement →"). Rows: VPC, Subnets (id (N tasks)), Security groups, Public IP (Assigned/Not assigned), Load balancer ("tg-name → port N" or None), Service Connect (Off).
     - "Task definition". Rows: Family, Size ("x.xx vCPU · N MiB"), Containers (name · image), Task role, ECS Exec (On/Off), EFS ("fs-… → path" or None).
  7. "Recent events" (6 rows, link "Operations log →"). Each row has a source, actor, text and time.
- **Rollout card** (also used on the Deploy tab):
  - Pill "Rollout in progress" (info): ":old → :new", "started <ago>", two-segment progress bar, text "N new tasks on :new · N old on :old · N failed to start · circuit breaker is off | with rollback is on | on, without rollback".
  - Pill "Failed" (bad) with the reason text in red.
  - Pill "Completed" (ok) with the strategy text.

### Tab: Metrics
- Seg "Range: 1h | 6h | 24h | 72h". Caption "Container Insights via CloudWatch · 5-minute resolution, averaged across N tasks".
- Panels, each with a "now …" figure at the right and a stale note:
  - "CPU utilization" (now x% of N units)
  - "Memory utilization" (of N MiB)
  - "Requests per minute" (ALB only)
  - "Response time p95" (ALB only)
  - "Network" (Received / Sent, KB/s)
  - "Running task count" (desired N)
- Insights disabled: amber banner "Container Insights is off for X, so per-service metrics are not available. Turn it on in the cluster's Terraform (setting { name = "containerInsights" value = "enhanced" })." Error: red banner. Per-panel empty "No data yet".

### Tab: Task definition
- Banner (Edit permission): blue "Saving registers a new revision of <svc>. Deploy it afterwards from the Deploy tab. Terraform will show the new revision as drift on its next plan. To keep Terraform from reverting a deploy, add lifecycle { ignore_changes = [task_definition, desired_count] } to the service."
- Banner (no Edit): amber "Read only. You need **Edit task definition** on <Tag> to register a new revision."
- Panel "Task definition JSON" (right: "based on :N"). Line-number gutter with changed-line marks; textarea; red invalid state with a parse error. Footer buttons: "Discard", "Review and register :N" (disabled when not dirty, invalid, or read-only).
- ReviewDialog (wide):
  - Title "Register svc:N?"; sub "Changes against the deployed revision :M."
  - Amber banner if the service moved: "The service moved from :a to :b while you were editing. Check the diff before saving."
  - Line diff with +/-/gap rows; red banner on error.
  - Buttons "Keep editing" / "Register revision :N".
  - Success toast: "svc:N is registered. Deploy it from the Deploy tab when you are ready."
- Panel "Revisions" ("N shown · newest first"). Columns: Revision, Status (Deployed / Rolling out / Not deployed / Previous), Image tag, CPU / memory, Registered, By, action. The action is "Deploy" on newer revisions or "Roll back to this" on older ones. It is disabled with a reason tooltip (no permission / controller / rollout running) and opens the Deploy tab.

### Tab: Deploy (the main editing surface)
Top to bottom:
1. RolloutProgress (when a rollout is running, or completed within 60 s): pill (Rollout in progress / Failed / Completed), ":old → :new" or "svc:N", age, bar, "N new tasks on :N · N old on :M", "N failed to start", failure reason, and a "Roll back to :M" button (needs Deploy).
   - Confirm dialog: "Roll back svc to :M?" Sub "Stops the rollout of :N and returns the service to :M." Danger confirm "Roll back to :M".
   - Toasts: "You need Deploy on <Tag> to roll back." / "The service changed meanwhile; check the rollout and try again."
2. Panel **Containers**: sub " · image repository"; a Seg of container names when there are 2 or more (e.g. payments-api / otel-collector). The whole panel is disabled with the tooltip "You need Edit on <Tag>". Sections (h3):
   - **Image tag**: ImageTagPicker.
     - Combobox "Image tag" with a dropdown. Option rows show the tag, a "newest" badge on the first, a "deployed" badge, and a push age. Footer "N of M tags" or "N of M tags match · newest N shown". Empty "No tags match."
     - Non-ECR images get free text with the hint "These images are not in ECR, so their tags cannot be listed. Type the tag to deploy." Invalid tag: "Tag may contain letters, digits, . _ - and must start with a letter, digit or _ (128 characters at most)."
   - **Environment**: rows of Name / Value inputs, remove "×" per row, "Add variable", empty "No environment variables."
   - **Secrets**: rows of "Secret name" / "Value from" (ARN) with remove, "Add secret", empty "No secrets." Notes: ECS reads these when a task starts using the task execution role (needs secretsmanager:GetSecretValue, ssm:GetParameters, kms:Decrypt). Non-admins may only add secrets from the same account under an existing prefix.
   - **Command** and **Entry point**: argument rows, "Add argument", hint "Uses the image default.", error "Arguments cannot be empty."
   - **Health check**: empty "No container health check." + "Add health check". Otherwise Seg "CMD-SHELL | CMD", "Command", number fields "Interval (s)", "Timeout (s)", "Retries", "Start period (s)" (each with a min–max hint), and a red "Remove health check".
3. Panel **Task size** (sub " · CPU units, memory MiB"): selects "Task CPU (units)" and "Task memory (MiB)" (Fargate valid combos). Error "N MiB is not available with N CPU units. Pick one of the listed sizes." Amber OOM hint "Tasks of this service stop with OutOfMemoryError (exit 137) at N MiB. Try N MiB."
4. Panel **Service** (sub " · N running"):
   - Desired stepper (Operate).
   - "ECS Exec" toggle (Deploy). Note "Applies to new tasks only: turning it on or off restarts the service's tasks". Warning if there is no task role: needs ssmmessages:CreateControlChannel, CreateDataChannel, OpenControlChannel and OpenDataChannel.
   - "Rollout safety": toggles "Circuit breaker" and "Roll back on failure" (disabled until the breaker is on; tooltip "Turn on the circuit breaker to roll back automatically"). Number inputs "Minimum healthy percent" (0–100), "Maximum percent" (100–200), "Health check grace period (seconds)" (disabled without a load balancer: "Only services behind a load balancer have a health check grace period").
   - Toggle "Force new deployment", with "Replaces every running task with a fresh one from the same task definition."
   - Validation text: "Desired count must be a whole number between 0 and N." etc. Locks: "You need Operate|Deploy on <Tag>".
5. Panel **Auto scaling** (ScalingPanel):
   - Min / Max inputs. Error "Minimum and maximum must be whole numbers with minimum at most maximum." Status "Auto scaling would move N back into min–max." Buttons "Remove target" (danger) / "Keep target". If there is no target: "No auto scaling" + "Add auto scaling target".
   - **Policies** table: Name, Metric (CPU % / Memory % / ALB requests per target / Step scaling / Other(...)), Target, Cooldowns ("in N s · out N s"), Scale-in (On/Disabled), row buttons "Edit" and "Remove". Button "Add policy".
   - Policy editor: "Policy name", "Metric" (CPU / Memory / ALB), "Target value", "Target group" ("Choose a target group"), "Scale-in cooldown (seconds)" (default 300), "Scale-out cooldown (seconds)" (default 60), checkbox "Disable scale-in". Buttons "Save policy" / "Cancel". Errors: "A policy named X already exists.", "CPU and memory targets are percentages above 0 up to 100.", "Choose the target group that receives the requests."
   - **Scheduled actions** table: Name, Schedule, Timezone, Min, Max, "Edit", "Remove". Button "Add scheduled action".
   - Action editor: "Name", "Timezone" (select + "Region/City" input), Seg "Cron | At | Rate", "Cron fields" (hint "Minutes, hours, day of month, month, day of week, year: for example 0 7 * * ? *"), "Date and time", "Every" + "Unit" (minutes/hours/days), "Minimum capacity", "Maximum capacity". Hint "Set at least one. The other stays as it is when the action runs." Buttons "Save action" / "Cancel". Error "A scheduled action named X already exists."
   - All of it is locked for non-Operate users.
6. Sticky "Pending changes" bar: "N changes", an optional chip ":N is loaded from the revisions list" + "Dismiss :N", and a Problems list. Buttons "Discard" and "Review…" (disabled with no changes or with problems).
7. **ReviewChangesDialog** (wide):
   - Title "Review changes to <svc>". Sub is a summary sentence, e.g. "Registers revision :N and deploys it to svc, changes the service settings and changes auto scaling."
   - Banners: amber "This is a Prod service." (only when the tag is named Prod); "The service moved from :a to :b while you were editing. Check the diff before applying."; "**Settings changed while you were editing** The left-hand values are the current ones; check them before applying."
   - Sections: "What happened" (steps Register revision / Update service / Update auto scaling, each Done|Failed|Skipped + message; shown after a partial failure); "Task definition: new revision :N" or ":a → :b" (line diff); "Service" (rows Label before → after: Desired count, ECS Exec, Circuit breaker, Roll back on failure, Minimum healthy percent, Maximum percent, Health check grace period, Restart tasks); "Auto scaling" (lines).
   - Field "Reason" (textarea, placeholder "Optional. Shown in the operations log.", counter "n / max").
   - Buttons: "Keep editing" (or "Close" after a partial failure), then primary "Deploy :N" / "Register and deploy :N" / "Register :N" (no Deploy permission) / "Apply changes", or "Retry remaining steps".
   - Red error banner. Toasts: "Registered :N. Someone with Deploy on <Tag> can roll it out." / "Deploying svc to :N." / "Applied the changes to svc."
- Loading: panels "Containers" / "Task size" show "Loading…" or "The task definition could not be loaded." Error: "The deploy settings could not be loaded."

### Tab: Tasks
- Note "Stopped tasks older than N min are hidden." (admins get a "Change" link to /settings/general.)
- Panel "Tasks": Seg "Running | Stopped | All" (n). Task table (see Tasks list; no Service column), "Load more" for cursor paging. Error: "The tasks could not be loaded."

### Tab: Scaling
- Left panel "Desired count":
  - Range bar (min / max / desired marker) when auto scaling exists.
  - "−" "Scale to" "+" and the button "Update desired count" (needs Operate; tooltip "You need Operate on <Tag>").
  - Texts: "Outside the auto scaling range a–b.", "The most this service can run is N tasks.", "Running X of Y. Auto scaling keeps this service between a and b tasks and may change a value outside that range back. | No auto scaling: the count stays where you set it. Setting 0 stops every task without deleting the service."
  - Same confirm dialog as the header. Toasts "Desired count set to N." / "Desired count is already N."
- Right panel "Auto scaling" (action text "Application Auto Scaling · set in Terraform" + link "Edit in Deploy →" for Operate users). Rows: Minimum tasks, Maximum tasks, Policy ("Target tracking · <metric> at N%" or "Step scaling · name"), Scale-out cooldown, Scale-in cooldown, Scheduled actions ("name: schedule → min N" or "None"). Empty: "This service has no scalable target registered."
- Panel "Scaling activity": feed of ECS activities, newest first. Empty: "No scaling activity in the last 7 days."

### Tab: Logs (needs Logs)
- Toolbar:
  - Select "Container" ("All containers"; unsupported containers are disabled).
  - Seg "Level: All | Warn+ | Errors".
  - Input "Search logs".
  - Seg "Tail: Live | Paused".
  - Status text "CloudWatch Logs · <log group> · live tail|paused|tail stopped|connecting".
- Banners:
  - Amber, per unsupported container: "<name> logs go to a different log group. Harbormaster cannot show those logs." / "… go to the <driver> driver. Harbormaster cannot show those logs."
  - Blue "Live tail stopped after 30 minutes idle." + "Resume".
  - Amber "Your session changed. Reload the page to follow logs again."
  - Amber "Live tail disconnected." + "Resume".
- Log box: rows of time (HH:MM:SS), stream "container/taskid8", message. Rows are coloured by level (warn/error). "Load older" button at the top. Empty "No log lines." Error banner "The logs could not be loaded."

### Tab: Terminal (needs Terminal)
- Lock panel as above.
- Amber banner "**ECS Exec is off for <svc>.** Turn on enable_execute_command = true on the service in Terraform and give the task role the ssmmessages:* permissions. New tasks then accept terminal sessions."
- Empty "No running tasks to connect to."
- Otherwise a dark xterm host. Greeting "Pick a task and a container, then Connect." Selects: "Task" (shortid · ip · az), "Container" (a container with a non-running agent shows "name · agent stopped"), "Command" (/bin/sh, /bin/bash). Button "Connect" (disabled if no agent or no CSRF) turns into red "Disconnect".
- System lines: "Starting session…", "Starting session with SessionId: …", "Exiting session with sessionId: …", "Disconnected after 20 minutes without input.", "Session reached the 4 hour limit.", "Close another terminal to open a new one."
- Side panel "Session". Rows: Via ECS Exec; Idle timeout 20 minutes; Max duration 4 hours. Text: "Keystrokes and output are not recorded. The operations log records who opened the session, the task, container and command."

### Tab: Operations log
- Panel "Operations log". Toolbar toggle "Show ECS service events" (default on). Right text "Actions taken in Harbormaster are kept for 90 days. Changes made outside Harbormaster come from CloudTrail."
- Feed rows: time, source, actor, text. "Load more". Empty "No entries."

## Tasks list (/tasks)
- Header: h1 "Tasks"; sub "Every task in every cluster you can see. ECS keeps stopped tasks for about an hour; Harbormaster keeps them for 7 days."
- KPI strip: Running; Unhealthy (amber); "Stopped, error" (red, "last N min"); "Stopped, normal"; "Stopped, expected" (teal); "On spot" ("N% of running").
- Controls:
  - Seg "Problems | Running | Stopped | All" (n). Default is Problems.
  - Search "Filter by task ID, service, cluster, IP".
  - Toggle "Group repeats". Repeated stops collapse to a row "×N tasks" with an expander.
  - Note "Stopped tasks older than N min are hidden."
- Table columns: Task (12 chars, tooltip full id), Service (link + cluster; standalone shows "Standalone · started by X"), Status, Health (Healthy / Unhealthy / No check / –), Rev, IP · Subnet, Capacity (FARGATE / FARGATE_SPOT), CPU, Memory, Started, Stopped reason, and an action column (Logs link on group rows).
  - Row action "Terminal" link (needs Terminal permission and ECS Exec enabled on a running task).
  - Row action "Stop" (red; running tasks, needs Operate). It opens StopTaskDialog.
  - The shared TaskTable (service Tasks tab) uses the same columns with "Revision" and "Subnet / IP" headers.
- Empty "No tasks match." Loading "Loading tasks…". Error "The tasks could not be loaded."
- Truncation note: "Showing the first N tasks. Filter by service, cluster or account to see the rest." Pager.
- **Status colours** (stopStatus.tsx): Running = ok green; Provisioning / Pending / Activating = info blue; Stopped with error stop_kind = red "Stopped (exit N)"; Stopped with expected stop_kind (Spot, deployment replace, scale-in, user stop, non-zero exit that is no fault) = teal "Stopped (exit N)"; any other Stopped = grey "Stopped". Row stripe follows the same colours (amber for unhealthy running).
- A neutral chip beside a stopped status shows the stop cause (spot, deploy, …). Tooltip "Stop cause: X".
- Reason chart colours:
  - Out of memory (exit 137) = red
  - Failed load balancer health checks = amber
  - Stopped by a user = series-3
  - Scaled in = series-4
  - Replaced by a deployment = grey
  - Spot interruption = teal
  - Failed to start = red
  - Container exited with an error = red
- **StopTaskDialog**: title "Stop task <12-char id>?". Sub "<svc> will start a replacement to keep N running." or "This task belongs to no service; nothing will replace it." Buttons "Cancel" / danger "Stop task".
- **TaskTerminal** (/tasks/:shortId/terminal, standalone tasks). Breadcrumb Tasks / id / Terminal. States:
  - Empty "This task is not running."
  - Amber "You need **Terminal** on this cluster's tag to use this. Ask a global admin to change your permissions."
  - Amber "**This task was started without ECS Exec.** Run it with --enable-execute-command and give the task role the ssmmessages:* permissions." + "Back to tasks".
  - Otherwise the same terminal panel as the Terminal tab.

## Networks (/networks)
- Header h1 "Networks"; summary "N VPCs · N accounts · N subnets · N running tasks".
- Empty "No active VPCs. A VPC is active when ECS tasks run in it." Error "The networks could not be loaded."
- Left list: input "Filter VPCs, accounts, regions". Grouped by account (header = name + count). Each VPC button has a dot, name, "region · cidr" and "N cl · N t". Empty "No VPCs match this filter." Footer "N VPCs without tasks hidden". The selection is kept in ?vpc=acct/region/vpc-id. Old routes /networks/:acct/:region[/vpc] redirect here.
- Right: VpcDetail (the first VPC is selected by default). Unknown selection: "No running tasks you can see in this VPC."
  - Header: h2 name; "vpc-id · cidr · account · region".
  - Optional note "N tasks without a subnet not shown".
  - KPI strip "VPC summary": Subnets (N private · N public); Clusters; Running tasks (N zones); IPs used ("of N"); NAT gateways ("one per zone" | "single zone" | "none").
  - Panel "Subnets", columns: Subnet (name + id), Type (Public/Private), Zone, CIDR, IPs used (bar, amber over 80%, "N of M"), Clusters, Tasks.
  - Panel "Placement" (toolbar "zone ▸ subnet ▸ cluster ▸ service"; button "Expand all" / "Collapse to clusters"). A tree of rows ZONE (name, "N subnets · N tasks"), SUBNET (cidr · "N of M IPs"), CLUSTER (status dot, "N services · N tasks here", expander, ↗ link), SERVICE (status dot, "N tasks here · :rev · N unhealthy"; link). It can also show "No subnet" and "Standalone tasks".
  - Panel "Gateways and endpoints": Internet gateway, NAT gateways, VPC endpoints (each the ids or "none").

## Storage (/storage)
- Header h1 "Storage"; sub "EFS file systems and the services that mount them. File systems are created by Terraform."
- KPI strip: File systems; Total stored; Mounted; "Not mounted, still billed" (size + "N file systems", amber).
- Panel "File systems": Seg "All | Mounted | Not mounted" (n); input "Search name, id, region or service".
- Columns: File system (name + "id · region · vpc"), Size, Growth ("+N% in 30 days" or "—"), Throughput (MiB/s + mode), Access points ("None" / "N access points" with a tooltip of id · path · uid), "Mounted by" (amber "Not mounted — No service mounts this file system." or green "N clients" + service links "(cluster) /path"). Default sort is Size descending. Unmounted rows get an amber stripe.
- Empty "No file systems you can see." / "No file systems match." Error "The storage could not be loaded."

## Analytics (/analytics/:tab; /analytics redirects to delivery)
- Header h1 "Analytics"; sub "Delivery, reliability, capacity and operations, across your clusters and services."
  - Seg Tag "All | <tag names>".
  - Seg Range "24h | 7d | 30d | 90d" (default 30d). On Capacity it is disabled, and clicking it toasts "This page does not use the range filter".
- Tabs with a window badge: Delivery (90d), Reliability (7d), Capacity (live + 7d), Operations (90d).
- A "Display" disclosure with the toggle "Show data sources".
- Banners (amber):
  - Capacity: "Breakdowns use the latest poll. The 7-day trend is estimated from task-count history and each service's current task definition, so the range filter does not apply."
  - Clamped window: "Showing the last N. Stopped tasks and task-count history are kept …".
- Error: "The analytics could not be loaded."
- Each panel has a footer "source <table> · kept <retention>" when sources are shown. A daily panel shows "Daily data started on <date>". The last panel on every tab is "Data used on this page" (columns Table, What this page reads, Kept, Setting).
- Layout: panels are "half" or "full" width.
- **Delivery**
  - KPI: Rollouts (N per day); Change failure rate ("failed ÷ (completed + failed)", amber above 5%); Median rollout (p90 …); Time to recover ("median, failed → next completed"); Rollbacks ("deploys to a lower revision"); Superseded ("replaced before finishing").
  - "Rollouts" (full, stacked: completed / failed / superseded).
  - "Change failure rate" (half, area, 5% reference line).
  - "Rollout duration" (half, median / p90).
  - "Why rollouts failed" (full, stacked: Circuit breaker, Health check, Failed to start, Out of memory, Stopped by user, Other).
  - Table "Recent failed rollouts, with reason": When, Service, Revision (":a → :b"), Reason, Detail, Ran for. Empty "No failed rollouts with a recorded reason in this range".
  - Table "Services by change failure rate" ("most failures first"): Service, Cluster, Tag, Rollouts, Failed, Failure rate (bar; amber above 10%, red above 20%), Median, Recover, Last failed. Shows the top 8 with "Showing N of M services with rollouts in this range" + "Show all". Empty "No rollouts in this range."
- **Reliability**
  - KPI: Availability ("5-min samples with running ≥ desired"); Time below desired; Task stops (per day); Unexpected stops (% of stops); Out of memory ("exit 137 / OutOfMemoryError"); Placement failures ("from ECS service events").
  - "Availability" (area, 99.5% reference); "Services below desired" (area).
  - "Task stops by reason" (stacked, "grey = expected").
  - "Unexpected stops by zone" (lines); "Container exit codes" (stacked: 1, 137, 143, 255, other).
  - "Availability, last 90 days" ("daily"); "Time below desired, last 90 days" ("summed across services"). Empty "Collecting daily data since <date>" / "Daily data appears after the first rollup".
  - Table "Services getting less available" ("last 30 days vs the 30 before"): Service, Cluster, Tag, Previous 30d, Last 30d, Change.
  - Table "Least available services": Service, Cluster, Tag, Availability (bar), Below desired, Unexp. stops, Top reason. Empty "No samples in this range."
- **Capacity**
  - KPI: Reserved vCPU; Reserved memory; Running tasks ("in N services"); Spot share ("tasks on FARGATE_SPOT"); At scaling max (amber); "Subnets low on IPs" ("under 10% addresses free").
  - "Reserved vCPU, last 7 days" (full, stacked, "estimated: task counts × current task definition").
  - Donuts: "Reserved vCPU by account" and "Tasks by capacity provider".
  - "Reserved vs used vCPU, last 90 days" (full; lines reserved / used p95 / used avg).
  - "Spot share, last 90 days"; "Memory reserved vs used p95" (GiB).
  - Table "Most over-reserved services" ("90-day p95 use vs what the task definition reserves"): Service, Cluster, Tag, Reserved vCPU, CPU p95 / reserved, Reserved memory, Memory p95 / reserved, Unused vCPU.
  - Table "Clusters by reserved vCPU": Cluster, Account, Tag, Tasks, vCPU, Memory, Share of vCPU, Spot. Empty "No running tasks."
  - "Services at auto-scaling max": Service, Desired / max, At max for. Empty "No service is at its scaling max."
  - "Subnets running low on IPs": Subnet, Zone, Free, Used (bar). Empty "No subnet is running low."
- **Operations**
  - KPI: Operations ("by N people"); Revisions registered; Scale changes; Restarts ("force new deployment"); Tasks stopped by hand; Terminal sessions.
  - "Operations through Harbormaster" (full, stacked by type: deploy, rollback, task_definition, scale, restart, stop_task, terminal).
  - "Hands-on operations" ("restarts, stops and terminal sessions"); "Restarts per 100 deploys".
  - Table "Who operates": User, Total, Deploy, Restart, Scale, Terminal, Last. Empty "No operations in this range."
  - Table "Most restarted services": Service, Restarts, People, Last. Empty "No restarts in this range."

## Settings (/settings/:tab; admin only)
- Non-admin sees a panel "No access" with per-area text:
  - users: "Only global admins manage users and permissions."
  - tags: "Only global admins create tags and assign them."
  - accounts: "Only global admins manage AWS accounts."
  - sso: "Only global admins change single sign-on."
  - audit: "Only global admins can read the audit log."
  - general: "Only global admins change settings."
- h1 "Settings" with a right-hand action slot (the active tab's primary button). Tabs: General, AWS accounts, Users & permissions, Tags, Single sign-on, Audit log. Old /admin/* routes redirect.

**General**
- Panel "Stopped tasks": "Show stopped tasks from the last [number] minutes" (1–1440). Action "Save". Error "Enter a number from 1 to 1440." Toast "Saved."

**AWS accounts**
- Action "Connect account".
- Panel "AWS accounts" ("Reached by assuming an IAM role"). Columns: Account (name + id), "Role Harbormaster assumes" (ARN), "Regions scanned", Clusters, Status (Connected green / Disabled grey / Error red; plus "Data is N min old|unavailable: err" and "Slowed by AWS throttling"), "Last check", and row buttons "Test now", "Edit", "Disconnect" (red). Rows are striped red or amber on failure/throttling. Empty "No accounts connected."
- "Test now" toast: "name: us-east-1: 3 clusters; eu-west-1: <error>".
- Side panel "Where Harbormaster runs". Rows: "Cluster / service", Updates ("Polls ECS every N s per account and region; metrics every 60 s; VPCs and EFS every 5 min"), "Own data" ("PostgreSQL on RDS: users, sessions, tags, permissions, audit and operations logs. No AWS definitions are stored.").
- Side panel "Tags": text "Tags are kept in Harbormaster only and never written to AWS. An untagged cluster is Dev; an untagged service inherits its cluster's tag." Tag chips with "N services", and a link "Manage tags".
- **Connect dialog** "Connect an AWS account".
  - Sub "Harbormaster assumes this role from its own task role."
  - Fields: "Display name" (placeholder "production"), "Role ARN" (placeholder "arn:aws:iam::123456789012:role/harbormaster-access"), "External ID" (read-only, generated; hint "Put this in the role's trust policy as sts:ExternalId."), "Regions to scan" checkboxes.
  - Regions: us-east-1, us-east-2, us-west-1, us-west-2, ca-central-1, eu-west-1, eu-west-2, eu-west-3, eu-central-1, eu-north-1, ap-south-1, ap-southeast-1, ap-southeast-2, ap-northeast-1, ap-northeast-2, ap-northeast-3, sa-east-1. Default checked: us-east-1, eu-west-1, eu-central-1.
  - Per-region results "✓ region · N clusters" / "✗ region · error". Help text lists the IAM permissions: read ECS/EC2/EFS/CloudWatch/Logs plus ecs:RegisterTaskDefinition, ecs:UpdateService, ecs:StopTask, ecs:ExecuteCommand, iam:PassRole.
  - Buttons "Cancel" / "Test and connect". Toast "Connected <name>."
- **Edit dialog** "Edit <name>": "Display name", "Regions to scan", "Cancel" / "Save". Toast "Saved <name>."
- Disconnect confirm: title "Disconnect account"; sub "Disconnect <name>? Its clusters, services and tasks disappear from Harbormaster; tags and permissions are kept for 30 days."; danger "Disconnect"; toast "Disconnected <name>."

**Users & permissions**
- Action "Add user". Intro: "Global admins can do everything. Every other user gets permissions per tag. Sign-in is single sign-on; break-glass accounts can also use a password. | Sign-in is username and password."
- **Permission matrix** (panel "Users & permissions"; legend "V View, L Logs, O Operate, D Deploy, E Edit task definition, T Terminal").
  - Rows: user (name + "Global admin" | "User"). Columns: one per tag (chip + "N services").
  - Each cell has 6 toggle buttons V L O D E T (tooltip = action name) and a preset select None / Viewer / Operator / Deployer / Full (or "Custom").
  - Admin rows show a single pill "Everything, on every tag".
  - Footnote: "Turning on any action also turns on View. Turning off View removes everything on that tag. A user sees a cluster when they can view at least one of its services."
- **Users table** (panel "Users", "N users"). Columns: Username ("(you)", email beneath), Sign-in (pills Password / SSO linked / "Link pending" / "Link expired" / Break-glass), Role (pill Global admin / User), "Can see" ("Everything", "Nothing yet", or tag chips), "Last sign-in" (or "never"), and row buttons:
  - "Reset password" / "Set password"
  - "Make break-glass" / "Remove break-glass" (disabled without a password: "Set a password for X first.")
  - "Link SSO…" / "Change pending link…" (only when SSO is configured)
  - "Clear pending link"
  - "Unlink SSO…"
  - "Make global admin" / "Remove global admin"
  - "Delete" (red; disabled for yourself: "You cannot delete yourself")
- Confirm dialogs:
  - Reset: "Reset the password of X?" / "Set a password for X?". Sub "They get a new temporary password and must choose their own at next sign-in. Their sessions end now." (or the no-password variant). Confirm "Reset password" / "Set password". A sticky info toast then shows "New temporary password for X: <code>".
  - Admin: "Make X a global admin?" / "Remove global admin from X?". Sub "Global admins can do everything on every tag, and manage users, tags and AWS accounts." / "They keep only the per-tag permissions in the matrix."
  - Break-glass: "Make X a break-glass account?" / "Remove break-glass from X?". Sub "Break-glass accounts can sign in with a password even when single sign-on is on. Keep this to a few global admins."
  - Unlink: "Unlink X from SSO?" Sub "Their SSO sessions end now. They keep their password." (or the temporary-password variant). Confirm "Unlink".
  - Delete: danger "Delete X?" Sub "Their sessions end now. Their entries in operations and audit logs stay." Confirm "Delete user".
- **AddUserDialog**: title "Add user"; sub "They sign in with this username and password. Give permissions in the matrix afterwards." Fields: "Username" (placeholder "alex"; error "Use 2–32 lowercase letters, digits, dots, hyphens or underscores."), checkbox "Global admin (can do everything)". Note "They are asked to choose a new password at first sign-in." With SSO on, an extra note: "New local users can only sign in with a password if you make them break-glass. SSO users are created automatically at their first sign-in, or set a pending link to attach one to an existing account." Buttons "Cancel" / "Add user". Second step: title "Temporary password for X", sub "Shown once. Copy it now.", the password displayed, button "Done".
- **PendingLinkDialog**: title "Link X to an SSO identity". Sub "At their next SSO sign-in with this NameID, X keeps this account and its permissions instead of getting a new one. The link expires in 14 days." Field "NameID" (max 256). Hint "Copy it exactly as the identity provider issues it. Test sign-in shows it." Buttons "Cancel" / "Set pending link".

**Tags**
- Action "Create tag".
- Panel "Tags" (right text "Kept in Harbormaster only"). Columns: Tag (chip; "default" for the built-in Dev), Clusters, "Services tagged directly", "Services in effect", "Users with access" (username + preset, or "Global admins only"), and "Rename" / "Delete". Both are disabled for Dev with the tooltip "Dev is the default tag".
- **TagDialog**:
  - Create: title "Create tag"; sub "New tags start with no users. Only global admins can see resources with it until you give permissions."
  - Rename: title "Rename tag X"; sub "Clusters, services and permissions keep pointing at it."
  - Fields: "Name" (placeholder "QA"; 1–24 chars, starts with a letter, letters/digits/space/_/-), "Color" radio (neutral, red, amber, blue, violet, green).
  - Buttons "Cancel" / "Create tag" or "Save". Toast "Created tag X. Assign it on this page." / "Tag saved."
- **DeleteTagDialog**: title "Delete tag X?"; sub "This changes who can see and do what." Impact bullets "N cluster(s) become Dev.", "N service(s) go back to inheriting their cluster's tag.", "N user(s) lose the permissions they had on X (names)." Buttons "Cancel" / danger "Delete tag".
- Panel "Assignments" (text "Dashed tags are inherited from the cluster"; "Expand all" / "Collapse all").
  - Columns: "Account / region / cluster / service", Tag, "In effect".
  - Rows: account group row (summary "N clusters · N services" + distinct tag chips), region group row, then cluster rows (disclosure, "N services", tag select, chip). Expanded service rows have a select with the first option "Inherit (<cluster tag>)".
  - Pager 15 per page. Toast "<name> is now <chip>".
- **EditTagDialog** (opened from a cluster or service tag chip, admins only): title "Tag for X"; sub "Permissions follow the tag. Stored in Harbormaster only; AWS tags are not changed." A radio list of tags (for a service, the first option is "Inherit from cluster <name>" with an inherited chip). Footer text "Global admins create tags on the Tags page." (links to /settings/general). Buttons "Cancel" / "Save tag".

**Single sign-on** (5 panels, top to bottom)
1. "Status" with a pill "SSO on" / "SSO off".
   - Action "Turn on single sign-on" (primary) / "Turn off single sign-on". It is disabled when there are no break-glass admins, with the tooltip "Flag at least one global admin as break-glass before turning on single sign-on. Create one with harbormaster create-admin, or flag an existing admin who has a password."
   - Amber warning banners from the server. Hint "Link your own account first so you can sign in with SSO: use Link SSO account in the sidebar."
   - Confirm: "Turn on single sign-on?" Sub "Only break-glass accounts can sign in with a password while single sign-on is on." or "N users have a password but are not break-glass; they can only sign in with SSO while it is on. Passwords are kept and work again if you turn it off." Confirm "Turn on". Off: "Turn off single sign-on?" Sub "Everyone signed in with SSO is signed out now. Users with a password can sign in with it again; users without one need an admin to set one." Danger "Turn off".
2. "Identity provider": Seg "Metadata URL | Metadata file | Manual".
   - URL: field "Metadata URL" (placeholder "https://idp.example.com/metadata"), button "Fetch".
   - File: file input (.xml), button "Upload".
   - Manual: "Entity ID", "SSO URL (HTTP-Redirect)", "Certificate PEM" (placeholder "-----BEGIN CERTIFICATE-----"; "Leave the certificate empty to keep the stored certificates."), button "Save identity provider".
   - Entity-ID-change warning banner: "Changing the identity provider signs out every SSO user and detaches every linked and pending SSO identity. Users keep their permissions and must be linked again, or will be created again at their next sign-in."
   - Saved summary: Entity ID, SSO URL, Certificates (subject, fingerprint, "Expires <date>"). "Last fetched <ago>" + "Refresh".
   - Replace confirm: "Replace the identity provider configuration?" Confirm "Continue".
3. "Service provider": rows "Entity ID", "ACS URL", Certificate (subject, fingerprint, "Expires <date>", or "Created when you first save the identity provider."). Link "Download SP metadata". Button "Generate new certificate" (disabled until the IdP is configured).
   - Confirm "Generate new certificate?" Sub "The current certificate keeps decrypting assertions for 7 days. Update your identity provider with the new SP metadata." Toast "New certificate generated."
   - Red alert if the key is undecryptable: "The SP key cannot be decrypted (HM_SESSION_KEY changed). Generate a new certificate and update your identity provider."
4. "Sign-in options": "Button label", select "NameID format" (Let the IdP choose / Email address / Persistent), "Email attribute" (placeholder "email"), "Display name attribute" (placeholder "displayName"), "Username attribute" (placeholder "Derived from email"), "Clock skew (seconds)" (0–300). Button "Save".
5. "Test sign-in": link "Test sign-in" (opens a new tab; a disabled button when not configured). The result card:
   - Pass: "Sign-in test passed.", an outcome line (Would create user X / Would sign in X / Would link X (pending link)), NameID (read-only, "Use this value for pending links."), "Format: …", an attributes table, "Dismiss".
   - Fail: red "Sign-in test failed: <message>" + a code reason.
   - Loading text "Loading the test result..."
- Page states: "Loading..." / red error.

**Audit log**
- Panel "Audit log" (text "Sign-ins and changes to users, permissions and tags"; tooltip "Service actions are in each service's operations log.").
- Toolbar: select "All types" (types come from the action prefix: user, permission, tag, cluster, …); search "Filter by text".
- Feed rows: time, actor, summary. Empty "No entries match." "Load more".

## My access (/me/access)
- h1 "My access". Admin: "You are a global admin: you can do everything, on every tag."
- Others: "What you can do on each tag. Ask a global admin to change it." Table columns: Tag (chip), View, Logs, Operate, Deploy, Edit task definition, Terminal (✓ or –), Services (count in effect).

## Fake-data vocabulary (internal/awsx/fake/seed.go, estate_network.go, devseed/, status/stopreason.go)
- **Accounts**: homelab-admin (482109375512; us-east-1, eu-west-1, eu-central-1); staging-example (731022849906; us-east-1). External ID "harbormaster-dev-external-id". Role ARN example: arn:aws:iam::123456789012:role/harbormaster-access.
- **Clusters (account, region, Insights)**:
  - core-services (homelab, us-east-1, "enhanced")
  - background-jobs (homelab, us-east-1)
  - ci-tools (homelab, us-east-1)
  - eu-apps (homelab, eu-west-1)
  - edge-proxy (homelab, eu-central-1, Insights OFF)
  - staging-web (staging, us-east-1)
- **Services**, as name (cluster; desired/running; :rev; image tag; notes):
  - payments-api (core-services; 3/3; :42; 1.18.2; LB; sidecar otel-collector, AWS FireLens log driver)
  - web-frontend (core-services; 2/2; :87; 3.4.0)
  - auth-service (core-services; 2/2; :15; 0.9.7)
  - notifications (core-services; 2/2; :9; 2.2.0; rolling out to 2.3.0)
  - report-worker (background-jobs; 1/1; :31; 5.0.1; EFS)
  - email-sender (background-jobs; 1/1; :12; 1.3.3)
  - image-resizer (background-jobs; desired 2 running 0; :6; 0.4.2; OOM-killing, Failing)
  - jenkins-controller (ci-tools; 1/1; :58; repo "jenkins", 2.479.1-lts; EFS)
  - artifact-proxy (ci-tools; 1/1; :7; 1.0.4)
  - catalog-api (eu-apps; 2/2; :23; 4.1.0; 1 unhealthy)
  - search-indexer (eu-apps; 1/1; :14; 2.0.3)
  - edge-nginx (edge-proxy; 2/2; :19; 1.27.2; port 443)
  - cert-renewer (edge-proxy; 1/1; :4; 0.2.1)
  - web (staging-web; 1/1; :33; 3.5.0-rc.1)
  - api (staging-web; 1/1; :21; 1.19.0-rc.2; sidecar migrator 1.4.2)
- **Standalone tasks**: release-build (ci-tools, 3.0.0), db-migrate (background-jobs, 12.0.0).
- **Images**: <acct>.dkr.ecr.<region>.amazonaws.com/<repo>:<semver> (payments-api has 24 ECR tags); public.ecr.aws/aws-observability/aws-otel-collector:v0.40.0. Target groups are named "<svc>-tg". Task roles and CPU/memory: 256/512, 512/1024, 1024/2048, 2048/4096.
- **Revisions** look like "payments-api:42"; registered_by is a username.
- **Env vars**: NODE_ENV, PAYMENTS_PROVIDER=stripe, DB_POOL_SIZE=20, API_URL, TOKEN_TTL, ISSUER, QUEUE_URL, REPORT_BUCKET=rcss-reports, SMTP_HOST, JAVA_OPTS=-Xmx3g, CACHE_TTL, BATCH_SIZE, WORKER_PROCESSES=auto, ACME_EMAIL=ops@rcss.local.
- **VPCs** (account, region, id, cidr):
  - homelab-main (homelab, us-east-1, vpc-0a41c7e29b3d, 10.20.0.0/16); subnets private-1a subnet-03f1a9c2 10.20.1.0/24, private-1b subnet-0b77e410 10.20.2.0/24, public-1a subnet-09c2d5ee 10.20.101.0/24
  - shared-services (homelab, us-east-1, vpc-07de19a4c011, 10.21.0.0/16)
  - legacy-default (homelab, us-east-1, vpc-5c1e0f2a, 172.31.0.0/16; default VPC, no tasks, so hidden)
  - eu-apps (homelab, eu-west-1, vpc-0f3392bb71e2, 10.30.0.0/16)
  - edge (homelab, eu-central-1, vpc-03b1d7f0c5a8, 10.40.0.0/16)
  - staging (staging-example, us-east-1, vpc-0b18e6cd2a91, 10.50.0.0/16)
- **AZs**: us-east-1a/b/c, eu-west-1a/b, eu-central-1a. Gateways: igw-<hex>, nat-<hex>, vpce-0<hex> (com.amazonaws.<region>.<svc>).
- **EFS**:
  - jenkins-home fs-0c91a4e7 (38.4 GB, Elastic)
  - reports-data fs-02e7d1c9 (12.1, Bursting)
  - shared-certs fs-0a6b33f0 (0.02, Bursting)
  - search-index fs-0d18c2ab (74.9, Provisioned 64)
  - uploads-archive fs-07a2b9e1 (211.6, Elastic; unmounted, so it shows "Not mounted")
  - Access points fsap-0a1b2c3d4e etc.; paths /var/jenkins_home, /data/reports, /certs, /etc/nginx/certs, /index.
- **Users** (dev seed): pavel (global admin), dana, omer, oncall.
  - dana: Dev Full, Staging Deployer, Prod V+L.
  - omer: Dev Operator, Staging Viewer.
  - oncall: Staging and Prod V+L+O+T.
- **Tags**: Dev (default, neutral), Staging (amber), Prod (red). core-services is tagged Prod, staging-web Staging, and the service artifact-proxy is tagged Prod. Audit examples: "Gave oncall Terminal on Prod.", "Tagged cluster staging-web as Staging (was Dev).", "Created user omer with Operator on Dev.", "Created tag Prod."
- **Stop-reason groups** (exact labels): "Out of memory (exit 137)", "Failed load balancer health checks", "Stopped by a user", "Scaled in", "Failed to start", "Container exited with an error", "Spot interruption", "Replaced by a deployment".
- **Raw ECS stopped reasons**: "OutOfMemoryError: Container killed due to memory usage" (exit 137), "Your Spot Task was interrupted.", "Task failed ELB health checks in (target-group <svc>-tg)" (exit 143), "Scaling activity initiated by (deployment …)", "Essential container in task exited".
- **Stop codes**: UserInitiated, TaskFailedToStart, EssentialContainerExited, SpotInterruption, TerminationNotice.
- **Capacity providers**: FARGATE, FARGATE_SPOT.
- **Service events**: "(service X) has reached a steady state.", "(service X) has started 2 tasks: (task 9f2e1a3b).", "(service X) is unable to consistently start tasks successfully.", "(service X) has stopped 2 running tasks: (task …) (task …). Reason: OutOfMemoryError.", "(service X) (port 8080) is unhealthy in (target-group X-tg) due to (reason Health checks failed with these codes: [503]).", "(service X) registered 1 targets in (target-group X-tg)."
- **Scaling**: policy "cpu-target"; scheduled actions "scheduled-1" cron(0 7 ? * MON-FRI *) min 3 and "scheduled-2" cron(0 20 ? * MON-FRI *) min 2.
- **Log lines**:
  - default: "GET /health 200 2ms", "GET /api/v1/items 200 41ms", "slow query took 812ms: SELECT * FROM items WHERE owner_id = $1", "upstream timeout after 5000ms (retrying 1/3)"
  - payments-api: "POST /v1/charges 201 143ms provider=stripe", "card_declined code=insufficient_funds charge=ch_3Q8x"
  - image-resizer: "fatal: runtime: out of memory"
  - jenkins-controller: "Started pipeline platform/main #1842"
- **Sample hostnames/ids**: SQS URL https://sqs.us-east-1.amazonaws.com/482109375512/notify; "ECS control center"; "harbormaster/harbormaster-server".

## Not determined / caveats
- Default SSO button label: not found. The field is "Button label" and the SSO link on Login shows whatever is configured.
- Exact CSS colours and spacing: not captured, only semantic kinds.
- Pager/Dialog Esc behaviour and Tasks "Group repeats" grouping rules: partly inferred.
- Cluster table "Tasks" column cell format: not seen (header only).
- Analytics series colour names (COLOR.*) were not mapped to hex.
- The 4 dev-seed users' passwords and the live Overview/Analytics numbers come from generated data, not fixed fixtures.