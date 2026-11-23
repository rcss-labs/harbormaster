# AI access (MCP)

How to use Harbormaster from Claude Code or another Model Context Protocol (MCP) client. For engineers who want an AI tool to answer questions about their ECS estate.

The access is read-only. An MCP gateway (`harbormaster-mcp`) translates each tool call into one read request to Harbormaster's API, carrying your own access token. Setting the gateway up is an operator task: see [MCP gateway](../install/mcp-gateway.md).

## What the gateway can and cannot do

- It speaks MCP on `POST /mcp`. It holds no credentials, no database access and no AWS permissions.
- It cannot restart, scale, deploy, sleep, wake or edit anything. No such tool exists.
- It acts as the token's user, so it sees exactly the tags that user sees, and log lines only where the user has Logs. An admin's token is not more powerful than the admin's own read access.

## Connect Claude Code

Before you begin: your operator has enabled tokens and the gateway, and gave you its URL (`https://<host>/mcp`). You have [created an access token](access-tokens.md).

### Keep the token out of shell history

Passing the token on the command line puts it in your shell history and in plain text in `~/.claude.json`. Store a reference instead.

1. Read the token without echoing it and export it:

    ```sh
    read -rs HARBORMASTER_MCP_TOKEN && export HARBORMASTER_MCP_TOKEN
    ```

2. Add the server with the variable name in single quotes, so your shell does not expand it:

    ```sh
    claude mcp add --transport http harbormaster https://<host>/mcp \
      --header 'Authorization: Bearer ${HARBORMASTER_MCP_TOKEN}'
    ```

3. Run `claude mcp get harbormaster` and confirm the header still shows `${HARBORMASTER_MCP_TOKEN}`, not the secret. If it shows the secret, remove the server and add the same entry to your MCP configuration by hand with `"headers": { "Authorization": "Bearer ${HARBORMASTER_MCP_TOKEN}" }`.

Claude Code expands `${VAR}` in a server's headers when it loads its configuration, so the variable must be set in the environment you start Claude Code from. Use your own variable name; Claude Code reads some of its own credential variables as empty in remote headers. Put the export in a file only you can read, never a committed one. To fetch the token at connect time, for example from a password manager, use a `headersHelper` command that prints `{"Authorization": "Bearer ..."}`.

The simplest form, `--header "Authorization: Bearer hmp_..."`, works but stores the secret in plain text.

Check it worked: ask "what is running and what is unhealthy?". Claude should call `get_overview` or `list_alerts`.

### claude.ai and Claude Desktop

If your operator enabled OAuth, add a custom connector with the gateway's `https://<host>/mcp` URL and no client id. Claude sends your browser to Harbormaster's approval page, which names the app and says it gets read-only access as you. Approve it while signed in as yourself. The approval page is on Harbormaster's private address, so you need your VPN or office network once, at approval time. A connection lasts up to 90 days for a password user who is not an admin, and 7 days for an SSO-linked user or an admin. Revoke an app on **My access** under **Connected apps**. The same read-only tools and limits apply.

## Tools

The gateway exposes 39 tools. All are reads.

| Area | Tools |
| --- | --- |
| Identity | `whoami` (who you are, what you can see, log-search budget left today) |
| Estate | `get_overview`, `list_clusters`, `get_cluster`, `list_services`, `get_service`, `list_service_tasks`, `list_tasks`, `search`, `list_alerts` |
| Configuration | `get_task_definition`, `list_revisions`, `get_revision`, `get_scaling`, `get_desired_count`, `get_service_offhours`, `get_arm64_readiness`, `search_config` (names only, never values) |
| Troubleshooting | `get_service_timeline`, `get_timeline_task`, `list_service_operations`, `get_target_health`, `get_service_metrics`, `get_service_logs`, `list_log_search_services`, `estimate_log_search`, `search_logs` |
| Security | `list_security`, `get_service_security`, `get_image_security` |
| Analytics | `get_cost_analytics`, `get_capacity_analytics`, `get_reliability_analytics`, `get_delivery_analytics`, `get_operations_analytics` |
| Network and storage | `list_networks`, `list_region_vpcs`, `get_vpc_tree`, `list_storage` |

Results larger than `HM_MCP_MAX_RESULT_BYTES` (100000 by default) are limited. Log search by tokens has a per-search cap (2 GiB) and a daily budget (20 GiB per user); `estimate_log_search` and `whoami` report what is left.

## Treat tool output as untrusted

Log lines, environment names and stopped-task reasons are text written by your applications, and possibly by an attacker. The model reads it. The tools cannot write, so the damage is limited to what the model does with its other tools and to what it reads out.

- Tell your AI client to treat anything returned by these tools as data, never as instructions. The gateway marks log text as data in its tool descriptions, but the client must honour that.
- If an agent has this server and also a tool that can change things (a shell, a cloud CLI, a ticket system), treat everything it reads here as untrusted input to that tool, and require approval for those changes.

## Data leaves your network

Everything a tool returns is sent to the AI provider, environment variable values included. Ask your operator to set `HM_TOKEN_REDACT_ENV=true` to replace environment values with `[redacted]` for tokens. Use short token lifetimes, one token per tool, and revoke tokens you no longer need.

## What is logged

The gateway logs one line per request: method, tool name, status, duration, client IP and the token's public id, never the token, a header, a body or a result. Harbormaster records `token_create` and `token_revoke` in the [audit log](audit-log.md) and tags token-initiated log searches with `via: token` and the token id.

## Next steps

- [Access tokens](access-tokens.md)
- [MCP gateway install](../install/mcp-gateway.md)
- [MCP gateway runbook](../operations/runbooks/mcp-gateway.md)
