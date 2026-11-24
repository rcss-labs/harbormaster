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
- **Only as the asking user.** Each question gets a run token for that user, tied to one run. It is read-only like an access token: only the routes tokens may call, only View and Logs, an admin's included. It expires after `HM_AGENT_RUN_TOKEN_TTL` (15 minutes by default) and ends with the user's sessions. The public MCP gateway refuses it.
- **No estate code in the agent.** The agent service imports no database, cache, SAML or AWS estate code. Its only AWS SDK client is Bedrock runtime. It has no database URL, session key or access role.
- **Internal load balancers only.** The agent may be enabled only when Harbormaster's load balancer is internal, so run tokens are never accepted on a public one. The agent has an internal load balancer of its own that admits only Harbormaster's web tasks.
- **Keys stay in the agent.** Provider API keys exist only in the agent task's environment, injected from Secrets Manager. They never appear in Postgres, the main app, a log line, an error message, an audit row or a browser response.
- **A budget bounds the cost.** A monthly allowance, set by an admin, is enforced with reservations and worst-case accounting. See [AWS costs](../operations/aws-costs.md#ai-agent-and-model-spend).

### Text from your estate is untrusted

Logs, task definitions, tags and service names are written by applications, and possibly by an attacker. The agent's instructions say that tool results are data, not instructions, and each result arrives inside tags carrying a random code for the run, so text inside it cannot close the data block or pose as the loop's own message. This lowers the risk of prompt injection but cannot remove it: a model can be misled. The limit that holds regardless is the one above: even a misled agent can only read what the asking user may read, and cannot change anything. What it reads can still reach its answer and the provider.

### What is redacted, and what leaves the network

- Environment values from task definitions are replaced with `[redacted]` for every provider unless an admin lifted that for that provider in **Settings > AI agent**. Values that look like secrets (cloud keys, tokens, private keys, passwords in URLs, values under secret-looking names) are always redacted.
- Tool results are scrubbed for secret-shaped text before they go to the model, and the page context the browser sends is cleaned the same way. Scrubbing is best effort. It uses patterns, so a secret with an unusual shape can pass, and in text that is not valid JSON (an answer cut at the size cap) redaction stops at the end of the line, so later lines of a multi-line list under a secret name can stay. Treat logs as possibly sensitive and keep redaction on.
- Tool results are never written to Postgres. Saved are the user's text, the answer and the tool names, for `HM_RETENTION_AGENT_CONVERSATIONS` (30 days by default).

What leaves your network for each provider, for every question: the system text, the conversation (your newest messages that fit), the tool definitions, and the scrubbed results of the tools the model called, including log lines the user may see.

| Provider | Destination | Notes |
| --- | --- | --- |
| Anthropic | `api.anthropic.com` | Over the internet through your NAT gateway. Governed by Anthropic's terms |
| OpenAI | `api.openai.com` | Over the internet through your NAT gateway. Governed by OpenAI's terms |
| Google | `generativelanguage.googleapis.com` | Over the internet through your NAT gateway. Governed by Google's terms |
| Amazon Bedrock | The regional `bedrock-runtime` endpoint | Stays with AWS, under your account and the task role. Governed by your AWS and model terms |

Check each provider's data handling terms before you enable it.

### Egress is fenced in the application only

The agent's tasks have outbound 443 to anywhere, because security groups cannot filter by hostname. That the agent dials only the configured providers and Harbormaster is a property of its code, which fixes each provider's address, refuses redirects and ignores proxy settings. It is not enforced by the network. See [Hardening checklist](hardening.md#ai-agent) for stricter options.

### Audit

Every question writes an `agent_run` audit entry (who, model, tools, tokens, cost, outcome, never the text). Changes to the allowance, chat model and redaction write `agent_settings_update`, and turning chat on or off for a user writes `user_ai_chat`. See [Audit log](../guides/audit-log.md).

## Related pages

- [IAM and AWS access](iam-and-aws-access.md)
- [Hardening checklist](hardening.md)
- [AI chat](../guides/ai-agent-chat.md)
- [Reporting vulnerabilities](reporting-vulnerabilities.md)
