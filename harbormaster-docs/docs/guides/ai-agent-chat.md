# AI chat

How to ask the Harbormaster AI chat questions about your ECS estate, what it can and cannot do, and what it saves. For anyone who has been given AI chat, and for admins who decide who gets it.

The chat is read-only. It can look at your estate and explain what it sees. It cannot restart, scale, deploy, sleep, wake or edit anything, and it has no tool that could. Setting it up is an operator task: see [Install the AI agent](../install/ai-agent.md).

## What it can see

- **Only what you may see.** Each question runs with a short-lived, read-only token for you. The chat sees the clusters and services on your tags, and log lines only where you have Logs. An admin's chat has no more reach than the admin's own read access.
- **The same read-only tools as the MCP gateway.** It can read the estate, task definitions, scaling, timelines, metrics, alerts, logs, image security, analytics, networks and storage. See [AI access (MCP)](ai-access-mcp.md#tools) for the list.
- **Environment values are hidden by default.** Environment variable values in task definitions reach the model as `[redacted]` unless an admin lifted that for the model's provider. Values that look like secrets are always redacted, and log text is scrubbed for secret-looking values before it leaves for the provider. If your operator sets `HM_TOKEN_REDACT_ENV=true`, environment values stay redacted whatever an admin lifts. Scrubbing is best effort: it can miss a secret that has an unusual shape. See [Security model](../security/security-model.md#ai-agent).

## Get access

Chat is off for everyone until an admin turns it on for you.

1. An operator sets up the agent. See [Install the AI agent](../install/ai-agent.md).
2. A global admin opens **Settings > AI agent** and chooses the chat model and the monthly allowance.
3. A global admin opens **Settings > Users** and uses **Turn on AI chat** in the user's menu. Global admins can always chat once a model is chosen.

If you do not have access, the **Ask AI** button does not appear.

## Ask a question

1. Select **Ask AI** in the top bar.
2. Type a question of 1 to 8000 characters, for example "Which services in production are unhealthy, and why?". The chat knows which page you are on and uses it as a hint, so "what is wrong here?" works on a service page.
3. The answer streams in. The tools the chat used are shown as chips with the answer.

You may have one question in progress at a time. A second question while one is running is refused with a message to wait, not queued.

Check it worked: ask "who am I and what can I see?". The answer should match **My access**.

## Limits on one question

Each question is a run with limits set by your operator:

- At most 20 steps, 200000 tokens and 5 minutes by default. A step is one model call. The model may ask for at most 8 tool calls in one turn, and extra calls in that turn are refused.
- Every run keeps room for a final answer. When a limit is close, the chat stops gathering data and answers from what it has, and says what is missing.
- A long conversation sends only its newest messages to the model. When earlier messages are left out, the chat says so.
- Tool definitions alone take about 10,000 input tokens on every model call, so even a short question uses real tokens.

If a question is stopped, for example by a restart of Harbormaster, the answer is marked as interrupted. Ask again.

## The monthly allowance

An admin sets a monthly allowance in US dollars for the whole installation. The month is the UTC calendar month. When it is used up, chat answers "This month's AI allowance is used up. Ask an admin to raise it." until the month ends or an admin raises the allowance. An allowance of 0 stops all questions.

The allowance is firm. Before a question starts, Harbormaster reserves its worst case cost, so a question that might overspend is refused, and several questions asked at once cannot overspend together. If a question is interrupted, the run may be charged more than it used. The usage table on the settings page shows each day's cost and tokens.

## What is saved, and for how long

Saved in Harbormaster's database, for each conversation:

- Your messages and the chat's answers.
- The names of the tools used for each answer.
- Whether an answer was interrupted.

Not saved: the data the tools returned. Tool results are never written to the database. They go to the model for that question only.

Conversations are removed after `HM_RETENTION_AGENT_CONVERSATIONS` without activity (30 days by default). You can delete one sooner from the conversation list in the chat. Only you can see your conversations. Admins cannot read them.

Kept separately and longer: a record of each run (who, which model, which tools, tokens and cost, never what was said) and an audit entry for it, kept for `HM_RETENTION_AUDIT_LOG`. See [Audit log](audit-log.md) and [Data retention](../operations/data-retention.md).

What leaves your network: the question text, the conversation history and the tool results for that question go to the model provider your admin chose. See [Security model](../security/security-model.md#ai-agent) for what each provider receives.

## Answers can be wrong

A model can misread data, miss something it was not shown, or state a guess as a fact. Check anything you will act on against the page it came from. The chat says so when a tool failed, a result was cut short or access was refused, but it cannot always know what it missed. Text in logs and task definitions is written by applications, and possibly by an attacker. The chat is told to treat it as data, not instructions, but a model can still be misled by it.

## For admins

- Turn chat on or off for a user in **Settings > Users**. Each change is audited as `user_ai_chat`.
- Choose the model, set the allowance, lift redaction for a provider and read usage in **Settings > AI agent**. Each save is audited as `agent_settings_update`.
- **Test model** makes one small paid call to the chosen model. It counts against the allowance.
- Lift redaction only for a provider you accept receiving environment values. Keep it on for any provider outside your own AWS account. An operator who sets `HM_TOKEN_REDACT_ENV=true` forces full redaction for every provider, and an admin's lift then has no effect.

## Next steps

- [Install the AI agent](../install/ai-agent.md)
- [AI agent runbook](../operations/runbooks/ai-agent.md)
- [Users, tags and permissions](users-tags-permissions.md)
