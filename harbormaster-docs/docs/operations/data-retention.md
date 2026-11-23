# Data retention

Harbormaster keeps history in its Postgres database and removes old rows on a schedule. This page lists what is kept, for how long by default and how to change it. It is for operators who size the database or answer audit questions.

## How retention runs

The coordinator task runs a retention job once an hour, together with the daily rollups. Only the active coordinator runs it. Each setting is a Go duration (`168h`, `720h`). A value of `0` or less for a timed table turns pruning off for that table, so the rows are kept forever.

Set these variables on the Harbormaster server tasks. With the Terraform module, use `extra_env`:

```hcl
extra_env = {
  HM_RETENTION_AUDIT_LOG = "17520h" # two years
}
```

An invalid duration stops the server at start with an error that names the variable.

## Configurable retention

| Variable | Default | Data | Deleted when |
| --- | --- | --- | --- |
| `HM_RETENTION_STOPPED_TASKS` | `168h` (7 days) | Stopped tasks | Older than the age, measured from the stop time |
| `HM_RETENTION_SERVICE_EVENTS` | `720h` (30 days) | Service events | Older than the age |
| `HM_RETENTION_COUNTS_HISTORY` | `168h` (7 days) | Task-count history | Older than the age |
| `HM_RETENTION_OPERATION_LOG` | `2160h` (90 days) | Operations log | Older than the age |
| `HM_RETENTION_DEPLOYMENT_HISTORY` | `2160h` (90 days) | Deployment history | Older than the age |
| `HM_RETENTION_AUDIT_LOG` | `8760h` (365 days) | Audit log, and expired image-risk acceptances | Older than the age |
| `HM_RETENTION_ORPHAN_TAGS` | `720h` (30 days) | Tags whose cluster or service no longer exists | Orphaned longer than the age |
| `HM_RETENTION_AVAILABILITY_DAILY` | `8760h` (365 days) | Daily service availability rollups | Day older than the age |
| `HM_RETENTION_CAPACITY_DAILY` | `8760h` (365 days) | Daily cluster capacity rollups | Day older than the age |
| `HM_RETENTION_UTIL_DAILY` | `2160h` (90 days) | Daily service utilisation rollups | Day older than the age |
| `HM_RETENTION_NOTIFICATION_LOG` | `720h` (30 days) | Notification delivery log and resolved alert incidents | Older than the age. Deliveries of an incident that is still open are kept |
| `HM_RETENTION_SCAN_IMAGES` | `168h` (7 days) | Image scan results | Not requested for the age |
| `HM_RETENTION_SCAN_RUNS` | `720h` (30 days) | Image scan run records | Finished longer ago than the age |

The task-count history is also trimmed by the poller that samples it, using the same `HM_RETENTION_COUNTS_HISTORY` value.

## Fixed retention

These are not configurable.

| Data | Kept for |
| --- | --- |
| Login attempts | 24 hours |
| Cached CloudWatch metric series | 10 minutes |
| Retired SSO signing keys | 7 days after retirement (the metadata advertises a retired key for that long) |
| Image platform records (ARM64 check) | Removed after 7 days without a task definition that mentions the image |
| Expired access tokens | 1 day after expiry |
| Daily token log-search ledger | 35 days |
| Expired sessions, SSO requests and assertion IDs, OAuth codes and grants | Removed once expired |

## What is not in the database

- **Logs and metrics** stay in CloudWatch in your AWS accounts, under your own retention settings. Harbormaster reads them and does not copy them.
- **Terminal sessions** are not recorded. The operations log records who opened a session, the task, the container, the command and how long it lasted. To keep a transcript, turn on Session Manager logging.
- **Server logs** go to the CloudWatch log group of the module, with `log_retention_days` (default 30).
- **Scan results** in S3 expire by lifecycle rule: `jobs/` after 7 days, `results/` after 30 and `grype-db/` after 10.

## Check it worked

The coordinator's retention step returns the number of rows removed per table. If the database keeps growing, check that exactly one task reports `harbormaster_coordinator_active` = 1 and that its log shows no `coordinator: retention` error.

## Next steps

- [Backup and restore](backup-and-restore.md) for what a restore brings back.
- [Audit log](../guides/audit-log.md).
