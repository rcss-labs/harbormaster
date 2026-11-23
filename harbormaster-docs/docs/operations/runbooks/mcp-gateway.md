# MCP gateway runbook

This runbook covers the day-two operation of the MCP gateway: rollout checks, verifying a deployment, shutting it down, revoking tokens, reading the WAF log, diagnosing an outage and enabling OAuth connectors. It is for operators who run the gateway. To install it, see [MCP gateway](../../install/mcp-gateway.md). For its Terraform inputs, see [Terraform module: harbormaster-mcp](../../reference/terraform-mcp.md).

Plans and applies run in your deployment system. Nothing below runs `terraform plan` or `terraform apply` locally. AWS commands here are read-only and always take `--profile` and an explicit `--region`. Never paste a token into a ticket, chat or shell history. Where a command needs one, it reads `$HARBORMASTER_MCP_TOKEN` (see [Keep the token out of history](#8-keep-the-token-out-of-history)).

## 1. Rollout

| Step | What | How to verify |
| --- | --- | --- |
| 1 | Ship Harbormaster with the token code and `HM_API_TOKENS` unset. Nothing changes | `curl -s -o /dev/null -w '%{http_code}' https://<harbormaster>/api/tokens` as no user returns `404` while tokens are off |
| 2 | Set `HM_API_TOKENS=true` (module `extra_env`) in a test environment. Create a token on My access and call the internal API from inside the network | `curl -s -H "Authorization: Bearer $HARBORMASTER_MCP_TOKEN" https://<internal-host>/api/tokens/current` returns your user. `GET /api/clusters` works. `POST` and `DELETE` routes and `/api/users` answer `403 token_not_allowed` |
| 3 | Build and push the gateway image, then run the `harbormaster-mcp` module with `allowed_cidrs` narrowed to your own address, and the Harbormaster module with `alb_ingress_security_group_ids = [module.harbormaster_mcp.task_security_group_id]` | The checks in section 2 |
| 4 | Add the server in Claude Code and ask the acceptance questions | Section 2 |
| 5 | Set `allowed_cidrs` back to `["0.0.0.0/0"]` and run again. Watch WAF counts for a week before you move count-mode rules to block | Section 6 |
| 6 | OAuth connectors: only after you verify against real clients in a test environment. The example ships with OAuth off | Section 9 |

## 2. Acceptance checklist

Run this after the apply. Record each result. Never record a token.

### Before the apply

- [ ] A public hostname and an ACM certificate for it (`certificate_arn`). A DNS record will point at the `alb_dns_name` output (alias, `alb_zone_id`).
- [ ] The private subnets route to a NAT gateway. The gateway pulls its image and writes logs through it.
- [ ] The Harbormaster module has `internal = true`, and `HM_API_TOKENS=true` and `HM_MCP_PUBLIC_URL=https://<public host>` in `extra_env`, and a private DNS name for `upstream_url` that matches the internal certificate.
- [ ] `allowed_cidrs = ["<your-ip>/32"]` for the first run.
- [ ] The gateway runs in its own private subnets, and Harbormaster's `extra_env` has `HM_MCP_GATEWAY_CIDRS` set to exactly those subnets' CIDRs. Otherwise `last_used_ip` and the audit IP are wrong.
- [ ] Harbormaster already runs the new image with `HM_API_TOKENS` unset, and `GET /api/tokens` answers `404`. Turn the switch on in a later run, so no old task without the revocation hooks still serves while tokens exist.
- [ ] The gateway trusts the internal load balancer's certificate: a public CA, or `upstream_ca_pem` set to your private CA.
- [ ] You decided on `HM_TOKEN_REDACT_ENV`. By default, task-definition environment values are shown, so they reach the AI provider.
- [ ] `alarm_sns_topic_arns` is set on the gateway module before you open `allowed_cidrs`.
- [ ] The image is built and pushed to your registry under a version tag, never `latest`.
- [ ] One run can carry both modules when they share a root (see the example in the [module reference](../../reference/terraform-mcp.md#minimal-usage-example)). Otherwise run the Harbormaster module first (it has the new ingress variable), then the gateway module.

### After the apply, from outside the network

- [ ] `curl -s -o /dev/null -w '%{http_code}' https://<host>/healthz` returns `200`.
- [ ] `curl -s -o /dev/null -w '%{http_code}' -X POST https://<host>/mcp` returns `401`.
- [ ] `curl -s -o /dev/null -w '%{http_code}' https://<host>/api/overview` returns `404`. The gateway serves no API path.
- [ ] A burst above `waf_rate_limit` (300 per 5 minutes) from one address is answered `403` by WAF. Run it once from your own address, then expect to be blocked for a few minutes.
- [ ] The gateway task log (`/ecs/harbormaster-mcp`) has one start line (`harbormaster-mcp starting`) and request lines with a token id only, no token and no body.
- [ ] The task definition has no secrets and the task role has no policy:

    ```sh
    aws ecs describe-task-definition --task-definition harbormaster-mcp --profile <profile> --region <region> \
      --query 'taskDefinition.containerDefinitions[].secrets'      # expect null or []
    aws iam list-attached-role-policies --role-name <task-role-name> --profile <profile> --region <region>   # expect none
    aws iam list-role-policies --role-name <task-role-name> --profile <profile> --region <region>            # expect none
    ```

### In Claude Code

Use a token of a non-admin user who sees one tag.

- [ ] Add the server with the variable reference, as in section 8. `claude mcp get harbormaster` shows the reference, not the secret.
- [ ] Ask: "what is running and what is unhealthy?", "what changed on `<service>` today?", "search the last hour of `<service>` logs for timeout", "what does `<service>` cost?". Record the tools called. Confirm in the web app, as the same user, that nothing outside the tag was returned.
- [ ] Negative test: revoke the token in the web app. The next question fails with an authentication error. Ask Claude to restart a service. It has no tool to do that.

### After the checks

- [ ] Reset `allowed_cidrs` to the default and run again.
- [ ] File the report. Name any check you did not run.

## 3. Verify a deployment later

- **Target health:** `aws elbv2 describe-target-health --target-group-arn <arn> --profile <profile> --region <region>`. All targets should be `healthy`.
- **WAF metrics** (CloudWatch namespace `AWS/WAFV2`, web ACL `harbormaster-mcp`, in the region of the ACL): `BlockedRequests` and `CountedRequests` by rule: `harbormaster-mcp-rate-per-ip`, `-ip-reputation`, `-known-bad-inputs` and `-common`. A steady non-zero `rate-per-ip` block means a client polls too hard or an address is shared.
- **Service events:** `aws ecs describe-services --cluster <cluster> --services harbormaster-mcp --profile <profile> --region <region>`.

## 4. Turn it off

1. **Gateway:** set `desired_count = 0` in the module, or destroy the module. The public load balancer then answers `503`.
2. **Harbormaster:** unset `HM_API_TOKENS` (remove it from `extra_env`) and redeploy. Bearer tokens are ignored at once and the token routes answer `404`. Do both for a full shutdown. The second step alone already stops all token access.
3. **The switch does not revoke anything.** Every token, grant and pending code stays in the database and works again when you turn the switch back on, for up to 90 days. After an incident, revoke them first. Run `harbormaster revoke-tokens --all` as a one-off ECS task with the server image and the same database settings. It prints the counts and writes an audit entry. An image rollback to a version without the token code also skips the revocation hooks while it runs, so run the command again after you roll forward.

## 5. Revoke tokens

- **One token:** the owner revokes it on My access. An admin revokes it on the Users screen (per-user token list), or with `DELETE /api/users/{id}/tokens/{tokenId}` in an admin session.
- **All of one user's tokens:** reset their password or change their admin flag (both end every token). For an SSO user, unlinking SSO ends their SSO tokens. Removing the user ends everything.
- **Everyone's SAML tokens:** turn single sign-on off or replace its configuration. This ends every token created in an SSO session.
- **Everyone's tokens and OAuth grants, for real:** `harbormaster revoke-tokens --all` (section 4, step 3). Unsetting `HM_API_TOKENS` only suspends them.
- **A locked-out account's tokens keep working.** Lockout does not revoke them.
- Every create and revoke is in the audit log as `token_create` or `token_revoke`. `GET /api/tokens/current` (the `whoami` tool) shows which token a client uses.

## 6. Read the WAF log for false positives

The log group is `aws-waf-logs-harbormaster-mcp` (output `waf_log_group_name`). The `authorization` and `cookie` headers are redacted. In CloudWatch Logs Insights:

```text
fields @timestamp, action, terminatingRuleId, httpRequest.clientIp, httpRequest.uri
| filter action = "BLOCK"
| sort @timestamp desc
| limit 50
```

Counted requests (body rules start in count mode) appear as `ruleGroupList` entries. Filter on `ruleGroupList.0.ruleGroupId = "AWSManagedRulesCommonRuleSet"`, or search for the rule name (`SizeRestrictions_BODY`, `CrossSiteScripting_BODY`, `GenericRFI_BODY`). A `search_logs` call whose query looks like an injection is the expected false positive. Only after a clean week, remove a rule from `waf_common_count_rules` to let it block.

## 7. What a gateway outage looks like, and what to check

Users see tool errors (`upstream unavailable`, the client failing to connect, or HTTP 5xx from the load balancer). The gateway answers a malformed token with `401` by itself. Any other connect-time check calls Harbormaster, so a Harbormaster outage shows as `502 upstream_unavailable`.

Check, in this order:

1. **Target health** of the gateway's target group. Are tasks running and healthy (`/healthz`)?
2. **Harbormaster's internal load balancer ingress rule.** Its security group must allow 443 from the gateway task security group (`alb_ingress_security_group_ids` on the Harbormaster module). A removed rule makes every call time out.
3. **`HM_MCP_UPSTREAM_URL` name resolution and certificate.** The task log shows `upstream call failed` or `token check failed` with the reason. The name must resolve in the VPC and match the internal certificate.
4. **NAT gateway and image pull** if tasks cannot start. An invalid `HM_MCP_PUBLIC_URL` or `HM_MCP_UPSTREAM_URL` is logged as `invalid configuration`, naming the variable, and the process exits.

Harbormaster rolling deploys make the gateway answer `unavailable` for a few seconds. Clients retry.

## 8. Keep the token out of history

`claude mcp add ... --header "Authorization: Bearer hmp_..."` writes the secret to your shell history and, in plain text, to `~/.claude.json`. Do this instead:

```sh
read -rs HARBORMASTER_MCP_TOKEN && export HARBORMASTER_MCP_TOKEN
claude mcp add --transport http harbormaster https://<host>/mcp --header 'Authorization: Bearer ${HARBORMASTER_MCP_TOKEN}'
claude mcp get harbormaster     # must show ${HARBORMASTER_MCP_TOKEN}, not hmp_...
```

Single quotes keep your shell from expanding the variable, so the stored header holds the reference. Claude Code expands it at start-up, so export the variable in the environment Claude Code starts from. For `curl` checks use `-H "Authorization: Bearer $HARBORMASTER_MCP_TOKEN"` the same way, never a literal token. If a token reached history or a config file, revoke it (section 5) and create a new one.

## 9. OAuth connectors (claude.ai, Claude Desktop, Claude Code)

### Enable

1. In Terraform: set `authorize_url = local.base_url` and `enable_oauth_paths = true` on the gateway module, and `HM_OAUTH = "true"` in the Harbormaster module's `extra_env`. `HM_OAUTH` needs `HM_API_TOKENS=true` and `HM_MCP_PUBLIC_URL`. Trigger the run. Both services redeploy.
2. Check from outside the network:
    - `curl -s https://<host>/.well-known/oauth-authorization-server | grep -c registration_endpoint` prints `0`.
    - `curl -si -X POST https://<host>/mcp -d '{}'` answers `401` with `WWW-Authenticate: Bearer resource_metadata="https://<host>/.well-known/oauth-protected-resource/mcp", scope="harbormaster:read"`.
3. Add the connector. In claude.ai: Settings, Connectors, add a custom connector with the URL `https://<host>/mcp` and no client ID. The browser opens Harbormaster's approval page. Approve it on the VPN. In Claude Code: `claude mcp add --transport http harbormaster https://<host>/mcp`, then `/mcp` and sign in.
4. These settings stay configurable because client behaviour can differ from what the defaults assume:

    | Setting | Purpose |
    | --- | --- |
    | `HM_MCP_AUTHORIZE_URL` | Authorization endpoint on another host than the issuer |
    | `HM_OAUTH_CLIENT_HOSTS` | Hosts allowed for client metadata documents (default `claude.ai`) |
    | `HM_OAUTH_REDIRECT_URIS` | Allowed redirect URIs (default: Claude's callbacks and loopback) |
    | `HM_MCP_AUTHORIZE_VIA_GATEWAY` | Redirect-through-gateway fallback |

    Change them from what you observe in your own test, not by editing code.

### Verify against real clients

Do this in a test environment before you rely on OAuth. It needs a claude.ai account that may add a custom connector. Do not paste tokens, codes or `Authorization` headers into notes. For claude.ai, then again for Claude Code, record from the browser address bar, the gateway log and the WAF log:

1. Does the client accept an `authorization_endpoint` on a different host from the issuer and open it? If not, set `HM_MCP_AUTHORIZE_VIA_GATEWAY=true` on the gateway and retry.
2. The `client_id` it sends: an HTTPS URL (a metadata document)? On which host? Fetch it and note `redirect_uris`, `client_name` and `token_endpoint_auth_method`. If the host is not `claude.ai`, set `HM_OAUTH_CLIENT_HOSTS`.
3. The `redirect_uri` (for Claude Code, the exact loopback form), `scope`, `resource` (it must equal the `/mcp` URL), `code_challenge_method` and whether `state` is present. If the redirect is not in the defaults, set `HM_OAUTH_REDIRECT_URIS`.
4. The `MCP-Protocol-Version` (or initialize version) sent to `/mcp`.
5. Whether any managed WAF rule blocked or counted the discovery requests, including the `/oauth/` rate rule. If a managed rule blocks the request, add its name to `waf_common_count_rules`.
6. That `client_id` is sent on refresh, and whether the client refreshes in parallel. Harbormaster allows a 60 second reuse grace on refresh tokens.
7. That `iss` in the callback is accepted. It must equal `HM_MCP_PUBLIC_URL` on both services.
8. How many distinct egress addresses reach `/oauth/token`, to size the `/oauth/` rate rule (`waf_oauth_rate_limit`, default 60 per 5 minutes per IP).

If the client will not use a metadata document, stop. The fallbacks (a user-entered client ID, dynamic registration) change the design and are a decision for the install's owner.

### Read the gateway log

- **`invalid_grant`**, seen as `upstream_status=400` on an `oauth request` line (the body is never logged). A code was reused, expired (60 seconds), or sent with the wrong verifier or redirect URI. Or a refresh token was reused after its short grace window, which revokes the whole grant. Or the grant passed its lifetime: 7 days for an SSO-linked user or an admin, 90 days otherwise. The user reconnects. Nothing needs fixing unless it repeats for one client. In that case, compare the client's redirect URI with `HM_OAUTH_REDIRECT_URIS`.
- **Token endpoint `400` responses do not count** against the gateway's failure limiter. A shared egress such as a hosted client's would otherwise lock every hosted user out. The WAF `/oauth/` rate rule and Harbormaster's own per-IP limit on `/api/oauth/token` limit them.
- **`502 temporarily_unavailable` on `/oauth/token`:** the gateway could not reach Harbormaster. Follow section 7.

### Revoke a grant

- **The user:** My access, Connected apps, revoke. The access token stops working at once. The connector's next call fails and Claude asks to reconnect.
- **An admin:** the Users screen, the user's tokens dialog, Connected apps, revoke.
- **Everyone:** unset `HM_OAUTH`. The OAuth routes answer `404` and issued OAuth tokens are no longer accepted. Use section 4 for everything.

## Related

- [Access tokens](../../guides/access-tokens.md)
- [AI access with MCP](../../guides/ai-access-mcp.md)
- [Troubleshooting](../troubleshooting.md)
