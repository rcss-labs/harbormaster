# Scheduled AI reviews

How Harbormaster can run the AI agent on a schedule to write a short review of your costs, image security findings, reliability or capacity, and send it to notification routes. For admins who set reviews up and for anyone who reads their results.

A review is a read-only run of the AI agent. An admin gives it a type, a scope, a schedule and the routes that get the result. At each scheduled time the agent reads the data Harbormaster already holds and writes a summary. Setting up the agent is an operator task: see [Install the AI agent](../install/ai-agent.md). The chat and the investigations of alerts are separate features: see [AI chat](ai-agent-chat.md) and [Automatic AI investigations](ai-agent-investigator.md).

## What you need to know first

- **A review can be wrong.** A model reads numbers and lists and states a conclusion. It can misread them, miss something it was not shown, or state a guess as a fact. Every place that shows a review says it was written by AI. Check it before you act.
- **The summary is a conclusion. The evidence needs permission.** A summary is shown to people who can see everything the review covered. Each piece of evidence behind it is shown only to people whose own access covers that service (see [Who sees what](#who-sees-what)).
- **A review reads no logs.** The agent is not offered the log tools in a review, so a review never searches or reads log lines and never uses the agent identity's log-search budget.
- **A review reads data as fresh as the last poll.** It reads what Harbormaster stored from its AWS polls and daily rollups, not live AWS state. A change made a minute ago may not be in it. See [Polling cadence](../operations/aws-costs.md#polling-cadence).
- **One run covers a limited amount.** See [What one run can cover](#what-one-run-can-cover).
- **It spends from the same monthly allowance as the chat.** Reviews are skipped first when the allowance runs short. See [Cost and limits](#cost-and-limits).
- **A review changes nothing.** Like every run of the agent, it has no tool that writes to AWS or to Harbormaster.

## What each type reads

All four types have the same tools, the same identity and the same checks. The type decides only what the agent is asked to read first and how it is asked to write the summary. The summary has three parts, in a fixed order.

| Type | Label | Reads first | The three parts of the summary |
| --- | --- | --- | --- |
| `cost` | Cost | The cost analytics (estimated Fargate run rate and the savings advisors) for the range below | Spend, largest costs, savings (which to take first, and which are estimates) |
| `security` | Security findings | The latest image scan results. They have no range | Exposure (images with critical or high findings, and how many were not scanned), worst images (and the services that run them), what to fix first |
| `reliability` | Reliability | The reliability analytics | Availability, services at risk (the least available, those getting worse, how their tasks stopped), what to check |
| `capacity` | Capacity | The capacity analytics. They show the latest poll and recent history whatever the range is | Headroom (what is reserved, which subnets or clusters are close to full), at their limit (services at their scaling maximum), over-reserved (what to lower first) |

The range follows the cadence: a daily review reads 24 hours, a weekly review 7 days and a monthly review 30 days.

The agent may then open a single service to explain one of its worst findings. A reviewer has 35 of the agent's read-only tools. The four log tools are not among them. It can make at most 60 tool calls in one run and must then submit its result.

Because a review has no logs, the reliability type reasons from stop reasons, exit codes and the timeline, not from what the application printed.

## What it does

1. A review has a schedule: daily, weekly (on a weekday) or monthly (on a day of the month from 1 to 28), at a local time in a time zone. See [Schedules and time zones](#schedules-and-time-zones).
2. At the scheduled time the coordinator records one run for that period and starts it when the agent is free. It runs as the [agent identity](ai-agent-investigator.md#the-agent-identity) with the `reviewer` role and the model an admin chose for reviews.
3. The agent finishes by submitting a summary and up to 12 pieces of evidence it cites. Harbormaster checks them again and stores them.
4. For each route on the review (never PagerDuty), Harbormaster sends the result as a message. See [Alert notifications](alert-notifications.md#review-messages).

Results are also read in the web app: **Reviews** in the sidebar lists them, and each has its own page.

## Turn it on

Reviews need the AI agent (`HM_AGENT_URL`), an allowance, a model for reviews, an access grant for the agent identity and a review with at least one route.

1. Set up the agent and a monthly allowance. See [Install the AI agent](../install/ai-agent.md).
2. A global admin opens **Settings > AI agent** and chooses **Model for reviews**. **Test model** makes one small paid call. Without a model, reviews are skipped as `not_configured`.
3. In **Agent identity** on the same page, choose what the agent may read for each tag. A review reads what the identity may View and nothing else. With no access at all, every review is skipped as `not_visible`. A review never uses Logs, so View is enough.
4. In **Settings > AI agent > Reviews**, select **Add review**. Give it a name, a type, a scope, a cadence, a time and a time zone, and choose 1 to 5 routes. PagerDuty routes are not offered. A new review starts switched off: tick **Review is on** and save.

Check it worked: the editor shows how many services are in scope, how many of them the agent cannot see, the next run time and the worst case for a month. Select **Run now** on the review to start one run at once. When it finishes, the result is listed under **Reviews** and sent to the routes.

While `HM_AGENT_REVIEWS_PER_DAY` is `0`, the section says "Reviews are turned off by configuration." To turn reviews off, see the [runbook](../operations/runbooks/ai-agent.md#15-turn-reviews-off).

### Limits on definitions

- At most 20 reviews. A review name is 1 to 80 characters and is unique without regard to case.
- At most 10 scope entries and 5 routes for a review.
- The time zone is an IANA name such as `Europe/Berlin`.

## Scope

The scope tells the agent where to look. An entry is a Harbormaster tag, a cluster, or an account (with a region if you want one). Entries add together. A review with no entry looks at everything the agent identity can see.

The scope is a focus, not a boundary. The boundary is what the agent identity may View. A review never reads a service the identity cannot View, whatever its scope says, and nothing in a scope entry, a name or a tool answer can widen that. Give the identity View only on the tags you want a model to read.

When a run starts, Harbormaster works out which services the scope reaches and which of them the identity can View. If it can View none, the run is skipped as `not_visible` and costs nothing. A result shows the scope label and how many services were in scope.

### What one run can cover

One run is one conversation with the model, under the limits of every agent run (steps, tokens and time, see [Configuration](../reference/configuration.md#agent-settings)) and 60 tool calls. A tool answer is cut at `HM_AGENT_MAX_RESULT_BYTES`. In an analytics answer the totals come before the rows, so a cut takes rows and keeps the totals. A review of a large estate reads the largest problems first and may not see the rest.

- The editor shows a line when more than 150 services are in scope: "One run reads the largest problems first. Split this review by account or tag for more depth."
- To split, make several reviews, each with a scope of its own (one account, one cluster or one tag) and its own schedule and routes. Each review costs one run for each period.
- A run that cannot cover its whole scope does not say so. The summary is about what it read. If a result looks thin, narrow the scope.

## What you see

- **Reviews.** The sidebar entry **Reviews** appears while reviews are on. It lists the results you may read, newest first, with name, type, period, the first line of the summary and the state.
- **A result.** `/reviews/runs/<id>` shows the review's name, type, period, scope and the number of services in scope, the state and times, the summary and the evidence. The summary is plain text: no links, formatting or images. It carries the caption "Written by AI from what its identity may read. Check it before acting."
- **Settings > AI agent > Reviews.** Admins see each review with its next run and last result, and can switch it on or off, run it now, edit it or delete it. Opening a review shows its past results.
- **The delivery log.** On **Settings > Notifications**, the delivery log lists each review message as **Review**, with a link to the result.

### States

| State shown | Meaning |
| --- | --- |
| Waiting to run | Recorded, and waiting for its turn or for a retry. |
| Running | The agent is working on it. |
| Review ready | Done. The summary and evidence are stored. |
| The review did not finish. | It failed. The Settings list gives the reason. |
| Not run: this month's AI allowance has too little left. Reviews keep room for chat and investigations. | Reason `budget`. |
| Not run: the agent cannot see any service in this scope. | Reason `not_visible`. |
| Not run: no model is set up for reviews. | Reason `not_configured`. |
| Not run: too many reviews in the last day. | Reason `rate_limited`. |
| Not run: it could not start in time. | Reason `missed`. |
| Not run: the review was turned off. | Reason `disabled`. The review was switched off or deleted before the run started, or before its retry. A manual run of a switched-off review is not skipped for this; a manual run of a deleted review is. |

The runbook lists every reason and what to do: [States and reasons](../operations/runbooks/ai-agent.md#13-review-states-and-reasons).

## Schedules and time zones

A schedule is a cadence, a local time of day, a weekday (weekly) or a day of the month from 1 to 28 (monthly), and an IANA time zone. Days stop at 28 so every month has them.

- **One run for each period.** A period is a local day (daily), an ISO week (weekly) or a month (monthly), named from the local date the run was scheduled for. However many coordinator tasks run, however often a task restarts and however often you edit the schedule, a period has one run. Moving a weekly review to another weekday in the middle of a week does not give that week a second review.
- **A new or edited review starts from its next time.** A review made at 10:00 for 08:00 first runs the next day. A change to the cadence, the time, the weekday, the day of the month or the zone, and switching a review on, restarts this: only a time after the change counts. Changing the name, scope or routes does not.
- **Clock changes.** A local time that does not exist (the hour skipped when clocks go forward) is the moment the gap ends. A local time that happens twice (when clocks go back) is the earlier one.
- **Zone data.** The rules of a zone come from the time zone data in the container image, or from a copy built into Harbormaster when the image has none. A government's new daylight saving rule takes effect when you run an image or release that contains it.
- **Late and missed runs.** A run that is due starts when it can. If Harbormaster was down at the scheduled time, a run that is at most `HM_AGENT_REVIEW_MAX_LATE` (6 hours by default) late still happens. A later one is recorded as `missed` and does not run. A retry that had to start later than `HM_AGENT_REVIEW_MAX_LATE` plus the run's lease after the scheduled time is not started: the run ends as `interrupted`, and one that waited past the limit for an agent that did not offer the role ends as `agent_unavailable`. After a long outage a review has one record, not one for every period it missed.
- **Run now.** An admin can start a review at once, also one that is switched off. It is a period of its own and does not replace the scheduled run.

## Who sees what

Reading is checked on every request against your current permissions, not those you had when the review ran. Access tokens and the MCP gateway cannot read reviews, and neither can the agent. The routes are for signed-in people only.

- **The summary.** A global admin reads every result. Anyone else needs View on every tag the review covered. The tags covered are all the tags the agent identity could View when the run started or when it finished, not only the tags of the services in scope, because the identity reads everything it may View. Both sets are stored with the run. A tag that was granted and removed again during one run is not covered. A result that covered no tag, or a tag that has since been deleted, is for global admins only. When the identity can View many tags, few people other than admins can read a result.
- **The evidence.** Each item is for one service. You need View on that service now. A review cites View evidence only, so no item needs Logs, and an item with any other action is hidden from everyone, admins included. Hidden items are counted, not described: the page says only "Some evidence is not shown because you do not have permission to read it."
- **The list.** **Reviews** lists only the results you may read. A result you may not read answers "not found", as if it did not exist.
- **Managing reviews.** Only global admins can create, change, run, switch or delete a review, and choose the model for reviews.

See [Permissions](../reference/permissions.md#who-reads-a-review).

### Evidence is limited

The model does not write evidence. It points at numbered pieces of tool output it was shown, with a one-line note. Harbormaster takes the excerpt from the tool result and labels it with the service and the action from a fixed rule, in the agent and again in Harbormaster. A piece it cannot label is dropped. At most 12 items are stored, each at most 1500 bytes. The estate-wide totals of an analytics answer cannot be cited, so a summary can rest on something you cannot open.

## What the summary check does and does not catch

Harbormaster checks every summary and note before it is stored, and again before every message is sent. The checks are those of [investigations](ai-agent-investigator.md#what-the-summary-check-does-and-does-not-catch). A summary that is empty, too long, has hidden characters or image markup, or is changed by the secret scrubber is replaced whole by a fixed sentence. For a review it is "The review finished. Open it in Harbormaster to read the findings." The copy rule has two lengths and one exemption.

- **Copying.** The summary is replaced when it repeats, ignoring case and punctuation, 32 or more characters of a log message, an environment variable value, a command or entry point of a task definition, or anything Harbormaster has not listed (error texts, labels attached to a container, log options beyond the standard ones, an answer cut by the read cap), or 80 or more characters of the other values it lists (a status sentence, an image reference, a label, a name). A summary may therefore hold up to 79 characters in a row of one listed value. A review reads no log messages.
- **Names are exempt.** A review has to name services and clusters, and a name can be longer than 32 characters. A name is not counted as copied text when it was decoded from an id of a service or cluster in any answer the agent read. A name is one word of letters, digits, hyphens and underscores, up to 128 characters. This applies to the summary, not to notes.
- **What the exemption does not cover.** A name with spaces, dots or other characters. A stretch of text that takes in a name and the words beside it, which is checked in full. A name the agent did not read in an id. Anything in a note. Text a person wrote, such as a tag name or a review name, is not exempt unless it is also part of a service or cluster id the agent read.

The check is best effort, as for investigations: paraphrase is not caught, a bare host name may stay, and the scrubber uses patterns. Treat a review as text written by a model that has read your estate.

## What leaves your network

For each run, the following go to the provider of the reviewer model:

- the instructions of the type, the type's label, the range, the period (for example "Week 48, 2026"), the number of services in scope and the scope entries (kind, ids, region and the names of tags and clusters), each scrubbed and cut. The review's own name is not sent;
- the tool definitions, and the scrubbed results of the tools the agent called: analytics and security rows, names and sizes of services and clusters, task definitions with environment values redacted, scaling limits and the like. No log line.

Environment values in task definitions are redacted unless an admin lifted that for the provider. `HM_TOKEN_REDACT_ENV=true` overrides the lift. Secret-looking values are always redacted, best effort. Because nobody asks for these runs, check the provider terms before you set the reviewer model, and prefer a provider inside your own AWS account (Amazon Bedrock) for sensitive estates.

A review message sends the summary to each route's destination. That is a second place the model's text goes. See [Alert notifications](alert-notifications.md#review-messages).

## What is stored, and for how long

Stored in Harbormaster's database:

- The review: name, type, schedule, scope, routes and whether it is on.
- Each run: the review's name and type at that time, the period, the scope label, the number of services, the tags covered, the state and reason, and the summary.
- The evidence: up to 12 excerpts of tool output for each run, at most 1500 bytes each, scrubbed in the agent and again in Harbormaster, with the notes, the tool name and the service.
- The run record and an `agent_run` audit entry: model, tools, tokens and cost, never text. An admin's changes to reviews are audited too. See [Audit log](audit-log.md).
- One row for each message in the notification delivery log. It holds the review's name, not the summary: the message is built from the stored result when it is sent.

Stored evidence is never sent back to a model.

Finished results are deleted `HM_RETENTION_AGENT_RUNS` after they finish (90 days by default). A waiting or running one, and one that finished less than 48 hours ago, is never deleted. The newest scheduled run of each review stays however old, because it is what stops a period that already ran from running again. Rows in the delivery log go after `HM_RETENTION_NOTIFICATION_LOG` (30 days). See [Data retention](../operations/data-retention.md).

## Cost and limits

- **One allowance.** Reviews spend from the monthly allowance that the chat and the investigations use.
- **Worst case is reserved.** Before a run starts, Harbormaster reserves its worst case: the run's token limit at the model's higher price. A run is settled once, whatever way it ends.
- **A review never takes the last room.** A review run starts only if, after its reservation, the allowance still holds the worst case of one chat run and one investigation at the models chosen for them. Otherwise it is skipped as `budget`, and the chat and the investigations keep working. One review runs at a time, and none starts while an investigation is waiting.
- **At most two attempts for each period:** the first, and one retry. A busy or missing agent does not use one.
- **Counts.** `HM_AGENT_REVIEWS_PER_DAY` (24 by default, 0 to 200) caps how many reviews start in 24 hours. `0` turns reviews off. A review past the cap is skipped as `rate_limited`, and that period gets no review.
- **The worst case is shown.** **Settings > AI agent > Reviews** shows "Worst case of one run". The editor shows "Worst case: $X a month at the current model": one reservation for each run the cadence can have in a month (31 daily, 5 weekly, 1 monthly). It leaves out retries, and it is the money kept back, not what is usually spent.
- **No log search.** A review never uses CloudWatch Logs Insights. Opening a single service can run a CloudWatch metrics query, as the Metrics tab does.

## Next steps

- [Alert notifications](alert-notifications.md#review-messages)
- [AI agent runbook](../operations/runbooks/ai-agent.md)
- [Security model](../security/security-model.md#scheduled-reviews)
- [Configuration reference](../reference/configuration.md#ai-agent)
