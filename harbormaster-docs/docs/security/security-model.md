# Security model

This page describes the controls Harbormaster applies to sign-in, authorization, secrets, network exposure and the terminal. It is for security reviewers and platform engineers who need to know what Harbormaster protects and how.

Harbormaster can deploy to and open shells in production tasks in every connected account. Treat its task role, its database and its global admin accounts as highly privileged.

## Authentication

### Local passwords

- Passwords are at least 12 characters and are stored as argon2id hashes with a random salt per password.
- Sign-in attempts are throttled per user and per client IP (`HM_LOGIN_MAX_PER_USER`, `HM_LOGIN_MAX_PER_IP` within `HM_LOGIN_WINDOW`).
- The first global admin is created with a one-time setup token, a bootstrap secret in Secrets Manager, or the `create-admin` command. See [First run](../install/first-run.md).

### Single sign-on (SAML 2.0)

- Harbormaster accepts only assertions signed by a configured IdP certificate and addressed to this Harbormaster. Unsigned responses and responses to sign-ins Harbormaster did not start are rejected.
- Each sign-in request is single use and expires after `HM_SAML_REQUEST_TTL`. Assertion and response IDs are recorded, so a captured response cannot be replayed.
- A binding cookie must match the request, so a response that arrives in another browser is refused.
- The service-provider private key is stored encrypted in the database. SAML messages, certificate PEM and private keys are never written to logs.
- While SSO is on, only break-glass admin accounts can sign in with a password.

Setup steps are in [Single sign-on](../install/single-sign-on.md).

### Sessions

- The session cookie (`hm_session`) is `HttpOnly`, `SameSite=Lax` and `Secure` by default (`HM_COOKIE_SECURE`).
- Sessions end after `HM_SESSION_IDLE` of inactivity (default 12 hours) or `HM_SESSION_ABSOLUTE` in total (default 7 days).
- Every state-changing request carries a per-session CSRF token in the `X-CSRF-Token` header.
- `HM_SESSION_KEY` is mixed into stored session id hashes.
- A password change or reset ends the user's access tokens (see below). <!-- TODO(docs): verify that a password change also ends other browser sessions -->

### Access tokens and the MCP gateway

Access tokens are off until `HM_API_TOKENS=true`. When they are off, token routes answer 404 and an `Authorization` header is ignored.

- A token is an opaque secret that starts with `hmp_`. It is shown once. Harbormaster stores only a hash and compares it in constant time.
- A token is read-only. It sees what its user sees and calls only an explicit list of read routes. Every other route answers `403 token_not_allowed`, an admin's token too. A token cannot create, list or revoke tokens.
- Lifetimes are capped by `HM_TOKEN_MAX_TTL` (90 days by default). Users linked to SSO and all global admins are capped by `HM_TOKEN_MAX_TTL_SSO` (7 days by default). Each user holds at most `HM_TOKEN_MAX_PER_USER` tokens.
- Tokens end where sessions end: on a password change or reset, when global admin is granted or removed, and when SSO is unlinked or turned off. Admins can revoke any user's token.
- Token calls are rate limited per user, and token log searches have a daily scan budget.
- `HM_TOKEN_REDACT_ENV=true` replaces environment values with `[redacted]` in token answers.

The optional OAuth 2.1 server (`HM_OAUTH`) lets MCP connectors obtain tokens after a consent page. It accepts only allowlisted client metadata hosts and redirect URIs. See [Access tokens](../guides/access-tokens.md) and [AI access (MCP)](../guides/ai-access-mcp.md).

## Authorization

Every API endpoint and every WebSocket checks permission on the server. Nothing depends on the browser hiding a control.

- Permissions are granted on tags. A user holds a permission set on each tag: View, Logs, Operate, Deploy, Edit, Terminal, and Accept risk (which no preset includes). Global admins hold all of them.
- List endpoints filter results by what the caller may see. The IDs of resources the caller cannot see answer 404, not 403, so their existence is not disclosed.
- The terminal and live-log WebSockets check the session cookie and the request `Origin` before the upgrade (same host or the configured base URL's host; anything else gets 403), then check the specific permission on the service.
- Notification routes and account administration are global-admin only.
- Task definition fields that can reach the container host are admin-only. Non-admins can keep or remove them relative to the deployed revision but cannot add or change them. A rejected draft returns 422 and registers nothing. See [Users, tags and permissions](../guides/users-tags-permissions.md) and the [permissions reference](../reference/permissions.md).
- The image scan gate before deploy is enforced on the server, not in the browser.

## Audit

Sign-ins, permission and user changes, deploys, token creation and revocation, log searches, exec sessions and other changes are written to the audit log. Entries name the actor and the action, and carry field names or fingerprints rather than secret values. The audit log is kept for `HM_RETENTION_AUDIT_LOG` (one year by default). See [Audit log](../guides/audit-log.md).

## Secrets and encryption

- The database password and `HM_SESSION_KEY` come from Secrets Manager and are never logged.
- Small secrets that Harbormaster must store (SSO signing key, notification route secrets) are sealed with AES-256-GCM under a key derived from `HM_SESSION_KEY`. Each sealed value is bound to its purpose, so a value sealed for one purpose cannot be opened for another. Sealed values are write-only: the API never returns them.
- Environment variable values from task definitions are shown to users with View. Values from `secrets` (SSM or Secrets Manager references) are shown as references only and are never resolved. Harbormaster never reads secret values, so the access role has no secret-read permission.
- The database can be encrypted with a customer-managed KMS key (`db_kms_key_id`).
- Access tokens are stored as hashes. Passwords are stored as argon2id hashes.

## Network exposure

- The Terraform module puts the load balancer in private subnets (`internal = true`, recommended) or creates an internet-facing one. `allowed_cidrs` is required and limits who can reach the ALB on port 443.
- Deletion protection is on for the ALB by default.
- The server sends security headers: a Content Security Policy (self and fonts), HSTS and `X-Frame-Options: DENY`.
- The MCP gateway is the only component meant to be reachable from outside. It runs behind its own ALB with an AWS WAF web ACL that applies a per-IP rate limit and, when OAuth paths are enabled, a separate rate limit on `/oauth/`. The gateway holds no credentials and forwards each call with the caller's own token. See [MCP gateway](../install/mcp-gateway.md).
- Outbound requests to destinations an operator or remote party chooses (notification webhooks, SSO metadata, registry lookups) go through a dial-time address guard. It checks the address actually connected to, so DNS rebinding, redirects and IP literals cannot reach internal addresses. Notifications go only to `https` destinations on public addresses, plus ranges you list in `HM_NOTIFY_ALLOWED_PRIVATE_CIDRS`. Email goes to the SMTP host the operator sets in `HM_SMTP_HOST`.
- The server image is distroless, runs as a non-root user and has a read-only root filesystem and a health check.

## Tenancy

Harbormaster does not create separate tenants. It separates what users can see and do with tags. A service or cluster carries a tag, and a user's permission set on that tag decides what appears and what actions are allowed. A service with no tag is treated as production for suggestions and by default tag rules. <!-- TODO(docs): verify default-tag fallback wording --> See [Users, tags and permissions](../guides/users-tags-permissions.md).

## Terminal controls

- A user needs the Terminal permission on the service's tag.
- ECS Exec must be on for the service, and the task role needs the `ssmmessages` permissions. Harbormaster does not grant these.
- Limits apply per session and per user: `HM_EXEC_IDLE_TIMEOUT` (20 minutes by default), `HM_EXEC_MAX_DURATION` (4 hours) and `HM_EXEC_MAX_SESSIONS_PER_USER` (3).
- Harbormaster calls `ssm:TerminateSession` whenever a session ends, for any reason.
- The operations log records who opened a session, the task, container and command, and how long it lasted. Keystrokes and output are not recorded. Turn on Session Manager logging if you need a transcript.

See [Terminal](../guides/terminal.md).

## Image scanning

The scanner task holds only the `harbormaster-scan` role and its bucket. It runs under its own execution role and has no database URL, session key or `harbormaster-access` role. Everything it writes back is validated against fixed size and shape limits before use. See [Image security](../guides/image-security.md).

## AI agent

The AI agent is off unless `HM_AGENT_URL` is set. With it unset there is no agent route (404), no setting, no button, and a run token does not authenticate. See [AI chat](../guides/ai-agent-chat.md) and [Install the AI agent](../install/ai-agent.md).

### What the agent can reach

- **Read-only.** The agent changes nothing. It has no tool, route or code path that writes to AWS or to Harbormaster's data. Its tools are the same read-only table as the MCP gateway's, and each tool call is one read request to Harbormaster.
- **Only as the asking user (chat).** Each question gets a run token for that user, tied to one run. It is read-only like an access token: only the routes tokens may call, only View and Logs, an admin's included. It expires after `HM_AGENT_RUN_TOKEN_TTL` (15 minutes by default) and ends with the user's sessions. The public MCP gateway refuses it.
- **No estate code in the agent.** The agent service imports no database, cache, SAML or AWS estate code. Its only AWS SDK client is Bedrock runtime (plus the AWS credential chain, whose STS and SSO clients are linked through it and follow `AWS_ENDPOINT_URL_*`). It has no database URL, session key or access role.
- **Internal load balancers only.** The Terraform module refuses to set `HM_AGENT_URL` unless Harbormaster's load balancer is internal, so run tokens are never accepted on a public one. The application itself does not check this: if you run it without the module, keeping the load balancer internal is up to you. The agent has an internal load balancer of its own that admits only Harbormaster's web tasks.
- **Keys stay in the agent.** Provider API keys exist only in the agent task's environment, injected from Secrets Manager. They are kept out of Postgres, the main app, logs, error messages, audit rows and browser responses by design: keys are held in a type that prints as `[redacted]`, and the agent's error texts are fixed and never repeat a key. Error text that comes from a provider is scrubbed for secret-shaped values on a best-effort basis.
- **A budget bounds the cost.** A monthly allowance, set by an admin, is enforced with reservations and worst-case accounting. See [AWS costs](../operations/aws-costs.md#ai-agent-and-model-spend).

### Text from your estate is untrusted

Logs, task definitions, tags and service names are written by applications, and possibly by an attacker. The agent's instructions say that tool results are data, not instructions, and each result arrives inside tags carrying a random code for the run, so text inside it cannot close the data block or pose as the loop's own message. This lowers the risk of prompt injection but cannot remove it: a model can be misled. The limit that holds regardless is the one above: even a misled agent can only read what the asking user may read, and cannot change anything. What it reads can still reach its answer and the provider.

### What is redacted, and what leaves the network

- Environment values from task definitions are replaced with `[redacted]` for every provider unless an admin lifted that for that provider in **Settings > AI agent**. Values that look like secrets (cloud keys, tokens, private keys, passwords in URLs, values under secret-looking names) are always redacted.
- Tool results are scrubbed for secret-shaped text before they go to the model, and the page context the browser sends is cleaned the same way. Scrubbing is best effort and has documented limits. It uses patterns, so a secret with an unusual shape can pass. In text that is not valid JSON, such as an answer cut at the size cap, redaction never goes past the end of the line, so the later lines of a list or object spread over several lines under a secret name stay. A value that comes before its name, as in `"value":"x","name":"DB_PASSWORD"`, is found only in valid JSON. Text glued to a redaction without a separator may be hidden only by a second pass. A multi-line list under a secret name whose later lines start with `\`, `]` or `}` can also stay. Treat logs as possibly sensitive and keep redaction on.
- `HM_TOKEN_REDACT_ENV=true` overrides an admin's per-provider lift: environment values are then redacted for every provider. The operator can force full redaction this way.
- Tool results of the chat are never written to Postgres. Saved are the user's text, the answer and the tool names, for `HM_RETENTION_AGENT_CONVERSATIONS` (30 days by default). Automatic investigations store cited excerpts: see [Automatic investigations](#automatic-investigations).

What leaves your network for each provider, for every question: the system text, the conversation (your newest messages that fit), the tool definitions, and the scrubbed results of the tools the model called, including log lines the user may see.

| Provider | Destination | Notes |
| --- | --- | --- |
| Anthropic | `api.anthropic.com` (the Anthropic SDK's default host) | Over the internet through your NAT gateway. Governed by Anthropic's terms |
| OpenAI | `api.openai.com` | Over the internet through your NAT gateway. Governed by OpenAI's terms |
| Google | `generativelanguage.googleapis.com` | Over the internet through your NAT gateway. Governed by Google's terms |
| Amazon Bedrock | The regional `bedrock-runtime` endpoint | Stays with AWS, under your account and the task role. Governed by your AWS and model terms |

Check each provider's data handling terms before you enable it.

### Egress is fenced in the application only

The agent's tasks have outbound 443 to anywhere, because security groups cannot filter by hostname. Provider calls and calls to Harbormaster go to fixed hosts: each provider's address is fixed in code, redirects are refused and proxy settings are ignored. That is a property of the code, not enforced by the network. The AWS credential chain (container credentials endpoint, STS, SSO) is separate: for STS and SSO it follows the `AWS_ENDPOINT_URL_*` variables, so keep those out of the agent's environment. See [Hardening checklist](hardening.md#ai-agent) for stricter options.

### Audit

Every question writes an `agent_run` audit entry (who, model, tools, tokens, cost, outcome, never the text). Changes to the allowance, the chat and investigation models and redaction write `agent_settings_update`, and turning chat on or off for a user writes `user_ai_chat`. Each investigation or review run writes an `agent_run` entry with the agent identity as actor, changes to the identity's access write `agent_identity_update`, and an admin's changes to scheduled reviews write `agent_review_create`, `agent_review_update`, `agent_review_delete` and `agent_review_run`. See [Audit log](../guides/audit-log.md).

### Automatic investigations

A notification route with **Investigate with AI** on has the agent investigate alerts without a person asking. This changes three rules above, so read it before you switch a route on. How it works: [Automatic AI investigations](../guides/ai-agent-investigator.md).

- **Not as a person, but as the agent identity.** Nobody's access is used. The identity holds View, and Logs where granted, on the tags an admin chose, and is read-only like any run token. It cannot sign in, hold a session or token, or be an admin. See [Permissions](../reference/permissions.md#the-agent-identity). Treat the grant as a decision to let a model provider read what those tags hold, for every alert, with nobody present. Grant logs last.
- **The evidence path.** The model never writes evidence. It names numbered pieces of tool output it was shown. The agent labels each piece with the service and the action (View or Logs) from a fixed rule on the tool call, takes the excerpt from the scrubbed tool result, and Harbormaster derives the label again from the tool name and drops what does not match. A tool whose answer belongs to no single service cannot be cited. A stored item is shown only to a reader who holds View (and Logs for log text) on that service now, so losing access hides it. Stored evidence is never sent to a model, and investigation routes accept no access token.
- **What is stored.** Tool results of the chat are never written to Postgres. This is the one exception: the cited evidence of an investigation, at most 12 items of 1500 bytes each, scrubbed in the agent and again in Harbormaster, plus the summary and the alert snapshot, for `HM_RETENTION_AGENT_RUNS` (90 days by default). They can hold log text that the scrubber did not recognise as a secret.
- **The summary check does not prove the summary is safe.** The summary and notes are replaced whole (never trimmed) when they are too long, contain hidden or direction-changing characters (including a zero-width joiner, so some Persian, Sinhala and emoji text is withheld), contain image markup, are changed by the secret scrubber, or repeat 32 or more normalised characters of what the agent read. Links not to your own Harbormaster are replaced when a message is sent. The check is best effort: it does not catch a paraphrased secret, a bare host name, or a leak from text the agent read beyond its 2 MiB read limit (past it the check fails closed and the summary is replaced). The summary is rendered as plain text everywhere, and Slack and Telegram get it in a form that makes no mention, command or link.
- **A prompt injection can reach a destination.** A log line can tell the model what to write. The agent can only read, and the summary passes the check above, but its text then goes to the route's Slack channel, chat, webhook or email address. The notice "Written by AI. Check it before acting." comes from code, not from the model. Do not route diagnoses to a place where text is run as a command.
- **What the model is told about a group alert.** For a cluster or account alert, the alert's own title and text are not sent. Harbormaster writes them anew: the place by cluster name or by account id and region (never a display name), the counts, and the services that were failing and that the agent identity may View now, at most ten, then "and K more". The AWS error text is not sent. The subject also carries the account id, region and cluster, and the ids, which contain the names, of up to 20 visible services. The names of services the identity may not View are never sent, but the counts can include them. A service alert is sent as stored (title and text, scrubbed and cut). The results of the tools it calls go as well.
- **No spending limit per run beyond the allowance.** Runs share the monthly allowance with the chat, are reserved at worst case, and are capped by count (per hour, in parallel, at most two attempts each).

### Scheduled reviews

A scheduled review has the agent write a cost, security, reliability or capacity summary on a timetable, without a person asking, and sends it to notification routes. It runs as the agent identity and shares the rules of [automatic investigations](#automatic-investigations): the identity's access, the evidence path, the summary check and the allowance. How it works: [Scheduled AI reviews](../guides/ai-agent-reviews.md). What it changes or adds:

- **Reviews read no logs.** The reviewer role is not offered the four log tools. A call to one is refused as an unknown tool, so a review cannot read log lines and cannot spend the identity's log-search budget. A cited item whose action is not View is dropped when the result is stored and hidden when it is read, from admins too. Environment values and commands in task definitions can still be read, redacted as for the chat.
- **Scope is a focus, visibility is the boundary.** A review reads what the agent identity may View and nothing else. The scope tells the model where to look. It is not an access rule, and nothing in a scope entry, a tool answer or a name can change the tools, the identity or the checks. The four types differ only in the instructions of the first message.
- **Who reads a result.** A result is shown to a global admin, or to someone who has View on every tag the identity could View when the run started or when it finished. Both sets are stored with the run, and a tag granted and removed again during one run is not covered. Someone who holds only some of them cannot read a result that rests on all of them. See [Permissions](../reference/permissions.md#who-reads-a-review).
- **What is stored.** The summary, the covered tags, the scope label and the same evidence as an investigation (at most 12 items of 1500 bytes, View only), for `HM_RETENTION_AGENT_RUNS`. The review's definition holds no result. A run is limited to 60 tool calls.
- **What the model is told.** The type, the range and period, the number of services in scope and the scope entries (kind, ids, region and the names of tags and clusters), scrubbed and cut. The review's own name is not sent. The scope names are data: the frame says text that tools return and the names in the scope are not instructions.
- **The copy check and names.** A summary is replaced whole when it repeats 32 or more normalised characters of what the agent read. A name of a service or cluster that the agent read in an id is exempt in the summary, however long it is. The exemption covers one word of letters, digits, hyphens and underscores up to 128 characters, wholly inside the window. It does not cover a name with other characters, text beside a name, a name the agent did not read in an id, or notes. Text that a person wrote, such as a tag name or a review name, is exempt only if it is also part of a service or cluster id the agent read. The check stays best effort: see [what the summary check does and does not catch](../guides/ai-agent-reviews.md#what-the-summary-check-does-and-does-not-catch).
- **A prompt injection can reach a destination.** A service name or tag in your estate can tell the model what to write. A review can only read, and its summary passes the check above, but the text then goes to each route's chat, channel, webhook or email address, on every run. The notice "Written by AI. Check it before acting." comes from code. Do not route reviews to a place where text is run as a command. PagerDuty gets no review.
- **Custom reviews.** A review of type `custom` carries text an admin wrote: see [Custom reviews](#custom-reviews).
- **The allowance stays whole.** A review run reserves its worst case, is skipped unless the allowance would still hold one chat run and one investigation after it, and runs one at a time and only when no investigation is waiting. Counts, not money, limit starts: `HM_AGENT_REVIEWS_PER_DAY`. At most 20 reviews exist.

### Custom reviews

A custom review lets an admin write the instructions of a [scheduled review](#scheduled-reviews). It is off unless the operator sets `HM_AGENT_CUSTOM_REVIEWS=true`. How it is used: [Custom reviews](../guides/ai-agent-reviews.md#custom-reviews). What it changes:

- **The text sits in an envelope of its own.** In the first message, the text is inside `<admin_request_NONCE>` tags, where NONCE is a code that changes for every run. The frame, which is fixed text, tells the model that the text inside was written by an administrator of this installation, that it decides what the review looks at and how the summary is laid out, and that it cannot change the rules, the tools, the limits, the scope, what can be cited or the form of the result. The text of the request is not part of the system text. Text that claims to be an administrator's request in a tool answer or a scope name is data. The review's name is not sent.
- **A model can still go wrong.** It can follow a request badly, ignore part of it, or be talked round by text it reads in a name or a tool answer. What stops that from mattering does not depend on the model: the tool list (35 read-only tools, no log tools, 60 calls), the identity and its read-only run token, the labels that come from code, and the checks of the summary and the evidence. The instructions are not read by any code that chooses tools, caps, identity, checks or recipients. A model that declines ends as `no_result`, one paid run, nothing sent.
- **Only admins write and read the text.** It is written through the admin routes of the session, which no access token reaches, and read by admins through the review list, the run list and the audit log. The read routes for results have no member for it. No tool of the agent reads a review definition.
- **Where the text exists.** Two database columns (the review and a copy on each run), the admin-only audit entries of a change (old and new, with the SHA-256), the admin API, and the request to the model provider of the reviewer role. It is in no log line of either service, no error message, no `agent_run` entry (the hash only), no notification and no metric label. Deleting a review keeps the copies on its past runs until `HM_RETENTION_AGENT_RUNS` removes them, and the audit entries keep it for `HM_RETENTION_AUDIT_LOG`.
- **Saved before sent.** A text reaches a model only after the API saved it, which cleans, checks and audits it. No route runs a draft. A text that stops passing the checks after an upgrade is not sent: the run is skipped as `instructions`.
- **Secret and hidden characters.** A text with hidden characters, or one the secret scrubber would change, is refused and not stored. The check is best effort: it matches shapes, and a secret in a JSON value in the middle of a line can pass it, so the model provider would see it. The agent scrubs the request again before sending, which removes what its patterns find. Do not paste a credential into instructions.
- **Repeating the request.** A summary or note that repeats 80 or more normalised characters of the request is replaced in the agent and again in Harbormaster. This keeps the request out of a notification. It is best effort, like the copy check.
- **The copy check and labels.** A custom review that reports by tag or account is likelier to name a tag or account label of 32 or more characters, which replaces the summary, because only names read in service and cluster ids are exempt.
- **Test runs.** A test run is stored and never sent, and only admins read it. It spends from the allowance like any run.
- **Built-in types do not change.** Their text to the model is the same bytes as before the release that adds custom reviews, and a test pins that.

## Related pages

- [IAM and AWS access](iam-and-aws-access.md)
- [Hardening checklist](hardening.md)
- [AI chat](../guides/ai-agent-chat.md)
- [Automatic AI investigations](../guides/ai-agent-investigator.md)
- [Reporting vulnerabilities](reporting-vulnerabilities.md)
