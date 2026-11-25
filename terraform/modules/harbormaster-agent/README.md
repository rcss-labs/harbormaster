# harbormaster-agent module

Deploys the Harbormaster AI agent (`harbormaster-agent`) on AWS: an ECS Fargate service behind an internal Application Load Balancer. Harbormaster calls it for chat runs; the agent calls model providers and reads Harbormaster through its internal load balancer with a short-lived, read-only run token.

## What it creates

- An internal Application Load Balancer in the private subnets: HTTPS on 443 only (`ELBSecurityPolicy-TLS13-1-2-2021-06`), idle timeout 600 seconds (a run lasts at most 5 minutes). No public subnet input, no public listener, no port 80, no WAF.
- An ECS Fargate service (private subnets, no public IP, deployment circuit breaker with rollback) in an existing cluster.
- Security groups with separate rules only: the load balancer admits 443 only from `harbormaster_task_security_group_ids`; the tasks admit 8484 only from the load balancer; the tasks may reach Harbormaster's load balancer group on 443 and `0.0.0.0/0` on 443.
- A task role whose only policy is `bedrock:InvokeModel` and `bedrock:InvokeModelWithResponseStream` on `bedrock_model_arns`, created only when the catalogue lists `bedrock`.
- An execution role with `AmazonECSTaskExecutionRolePolicy` plus `secretsmanager:GetSecretValue` on exactly the ARNs in `api_key_secret_arns`.
- The log group `/ecs/<name>` and, with `alarm_sns_topic_arns`, three alarms: `<name>-alb-5xx`, `<name>-target-5xx`, `<name>-unhealthy-hosts`.

## Keys and egress

Provider API keys reach the task only through the task definition's `secrets` (`HM_AGENT_ANTHROPIC_API_KEY`, `HM_AGENT_OPENAI_API_KEY`, `HM_AGENT_GOOGLE_API_KEY`), never `environment`. Bedrock uses the task role, with no key.

A secret encrypted with a customer-managed KMS key needs more than this module grants: the execution role must also be allowed `kms:Decrypt` on that key (add it to the role from outside the module, or allow the role in the key policy). Without it the task fails to start with an access-denied error on the secret.

The module sets `HM_AGENT_TRUSTED_PROXY=true`: the internal load balancer is the one proxy in front, and the agent reads the caller's address from the entry it appends to `X-Forwarded-For`. The agent's own default is `false`.

The tasks have outbound 443 to `0.0.0.0/0`, because security groups cannot filter by hostname and the providers' addresses change. "The agent dials only the configured providers and Harbormaster" is therefore a property of its code, not of the network. For a stricter fence, put your own egress filter in front of the NAT gateway (a firewall or proxy allowing only your providers), or use Bedrock only and replace the `task_to_internet` rule with VPC endpoints.

## Prerequisites

- The harbormaster module with `internal = true` (a precondition rejects `HM_AGENT_URL` otherwise), the agent's `task_security_group_id` in its `alb_ingress_security_group_ids` (this module opens no rule on Harbormaster's load balancer group; the harbormaster module owns that group's rules, so the ingress comes from that input), and `HM_AGENT_URL` in its `extra_env` set to this module's `agent_url`.
- `harbormaster_task_security_group_ids`: the harbormaster module's `security_group_ids.web` and `security_group_ids.coordinator` (the chat tasks and the coordinator, which runs investigations), or `security_group_ids.server` in single-service mode, the tasks that call the agent.
- A private DNS name for the agent that matches `certificate_arn`, pointing at `alb_dns_name`, passed as `hostname` so `agent_url` carries it. If the certificate comes from a private CA, set `HM_AGENT_CA_PEM` in the harbormaster module's `extra_env` to that CA's PEM.
- A private DNS name for `upstream_url` that matches Harbormaster's internal certificate.
- Private subnets that route to a NAT gateway; the agent image pinned to a tag or digest (no tag or digest, or `latest` in any case, is rejected), matching `cpu_architecture`.
- For each non-Bedrock provider, a Secrets Manager secret holding its API key as plain text.
- AWS provider 5.82 or newer (below 7.0), Terraform 1.5 or newer.

## Inputs

| Variable | Type | Default | Notes |
|---|---|---|---|
| `name` | string | `harbormaster-agent` | Prefix of every resource name; lowercase, digits, hyphens. |
| `vpc_id` | string | required | |
| `private_subnet_ids` | list(string) | required | Load balancer and tasks. |
| `certificate_arn` | string | required | ACM certificate for the private hostname. |
| `hostname` | string | `null` | Private DNS name for the `agent_url` output; null uses the load balancer's own name. |
| `cluster_arn` | string | required | ECS cluster (the Harbormaster module's). |
| `image` | string | required | Must end in a tag or digest; `latest` is refused in any case. |
| `cpu_architecture` | string | `X86_64` | `X86_64` or `ARM64`. |
| `cpu` / `memory` | number | `512` / `1024` | Valid Fargate pair; also set on the container. |
| `desired_count` | number | `2` | |
| `upstream_url` | string | required | `HM_AGENT_UPSTREAM_URL`; `https://`. |
| `upstream_ca_pem` | string | `null` | `HM_AGENT_UPSTREAM_CA_PEM`, a plain value (a CA certificate is public). |
| `harbormaster_alb_security_group_id` | string | required | The harbormaster module's `security_group_ids.alb`. |
| `harbormaster_task_security_group_ids` | list(string) | required | The only callers of the load balancer: the web and coordinator groups (`server` in single-service mode); at least one. |
| `model_providers` | list(object) | required | The catalogue, serialised as `HM_AGENT_PROVIDERS`: `provider` (`anthropic`, `openai`, `google`, `bedrock`) and `models` (`id`, `input_per_mtok_usd` and `output_per_mtok_usd` as decimal strings, `context_tokens`). |
| `api_key_secret_arns` | map(string) | `{}` | Provider (`anthropic`, `openai`, `google`) to Secrets Manager ARN. Must match the non-Bedrock providers exactly. |
| `bedrock_region` | string | `null` | `HM_AGENT_BEDROCK_REGION`; required with a `bedrock` provider. |
| `bedrock_model_arns` | list(string) | `[]` | Required (at least one) with a `bedrock` provider. |
| `limits` | object | 20 steps, 200000 tokens, `5m`, 8 concurrent runs, 100000 result bytes | `max_steps`, `max_run_tokens`, `run_timeout` (at most `5m`), `max_concurrent_runs`, `max_result_bytes`. |
| `alarm_sns_topic_arns` | list(string) | `[]` | Empty creates no alarms. |
| `log_retention_days` | number | `30` | |
| `tags` | map(string) | `{}` | |

The input is named `model_providers`, not `providers`: Terraform reserves `providers` as a module argument.

## Outputs

| Output | Meaning |
|---|---|
| `agent_url` | Set as `HM_AGENT_URL` in the harbormaster module's `extra_env`. |
| `alb_dns_name` | Point the private hostname at it. |
| `task_security_group_id` | Pass to the harbormaster module's `alb_ingress_security_group_ids`. |
| `alb_security_group_id` | The load balancer's security group. |
| `task_role_arn` | Task role (Bedrock invocation only, if used). |
| `log_group_name` | Agent log group. |

## Plans and applies

Run plan and apply with your Terraform workflow. To check changes locally without touching AWS, run `terraform fmt` and `terraform validate` (`make tf-check`).
