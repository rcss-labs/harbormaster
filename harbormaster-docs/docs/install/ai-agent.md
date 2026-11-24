# Install the AI agent

How to deploy the Harbormaster AI agent with the `harbormaster-agent` Terraform module and switch the chat on. For the engineer who owns the Harbormaster deployment. To use the chat, see [AI chat](../guides/ai-agent-chat.md).

The agent is a separate service. Harbormaster calls it for chat runs. The agent calls a model provider and reads Harbormaster through its internal load balancer with a short-lived, read-only run token. It has no database, cache, SAML or AWS estate access. It is off by default: with `HM_AGENT_URL` unset there are no agent routes, no setting, no button, and a run token does not authenticate.

## Before you begin

- Harbormaster is deployed with `internal = true`. This is required. Run tokens must never be accepted on a public load balancer, and a plan fails if `HM_AGENT_URL` is set on an internet-facing one.
- The agent needs an internal load balancer of its own. The module creates it. Set `HM_AGENT_TRUSTED_PROXY=true` only behind it (the module does).
- The agent image is built and pushed to your registry (see [Build the image](build-image.md)), pinned to a tag or digest. `:latest` is rejected, and the architecture must match `cpu_architecture`.
- Private subnets that route to a NAT gateway: image pull, logs and the model providers all go through it.
- A private DNS name and an ACM certificate for the agent (`certificate_arn`, `hostname`), and a private DNS name for `upstream_url` that matches Harbormaster's internal certificate.
- For each provider other than Bedrock, a Secrets Manager secret holding the provider's API key as plain text. For Bedrock, model access enabled in the Bedrock console for the models you list.
- A decision on which providers you accept. Read what each provider receives in the [Security model](../security/security-model.md#ai-agent). The question text, the conversation and the tool results of each question leave your network.

## Steps

1. Deploy the agent module. Pass the Harbormaster module's security groups and the catalogue of models you offer.

    ```hcl
    data "aws_ecs_cluster" "harbormaster" {
      cluster_name = module.harbormaster.cluster_name
    }

    module "harbormaster_agent" {
      source = "github.com/rcss-labs/harbormaster//terraform/modules/harbormaster-agent?ref=v1.0.0"

      vpc_id             = "<vpc-id>"
      private_subnet_ids = ["<private-subnet-1>", "<private-subnet-2>"]
      cluster_arn        = data.aws_ecs_cluster.harbormaster.arn
      certificate_arn    = "<acm-certificate-arn>"
      hostname           = "agent.internal.example.com"
      image              = "<account-id>.dkr.ecr.eu-west-1.amazonaws.com/harbormaster-agent:1.0.0"

      upstream_url                         = "https://harbormaster.internal.example.com"
      harbormaster_alb_security_group_id   = module.harbormaster.security_group_ids.alb
      harbormaster_task_security_group_ids = [module.harbormaster.security_group_ids.web]

      model_providers = [
        {
          provider = "anthropic"
          models = [{
            id                  = "<model-id>"
            input_per_mtok_usd  = "<input-price>"
            output_per_mtok_usd = "<output-price>"
            context_tokens      = 200000
          }]
        }
      ]
      api_key_secret_arns = { anthropic = "<secrets-manager-arn>" }

      alarm_sns_topic_arns = ["<sns-topic-arn>"]
    }
    ```

    Use `security_group_ids.server` instead of `web` when Harbormaster runs as a single service. The `web` tasks (or the single service) are the only callers the agent's load balancer admits.

    Prices are what your provider charges per million tokens. Harbormaster hardcodes no model id and no price: the catalogue is the only source, so keep it current. See [Terraform module: harbormaster-agent](../reference/terraform-agent.md) for every input.

2. Point the Harbormaster module at the agent. Admit the agent's tasks on Harbormaster's internal load balancer, and set the agent's URL.

    ```hcl
    module "harbormaster" {
      # ... existing inputs, with internal = true ...

      # append to any groups already listed (for example the MCP gateway's)
      alb_ingress_security_group_ids = [module.harbormaster_agent.task_security_group_id]

      extra_env = {
        HM_AGENT_URL = module.harbormaster_agent.agent_url
      }
    }
    ```

    `extra_env` reaches every service, so the web tasks (which serve chat) and the coordinator tasks (which run the repair job for interrupted runs) both get `HM_AGENT_URL`. In split mode, check that both roles got it. If the agent's certificate comes from a private CA, also set `HM_AGENT_CA_PEM` to the CA's PEM.

3. Apply in your deployment system. Nothing here runs `terraform plan` or `terraform apply` locally.

4. Sign in as a global admin and open **Settings > AI agent**. The status should read **Reachable**. Set a monthly allowance first: it starts at 0, which refuses every question, and the model test too. Then choose a provider and model and use **Test model**.

5. Turn on **AI chat** for users in **Settings > Users**. See [AI chat](../guides/ai-agent-chat.md).

## Bedrock

Bedrock needs no API key. List `bedrock` in `model_providers`, set `bedrock_region` and list the model or inference profile ARNs in `bedrock_model_arns`. The module gives the task role only `bedrock:InvokeModel` and `bedrock:InvokeModelWithResponseStream` on those ARNs.

- Credentials come from the task role. ECS container credentials are recommended.
- The agent fixes the Bedrock runtime endpoint to the regional `bedrock-runtime` address. The STS and SSO credential clients still honour the `AWS_ENDPOINT_URL_*` variables, so do not set those in the agent's environment unless you mean to.

## Check it worked

- **Settings > AI agent** shows **Reachable** and your models.
- **Test model** answers. It counts against the allowance.
- As a user with AI chat, ask "who am I and what can I see?" The answer matches **My access**.
- `GET /api/agent/status` answers `404` while `HM_AGENT_URL` is unset.
- The audit log shows `agent_settings_update` for your save and `agent_run` for each question.

## Try it locally

With a local Harbormaster (`HM_AGENT=1 make run`), run `make run-agent` in another terminal. It starts the agent on a canned model, so no provider key and no live model are involved. Then, as a global admin, choose the `fake` provider's model in **Settings > AI agent**, set an allowance and turn on AI chat for a user. `HM_AGENT_FAKE_PROVIDER=true` is accepted only when the upstream URL is a loopback address.

## Next steps

- [Terraform module: harbormaster-agent](../reference/terraform-agent.md)
- [AI agent runbook](../operations/runbooks/ai-agent.md)
- [Hardening checklist](../security/hardening.md)
- [Configuration reference](../reference/configuration.md#ai-agent)
