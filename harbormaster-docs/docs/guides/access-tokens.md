# Access tokens

How to create a personal access token, use it, and revoke it. For anyone who wants a script or an AI tool to read Harbormaster as themselves, without a browser session.

Tokens are off until an operator sets `HM_API_TOKENS=true`. With it unset the token routes answer 404 and an `Authorization` header is ignored, and the **Access tokens** panel does not appear.

## What a token is

- An opaque secret that starts with `hmp_`, created on **My access**. The secret is shown once. Harbormaster stores only a hash, so a lost token is replaced, never recovered.
- **Read-only.** A token sees what its user sees: the same tags, and logs only where the user has Logs. It can call only an explicit list of read routes (overview, clusters, services, tasks, metrics, alerts, analytics, security, timeline, off-hours, networks, storage, log search, config search and whoami). Every other route, including all writes and admin screens, answers `403 token_not_allowed`, an admin's token too. A token cannot create, list or revoke tokens.

## Create a token

Before you begin: tokens are on, and you hold fewer than the per-user limit (10 by default; **Create token** is disabled at the limit).

1. Open **My access** (`/me/access`) and find the **Access tokens** panel.
2. Select **Create token**.
3. Enter a **Name**, for example `Claude Code on my laptop` (up to 60 characters).
4. Pick a **Lifetime**. The choices are 1, 7, 30 and 90 days, capped at your maximum.
5. Select **Create token**, then copy the secret. The dialog says `This is the only time the token is shown.` It also shows a ready-made `claude mcp add` command when the MCP gateway is configured.

Check it worked: the token appears in the list with its public id (`hmp_<id>`), creation date, expiry and `Never used`. Once used, the list shows when. Do not paste the secret into tickets or chat. For how to hand it to an AI client without leaving it in shell history, see [AI access (MCP)](ai-access-mcp.md#keep-the-token-out-of-shell-history).

## Lifetimes and limits

| Setting | Default | Meaning |
| --- | --- | --- |
| `HM_TOKEN_MAX_TTL` | 90 days | Longest lifetime of a new token. |
| `HM_TOKEN_MAX_TTL_SSO` | 7 days | Longest lifetime for a user linked to single sign-on and for every global admin. |
| `HM_TOKEN_MAX_PER_USER` | 10 | Unexpired tokens one user may hold. |
| `HM_TOKEN_LOG_SEARCH_MAX_GB` | 2 | Most GiB one token-initiated log search may scan. |
| `HM_TOKEN_LOG_SEARCH_DAILY_GB` | 20 | GiB one user's tokens may scan per UTC day. `0` turns log search off for tokens. |
| `HM_TOKEN_REDACT_ENV` | `false` | Replace environment values with `[redacted]` in task definition answers to tokens. |

Requests are rate limited to a burst of 120, then 2 per second per user (HTTP 429 with `Retry-After`). After the log search budget is used, searches answer `429 log_search_budget_exhausted` until 00:00 UTC; the panel footer shows today's use. Searches made in the browser are not charged. An expired token is deleted a day after it expires. See the [configuration reference](../reference/configuration.md).

A token call counts as activity, like an open browser session, so an agent that calls often keeps the poller at full pace until its token ends.

## Revoke a token

1. Open **My access** and find the token.
2. Select **Revoke** and confirm **Revoke token**. Anything using it stops at once. This cannot be undone.

A global admin can revoke any user's token from **Settings > Users & permissions** (the per-user token list, which also lists connected apps).

Tokens also end wherever sessions end: a password change or reset, granting or removing global admin, unlinking single sign-on (SSO tokens only) and turning single sign-on off. A locked account does not stop tokens; removing the user or revoking the token does.

Creating and revoking are recorded in the [audit log](audit-log.md) as `token_create` and `token_revoke`, with the token's name and id, never the secret.

## Revoke every token

Turning `HM_API_TOKENS` or `HM_OAUTH` off stops tokens working but deletes nothing: turning it back on revives every unexpired token, OAuth grant and pending code. After an incident, delete them with the CLI, run in the same environment as the server:

```sh
harbormaster revoke-tokens --all           # every user's tokens, OAuth grants and pending codes
harbormaster revoke-tokens --user <username>   # one user's tokens, grants and codes
```

Without `--all` or `--user` the command prints usage and exits 2 without deleting anything. It prints how many tokens, grants and codes it removed and writes one audit entry (`tokens_revoke_all` or `tokens_revoke_user`) by actor `cli`. Users then create new tokens or approve their connectors again. See the [CLI reference](../reference/cli.md).

## Next steps

- [AI access (MCP)](ai-access-mcp.md)
- [MCP gateway runbook](../operations/runbooks/mcp-gateway.md)
- [Security model](../security/security-model.md)
