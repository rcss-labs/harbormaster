# AI agent runbook

Day-two operation of the AI agent: a rollout order, an acceptance check, what to do when chat is unavailable or the allowance runs out, how interrupted runs are repaired, key rotation and how to turn the agent off. It is for operators who run it. To install it, see [Install the AI agent](../../install/ai-agent.md). For its Terraform inputs, see [Terraform module: harbormaster-agent](../../reference/terraform-agent.md).

Plans and applies run in your deployment system. Nothing below runs `terraform plan` or `terraform apply` locally. AWS commands are read-only and always take `--profile` and an explicit `--region`. Never paste a provider API key or a run token into a ticket, chat or shell history.

## 1. Rollout

| Step | What | How to verify |
| --- | --- | --- |
| 1 | Ship the Harbormaster release with `HM_AGENT_URL` unset. Nothing changes | `GET /api/agent/status` answers `404`. There is no **Ask AI** button |
| 2 | Run the agent module and add its `task_security_group_id` to the Harbormaster module's `alb_ingress_security_group_ids` | The agent's target group is healthy (alarm `<name>-unhealthy-hosts` stays quiet) |
| 3 | Set `HM_AGENT_URL` (module `extra_env`) and apply. All Harbormaster tasks that run the web or coordinator role must get it | **Settings > AI agent** shows **Reachable** |
| 4 | Choose a model, run **Test model**, set a small allowance (for example a few dollars) | The test answers. Usage shows the call |
| 5 | Turn on **AI chat** for yourself or a small group first. Ask the acceptance questions in section 2 | Section 2 |
| 6 | Widen access. Watch spend for a week before you raise the allowance | Section 4 |

## 2. Acceptance checklist

- [ ] The Harbormaster module has `internal = true`. A plan fails otherwise.
- [ ] Both the web and coordinator tasks have `HM_AGENT_URL` (`extra_env` reaches every service).
- [ ] The agent's tasks are in `alb_ingress_security_group_ids`, so tool calls reach Harbormaster.
- [ ] **Settings > AI agent** shows **Reachable**, and the models you listed.
- [ ] The allowance is set. Allowance 0 refuses every question.
- [ ] Redaction is on for every external provider. You lifted it only where you decided to.
- [ ] A user without AI chat does not see **Ask AI**, and `POST /api/agent/conversations` answers `404` for them.
- [ ] Ask "who am I and what can I see?". The answer matches **My access**.
- [ ] Ask about a service the user cannot see. The chat finds nothing.
- [ ] The audit log shows an `agent_run` entry for each question, with provider, model, tools, tokens and cost, and no text.
- [ ] The public MCP gateway refuses a run token. A run token presented to it is never forwarded.
- [ ] `alarm_sns_topic_arns` is set on the agent module, so the three alarms notify someone.

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
4. Check that Harbormaster can reach the agent: the agent's load balancer admits only the Harbormaster web tasks' security groups on 443, and the certificate must match the host in `HM_AGENT_URL`. For a private CA, `HM_AGENT_CA_PEM` must hold it.
5. Check that the agent can reach Harbormaster: `upstream_url` must resolve to the internal load balancer and match its certificate (`upstream_ca_pem` for a private CA), and the agent's task group must be in `alb_ingress_security_group_ids`. Without that, the agent cannot confirm the run token or call tools, and every question fails.

Status checks stop asking a dead agent for about ten seconds after a failure, so the page does not hang.

## 4. The allowance and spend

- **"This month's AI allowance is used up."** An admin raises the allowance in **Settings > AI agent**, or waits for the next UTC month. A question needs room for its whole worst-case cost, so chat can refuse a question while some allowance is left. The worst case is the run's token limit at the model's higher price.
- **Spend looks higher than the provider's bill.** Reservations and worst-case accounting keep the allowance firm. An interrupted question can be charged more than it used. Compare the usage table with the provider's own usage page.
- **Spend rises fast.** Lower `limits.max_run_tokens` and `limits.max_steps`, remove an expensive model from the catalogue, or turn **AI chat** off for users. Remember that tool definitions use about 10,000 input tokens on every model call.

## 5. Interrupted and stale runs

A question stops early when Harbormaster restarts, the browser disconnects or the agent fails. The answer is marked interrupted and the user can ask again. The coordinator's repair job runs every minute. It fails runs that stopped reporting usage, settles their cost and writes the missing `agent_run` audit rows. It runs on the coordinator, so coordinator tasks need `HM_AGENT_URL` too: without it, the missing audit rows are not written, and a run that stopped mid-way is failed only when a later question reserves allowance.

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

Running questions end early during the deployment. Keys never appear in Postgres, logs, audit rows or browser responses.

## 7. Turn the agent off

1. Remove `HM_AGENT_URL` from `extra_env` and apply. The agent routes answer `404`, the button and setting disappear, and run tokens stop authenticating. Saved conversations stay until retention removes them.
2. Optionally set `desired_count = 0` on the agent module, or remove the module.

To stop only one user, turn **AI chat** off for them.

## 8. What to alarm on

- The module's alarms: `<name>-alb-5xx`, `<name>-target-5xx`, `<name>-unhealthy-hosts`.
- The agent's own metrics on `/metrics`: `harbormaster_agent_runs_total{status="done|error|cancelled|panic"}` and `harbormaster_agent_runs_active`. A rise in `panic` or `error` is worth a look at the log group.
- Budget: check **Settings > AI agent** weekly, or read `agent_run` audit rows.

## Next steps

- [AI chat](../../guides/ai-agent-chat.md)
- [Security model](../../security/security-model.md#ai-agent)
- [Throttling and limits](../throttling-and-limits.md)
