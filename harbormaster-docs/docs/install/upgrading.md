# Upgrading

This page explains how to move a running Harbormaster to a newer release. It is for the engineer who operates the deployment.

## What happens at startup

- **Migrations run at startup.** Every role (`all`, `web`, `poller`, `coordinator`) applies pending database migrations when `serve` starts, before it serves traffic. Each migration runs in its own transaction, and a database-level lock serializes tasks that start together. You do not run migrations by hand during a normal upgrade.
- **Manual migration.** `harbormaster migrate` applies pending migrations and prints the versions applied, or `no migrations to apply`. Run it with the same database settings as the server if you want to migrate before rolling tasks.
- **Migrations only go forward.** The release ships only forward migrations. There is no down migration. Migrations are additive, so old and new tasks can overlap during a rollout.
- **Rolling deploys are safe.** A task that polls or coordinates takes a shared database lock, and claims leases only while it holds it. While an old-version leader is alive no new task claims anything, and while a new task is alive no old task can become leader. A rolling deploy or a rollback never has two versions acting at once. `HM_LEGACY_LEADER_GATE=false` turns this off. Leave it on until every task runs the newer version.

## Before you begin

- Read the release notes for the version you are moving to: [Changelog](../community/changelog.md). <!-- TODO(docs): confirm the changelog or GitHub release notes are the canonical place for upgrade notes -->
- Take a database backup or snapshot. See [Backup and restore](../operations/backup-and-restore.md).
- Check whether the release changes the `harbormaster-access` role (see "Access role changes" below).

## Upgrade steps

1. Check out the release tag and build the new image. Use a new tag, not the one you run now.

    ```sh
    make image VERSION=v1.1.0
    make check-image VERSION=v1.1.0
    ```

2. Push it to ECR (see [Build the image](build-image.md)). If you use image scanning or the MCP gateway, build and push those images for the same version.

3. In Terraform, bump the pinned release.

    - Change `image` to the new tag.
    - Change the module `source` `?ref=` to the new release tag, so the module and the image come from the same release.
    - Change `scanner_image` (a digest) and the gateway module's `image` if you use them.

4. Run plan, read it, then apply, using your usual Terraform workflow. ECS replaces the tasks with the new image. The new tasks migrate the database as they start.

5. If the release notes say the access role changed, re-apply the `harbormaster-access` module in each managed account.

## Split services

With split services, roll the coordinator and poller services first and the web service last. Migrations are additive, so old and new tasks can overlap, and cache keys carry a schema version, so two versions never read each other's entries.

<!-- TODO(docs): verify how to sequence these services in a single Terraform apply, since the module ties all three to the same image variable -->

## The release that adds the AI agent

This release adds migrations `0216_agent_run_tokens` (run tokens: a new token kind and two columns on `api_tokens`) and `0217_agent_chat` (a column on `users` and the agent's settings, conversation, message and run tables). Both only add. Nothing changes for users until you set `HM_AGENT_URL`.

1. Deploy the new release first, with `HM_AGENT_URL` unset, and let the rollout finish. The new tasks apply both migrations as they start.
2. Only then set up the agent and set `HM_AGENT_URL` (module `extra_env`). See [Install the AI agent](ai-agent.md). Setting it before the rollout ends would give old tasks a variable they ignore, and the web tasks that serve chat would not all have the feature yet.

To roll back, unset `HM_AGENT_URL` first, then roll the image back. Old tasks ignore run tokens and the new tables, so they run unchanged. Leave the schema as it is: the agent tables and columns stay, empty or unused, and a run token that was minted just before the rollback stops working after `HM_AGENT_RUN_TOKEN_TTL` (15 minutes by default). A later upgrade finds the migrations already applied. Conversations and the agent's run records stay in the database and are removed by the usual retention (`HM_RETENTION_AGENT_CONVERSATIONS`, 30 days by default) once the new release runs again. Restore from your backup only if you need the schema itself undone.

## The release that adds automatic investigations

This release adds migrations `0218_agent_identity` (a `kind` column on `users` and its checks), `0219_agent_investigations` (investigations, their evidence and their links to alert incidents) and `0220_notification_diagnosis` (the Diagnosis event and a due-time column on the notification delivery log). All only add. No agent identity row is made by a migration: it is created when the agent is on. Nothing changes for users until a route has **Investigate with AI** on.

**Check `HM_RETENTION_AGENT_RUNS` first.** This release checks it at start. A negative value (older releases read it as "keep everything") and any non-zero value below `48h` are refused: the task logs the setting's name and exits, so a rollout that still carries such a value fails at start, where you see it, instead of deleting history later. Use `0` to keep everything, or `48h` or more. The same setting covers finished chat runs as well as investigations, so it also decides how long the chat's run records (who, model, tokens, cost) are kept. The current UTC month's runs are never deleted.

**Upgrade order.** Harbormaster and the agent service can be upgraded in either order. Until both are new, investigations are skipped as `not_configured` (an older agent does not offer the investigator role). Chat keeps working throughout. In split mode, the agent module must also admit the coordinator's security group: see [Install the AI agent](ai-agent.md).

**During the rollout.** While an older release still runs against the new schema:

- Its delivery summary and its "route failing" warning on **Settings > Notifications** count diagnosis messages, so a failed diagnosis can show up there as a failed delivery. Alerts themselves are not affected: an older task never claims, renders or sends a diagnosis row, and it still sends every Opened, Update, Repeat and Resolved message as before. The count is right again when the last older task is gone.
- It may list the agent identity on its Users page. It cannot give it a privilege (the schema refuses an admin switch, a password, a break-glass flag or an SSO link for it, and the older task answers such an attempt with an error).

**Migration 0220 takes a short exclusive lock** on the notification delivery log, with a lock timeout of 2 seconds, so that a waiting migration cannot make a sender's write time out and send an alert twice. If a long transaction holds the table at that moment, the migration times out and rolls back whole. The task then exits at start, ECS starts another and the migration is tried again from scratch. If tasks keep exiting at start after a rollout with a lock timeout in their log, find the long transaction on the delivery log and end it, then let the next task start:

```sh
psql "$DATABASE_URL" -c "select pid, state, now() - xact_start as open_for, left(query, 80) as query
  from pg_stat_activity where xact_start is not null and pid <> pg_backend_pid() order by xact_start limit 10"
```

Use a read-only account for this query. Ending a transaction (`pg_terminate_backend`) is your call: first find out what it is.

**Roll back.** Do this in order:

1. Clear **Investigate with AI** on every route (or set `HM_AGENT_INVESTIGATIONS_PER_HOUR=0`, or unset `HM_AGENT_URL`). New requests stop.
2. Roll the image back.

An older release never sends a diagnosis row left behind. They stay in the delivery log, inert, and go with their incident after `HM_RETENTION_NOTIFICATION_LOG`. Investigations and their evidence stay in the database, unread, until the new release runs again and retention (`HM_RETENTION_AGENT_RUNS`) removes them. The schema stays as it is.

An older release can list and delete the agent identity. Its permissions go with it. The new release creates it again, with no permissions, so set its access again in **Settings > AI agent**.

## The release that adds scheduled reviews

This release adds migrations `0221_agent_reviews` (the reviews, their scopes and routes, their runs and their evidence) and `0222_notification_review` (the Review event, a run id column and its indexes on the notification delivery log). Both only add. Nothing changes for users until an admin adds a review and sets a model for reviews.

**Upgrade order.** Harbormaster and the agent service can be upgraded in either order. Until both are new, a due review waits up to `HM_AGENT_REVIEW_MAX_LATE` (6 hours by default) and is then recorded as `missed` (an older agent does not offer the reviewer role). Chat and investigations keep working throughout.

**Migration 0222 takes a short exclusive lock** on the notification delivery log, with a lock timeout of 2 seconds, for the reason given for 0220 above. If a long transaction holds the table at that moment, the migration times out and rolls back whole, the task exits at start, and ECS starts another that tries again. Find and end the long transaction as described above if tasks keep exiting at start with a lock timeout in their log. Migration 0221 only adds tables.

**During the rollout.** An older task never reads the review tables and never claims, renders or sends a review row. Its delivery summary and its "route failing" warning may count a failed review row as a failed delivery, until the row ages out of the delivery log. A manual retry of a failed review row on an older task fails. Alerts are not affected.

**Roll back.** Do this in order:

1. Turn reviews off: set `HM_AGENT_REVIEWS_PER_DAY=0`, or switch off each review under **Settings > AI agent > Reviews**. Nothing new starts.
2. Roll the image back.

Review rows still waiting in the delivery log are not sent by an older release, but they are not dead either. When the new release runs again with reviews on, it sends a waiting row if its result finished less than 48 hours ago, and cancels an older one with the note "The review result is too old to send." Rows go after `HM_RETENTION_NOTIFICATION_LOG` in any case. Reviews, results and evidence stay in the database, unread, until the new release runs again and retention (`HM_RETENTION_AGENT_RUNS`) removes the results. No migration is undone, and the schema stays as it is. See the [runbook](../operations/runbooks/ai-agent.md#16-reviews-upgrade-and-rollback).

## The release that adds custom reviews

This release adds migration `0223_agent_custom_reviews`: a fifth review type, `custom`, a text column on reviews and on their runs, and a test flag on runs. It adds a setting, `HM_AGENT_CUSTOM_REVIEWS`, off by default. Nothing changes for users until the operator turns it on and an admin writes a custom review. The text sent for the four built-in types is the same bytes as before.

**Migration 0223 takes a short exclusive lock** on the review tables, with a lock timeout of 2 seconds, for the reason given for 0220 above. If a long transaction holds one of them at that moment, the migration times out and rolls back whole, the task exits at start, and ECS starts another that tries again. Find and end the long transaction as described above if tasks keep exiting at start with a lock timeout in their log.

**Upgrade order.** Harbormaster and the agent service can be upgraded in either order. Create custom reviews after the deploy has finished: until both are new, a custom review waits up to `HM_AGENT_REVIEW_MAX_LATE` and is then recorded as `missed`, or is refused by the agent before any model call. Built-in reviews and the chat keep working throughout.

**Roll back.** Do this in order:

1. Disable or delete every custom review. An older release refuses to update one, and can delete it. If `HM_AGENT_CUSTOM_REVIEWS` is off, a custom review cannot be disabled, only deleted, so disable them first and set it to `false` afterwards.
2. Roll the image back.

The text of a custom review stays in the database, in the audit log and in the copies on its past runs until retention removes them, and no migration is undone. An older release lists a custom review with a note that its type is unknown, and a custom result answers `404` to everyone until you roll forward. See the [runbook](../operations/runbooks/ai-agent.md#18-custom-reviews-switch-and-rollback).

## Access role changes

A release can add permissions to the `harbormaster-access` role. Features that need a new permission show a per-account message until you re-apply the access module in that account. Everything else keeps working. Known cases:

- `ecr:BatchGetImage` (the ARM64 advisor).
- `logs:StartQuery`, `logs:GetQueryResults`, `logs:StopQuery` (log search).

See [Connect AWS accounts](connect-aws-accounts.md).

## Check it worked

- `/healthz` answers `ok` and you can sign in.
- Metrics report the new version: `harbormaster_build_info{version="..."}`.
- `harbormaster version` in the new image prints the version you built.
- Accounts show fresh polls, with no stale banner, and each managed account still connects.
- For split services, lease series look healthy (see [Health and metrics](../operations/health-and-metrics.md)).

## Roll back

Rolling the image back is safe: old tasks cannot lead while a new task holds the shared lock. Because migrations are forward-only, a rollback keeps the newer schema. To undo a release's schema changes, restore from the backup you took (see [Backup and restore](../operations/backup-and-restore.md)).

<!-- TODO(docs): verify that every release's migrations are backward compatible with the previous release's code, since that is what makes an image rollback safe -->

## Next steps

- [Changelog](../community/changelog.md)
- [AI agent runbook](../operations/runbooks/ai-agent.md)
- [High availability](../operations/high-availability.md)
- [Release process](../development/release-process.md)
