# AI agent runbook

Day-two operation of the AI agent: a rollout order, an acceptance check, what to do when chat is unavailable or the allowance runs out, how interrupted runs are repaired, key rotation, how to turn the agent off, the automatic investigations of alerts (their states, an alert storm, upgrade and rollback), and scheduled reviews (their states, the budget, upgrade, rollback and time zones). It is for operators who run it. To install it, see [Install the AI agent](../../install/ai-agent.md). For its Terraform inputs, see [Terraform module: harbormaster-agent](../../reference/terraform-agent.md).

Plans and applies run in your deployment system. Nothing below runs `terraform plan` or `terraform apply` locally. AWS commands are read-only and always take `--profile` and an explicit `--region`. Never paste a provider API key or a run token into a ticket, chat or shell history.

## 1. Rollout

| Step | What | How to verify |
| --- | --- | --- |
| 1 | Ship the Harbormaster release with `HM_AGENT_URL` unset. Nothing changes | `GET /api/agent/status` answers `404`. There is no **Ask AI** button |
| 2 | Run the agent module and add its `task_security_group_id` to the Harbormaster module's `alb_ingress_security_group_ids` | In the agent's ECS service, `runningCount` equals `desiredCount` and its target group has healthy targets (the `<name>-unhealthy-hosts` alarm exists only if you set `alarm_sns_topic_arns`) |
| 3 | Set `HM_AGENT_URL` (module `extra_env`) and apply. All Harbormaster tasks that run the web or coordinator role must get it | **Settings > AI agent** shows **Reachable** |
| 4 | Set a small allowance first: several times what one question reserves (shown under the model in **Settings > AI agent**; for example $0.60 per question means $5 to $10), because an allowance of 0, or one below that figure, refuses the test. Then choose a model and run **Test model** | The test answers. Usage shows the call |
| 5 | Turn on **AI chat** for yourself or a small group first. Ask the acceptance questions in section 2 | Section 2 |
| 6 | Widen access. Watch spend for a week before you raise the allowance | Section 4 |
| 7 | Optional: set the investigator model and the agent identity's access, then switch one route to **Investigate with AI** | Section 9. An alert on a service the identity can see shows **Investigating**, then **Diagnosis ready**, and a Diagnosis message arrives |
| 8 | Optional: set the reviewer model, then add one review with one route and use **Run now** | Section 13. A result appears under **Reviews** and a message arrives on the route |

## 2. Acceptance checklist

- [ ] The Harbormaster module has `internal = true`. A plan fails otherwise.
- [ ] Both the web and coordinator tasks have `HM_AGENT_URL` (`extra_env` reaches every service).
- [ ] The agent's tasks are in `alb_ingress_security_group_ids`, so tool calls reach Harbormaster.
- [ ] Split services: both the web and the coordinator security groups are in `harbormaster_task_security_group_ids` of the agent module. The coordinator runs the investigator.
- [ ] **Settings > AI agent** shows **Reachable**, and the models you listed.
- [ ] The allowance is set. Allowance 0 refuses every question.
- [ ] Redaction is on for every external provider. You lifted it only where you decided to.
- [ ] A user without AI chat does not see **Ask AI**, and `POST /api/agent/conversations` answers `404` for them.
- [ ] Ask "who am I and what can I see?". The answer matches **My access**.
- [ ] Ask about a service the user cannot see. The chat finds nothing.
- [ ] The audit log shows an `agent_run` entry for each question, with provider, model, tools, tokens and cost, and no text.
- [ ] The public MCP gateway refuses a run token. A run token presented to it is never forwarded.
- [ ] `alarm_sns_topic_arns` is set on the agent module. The three alarms exist only when it is set.

## 3. Chat is unavailable

Users see "The agent is unavailable. Try again shortly." or **Not reachable** on the settings page.

1. Check the agent service and its target health.

    ```sh
    aws ecs describe-services --profile admin --region <region> \
      --cluster <cluster-name> --services <agent-service-name> \
      --query 'services[0].{desired:desiredCount,running:runningCount,events:events[0:5].message}'
    ```

2. Read the agent's log group (`log_group_name` output) for start-up errors. Configuration errors name the variable at fault and never repeat a key. Common ones:

    | Message starts with | Cause |
    | --- | --- |
    | `HM_AGENT_PROVIDERS is required` | The catalogue is empty |
    | `HM_AGENT_PROVIDERS lists <provider> but <variable> is not set` | A listed provider has no key or region |
    | `<variable> is set but HM_AGENT_PROVIDERS lists no <provider> models` | A key is set for a provider the catalogue does not list |
    | `HM_AGENT_FAKE_PROVIDER=true is allowed only when HM_AGENT_UPSTREAM_URL is a loopback address` | The fake provider is on outside local development |
    | `... must be a number between ...` | A limit is out of range |

3. A task that starts but fails its secret fetch has an access-denied error on the secret. A secret encrypted with a customer-managed KMS key needs `kms:Decrypt` for the execution role. See the module README.
4. Check that Harbormaster can reach the agent: the agent's load balancer admits only the Harbormaster web tasks' and coordinator tasks' security groups on 443, and the certificate must match the host in `HM_AGENT_URL`. For a private CA, `HM_AGENT_CA_PEM` must hold it.
5. Check that the agent can reach Harbormaster: `upstream_url` must resolve to the internal load balancer and match its certificate (`upstream_ca_pem` for a private CA), and the agent's task group must be in `alb_ingress_security_group_ids`. Without that, the agent cannot confirm the run token or call tools, and every question fails.

Status checks stop asking a dead agent for about ten seconds after a failure, so the page does not hang.

## 4. The allowance and spend

- **"The AI allowance is smaller than what one question can cost."** The allowance is below what one question reserves, so every question is refused although nothing was spent. **Settings > AI agent** shows the figure under the model ("One question reserves up to $X with this model") and warns when the allowance left is below it. Raise the allowance to several times that figure. Each refused question also leaves an `agent budget exhausted` warning in the Harbormaster log with the reservation and the amount left.
- **"This month's AI allowance is used up."** An admin raises the allowance in **Settings > AI agent**, or waits for the next UTC month. A question needs room for its whole worst-case cost, so chat can refuse a question while some allowance is left. The worst case is the run's token limit at the model's higher price.
- **Spend looks higher than the provider's bill.** Reservations and worst-case accounting keep the allowance firm. An interrupted question can be charged more than it used. Compare the usage table with the provider's own usage page.
- **Spend rises fast.** Check whether investigations are the cause (section 10). Lower `limits.max_run_tokens` and `limits.max_steps`, remove an expensive model from the catalogue, or turn **AI chat** off for users. Remember that tool definitions use about 10,000 input tokens on every model call.

## 5. Interrupted and stale runs

A question stops early when Harbormaster restarts, the browser disconnects or the agent fails. The answer is marked interrupted and the user can ask again. The coordinator's repair job runs every minute. It fails runs that stopped reporting usage, settles their cost and writes the missing `agent_run` audit rows. It runs on the coordinator, so coordinator tasks need `HM_AGENT_URL` too.

A run that stops reporting usage keeps its reservation held against the allowance until it is given up. The repair job gives up a run after 1 hour without usage. The next reservation by any question does it sooner: after twice the run timeout, and at least 10 minutes. Without the repair job, the missing audit rows are not written and only a later reservation clears such a run.

If the audit log lacks `agent_run` rows, check that a coordinator task is running and has `HM_AGENT_URL`.

## 6. Rotate a provider key

1. Put the new key in the provider's Secrets Manager secret (a new version, or a new secret and a changed ARN in `api_key_secret_arns`).
2. Force a new deployment of the agent service so tasks start with the new value.

    ```sh
    aws ecs update-service --profile admin --region <region> \
      --cluster <cluster-name> --service <agent-service-name> --force-new-deployment
    ```

3. Revoke the old key at the provider.
4. Run **Test model** in **Settings > AI agent**.

Running questions end early during the deployment. Keys are kept out of Postgres, logs, audit rows and browser responses by design; provider error text is scrubbed best effort.

## 7. Turn the agent off

1. Remove `HM_AGENT_URL` from `extra_env` and apply. The agent routes answer `404`, the button and setting disappear, and run tokens stop authenticating. Saved conversations stay until retention removes them.
2. Optionally set `desired_count = 0` on the agent module, or remove the module.

To stop only one user, turn **AI chat** off for them. To stop only the investigator, see section 11. To stop only reviews, see section 15.

## 8. What to alarm on

- The module's alarms, which exist only with `alarm_sns_topic_arns`: `<name>-alb-5xx`, `<name>-target-5xx`, `<name>-unhealthy-hosts`.
- The agent's own metrics on `/metrics`: `harbormaster_agent_runs_total{status="done|error|cancelled|panic"}` and `harbormaster_agent_runs_active`. `/metrics` needs no authentication and is served on the task port (8484). The agent's load balancer admits only Harbormaster's web tasks, so a scraper needs a path you add yourself, for example a rule on the task security group, or read the log group instead. A rise in `panic` or `error` is worth a look at the log group.
- Budget: check **Settings > AI agent** weekly, or read `agent_run` audit rows.

## 9. Investigation states and reasons

Automatic investigations are described in [Automatic AI investigations](../../guides/ai-agent-investigator.md). The coordinator runs them, under its lease, so coordinator tasks need `HM_AGENT_URL`. **Settings > AI agent > Investigations** lists them, newest first, with state, reason and cost.

States: `requested` (waiting for a slot, or for a retry), `running`, `done`, `failed` and `skipped`. A skipped investigation ran nothing and cost nothing. A failed one may have cost something. Each investigation has at most two attempts, and an attempt the agent refused as busy does not use one (it is retried after 30 seconds, within `HM_AGENT_INVESTIGATION_MAX_WAIT`). The next attempt waits 1 minute after an unreachable agent and 5 minutes after the model could not be reached.

| Reason | State | Meaning | What to do |
| --- | --- | --- | --- |
| `budget` | skipped | The monthly allowance is used up, or smaller than one run's reservation. An `agent_run` audit entry with outcome `skipped_budget` records it. | Raise the allowance, or wait for the next UTC month. See section 4 |
| `not_visible` | skipped | The agent identity cannot see the service (no View on its tag), none of a group's services, or the entry is an account entry with no region | Grant View on the tag under **Agent identity**, or accept it |
| `not_configured` | skipped | No investigator model is set, the model is no longer offered by the agent, the agent is older than Harbormaster and does not offer the investigator role, or the model's price or token limit is unknown | Choose a model in **Settings > AI agent**, check the catalogue and that the agent service is on the new release |
| `rate_limited` | skipped | `HM_AGENT_INVESTIGATIONS_PER_HOUR` was reached, or it is `0` | Section 10 |
| `expired` | skipped | The request waited longer than `HM_AGENT_INVESTIGATION_MAX_WAIT` for its first start | Section 10 |
| `agent_unavailable` | failed | The agent could not be reached on both attempts, or was busy past the maximum wait | Section 3. Check the coordinator's security group is admitted by the agent's load balancer |
| `limit_reached` | failed | A run ended without a summary because it reached its step, token or time limit | Raise the agent's per-run limits only if this is common, and it costs more |
| `no_result` | failed | The model ended without submitting a summary even after one reminder | Try another model if it repeats |
| `interrupted` | failed | The run was lost: the coordinator stopped on the last attempt, or the run's usage could not be recorded | Normally a restart. The cost is settled by the repair job (section 5) |
| `error` | failed | Anything else, including a model that could not be reached on both attempts | Read the coordinator log lines that begin `investigate:` |

Useful log lines on the coordinator: `investigate: agent catalogue unavailable`, `investigate: coordinator lease not confirmed, not starting`, `investigate: the previous run is not settled`, `agent identity not ready`, and, from the part that records requests and plans diagnosis messages, `notify: investigation step` with a `step` and a running `count` (a panic logs `notify: investigation step panicked`, and a send worker that panicked logs `notify: follow-up worker panicked`). They never contain alert text, the agent's message or any of the result.

An earlier run that cannot be settled. Before a new attempt, the investigator ends the run an earlier holder of the investigation left behind. If that cannot be written (almost always a database fault: the database is down, slow or locked), the coordinator logs `investigate: the previous run is not settled` with the error, starts no second run beside it, and tries again after 1 minute, every minute for as long as it fails. The retry does not use one of the two attempts and the request does not expire, so nothing is lost; the earlier run may still hold its reservation from the allowance until it is settled. To find the cause, read the error on that log line, check that the database is reachable from the coordinator and not at its connection limit, and look for a long transaction that holds a lock (the query in [Upgrading](../../install/upgrading.md#the-release-that-adds-automatic-investigations) lists them). Once the database answers the next retry settles the run, and the investigation goes on. If the line stops appearing, nothing is left to do.

An investigation request that is waiting stays in the database until it runs or is skipped. If you turn the agent off for good, such rows keep their alert text; retention does not remove an investigation that is waiting or running.

## 10. An alert storm

Many alerts at once do not delay any alert message: Opened messages are planned and sent first, and nothing in the investigator runs before them. Investigations are paced and capped:

- Alerts for one service or one cluster or account entry within `HM_AGENT_INVESTIGATION_WINDOW` (15 minutes) share one investigation.
- At most `HM_AGENT_INVESTIGATIONS_CONCURRENT` (2) run at once, and at most `HM_AGENT_INVESTIGATIONS_PER_HOUR` (20) start in an hour. The hourly cap can overshoot by up to the number of parallel runs. It is checked once, before an investigation's first start; the retry of one that already started is not held back by it.
- A request that has waited more than `HM_AGENT_INVESTIGATION_MAX_WAIT` (15 minutes) is skipped as `expired`, so a long queue does not deliver stale diagnoses. The rest of a storm ends as `rate_limited`.
- Each run reserves its worst case from the monthly allowance, which the chat shares. A storm can use it up: later investigations end as `budget` and chat refuses questions until an admin raises the allowance or the month ends.

What to do:

1. Check **Settings > AI agent** for the spend and the investigations list. Cost per investigation is shown (after a retry, only the last attempt's).
2. To stop new investigations at once, set `HM_AGENT_INVESTIGATIONS_PER_HOUR=0` and apply. Nothing starts, no new request is recorded, and requests still waiting (retries included) end as `rate_limited`. No Diagnosis message is planned or sent while it is `0`, so one already waiting stays unsent. To stop them for one route, clear **Investigate with AI** on it.
3. To keep investigations and bound a storm, lower `HM_AGENT_INVESTIGATIONS_PER_HOUR`. Raise the allowance only if you accept the spend.
4. Diagnosis messages waiting for an alert that then resolves are cancelled, so a storm that clears leaves little to send.

## 11. Turn investigations off

Any one of these works. Opened and other alert messages are never affected.

1. Clear **Investigate with AI** on the routes. Diagnosis rows already waiting for such a route are cancelled when they are due ("Investigate with AI is off."), and new requests are not recorded for it.
2. Set `HM_AGENT_INVESTIGATIONS_PER_HOUR=0`. No new request is recorded, and requests already waiting end as `rate_limited` without a new run. Diagnosis messages stop with it, and the switches on the routes are not changed.
3. Clear the investigator model in **Settings > AI agent**. Requests end as `not_configured`.
4. Unset `HM_AGENT_URL` (section 7). This also turns off the chat. The **Investigate with AI** switches stay on in the routes, so when `HM_AGENT_URL` is set again those routes start investigating, and spending from the allowance, at once. Clear the switches first if you do not want that.

## 12. Upgrade and rollback

- **Upgrade order.** Harbormaster and the agent service can be upgraded in either order. Investigations are skipped as `not_configured` until both are new. In split mode, add the coordinator's security group to the agent module's `harbormaster_task_security_group_ids` before you switch a route on.
- **While an older Harbormaster runs against the new schema,** its delivery summary and "route failing" warning count diagnosis messages. Alerts themselves are unaffected: an older task never claims, renders or sends a diagnosis row.
- **Migration 0220** takes a short exclusive lock on the delivery log (2-second lock timeout). If a long transaction holds the table, the task exits at start and is retried. Find and end the long transaction. See [Upgrading](../../install/upgrading.md#the-release-that-adds-automatic-investigations).
- **Rollback.** Clear **Investigate with AI** on the routes first, then roll back. An older version never sends a diagnosis row left behind. An older release can list and delete the agent identity, and its permissions go with it. The new release creates it again with none.

## 13. Review states and reasons

Scheduled reviews are described in [Scheduled AI reviews](../../guides/ai-agent-reviews.md). The coordinator runs them, under its lease, so coordinator tasks need `HM_AGENT_URL`. **Settings > AI agent > Reviews** lists each review with its last result, and **Reviews** in the sidebar lists the results.

States: `requested` (waiting for its turn, or for a retry), `running`, `done`, `failed` and `skipped`. A skipped run ran nothing and cost nothing. A failed one may have cost something. Each run has at most two attempts. An attempt the agent refused as busy does not use one: it is retried after 1 minute, within `HM_AGENT_REVIEW_MAX_LATE`. After an agent that could not be reached, the next attempt waits 5 minutes. A run does not start while an investigation is waiting, and one review runs at a time.

| Reason | State | Meaning | What to do |
| --- | --- | --- | --- |
| `budget` | skipped | The month's allowance would not still hold one chat run and one investigation after this review's worst case. An `agent_run` audit entry with outcome `skipped_budget` records it. The run is not retried in that period | Section 14 |
| `not_visible` | skipped | The agent identity can View no service in the scope | Grant View on the tags to review under **Agent identity**, or change the scope |
| `not_configured` | skipped | No reviewer model is set. Also see below for a model or role the agent does not offer | Choose a model in **Settings > AI agent** |
| `rate_limited` | skipped | `HM_AGENT_REVIEWS_PER_DAY` runs already started in the last 24 hours. The period gets no review | Raise it, or reduce the number of reviews. The cap can overshoot by one when two coordinator tasks overlap |
| `missed` | skipped | The run could not start within `HM_AGENT_REVIEW_MAX_LATE` of its scheduled time: Harbormaster or the coordinator was down, the agent was busy or did not offer the role, or the queue was long | Find out why (see below). The next period runs as usual |
| `disabled` | skipped | The review was switched off or deleted before the run started, or before its retry. A manual run of a switched-off review is not skipped for this; a manual run of a deleted review is | Switch the review on if that was a mistake |
| `agent_unavailable` | failed | The agent could not be reached on both attempts, or a run that had started waited past the late limit for an agent that did not offer the role, was unreachable, or had lost the model from its catalogue | Section 3. Check the coordinator's security group is admitted by the agent's load balancer |
| `limit_reached` | failed | A run ended without a summary because it reached its step, token or time limit | Narrow the scope or split the review. Raise the agent's per-run limits only if this is common, and it costs more |
| `no_result` | failed | The model ended without submitting a summary even after one reminder | Try another model if it repeats |
| `interrupted` | failed | The run was lost: the coordinator stopped on the last attempt, the run's usage could not be recorded, or a retry would have started later than `HM_AGENT_REVIEW_MAX_LATE` plus the run's lease after the scheduled time | Normally a restart. The cost is settled by the repair job (section 5) |
| `error` | failed | Anything else: a model that could not be reached (not retried), a refusal of the agent's request (not retried), or a set-up step that failed on both attempts | Read the coordinator's log lines for the run. They carry the run id |

Where a review is not offered. If the agent does not offer the reviewer role or the type (an older agent), or its catalogue cannot be read, the run is released without using an attempt and tried again after 5 minutes. It ends as `missed` once it is older than `HM_AGENT_REVIEW_MAX_LATE`. A review whose model has been removed from the agent's catalogue waits the same way. Check **Settings > AI agent** for the stale model warning.

The coordinator's log lines for reviews carry ids and counts and never the summary, a note, an excerpt, a review name or a scope label. A review reads no logs, so there is nothing of that kind for them to carry.

A review that is waiting stays in the database until it runs or is skipped. If you turn the agent off for good, such rows stay; retention does not remove a waiting or running one.

## 14. The review budget

Reviews spend from the month's allowance, which the chat and the investigations share. They are the first to be skipped when it runs short: a review run starts only if its worst case still leaves the worst case of one chat run and one investigation, at the models chosen for them.

1. **Read the worst case.** **Settings > AI agent > Reviews** shows "Worst case of one run": what one review run reserves. The review editor shows "Worst case: $X a month at the current model": one reservation for each run the cadence can have in a month (31 for a daily review, 5 weekly, 1 monthly). Add the lines of all your reviews. It is the most that can be kept back, not what is spent, and it leaves out the retry (a second run for the period).
2. **Compare it with the allowance.** Reviews that would not fit appear as `skipped` with reason `budget`. The chat and the investigations are unaffected: that is the point of the room they keep. A run in progress holds its reservation until it ends.
3. **Raise the allowance** in **Settings > AI agent** to let them run, or reduce what they cost: fewer reviews, weekly instead of daily, a smaller scope, a cheaper reviewer model, or lower `limits.max_run_tokens` on the agent (it lowers every run).
4. **Spend rises fast.** Look at the cost of recent results. `HM_AGENT_REVIEWS_PER_DAY` (24) caps how many reviews start in 24 hours. See section 4 for the allowance in general.

## 15. Turn reviews off

Any one of these works. Alert messages and investigations are never affected.

1. Switch off a review (**Review is on** in its editor, or the switch in the list). It runs no more. A run of it that is already waiting is skipped as `disabled`.
2. Set `HM_AGENT_REVIEWS_PER_DAY=0` and apply. No review is recorded or started, and the **Reviews** entry disappears from the sidebar. Past results stay readable by link while the agent is on.
3. Clear the reviewer model in **Settings > AI agent**. Reviews end as `not_configured`.
4. Unset `HM_AGENT_URL` (section 7). This also turns off the chat and the investigations.

## 16. Reviews: upgrade and rollback

- **Upgrade order.** Harbormaster and the agent service can be upgraded in either order. Until both are new, a due review waits up to `HM_AGENT_REVIEW_MAX_LATE` and is then recorded as `missed`. Chat and investigations are not affected.
- **Migrations 0221 and 0222** are additive. 0222 takes a short exclusive lock on the delivery log, with a lock timeout of 2 seconds. If a long transaction holds the table, the task exits at start and is started again, and the migration retries from the start at the next start. See [Upgrading](../../install/upgrading.md#the-release-that-adds-scheduled-reviews).
- **Rollback.** Turn reviews off first: `HM_AGENT_REVIEWS_PER_DAY=0`, or disable each review. An older Harbormaster ignores the review tables and never sends a leftover review row. Rows left waiting are not inert: when the new release runs again with reviews on, a waiting row whose result finished less than 48 hours ago is sent, and an older one is cancelled with the note "The review result is too old to send." The same holds for rows that waited while reviews were off. Its delivery log may count a failed review row toward "route failing" until the row expires, and a manual retry of such a row fails there. No migration is undone. The reviews and their results stay in the database, unread, until the new release runs again.
- **Reviews off keeps results readable.** With `HM_AGENT_REVIEWS_PER_DAY=0` and the agent on, past results stay readable (`/reviews/runs/<id>` and the list API still answer). Only the **Reviews** entry in the sidebar disappears. With `HM_AGENT_URL` unset, no review route exists.
- **A review switched off while a scheduled run is in progress does not send its result.** The message is not planned if the review is off, and a row already planned is cancelled when it is sent, with the note "The review is off." A manual run of a switched-off review still sends. The result stays readable either way.
- **Old review messages are not sent.** A message is cancelled with "The review result is too old to send." when its result finished more than 48 hours ago (24 hours to plan it and 24 more to send it). This holds for a manual retry of a failed review message from the delivery log too: a retry of one older than 48 hours is cancelled.

## 17. Time zones

- A review's schedule is read in its own IANA zone (for example `Europe/Berlin`), not in the server's. No part of the schedule uses the host's zone.
- The zone's rules come from the time zone data in the container image, or from the copy built into Harbormaster when the image has none. A change in a country's rules (a new daylight saving date) reaches reviews when you run an image or release that has it. Until then reviews follow the old rule.
- **When clocks go forward**, a scheduled local time that falls in the skipped hour runs when the gap ends (a review at 02:30 on a night when 02:00 becomes 03:00 runs at 03:00). If that moves it to the next date, it keeps the period of the date it was scheduled for.
- **When clocks go back**, a local time that happens twice runs at the earlier of the two.
- A period is one local day, one ISO week (Monday to Sunday) or one calendar month, named from the date the run was scheduled for. Editing a review's zone restarts its schedule: only a time after the edit counts, and a period never gets two scheduled runs.
- Time shown in the editor ("Next run") is in the review's zone. The zone must be an IANA name. `Local` and similar names are refused.

## Next steps

- [Automatic AI investigations](../../guides/ai-agent-investigator.md)
- [Scheduled AI reviews](../../guides/ai-agent-reviews.md)
- [AI chat](../../guides/ai-agent-chat.md)
- [Security model](../../security/security-model.md#ai-agent)
- [Throttling and limits](../throttling-and-limits.md)
