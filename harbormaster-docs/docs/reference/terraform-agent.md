# Terraform module: harbormaster-agent

The `harbormaster-agent` module runs the AI agent: a private ECS Fargate service behind an internal load balancer. Harbormaster calls it for chat runs and, from its coordinator, for automatic investigations. This page lists every input and output, what the module creates and which permissions it holds. Use it when you deploy or review the agent.

The module source is `terraform/modules/harbormaster-agent`. For the install steps, see [Install the AI agent](../install/ai-agent.md). For day-to-day operation, see the [AI agent runbook](../operations/runbooks/ai-agent.md).

!!! note "Where plans run"
    `terraform plan` and `terraform apply` run in your deployment system, not on a workstation. Locally, run only `terraform fmt` and `terraform validate`.

## Requirements

- Terraform 1.5 or newer and AWS provider 5.82 or newer (below 7.0).
- The `harbormaster` module with `internal = true`, `alb_ingress_security_group_ids` set to this module's `task_security_group_id`, and `HM_AGENT_URL` in `extra_env` (this module's `agent_url`). A precondition on the `harbormaster` module rejects `HM_AGENT_URL` on an internet-facing load balancer.
- Private subnets that route to a NAT gateway (image pull, logs and the model providers).
- A private DNS name for the agent that matches `certificate_arn`, pointing at `alb_dns_name`, passed as `hostname`. With a private CA, set `HM_AGENT_CA_PEM` in Harbormaster's `extra_env`.
- A private DNS name for `upstream_url` that matches Harbormaster's internal certificate.
- An agent image pinned to a tag or digest (`:latest` is refused in any case) that matches `cpu_architecture`.
- For each non-Bedrock provider, a Secrets Manager secret holding its API key as plain text.

## Inputs

A variable with no default is required.

| Name | Type | Default | Required | Description |
| --- | --- | --- | --- | --- |
| `name` | `string` | `"harbormaster-agent"` | no | Prefix of every resource name: 2 to 26 lowercase letters, digits and hyphens, starting with a letter. |
| `vpc_id` | `string` | none | yes | VPC to run in. |
| `private_subnet_ids` | `list(string)` | none | yes | Private subnets for the internal load balancer and the tasks. They must route to a NAT gateway. |
| `certificate_arn` | `string` | none | yes | ACM certificate for the agent's private hostname. |
| `hostname` | `string` | `null` | no | Private DNS name that matches `certificate_arn`. It only shapes the `agent_url` output. Null makes `agent_url` the load balancer's own DNS name, which a certificate for a private name will not match. |
| `cluster_arn` | `string` | none | yes | ARN of the ECS cluster to run in. |
| `image` | `string` | none | yes | Agent image ending in a tag or digest. `latest` is refused. |
| `cpu_architecture` | `string` | `"X86_64"` | no | `X86_64` or `ARM64`; it must match the image. |
| `cpu` | `number` | `512` | no | Fargate CPU units per task. |
| `memory` | `number` | `1024` | no | Fargate memory (MiB) per task. |
| `desired_count` | `number` | `2` | no | Number of agent tasks. |
| `upstream_url` | `string` | none | yes | `HM_AGENT_UPSTREAM_URL`: Harbormaster's internal address. Must start with `https://` and match the internal certificate's name. |
| `upstream_ca_pem` | `string` | `null` | no | `HM_AGENT_UPSTREAM_CA_PEM`: PEM of extra CAs that sign Harbormaster's internal load balancer certificate. A plain environment value, not a secret. |
| `harbormaster_alb_security_group_id` | `string` | none | yes | Security group of Harbormaster's internal load balancer (the egress target). Use `security_group_ids.alb`. |
| `harbormaster_task_security_group_ids` | `list(string)` | none | yes | The only callers the agent's load balancer admits on 443, at least one. Use `security_group_ids.web` and `security_group_ids.coordinator` with split services (`server` in single-service mode): the web tasks run the chat and the coordinator runs automatic investigations. |
| `model_providers` | `list(object)` | none | yes | The catalogue, serialised as `HM_AGENT_PROVIDERS`. Each entry has `provider` (`anthropic`, `openai`, `google` or `bedrock`, each at most once) and `models`: `id`, `input_per_mtok_usd` and `output_per_mtok_usd` (decimal strings in US dollars per million tokens, such as `"3.00"`) and `context_tokens`. At least one provider. The input is not named `providers` because Terraform reserves that name. |
| `api_key_secret_arns` | `map(string)` | `{}` | no | Secrets Manager ARN of each provider's API key, keyed `anthropic`, `openai` or `google`. Every non-Bedrock provider in the catalogue needs an entry and every entry needs a catalogue provider. The values reach the task as `secrets`, never as environment. |
| `bedrock_region` | `string` | `null` | no | `HM_AGENT_BEDROCK_REGION`. Required when the catalogue lists `bedrock`. |
| `bedrock_model_arns` | `list(string)` | `[]` | no | Foundation model or inference profile ARNs the task role may invoke. At least one when the catalogue lists `bedrock`. |
| `limits` | `object` | see below | no | Per-run limits and concurrency. |
| `alarm_sns_topic_arns` | `list(string)` | `[]` | no | SNS topics notified by the alarms. Empty creates no alarms. |
| `log_retention_days` | `number` | `30` | no | Retention of the agent log group. |
| `tags` | `map(string)` | `{}` | no | Tags for every resource that takes them. |

`limits` has these optional fields. Defaults are 20, 200000, `"5m"`, 8 and 100000.

| Field | Variable | Notes |
| --- | --- | --- |
| `max_steps` | `HM_AGENT_MAX_STEPS` | Positive. The agent accepts 1 to 100. |
| `max_run_tokens` | `HM_AGENT_MAX_RUN_TOKENS` | Positive. The agent accepts 1000 to 2000000. |
| `run_timeout` | `HM_AGENT_RUN_TIMEOUT` | At most `5m`, as whole seconds (`90s`), whole minutes (`5m`) or both (`2m30s`). The load balancer waits 600 seconds. |
| `max_concurrent_runs` | `HM_AGENT_MAX_CONCURRENT_RUNS` | Positive. The agent accepts 1 to 256. |
| `max_result_bytes` | `HM_AGENT_MAX_RESULT_BYTES` | Positive. The agent accepts 10000 to 140000. |

The module also sets `HM_AGENT_LISTEN=:8484` and `HM_AGENT_TRUSTED_PROXY=true`: the internal load balancer is the one proxy in front, and the agent reads the caller's address from the `X-Forwarded-For` entry it appends. The agent's own default is `false`.

## Outputs

| Name | Description |
| --- | --- |
| `agent_url` | Set as `HM_AGENT_URL` in the `harbormaster` module's `extra_env`. |
| `alb_dns_name` | DNS name of the internal load balancer (point the private hostname at it). |
| `task_security_group_id` | Security group of the agent tasks. Pass it to the `harbormaster` module's `alb_ingress_security_group_ids`. |
| `alb_security_group_id` | Security group of the agent's load balancer. |
| `task_role_arn` | Task role. Its only policy is Bedrock invocation, when the catalogue lists `bedrock`. |
| `log_group_name` | CloudWatch log group of the agent. |

## Resources the module creates

| Resource | Notes |
| --- | --- |
| Application Load Balancer (internal) | HTTPS on 443 only, policy `ELBSecurityPolicy-TLS13-1-2-2021-06`; idle timeout 600 seconds. No public subnet input, no public listener, no port 80 and no WAF |
| ECS Fargate service and task definition | In the existing cluster; private subnets, no public IP; read-only root filesystem; circuit breaker with rollback; image health check `/harbormaster-agent healthcheck` |
| Security groups | The load balancer admits 443 only from `harbormaster_task_security_group_ids`. The tasks admit 8484 only from the load balancer. The tasks may reach Harbormaster's load balancer group on 443 and `0.0.0.0/0` on 443 |
| Task role | Only `bedrock:InvokeModel` and `bedrock:InvokeModelWithResponseStream` on `bedrock_model_arns`, and only when the catalogue lists `bedrock` |
| Execution role | `AmazonECSTaskExecutionRolePolicy` plus `secretsmanager:GetSecretValue` on exactly the ARNs in `api_key_secret_arns` |
| Log group | `/ecs/<name>`, kept `log_retention_days` |
| Alarms | With `alarm_sns_topic_arns`: `<name>-alb-5xx`, `<name>-target-5xx`, `<name>-unhealthy-hosts` |

## Egress is fenced in the application

The tasks have outbound 443 to `0.0.0.0/0`, because security groups cannot filter by hostname and providers' addresses change. Provider calls and calls to Harbormaster go to fixed hosts, which is a property of the agent's code, not of the network. The AWS credential chain (container credentials endpoint, STS, SSO) is separate, and the STS and SSO clients follow `AWS_ENDPOINT_URL_*` through it. For a stricter fence, see [Hardening checklist](../security/hardening.md#ai-agent).

## KMS-encrypted secrets

A secret encrypted with a customer-managed KMS key needs more than this module grants: the execution role must also be allowed `kms:Decrypt` on that key. Add it to the role from outside the module, or allow the role in the key policy. Without it the task fails to start with an access-denied error on the secret.

## Next steps

- [Install the AI agent](../install/ai-agent.md)
- [AI agent runbook](../operations/runbooks/ai-agent.md)
- [Security model](../security/security-model.md#ai-agent)
