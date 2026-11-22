# harbormaster-mcp module

Deploys the Harbormaster MCP gateway (`harbormaster-mcp`) on AWS: an ECS Fargate service behind a public Application Load Balancer protected by AWS WAF. The gateway speaks MCP and forwards each tool call, with the caller's own personal access token, to one read-only route of Harbormaster's internal load balancer.

## What it creates

- A public Application Load Balancer (HTTPS on 443 with `ELBSecurityPolicy-TLS13-1-2-2021-06`, optional redirect from 80) and its security group.
- An ECS Fargate service (private subnets, no public IP, deployment circuit breaker with rollback) in an existing cluster.
- A task security group with no inline rules, plus separate rules: ingress on 8481 from the load balancer, egress on 443 to Harbormaster's load balancer group, egress on 443 to `0.0.0.0/0`.
- A task role with no policies and an execution role with `AmazonECSTaskExecutionRolePolicy` only.
- Log groups: `/ecs/<name>` and `aws-waf-logs-<name>`.
- A WAFv2 web ACL on the load balancer: a per-IP rate rule, optionally a stricter rate rule for `/oauth/`, the AWS IP reputation and known bad inputs rule sets, and the common rule set with the body rules in count mode. The `authorization` and `cookie` headers are redacted from the WAF log.

## What it holds, and what it does not

The gateway holds no credentials. The task definition has no `secrets`, the task role has no policy, and the execution role can only pull the image and write logs. Each request carries the caller's own token, which the gateway forwards and never stores or logs.

The tasks do have outbound 443 to `0.0.0.0/0` through the NAT gateway (image pull, logs and the upstream). "The gateway only talks to Harbormaster" is therefore a property of its code (the import fence and the upstream client), not of the network. Deleting the `task_to_internet` rule is the first step if you later move to VPC endpoints.

## Prerequisites

- Private subnets that route to a NAT gateway, and public subnets for the load balancer.
- The Harbormaster module with `internal = true`, `alb_ingress_security_group_ids = [<this module>.task_security_group_id]`, and in `extra_env`: `HM_API_TOKENS = "true"` and `HM_MCP_PUBLIC_URL` set to this module's `public_url`.
- A private DNS name for `upstream_url` that matches the internal load balancer's certificate.
- A public hostname, an ACM certificate for it, and a DNS record pointing at `alb_dns_name` (alias with `alb_zone_id`). There are no defaults for `public_url` or `certificate_arn`.
- The gateway image in ECR, pinned to a tag or digest (`:latest` is rejected), matching `cpu_architecture`.
- AWS provider 5.82 or newer (below 7.0), Terraform 1.5 or newer.

## Inputs

| Variable | Type | Default | Notes |
|---|---|---|---|
| `name` | string | `harbormaster-mcp` | Prefix of every resource name. |
| `vpc_id` | string | required | |
| `public_subnet_ids` | list(string) | required | Load balancer. |
| `private_subnet_ids` | list(string) | required | Tasks; must route to a NAT gateway. |
| `certificate_arn` | string | required | ACM certificate for the public hostname. |
| `allowed_cidrs` | list(string) | `["0.0.0.0/0"]` | Open by decision, WAF protects it. Narrow only for the first rollout run. |
| `http_redirect` | bool | `false` | Open port 80 and redirect it to 443. Off by default so a client misconfigured with `http://` fails instead of sending its token in cleartext first. |
| `cluster_arn` | string | required | ECS cluster (the Harbormaster module's). |
| `image` | string | required | Must not end in `:latest`. |
| `cpu_architecture` | string | `X86_64` | `X86_64` or `ARM64`. |
| `cpu` / `memory` | number | `256` / `512` | Valid Fargate pair. |
| `desired_count` | number | `2` | |
| `upstream_url` | string | required | `HM_MCP_UPSTREAM_URL`; `https://`. |
| `public_url` | string | required | `HM_MCP_PUBLIC_URL`; `https://` host, no path. |
| `authorize_url` | string | `null` | `HM_MCP_AUTHORIZE_URL` (OAuth phase). |
| `allowed_origins` | list(string) | `[]` | `HM_MCP_ALLOWED_ORIGINS`. |
| `harbormaster_alb_security_group_id` | string | required | The harbormaster module's `security_group_ids.alb`. |
| `log_retention_days` | number | `30` | Both log groups. |
| `waf_rate_limit` | number | `300` | Requests per 5 minutes per IP; at least 10. |
| `waf_common_count_rules` | list(string) | `SizeRestrictions_BODY`, `CrossSiteScripting_BODY`, `GenericRFI_BODY` | Common rule set rules that only count. |
| `waf_oauth_rate_limit` | number | `60` | Per IP on `/oauth/`; used when `enable_oauth_paths`. |
| `enable_oauth_paths` | bool | `false` | Adds the `/oauth/` rate rule (OAuth phase). |
| `tags` | map(string) | `{}` | |

## Outputs

| Output | Meaning |
|---|---|
| `alb_dns_name`, `alb_zone_id`, `alb_arn` | The public load balancer. |
| `alb_security_group_id` | Load balancer security group. |
| `task_security_group_id` | Pass to the harbormaster module's `alb_ingress_security_group_ids`. |
| `service_name` | ECS service name. |
| `task_role_arn` | Task role (no policies). |
| `log_group_name`, `waf_log_group_name` | Gateway and WAF log groups. |
| `web_acl_arn` | The web ACL. |

## WAF tuning

Blocked and counted requests are in the `aws-waf-logs-<name>` log group (`action` is `BLOCK` or `COUNT`, with `terminatingRuleId` and any `ruleGroupList` match). The common rule set's body rules count rather than block, because a `search_logs` query can look like an injection. Move a rule out of `waf_common_count_rules` only after a clean week in the log. If a legitimate client is rate limited, raise `waf_rate_limit` rather than removing the rule.

## Plans and applies

Plans and applies run in TerraDuckTel. Locally, run only `terraform fmt` and `terraform validate` (`make tf-check`).
