# Scheduled AI reviews

How Harbormaster can run the AI agent on a schedule to write a short review of your costs, image security findings, reliability or capacity, or of a subject an admin describes in words, and send it to notification routes. For admins who set reviews up and for anyone who reads their results.

A review is a read-only run of the AI agent. An admin gives it a type, a scope, a schedule and the routes that get the result. A type of `custom` also carries instructions the admin wrote: see [Custom reviews](#custom-reviews). At each scheduled time the agent reads the data Harbormaster already holds and writes a summary. Setting up the agent is an operator task: see [Install the AI agent](../install/ai-agent.md). The chat and the investigations of alerts are separate features: see [AI chat](ai-agent-chat.md) and [Automatic AI investigations](ai-agent-investigator.md).

## What you need to know first

- **A review can be wrong.** A model reads numbers and lists and states a conclusion. It can misread them, miss something it was not shown, or state a guess as a fact. Every place that shows a review says it was written by AI. Check it before you act.
- **The summary is a conclusion. The evidence needs permission.** A summary is shown to people who can see everything the review covered. Each piece of evidence behind it is shown only to people whose own access covers that service (see [Who sees what](#who-sees-what)).
- **A review reads no logs.** The agent is not offered the log tools in a review, so a review never searches or reads log lines and never uses the agent identity's log-search budget.
- **A review reads data as fresh as the last poll.** It reads what Harbormaster stored from its AWS polls and daily rollups, not live AWS state. A change made a minute ago may not be in it. See [Polling cadence](../operations/aws-costs.md#polling-cadence).
- **One run covers a limited amount.** See [What one run can cover](#what-one-run-can-cover).
- **It spends from the same monthly allowance as the chat.** Reviews are skipped first when the allowance runs short. See [Cost and limits](#cost-and-limits).
- **A review changes nothing.** Like every run of the agent, it has no tool that writes to AWS or to Harbormaster.

## What each type reads

All four built-in types have the same tools, the same identity and the same checks. A fifth type, `custom`, is off unless the operator turns it on, and has no fixed parts: see [Custom reviews](#custom-reviews). The type decides only what the agent is asked to read first and how it is asked to write the summary. The summary has three parts, in a fixed order.

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

## Custom reviews

A custom review is a review whose subject an admin writes in words. It is the fifth type, `custom` (label Custom). It has the same schedule, scope, routes, tools, identity, limits and checks as every other review. Only the type's instructions differ: the admin writes them, and the agent is told to follow them.

Custom reviews are off until the operator sets `HM_AGENT_CUSTOM_REVIEWS=true` on every Harbormaster service (see [Configuration](../reference/configuration.md#ai-agent)). While it is off, **Custom** is not offered, a custom review cannot be created or changed, and none is recorded or run. No result of a custom review is sent either, a manual run included; results stay readable. For the switch and a rollback, see the [runbook](../operations/runbooks/ai-agent.md#18-custom-reviews-switch-and-rollback).

To add one, open **Settings > AI agent > Reviews**, select **Add review**, choose **Custom** and write the instructions. The type of a review cannot be changed to or from `custom` after it is created. To change it, make a new review.

### What instructions can and cannot do

Instructions can choose what the review looks at and how the summary is laid out: "compare the three most expensive tags, then list anything that grew since last week, as a short list".

Instructions cannot:

- add tools or log access. A custom review has the 35 read-only tools and 60 calls of every review, and no log tools;
- widen what the agent sees. The agent identity's access is still the only boundary;
- change who the agent runs as;
- change the form or the checks of the result. The result is still one summary of at most 1200 characters and up to 12 cited items, and every check of [the summary check](#what-the-summary-check-does-and-does-not-catch) still applies;
- decide who reads the result or where it is sent.

The agent is told to leave a request to do any of these undone and to say so in one sentence. A model can still follow a request badly, or be talked round by text it reads. See the [security model](../security/security-model.md#custom-reviews) for what holds when it does.

### Where the text goes

The instructions are sent to the model provider chosen for reviews, in every call of the run. They are stored in Harbormaster's database. They are also written, old and new, into the audit log on every change, and shown to admins beside each past result as "Instructions used". They are never shown to people who are not admins, and they are never sent to a route. Do not write anything in them that you would not send to your model provider or keep in the audit log for a year.

### What is refused

A save is refused, and nothing is stored or audited, when the instructions:

| Problem | Code | What to do |
| --- | --- | --- |
| Are empty | `instructions_required` | Write what the review should look at. |
| Hold more than 2000 characters or 8000 bytes | `instructions_too_long` | Shorten them. The limit counts code points after cleaning, so a letter with an accent is one character, but an emoji can be two or more. Emoji that use a variation selector or a joiner are refused as hidden characters. The 8000-byte limit counts the bytes of the cleaned text. |
| Hold a NUL character | `instructions_not_text` | Type them again. |
| Hold hidden characters: controls other than line feed and tab, zero-width and other invisible format characters, variation selectors | `instructions_hidden` | Paste them as plain text. Text in a script that needs a zero-width joiner cannot be saved. |
| Look like a secret: a key, a token, a password, or a long random-looking name | `instructions_secret` | Remove it. To name a service whose name looks random, use its tag or the scope. |
| Are given to a built-in type | `instructions_not_allowed` | Only a custom review takes instructions. |

The secret check is best effort. It looks for known shapes, so a secret in an unusual form, or a JSON value in the middle of a line, can pass it. The model provider would then see it. The agent removes what it can before sending, but do not rely on that, and never paste a credential into instructions.

Bytes that are not valid text in a request are stored as replacement characters. The editor shows the problem under the field before you save, without a model call.

### How the text is cleaned

Before it is checked, stored, hashed and sent, the text is cleaned in one way: a carriage return and a line feed, a lone carriage return, and the Unicode line and paragraph separators (U+2028 and U+2029) each become a line feed, and white space at both ends is removed. Nothing else changes. The limits count the cleaned text.

### What the result looks like

A custom summary has no fixed parts. It is the model's own words.

- **A summary that repeats the instructions is replaced.** If it repeats 80 or more characters in a row of the request (ignoring case and punctuation), it is replaced by the fixed sentence "The review finished. Open it in Harbormaster to read the findings." A note that does so is replaced by "See the excerpt." A stretch that lies wholly inside one long word, such as a service name, is not counted. The agent checks this, and Harbormaster checks it again before it stores the result.
- **The other copy check still applies, by class.** A summary that repeats 32 or more characters of unlisted text the run read (a log message, an environment variable value, a command), or 80 or more characters of a listed value, is replaced, as for every review; see Copying [below](#what-the-summary-check-does-and-does-not-catch). A tag and an account label are listed values, so a summary that names one is replaced only at 80 characters or more, not 32. Names of services and clusters are exempt as described there. A custom review that asks for a report for each tag or account is still likelier to hit this than the built-in types.
- **Check the outcome with a test run** before a route gets the result. A test run appears under **Reviews** and in the review's past results, for admins only.

### Test runs

**Test run** on a saved review starts one run that is stored and not sent. It is for any review type, not only custom. It has the same cost as any run: it reserves what a run reserves, counts in `HM_AGENT_REVIEWS_PER_DAY`, and is refused while that review already has a run waiting or running. It is audited as `agent_review_run` with `test`.

- Only admins can read a test run, whatever tags it covered, and it does not appear for anyone else in **Reviews**. This holds under the code of the earlier release too.
- A test run is never sent to a route. It is marked as already planned for delivery when it is made, so no release plans a message for it.
- Only a saved review can be run. The text is saved, validated and audited first: there is no way to run a draft. The preview in the editor checks the text and counts services and cost, and makes no model call.
- The API call is `POST /api/agent/reviews/{id}/run` with `{"deliver": false}`. See the [API reference](../reference/api.md#ai-agent-reviews).

### Edits while a run waits

A run uses the text that was saved when it started, and keeps a copy. A retry of the same run uses the same copy, even if you edit the review in between. An edit before the run starts applies to it. An edit after it applies to the next run. Editing the instructions raises the review's version but does not move its schedule.

If the text no longer passes the checks when a run is about to start (for example after an upgrade made them stricter), nothing is sent to the model and the run is skipped as `instructions`. Save the text again to fix it.

### Cost

A custom review reserves what any review reserves: the run's token limit at the reviewer model's higher price. The request is at most 8000 bytes and is sent with every model call of the run, so a long request makes each run cost somewhat more, within that same reservation. A test run is a run: it is paid for.

A model that declines the request ends as any reviewer that submits nothing does: one reminder, then the run fails as `no_result`, and nothing is sent.

### Turning custom reviews off

- **Disable a custom review before you switch the operator's setting off.** While `HM_AGENT_CUSTOM_REVIEWS` is off, a custom review cannot be edited or switched off, only deleted. One that was on is still on in the database: it becomes due again when the setting returns. To keep one for later, switch it off first, then switch the setting off.
- **Delete or disable custom reviews before a roll back.** An older release cannot update a custom review. It lists it with a note that its type is unknown, and it can delete it.

See the [runbook](../operations/runbooks/ai-agent.md#16-reviews-upgrade-and-rollback).

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

- **Reviews.** The sidebar entry **Reviews** appears while reviews are on. It lists the results you may read, newest first, with name, type, period, the first line of the summary and the state. A test result is listed for admins only, marked as a test run.
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
| Not run: the review was turned off. | Reason `disabled`. The review was switched off or deleted before the run started, or before its retry. A manual run of a switched-off review is not skipped for this; a manual run of a deleted review is. A custom review is also skipped as `disabled` while the operator's switch for custom reviews is off. |
| Not run: its instructions no longer pass the checks. | Reason `instructions`. Custom reviews only. See [Custom reviews](#custom-reviews). |

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
- **Instructions and test runs.** Only admins read the instructions of a custom review and the copy kept with each result. A test run is read by admins only.

See [Permissions](../reference/permissions.md#who-reads-a-review).

### Evidence is limited

The model does not write evidence. It points at numbered pieces of tool output it was shown, with a one-line note. Harbormaster takes the excerpt from the tool result and labels it with the service and the action from a fixed rule, in the agent and again in Harbormaster. A piece it cannot label is dropped. At most 12 items are stored, each at most 1500 bytes. The estate-wide totals of an analytics answer cannot be cited, so a summary can rest on something you cannot open.

## What the summary check does and does not catch

Harbormaster checks every summary and note before it is stored, and again before every message is sent. The checks are those of [investigations](ai-agent-investigator.md#what-the-summary-check-does-and-does-not-catch). A summary that is empty, too long, has hidden characters or image markup, or is changed by the secret scrubber is replaced whole by a fixed sentence. For a review it is "The review finished. Open it in Harbormaster to read the findings." The copy rule has two lengths and one exemption.

- **Copying.** The summary is replaced when it repeats, ignoring case and punctuation, 32 or more characters of a log message, an environment variable value, a command or entry point of a task definition, or anything Harbormaster has not listed (error texts, labels attached to a container, log options beyond the standard ones, an answer cut by the read cap), or 80 or more characters of the other values it lists (a status sentence, an image reference, a label, a name). A summary may therefore hold up to 79 characters in a row of one listed value. A review reads no log messages. For a custom review the request has its own rule: see [Custom reviews](#what-the-result-looks-like).
- **Names are exempt.** A review has to name services and clusters, and a name can be longer than 32 characters. A name is not counted as copied text when it was decoded from an id of a service or cluster in any answer the agent read. A name is one word of letters, digits, hyphens and underscores, up to 128 characters. This applies to the summary, not to notes.
- **What the exemption does not cover.** A name with spaces, dots or other characters. A stretch of text that takes in a name and the words beside it, which is checked in full. A name the agent did not read in an id. Anything in a note. Text a person wrote, such as a tag name or a review name, is not exempt unless it is also part of a service or cluster id the agent read.

The check is best effort, as for investigations: paraphrase is not caught, a bare host name may stay, and the scrubber uses patterns. Treat a review as text written by a model that has read your estate.

## What leaves your network

For each run, the following go to the provider of the reviewer model:

- the instructions of the type (for a custom review, the text the admin wrote, in an envelope of its own), the type's label, the range, the period (for example "Week 48, 2026"), the number of services in scope and the scope entries (kind, ids, region and the names of tags and clusters), each scrubbed and cut. The review's own name is not sent;
- the tool definitions, and the scrubbed results of the tools the agent called: analytics and security rows, names and sizes of services and clusters, task definitions with environment values redacted, scaling limits and the like. No log line.

Environment values in task definitions are redacted unless an admin lifted that for the provider. `HM_TOKEN_REDACT_ENV=true` overrides the lift. Secret-looking values are always redacted, best effort. Because nobody asks for these runs, check the provider terms before you set the reviewer model, and prefer a provider inside your own AWS account (Amazon Bedrock) for sensitive estates.

A review message sends the summary to each route's destination. That is a second place the model's text goes. See [Alert notifications](alert-notifications.md#review-messages).

## What is stored, and for how long

Stored in Harbormaster's database:

- The review: name, type, schedule, scope, routes and whether it is on. For a custom review, also its instructions.
- Each run: the review's name and type at that time, the period, the scope label, the number of services, the tags covered, the state and reason, and the summary. A custom run also holds a copy of the instructions it used, and a test run is marked.
- The evidence: up to 12 excerpts of tool output for each run, at most 1500 bytes each, scrubbed in the agent and again in Harbormaster, with the notes, the tool name and the service.
- The run record and an `agent_run` audit entry: model, tools, tokens and cost, never text. An admin's changes to reviews are audited too. For a custom review the audit entry holds the instructions before and after, in full, and their SHA-256. The `agent_run` entry holds the hash only. See [Audit log](audit-log.md).
- One row for each message in the notification delivery log. It holds the review's name, not the summary: the message is built from the stored result when it is sent.

Stored evidence is never sent back to a model.

Finished results are deleted `HM_RETENTION_AGENT_RUNS` after they finish (90 days by default), and the copy of the instructions with them. The audit log keeps the text for `HM_RETENTION_AUDIT_LOG` (365 days by default). A waiting or running one, and one that finished less than 48 hours ago, is never deleted. The newest scheduled run of each review stays however old, because it is what stops a period that already ran from running again. Rows in the delivery log go after `HM_RETENTION_NOTIFICATION_LOG` (30 days). See [Data retention](../operations/data-retention.md).

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
- [Custom reviews in the security model](../security/security-model.md#custom-reviews)
- [Configuration reference](../reference/configuration.md#ai-agent)
