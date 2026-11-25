# Automatic AI investigations

How Harbormaster can investigate a failure alert on its own with the AI agent, what you see afterwards, what it costs and where it can be wrong. For admins who switch it on and for anyone who reads an investigation.

An investigation is a short, read-only run of the AI agent that starts when a notification route announces an alert. It reads your estate, then writes a summary of what looks wrong, the likely cause and what to check next. Setting up the agent is an operator task: see [Install the AI agent](../install/ai-agent.md). The chat is a separate feature: see [AI chat](ai-agent-chat.md).

## What you need to know first

- **The alert message is never held back.** The Opened message is planned and sent on its own, as before. The investigation starts afterwards and never delays it. If the agent is down, slow or refuses, the alert still arrives and nothing else changes.
- **The diagnosis can be wrong.** A model reads logs, task definitions and metrics and states a conclusion. It can misread data, miss something it was not shown, or state a guess as a fact. Check it before you act. Every place that shows a diagnosis says so.
- **The summary is a conclusion. The evidence needs permission.** Anyone who can see the service reads the summary. The log lines and data behind it are shown only to people whose own access covers them (see [Who sees what](#who-sees-what)).
- **The check on the summary is best effort.** Harbormaster tries to stop a summary from copying text out of the logs, but it cannot catch everything (see [What the summary check does and does not catch](#what-the-summary-check-does-and-does-not-catch)).
- **It spends from the same monthly allowance as the chat.** See [Cost and limits](#cost-and-limits).

## What it does

1. A route with **Investigate with AI** on sends the Opened message for an alert. Harbormaster never waits for the agent to do this.
2. Shortly after, Harbormaster records an investigation request for the alert. Alerts for the same service (or the same cluster or account entry) that open within `HM_AGENT_INVESTIGATION_WINDOW` (15 minutes by default) share one investigation. Two routes watching the same service share it too.
3. The coordinator starts the investigation when a slot is free. The agent runs as its own identity, the [agent identity](#the-agent-identity), with the `investigator` role and the model an admin chose for investigations. It can use the same read-only tools as the chat. It cannot change anything.
4. The agent finishes by submitting a summary and up to 12 pieces of evidence it cites. Harbormaster checks them again and stores them.
5. For each route with the switch on (except PagerDuty), Harbormaster sends a second message, the **Diagnosis**, if the alert is still open. See [Alert notifications](alert-notifications.md#diagnosis-messages).

An alert that has no service, such as an unreachable account, starts no investigation: there is nothing in the estate to read. Turning the switch on does not investigate alerts that were already open for hours: a request is recorded only for an Opened message planned within the window.

### Group alerts

A cluster or account entry that aggregates several services is investigated as one subject. Harbormaster records up to 50 services of the group as they were when it was requested. For a cluster or account alert, Harbormaster does not send the alert's own title and text. It writes them anew: the place (the cluster name, or the account id and region, never a display name), the counts from the alert (for example "2 of 5 services failing", which may include services the identity cannot see, or "unreachable"), and a list of the services that were failing when the alert was raised and that the agent identity may View now, at most ten, then "and K more". The AWS error text in the alert is not sent. The subject also carries the ids of up to 20 of those visible services; an id contains the service's name. The names of services the identity may not View are never sent. An account entry that has no region is not investigated (`not_visible`). The subject of an account alert has no cluster field, only the account id and region; a cluster alert's subject has the cluster. If the stored title does not end in the counts ("2 of 5 services failing" or "unreachable"), the title Harbormaster writes is "<place>: services failing". A service whose id is longer than 512 bytes is skipped: it is neither listed nor sent.

## Turn it on

Investigations need the AI agent (`HM_AGENT_URL`), an allowance, a model for investigations, an access grant for the agent identity and a route with the switch on.

1. Set up the agent and a monthly allowance. See [Install the AI agent](../install/ai-agent.md).
2. A global admin opens **Settings > AI agent** and chooses **Model for investigations**. **Test model** makes one small paid call. Without a model, investigations are skipped as `not_configured`.
3. In **Agent identity** on the same page, choose what the agent may read for each tag: **No access**, **View**, or **View and logs**. A new identity has no access at all, so every investigation is skipped as `not_visible` until you grant some. Grant View on the tags you want investigated. Grant logs only where you accept log text going to the model provider.
4. Open **Settings > Notifications**, edit a route and tick **Investigate with AI**. The editor warns when no model is set and shows how many of the route's services the agent cannot see.

Check it worked: when an alert opens on a service the agent can see, **Needs attention** on the Overview shows **Investigating**, then **Diagnosis ready**. Select it to read the investigation. Settings, AI agent lists every investigation with its state and cost.

To turn it off, clear the switch on the routes, unset `HM_AGENT_URL`, or set `HM_AGENT_INVESTIGATIONS_PER_HOUR=0`. Unsetting `HM_AGENT_URL` or setting the cap to `0` does not clear the switches: they stay on, and when the agent is set again the routes investigate again, and spend, at once. See the [runbook](../operations/runbooks/ai-agent.md#11-turn-investigations-off).

## What you see

- **Overview.** An alert with an investigation shows its state as a link: **Investigating**, **Diagnosis ready**, or one of the sentences below.
- **Service timeline.** Investigations appear as markers on the timeline. The investigation's service link opens `/services/<service-id>/timeline?investigation=<id>`. A Diagnosis message links to `/investigations/<id>`. The Opened event sent to PagerDuty for a route with the switch on links to `/investigations/incident/<incident id>`, which shows that alert's investigation once it exists.
- **Investigation page.** `/investigations/<id>` shows what it is about, the alert, the state, the summary and the evidence. The summary is plain text: no links, formatting or images. It carries the caption "Written by AI from what its identity may read. Check it before acting."
- **Settings > AI agent.** Admins see every investigation, newest first, with state, subject, time and cost. After a retry, the cost is that of the last attempt only; the month's total under usage is complete.

### States

| State shown | Meaning |
| --- | --- |
| Investigating | Waiting for a slot, or running. |
| Diagnosis ready | Done. The summary and evidence are stored. |
| The investigation did not finish. | It failed. The Settings list gives the reason. |
| Not investigated: this month's AI allowance is used up. | Reason `budget`. |
| Not investigated: the agent cannot see this service. | Reason `not_visible`. |
| Not investigated: no model is set up for investigations. | Reason `not_configured`. |
| Not investigated: too many alerts in the last hour. | Reason `rate_limited`. |
| Not investigated: it waited too long. | Reason `expired`. |

The runbook lists every reason and what to do: [States and reasons](../operations/runbooks/ai-agent.md#9-investigation-states-and-reasons).

## Who sees what

Reading is checked on every request against your current permissions, not the permissions you had when the investigation ran.

- **The summary.** You can read an investigation of a service when you can View that service. A cluster or account investigation is for admins and for readers who can View every service listed when it was requested; a list at the 50-service cap, or one that was not recorded, is for admins only. A service that no longer exists is for admins only.
- **The evidence.** Each item is for one service and one action. You need View on the service to see an item, and Logs as well for a log item. Items you may not read are not described: the page says only "Some evidence is not shown because you do not have permission to read it." It names no service and no tool.
- **If you lose Logs after the investigation,** the next time you open it the log evidence and its notes are hidden.
- **Access tokens and the MCP gateway cannot read investigations.** The routes are for signed-in people only. The agent never reads stored investigations either.
- **The alert's investigation link** is added to the alert list for signed-in people only, not for token callers.

### Evidence is limited

The model does not write evidence. It points at numbered pieces of tool output it was shown (`e1`, `e2` and so on), with a one-line note. Harbormaster takes the excerpt from the tool result itself and labels it with the service and the action (View or Logs) from a fixed rule, in the agent and again in Harbormaster. A piece it cannot label is dropped. At most 12 items are stored, each at most 1500 bytes.

Tools whose answers cannot be tied to one service (estate lists, clusters, networks, storage, analytics, image security, and a few others) cannot be cited. The agent may still read them and reason from them, so a summary can rest on something you cannot open.

## What the summary check does and does not catch

Harbormaster checks every summary and note before it is stored, and again before every message is sent.

It replaces the whole text with a fixed sentence ("The investigation finished. Open it in Harbormaster to read the findings.") when the summary:

- is empty, longer than 1200 characters, or not valid text;
- contains a character a reader cannot see or that reorders text: control characters other than line feed and tab, every Unicode format character (zero-width, bidi and tag characters, including the zero-width joiner), variation selectors (including the one that follows many emoji) and fillers. This also withholds some summaries in Persian and Sinhala and some emoji. Such a summary is refused, never shown with the character removed. In stored evidence excerpts, control, bidi and tag characters become spaces instead;
- contains image markup;
- is changed by the secret scrubber, which means it looks like it contains a secret;
- repeats a run of text the agent read in that run, ignoring case and punctuation: 32 or more characters of a log message, an environment variable value, or a command or entry point of a task definition, or 80 or more characters of anything else it read (a status sentence, an image reference). The names of the investigated service and cluster may be repeated.

A note that fails the same checks becomes "See the excerpt." Links to anywhere other than your own Harbormaster address are replaced with `[link removed]` when a message is sent. The unit that is replaced is a whole word between spaces, so in a script written without spaces (Chinese, Japanese, Thai) a link takes the text it touches with it, up to the next space: possibly a whole sentence. Over-removal is accepted: some harmless link-like text may be removed too.

This is best effort:

- A model can paraphrase a secret or a sentence from a log instead of copying it. Paraphrase is not caught.
- The copy check compares text the agent read in that run, up to 2 MiB. If the agent read more than that, the check fails closed and the summary is replaced.
- The scrubber uses patterns, so a secret with an unusual shape can pass. See [Security model](../security/security-model.md#what-is-redacted-and-what-leaves-the-network).
- A bare host name without a path, such as `example.com`, may survive link removal.

Treat the summary as text written by a model that has read your logs.

## The agent identity

The investigator does not borrow anyone's access. It runs as the agent identity, a built-in non-human user, named `harbormaster-agent` (or the first free `harbormaster-agent-1` to `-9`). It is created when the agent is first on. Names that start with `harbormaster-agent` are reserved for the identity: you cannot create a user with such a name.

- Its access is the tags an admin chose in **Settings > AI agent**, View or View and logs, and nothing else. It starts with none.
- It cannot sign in by password or SSO, hold a session, personal or OAuth token, or be an admin. It cannot Operate, Deploy, Edit, open a terminal or accept risk. It is not in the Users list and has no AI chat flag.
- Its requests are limited to `HM_AGENT_IDENTITY_RATE_PER_SECOND` (5 a second, with a burst of 60 times that) and its log searches to `HM_AGENT_IDENTITY_LOG_SEARCH_MAX_GB` (2 GiB a search) and `HM_AGENT_IDENTITY_LOG_SEARCH_DAILY_GB` (20 GiB a UTC day). Log searches run in your AWS accounts and use CloudWatch Logs Insights, which AWS bills by data scanned. The identity is one user, so it has one log-search slot, shared by all its runs: a search started while another investigation's search is running is refused, and that run carries on without it.
- Changes to its access are audited as `agent_identity_update`. Each investigation run is audited as `agent_run` with the identity as actor.

See [Permissions reference](../reference/permissions.md#the-agent-identity).

## What leaves your network

For each investigation, the following go to the provider of the investigator model:

- the instructions, the subject (the account, region, cluster and service of a service alert; for a group alert, its account id and region (and the cluster, for a cluster alert), and the ids, which contain the names, of up to 20 services the identity may View), and the alert's kind and opening time. For a service alert, its title and text go as stored. For a group alert, they are the ones Harbormaster writes anew (see [Group alerts](#group-alerts)). All of it passes secret scrubbing and a length cut;
- the tool definitions, and the scrubbed results of the tools the agent called, including log lines if the identity has Logs.

Environment values in task definitions are redacted unless an admin lifted that for the provider, as for the chat. `HM_TOKEN_REDACT_ENV=true` overrides the lift. Secret-looking values are always redacted, best effort. The provider keeps what it keeps under its own terms. Because nobody asks for these runs, check the provider terms before you set the investigator model, and prefer a provider inside your own AWS account (Amazon Bedrock) for sensitive estates.

A diagnosis message sends the summary to the route's destination (Slack, Telegram, a webhook or email). That is a second place the model's text goes. See [Alert notifications](alert-notifications.md#diagnosis-messages).

## What is stored, and for how long

Stored in Harbormaster's database:

- The investigation: the subject, the alert snapshot (kind, title, text, opening time), the state and reason, the summary and the run's identifiers.
- The evidence: up to 12 excerpts of tool output per investigation, at most 1500 bytes each, scrubbed in the agent and again in Harbormaster, with the notes, the tool name and the service.
- The run record and an `agent_run` audit entry: model, tools, tokens and cost, never text.

This changes one rule of the chat. Tool results of the chat are never stored. For investigations, the cited excerpts are, and they may contain log text. Stored evidence is never sent back to a model.

Investigations are deleted `HM_RETENTION_AGENT_RUNS` after they finish (90 days by default). A running or waiting one, and one that finished less than 48 hours ago, is never deleted. Alert incidents are deleted earlier (`HM_RETENTION_NOTIFICATION_LOG`, 30 days), so an investigation can outlive its alert: it stays readable, but the link from the alert is gone. See [Data retention](../operations/data-retention.md).

## Cost and limits

- **One allowance.** Investigations spend from the monthly allowance that the chat uses. When it is used up, investigations are skipped as `budget` and the chat refuses questions too. A storm of alerts can use up an allowance that you meant for the chat. Watch spend, and see the [runbook](../operations/runbooks/ai-agent.md#10-an-alert-storm).
- **Worst case is reserved.** Before a run starts, Harbormaster reserves its worst case (the run's token limit at the model's higher price). A run that might overspend is refused, and runs that start together cannot overspend together. An interrupted run can be charged more than it used.
- **At most two runs per investigation:** the first, and one retry.
- **Counts.** `HM_AGENT_INVESTIGATIONS_PER_HOUR` (20) caps starts in an hour, `HM_AGENT_INVESTIGATIONS_CONCURRENT` (2) caps parallel runs, and a request that waits longer than `HM_AGENT_INVESTIGATION_MAX_WAIT` (15 minutes) before its first start is skipped as `expired`. The hourly cap is checked once, before an investigation's first start: a retry of an investigation that already started is not counted again and is not held back by it (only `0` stops it). The hourly cap can overshoot by up to the number of parallel runs. These are counts, not spending limits: the allowance is the spending limit.
- **A run has the agent's per-run limits** (steps, tokens and time, set on the agent service). A run that ends without a summary fails as `limit_reached`.
- **Log search** by the identity uses CloudWatch Logs Insights in your accounts, within the limits above.

## Next steps

- [Alert notifications](alert-notifications.md)
- [AI agent runbook](../operations/runbooks/ai-agent.md)
- [Security model](../security/security-model.md#automatic-investigations)
- [Configuration reference](../reference/configuration.md#ai-agent)
