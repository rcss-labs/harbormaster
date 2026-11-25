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
| `HM_RETENTION_AGENT_CONVERSATIONS` | `720h` (30 days) | AI chat conversations: your messages, the answers and the names of tools used | No activity for the age. The run records (who, model, tools, tokens, cost) stay |
| `HM_RETENTION_AGENT_RUNS` | `2160h` (90 days) | Finished AI investigations (alert snapshot, summary and stored evidence excerpts), finished AI review results (summary and stored evidence excerpts) and finished agent run records | Finished longer ago than the age. `0` keeps everything. A non-zero value must be at least `48h`. Waiting or running investigations and reviews, ones finished less than 48 hours ago, the newest scheduled result of each review, and every run of the current UTC month stay, so the month's spend never shrinks |
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
| AI agent run records | Kept after their conversation or user is gone, so usage history survives, until `HM_RETENTION_AGENT_RUNS` (and never within the current UTC month). The `agent_run` audit rows follow `HM_RETENTION_AUDIT_LOG` |
| Run tokens | Expire after `HM_AGENT_RUN_TOKEN_TTL` (15 minutes by default) |
| Expired sessions, SSO requests and assertion IDs, OAuth codes and grants | Removed once expired |

## What is not in the database

- **Logs and metrics** stay in CloudWatch in your AWS accounts, under your own retention settings. Harbormaster reads them and does not copy them.
- **Terminal sessions** are not recorded. The operations log records who opened a session, the task, the container, the command and how long it lasted. To keep a transcript, turn on Session Manager logging.
- **Tool results** of the AI chat are never stored. Only the question, the answer and the tool names are. The model provider keeps what it keeps under its own terms: check them.
- **Tool results** of an automatic investigation are stored only as the cited evidence: at most 12 excerpts of 1500 bytes each, scrubbed for secrets. They can contain log text. They are deleted with the investigation (`HM_RETENTION_AGENT_RUNS`). An investigation can outlive its alert, because alert incidents go after `HM_RETENTION_NOTIFICATION_LOG` (30 days by default): the investigation stays readable and the link from the alert is gone. Stored text is never sent back to a model.
- **Diagnosis rows** in the notification delivery log follow `HM_RETENTION_NOTIFICATION_LOG` like other deliveries. They hold the alert snapshot, not the summary: the message is built from the investigation when it is sent.
- **Review results** are stored like investigations: the summary, the tags the run covered and at most 12 cited excerpts of 1500 bytes each, scrubbed for secrets. A review reads no logs, so none can be log text, but excerpts can hold names, sizes and figures of your services. They are deleted with the result (`HM_RETENTION_AGENT_RUNS`). The newest scheduled result of each review stays however old, because it is what stops a period that already ran from running again. Deleting a review keeps its results until they age out. Stored text is never sent back to a model.
- **Custom review instructions** are stored on the review and, as a copy, on each run that used them. The copy goes with the run's result (`HM_RETENTION_AGENT_RUNS`). The review's own text stays until the review is deleted. The audit entry of every change holds the old and the new text and goes with the other audit entries (`HM_RETENTION_AUDIT_LOG`, 365 days by default). A test run is kept like any result. Stored text is never sent back to a model, but a run sends its copy of the instructions to the model provider.
- **Review rows** in the notification delivery log follow `HM_RETENTION_NOTIFICATION_LOG` like other deliveries. They have no incident. They hold the review's name, not the summary: the message is built from the result when it is sent.
- **Server logs** go to the CloudWatch log group of the module, with `log_retention_days` (default 30).
- **Scan results** in S3 expire by lifecycle rule: `jobs/` after 7 days, `results/` after 30 and `grype-db/` after 10.

## Check it worked

The coordinator's retention step returns the number of rows removed per table. If the database keeps growing, check that exactly one task reports `harbormaster_coordinator_active` = 1 and that its log shows no `coordinator: retention` error.

## Next steps

- [Backup and restore](backup-and-restore.md) for what a restore brings back.
- [Audit log](../guides/audit-log.md).
