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
- [High availability](../operations/high-availability.md)
- [Release process](../development/release-process.md)
