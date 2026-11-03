# Deploy tab — implementation plan

Planned by Fable; branch `feat/deploy-tab` (base `affeee4`). Repo rules: test-first, no full code in the plan, up-only migrations, contract golden via `HM_UPDATE_CONTRACT=1 go test ./internal/api -run TestContract`, Terraform fmt/validate only (apply in TerraDuckTel). The `typesafe-ai` skill is unavailable; decisions were made by direct analysis and are stated with their reasoning.

Owner decisions: the Deploy tab label is bold; extras included (review + diff + reason, secrets, CPU/memory + rollout safety, force redeploy + live progress); the Deploy tab owns editing; scaling edits include target tracking and scheduled actions, with the IAM additions in §T4. No "Proposed"/beta styling anywhere.

## 1. Architecture

### One server-side change-set endpoint, not client orchestration

`POST /api/services/{id}/changeset` takes everything the review screen approved and applies it in a fixed order: **register revision → UpdateService → Application Auto Scaling**.

- **Preconditions are checked once, before any AWS write**: permissions for every section, stale `base_revision` (existing `StaleBaseError`), a settings `base_version` hash (new), unsupported controller, rollout in progress.
- **One audit trail**: every operations-log line of a run shares `details.changeset_id` and `details.reason`; `deployment_history` gets `reason`.
- **Partial failure is first-class**: the response reports per-step `done | failed | skipped`. Steps after a failure are skipped. A retry **never re-registers**: it sends `task_definition: {revision: N}` (the revision the first run registered) instead of `definition` — the same rule `register.go` follows.
- **Ordering**: register first (only step creating a new AWS object). UpdateService second, as **one call** carrying `taskDefinition`, `desiredCount`, `enableExecuteCommand`, `deploymentConfiguration`, `healthCheckGracePeriodSeconds`, `forceNewDeployment`. Scaling last: RegisterScalableTarget with a raised minimum scales the service itself, so doing it after the desired-count change avoids two competing counts.
- **Idempotency**: register once; UpdateService with the same values is a no-op except `forceNewDeployment`, which the rollout gate blocks on retry; `Put*`/`Delete*` on Application Auto Scaling are upserts/deletes by name.
- **Concurrency**: task definition keeps `base_revision` (409 + `deployed_revision`). Service and scaling settings get `version`, a sha256 of the canonical live settings from `GET deploy-settings`; the POST carries `base_version`, the server recomputes from live `DescribeServices` + three autoscaling `Describe*` calls and answers 409 `conflict` with the fresh settings when they differ. The HTTP `ETag` middleware (GET cache) is not involved.
- **Recorded**: `deployment_history` row (kind `deploy` or `restart`, `reason`), one operations line per change class (deploy / desired count / exec / rollout settings / scaling) with `details.changeset_id`, `details.reason`, plus the existing register line. Outcome logger unchanged.

Status codes: 422 `invalid`, 409 `conflict` (stale base, stale version, rollout running), 403 `forbidden` (names the first missing action) before any write; 200 with `status: "applied" | "partial" | "registered_only"` once any write succeeded. Non-validation AWS errors on the first step → 502 `aws_error`.

### Permissions (authz)

| Change | Action | Why |
|---|---|---|
| Register a revision (definition changed) | Edit | existing |
| Deploy a revision / rollback | Deploy | existing |
| Desired count, Force new deployment | Operate | existing (desired count, restart) |
| ECS Exec toggle, circuit breaker, min/max %, grace period | Deploy | changes how the service rolls out / what tasks allow |
| Scalable target, policies, scheduled actions | Operate | same class as desired count |
| Deploy tab unlocked | any of Edit, Deploy, Operate | each control is disabled with its own lock reason |

The server derives the required set from the body and refuses the whole request with 403 if any is missing. A `definition` without Deploy and no other section → register only (`status: "registered_only"`).

### Tab ownership

- **Deploy tab** owns every edit: containers, task size, service settings, scaling, review, progress, rollback. QuickEdit and DeployDialog are deleted.
- **Task definition tab** keeps the JSON editor + ReviewDialog → `POST /revisions` (register only), the revisions table and rollback. Its Deploy / Roll back buttons and the PendingRevisionBanner navigate to `/services/{id}/deploy?revision=N`; the Deploy tab loads that revision and shows the deployed→N diff in the same review. The one-click rollback on the progress panel is the only deploy outside the review (emergency action, confirmed by `ConfirmDialog`).
- **Scaling tab** stays read-only and gains an "Edit in Deploy →" link.

## 2. API

### `GET /api/services/{id}/deploy-settings` (new, View)

Live (DescribeServices + DescribeScalableTargets/Policies/ScheduledActions), fetched on tab open and after a write; `staleTime` 30 s, no polling.

```
DeploySettingsResponse {
  version string
  deployed_revision int, family string, controller ControllerRefDTO
  desired_count, running_count, max_desired int
  enable_execute_command bool
  task_role_arn string                // "" → UI warns exec needs a task role
  deployment_configuration { circuit_breaker {enable, rollback bool}, minimum_healthy_percent, maximum_percent int }
  health_check_grace_period_seconds *int, has_load_balancer bool
  target_groups []{ arn, name, resource_label string }
  scaling { has_target bool, min, max int, policies []DeployPolicyDTO, scheduled_actions []DeployScheduledActionDTO }
  can ChangeCanDTO
}
DeployPolicyDTO { name, type ("TargetTrackingScaling"|"StepScaling"), metric ("cpu"|"memory"|"alb"|"other"), metric_raw string,
                  target_value float64, scale_in_cooldown, scale_out_cooldown int, disable_scale_in bool, resource_label string, editable bool }
DeployScheduledActionDTO { name, schedule, timezone string, min_capacity, max_capacity *int }
```

### `GET /api/services/{id}/image-tags?container=NAME&q=TEXT&limit=N` (extended, View)

`container` defaults to the main container. The server pages all tags (existing 10×1000 cap), sorts newest first, caches the **full list** 60 s per account/region/repo, then applies `q` (case-insensitive substring) and `limit` (default 10, max 200). Response gains `container`, `image` (repo URL without tag), `total`, `matched`, `truncated`. Cross-account and non-ECR images stay `source: "none"`.

### `GET /api/services/{id}/revisions/{revision}` (new, View)

`RevisionDefinitionResponse { revision, arn, definition json.RawMessage, registered_at, registered_by }` via `loadTaskDefinition`.

### `POST /api/services/{id}/changeset` (new)

```
ChangeSetRequest {
  base_revision int, base_version string, reason string (≤ 500)
  task_definition *{ definition map[string]any } | *{ revision int }
  service *{ desired_count *int, enable_execute_command *bool,
             deployment_configuration *{ circuit_breaker {enable, rollback}, minimum_healthy_percent, maximum_percent },
             health_check_grace_period_seconds *int, force_new_deployment bool }
  scaling *{ target *{ min, max int }, remove_target bool,
             put_policies []{ name, metric ("cpu"|"memory"|"alb"), target_value, scale_in_cooldown, scale_out_cooldown, disable_scale_in, target_group_arn },
             delete_policies []string,
             put_scheduled_actions []{ name, schedule, timezone, min_capacity *int, max_capacity *int },
             delete_scheduled_actions []string }
}
ChangeSetResponse {
  status "applied"|"partial"|"registered_only"
  steps []{ name "register"|"service"|"scaling", status "done"|"failed"|"skipped", message string }
  registered_revision *int, deployment_id string, from_revision *int, to_revision *int, kind string, warning *string
}
```

Validation (422 before any write): at least one section; `definition` → existing `Prepare` rules; `revision` must be an ACTIVE revision of the family and ≠ deployed; desired 0..`max_desired`; min healthy 0..100, max 100..200, rollback requires enable; grace ≥ 0 and only with a load balancer; scaling min ≤ max ≤ `max_desired`; policy name `^[\w./-]{1,256}$`; cpu/memory target in (0,100], alb target > 0 with a `target_group_arn` of this service; cooldowns ≥ 0; schedule matches `cron(...)` / `at(YYYY-MM-DDThh:mm:ss)` / `rate(N unit)`; `timezone` passes `time.LoadLocation` (import `time/tzdata`); at least one of min/max, within the resulting range; deletes name existing items; policies/actions need a target; `desired_count` within the resulting [min,max] ("Auto scaling would move N back into min–max"); `force_new_deployment` ignored when a new revision is deployed. Gates: unsupported controller → 422 for deploy/exec/rollout sections; rollout IN_PROGRESS → 409 unless only desired count and/or scaling. Exec changed without a new revision → server sets `ForceNewDeployment`, `kind` is `restart`.

### `POST /api/services/{id}/deploy` (extended)

Body gains `interrupt bool`: when true and `revision < current`, the rollout-running gate is skipped. Used only by the progress panel's one-click rollback.

## 3. Store / migration

`internal/store/migrations/0090_deploy_tab.up.sql` (use the next free number): `deployment_history` gains `reason text`, `changeset_id text`. `AttributedDeployment` gains `Reason, ChangeSetID`. Operations keep `details` jsonb. After a successful scaling step the server re-describes and calls `Store.UpsertScaling` (write-through); export `poller.CollectServiceScaling(ctx, api awsx.AutoScalingAPI, key) (model.Scaling, error)`.

## 4. Tasks

Groups: **A** (T1–T6, parallel worktrees) → **B** (T7, T8) → **C** (T9–T12) → **D** (T13, T14).

### T1 — Fake AWS: autoscaling writes, structured scaling seed, UpdateService settings, per-repo ECR tags — Sonnet
Files: `internal/awsx/apis.go` (AutoScalingAPI gains Register/DeregisterScalableTarget, Put/DeleteScalingPolicy, Put/DeleteScheduledAction), `internal/awsx/fake/{autoscaling,seed_observe,rollout,change,estate,seed}.go` + tests.
- Structured `ScalingSeed {Min, Max; Policies []PolicySeed; Scheduled []ScheduledSeed}`; mockup seeds still produce `cpu-target` and `scheduled-1/2`.
- Writes mutate `Estate.ScalingConfig` under `Mu`; Deregister drops policies/actions; Put policy without target / delete unknown → `ObjectNotFoundException`; ALB policy without ResourceLabel → `ValidationException`; `Advance` clamps desired into [min,max].
- `UpdateService` honours exec, deployment configuration, grace; `DescribeServices` returns them; new tasks inherit exec.
- ECR `Estate.Repositories map[string][]string`; payments-api gets 24 tags; staging `api` gets an ECR sidecar `migrator` (6 tags).
Tests first: `TestFakeAutoScaling_RegisterThenDescribe`, `_PutPolicyWithoutTargetFails`, `_DeregisterDropsPoliciesAndActions`, `_AdvanceClampsDesiredIntoRange`, `TestFakeUpdateService_AppliesExecAndDeploymentConfiguration`, `TestFakeRollout_NewTasksInheritExec`, `TestFakeECR_DescribeImagesPerRepository`.
Acceptance: `go test ./internal/awsx/... ./internal/poller/...` green; `make run` still shows payments-api scaling.

### T2 — Image tags per container with search — Sonnet
Files: `internal/deploy/images.go`, `internal/api/change_read.go`, contract golden, `web/src/api/types.ts`, fixtures.
`ImageTagQuery {Container, Search string; Limit int}`; `ImageTags(ctx, key, q)`; result gains `Container, Image, Total, Matched, Truncated`; cache full sorted slice; unknown container → 422.
Tests first: `TestImageTags_DefaultsToMainContainer`, `_SidecarFromECR`, `_PublicImageIsSourceNone`, `_SearchIsCaseInsensitiveSubstringNewestFirst`, `_LimitAndTotal`, `_SearchServedFromCacheNoSecondDescribe`, `_UnknownContainer422`; `TestGetImageTags_QueryParams`, `_BadLimit422`.

### T3 — Store: reason/changeset on deployment_history — Haiku
Files: migration, `internal/store/deployments.go` + test. Tests first: `TestAttributeDeployment_StoresReasonAndChangeSetID`, `_ConflictUpdatesReason`.

### T4 — Terraform IAM + permission docs — Haiku
`terraform/modules/harbormaster-access/main.tf`:
- `AutoScalingWrites`: the six application-autoscaling write actions on `*`, condition `application-autoscaling:service-namespace = ecs`.
- `TargetTrackingAlarms`: `cloudwatch:PutMetricAlarm, DeleteAlarms` on `arn:aws:cloudwatch:*:${account}:alarm:TargetTracking-service/*`; `cloudwatch:DescribeAlarms` on `*`.
- `AutoScalingServiceLinkedRole`: `iam:CreateServiceLinkedRole` on the `AWSServiceRoleForApplicationAutoScaling_ECSService` ARN with `iam:AWSServiceName = ecs.application-autoscaling.amazonaws.com`.
- Nothing else: `ecs:UpdateService` covers exec/deployment config/grace; PassRole is consumed at register and existing patterns cover unchanged roles (document the admin-changes-role case).
Also update module README, design doc §11.2/§6.4/§7.3, hardening plan, README Security/Terminal. Acceptance: `make tf-check` passes; `read_only = true` omits all writes.

### T5 — Frontend shell: Deploy tab, bold label, any-of lock, hooks, fixtures — Sonnet
`tabs.ts` gets `['deploy', 'Deploy']` after `task-definition`, `BOLD_TABS`; `Tabs` item `bold` renders `<strong>`; `TAB_LOCKS` supports any-of (`deploy: ['deploy','edit','operate']`). `web/src/api/deploy.ts`: `useDeploySettings`, `useRevisionDefinition`, `useImageTags(serviceId, container, q, enabled)`, `useChangeSet` (→ `refetchWhileSettling`), `useDeploy` gains `interrupt`. Fixtures + MSW handlers; Scaling tab "Edit in Deploy →".
Tests first: Tabs bold only for marked items; Deploy tab bold and unlocked for Operate-only / Edit-only; locked for Viewer; `isTabKey('deploy')`.

### T6 — Revision definition read + deploy `interrupt` — Haiku
`RevisionDefinition(ctx, key, revision)`; `Deploy(..., interrupt bool)`; route + contract. Tests first: `TestRevisionDefinition_FromStore`, `_DescribesWhenMissing`, `_OtherFamily404`, `TestDeploy_InterruptAllowsRollbackDuringRollout`, `_InterruptDoesNotAllowForwardDeployDuringRollout`, `TestGetRevisionDefinition_ViewRequired`.

### T7 — `deploy.ChangeSet` — Sonnet (after T1, T3)
Files: `internal/deploy/{changeset,changeset_validate,settings,scaling_write}.go` + tests; export `CollectServiceScaling`; share the register step (`registerPrepared`).
Shape: `ChangeSet {BaseRevision; BaseVersion, Reason; TaskDefinition; Service; Scaling}`, `ChangeSetResult {Status; Steps; RegisteredRevision; Rollout; Warning}`, `RequiredActions(cs)`, `DeploySettings(ctx,key)`, `ApplyChangeSet(ctx,subj,key,cs)`, `StaleVersionError{Current}`.
Scaling order: RegisterScalableTarget, DeleteScheduledAction, DeleteScalingPolicy, PutScalingPolicy, PutScheduledAction, Deregister; write-through. Failure before first write → error; after → `partial`, nil error. Recording failures → `Warning`. UUID changeset id.
Tests first: validation (`RejectsEmpty`, `RejectsRollbackWithoutBreaker`, `GraceNeedsLoadBalancer`, `DesiredOutsideNewScalingRange`, `ScheduledActionBadScheduleOrTimezone`, `ALBPolicyNeedsOwnTargetGroup`, `PolicyWithoutTarget`, `RevisionNotInFamily`, `UnsupportedControllerRefusesDeploySections`, `RolloutRunningAllowsScalingOnly`); preconditions (`StaleBaseRevision409`, `StaleVersionReturnsFreshSettings`, `TestRequiredActions_Matrix`); execution (`RegisterDeployAndScaleHappyPath`, `ExecToggleWithoutRevisionForcesNewDeployment`, `UpdateServiceFailureAfterRegisterIsPartial`, `RetryWithRevisionDoesNotRegisterAgain`, `ScalingFailureAfterServiceIsPartial`, `ScalingWriteThroughUpdatesStore`, `RegisterOnlyWithoutDeploy`, `RecordingFailureIsWarning`, `TestDeploySettings_VersionChangesWhenDesiredChanges`, `_TargetGroupsResourceLabel`).
Acceptance: no AWS write in any 4xx test (assert `fake.Calls()`).

### T8 — API: deploy-settings, changeset, contract — Sonnet (after T7, T6)
Tests first: `TestChangeSet_403NamesFirstMissingAction`, `_EditOnlyRegistersOnly`, `_InvalidBodyStrictJSON`, `_PartialIs200WithSteps`, `_StaleVersion409CarriesSettings`, `TestDeploySettings_ViewOnly404ForInvisible`, `_CanFlags`, contract golden.

### T9 — Containers editor and task size — Sonnet (after T5, T2)
`deploy/{ContainersPanel,ImageTagPicker,EnvEditor,SecretsEditor,CommandEditor,HealthCheckEditor,TaskSizePanel}.tsx`, `draft.ts` pure reducers; delete QuickEdit.
Image picker: combobox with last 10 tags + pushed time, newest/deployed marks, debounced `?q=` search over all tags, "N tags"; free text for `source: none`. Secrets: name + valueFrom only, values never shown; note about execution-role permissions. Command/entryPoint as argv rows. Health check ranges: interval 5–300, timeout 2–60, retries 1–10, startPeriod 0–300, removable. Task size reuses Fargate helpers.
Tests first: picker (10 newest + deployed mark; search via `?q`; free text validation), container switching, secrets never render values, command argv, health check ranges/removal, draft reducers.

### T10 — Service settings and auto scaling panels — Sonnet (after T5)
`ServicePanel` (desired count, Exec toggle with new-tasks + task-role warnings, rollout safety, force new deployment), `ScalingPanel` (target, policies, scheduled actions), `PolicyRowEditor`, `ScheduledActionEditor` (cron/at/rate + timezone), `scalingDraft.ts`. Step-scaling policies read-only + removable.
Tests first: rollback needs breaker; grace disabled without LB; exec warnings; adding a target enables policies; ALB needs target group; removing target removes policies/actions; scalingDraft table.

### T11 — Review, submit, partial failure, reason — Sonnet (after T8, T9, T10)
`DeployTab` drafts + sticky footer "N changes · Discard · Review"; `ReviewChangesDialog` (JSON diff, service before→after, scaling changes, reason ≤ 500, Prod banner, primary label by case); `changeSummary.ts`; delete DeployDialog. Partial → step results + "Retry remaining steps" with `{revision}`; 409 base / 409 version → reopen with banner; `registered_only` toast.
Tests first: no changes → Review disabled; `?revision=N` preload; review lists all sections; partial retry; both 409s; Edit-only registers only; changeSummary omits unchanged fields.

### T12 — Live progress, one-click rollback, Task definition tab trimming — Sonnet (after T11, T6)
`RolloutProgress` (from→to, counts, failures, "Roll back to :N" → ConfirmDialog → deploy with `interrupt`); Task definition tab loses QuickEdit/DeployDialog/`?deploy=`; revisions buttons and PendingRevisionBanner navigate to `/deploy?revision=N`.
Tests first: progress counts + rollback; rollback uses interrupt; hidden without Deploy; register shows toast only; table Deploy navigates; pending banner navigates.

### T13 — e2e — Sonnet (after all)
`web/e2e/deploy.spec.ts` + updates to `change.spec.ts`, `service.spec.ts`. Specs: searched tag + env var + reason deploy with progress and operations log; sidecar image + task size; Operate user enables scaling with CPU policy + scheduled action; exec off restarts; rollout settings only; partial failure + retry; one-click rollback during failing OOM rollout; viewer sees locked bold Deploy tab; only Deploy label is bold. Acceptance: `make e2e` green.

### T14 — Docs and README — Haiku (after T4, T12)
README screens/security/Terraform interplay (`aws_appautoscaling_*` → `ignore_changes`), design doc §6.4/§7.3/§13, `docs/ops/ecs-api-budget.md`, access module README.

## 5. Risks and defaults

- Exec changes always add `ForceNewDeployment` (harmless if ECS starts one anyway).
- ALB `resourceLabel` may need a live `DescribeTargetGroups` if the model lacks the LB ARN.
- Timezones need `time/tzdata` in the distroless image.
- deploy-settings is on demand only; scaling write-through avoids waiting on the 60 s poll.
- Admins changing roles outside `pass_role_arns` fail at register with 502 (documented).
- Defaults: scaling edits gated by Operate; step-scaling policies read-only + removable; cross-account/public images use free-text tags.
