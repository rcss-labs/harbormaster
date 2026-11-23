# Terraform module: harbormaster-mcp

The `harbormaster-mcp` module runs the MCP gateway: a small, public, read-only service that lets AI tools query Harbormaster with a personal access token. This page lists every input and output, what the module creates and which permissions it holds. Use it when you deploy or review the gateway.

The module source is `terraform/modules/harbormaster-mcp`. For the install steps, see [MCP gateway](../install/mcp-gateway.md). For day-to-day operation, see the [MCP gateway runbook](../operations/runbooks/mcp-gateway.md).

!!! note "Where plans run"
    `terraform plan` and `terraform apply` run in your deployment system, not on a workstation. Locally, run only `terraform fmt` and `terraform validate`.

## Requirements

- Terraform 1.5 or newer and AWS provider 5.82 or newer (below 7.0).
- Private subnets that route to a NAT gateway (image pull, logs and the upstream), and public subnets for the load balancer.
- The `harbormaster` module with `internal = true`, `alb_ingress_security_group_ids` set to this module's `task_security_group_id`, and in `extra_env`: `HM_API_TOKENS = "true"`, `HM_MCP_PUBLIC_URL` (this module's `public_url`) and `HM_MCP_GATEWAY_CIDRS` (the CIDRs of the subnets the gateway tasks run in).
- A private DNS name for `upstream_url` that matches the internal load balancer's certificate.
- A public hostname, an ACM certificate for it and a DNS record that points at `alb_dns_name`.
- A gateway image pinned to a tag or digest (`:latest` is rejected) that matches `cpu_architecture`.

!!! warning "Keep Harbormaster internal"
    With `internal = false` on the `harbormaster` module and `HM_API_TOKENS` on, personal tokens are accepted on Harbormaster's own public load balancer. That bypasses the gateway's WAF and rate limits.

## Inputs

A variable with no default is required.

| Name | Type | Default | Required | Description |
| --- | --- | --- | --- | --- |
| `name` | `string` | `"harbormaster-mcp"` | no | Prefix of every resource name. |
| `vpc_id` | `string` | none | yes | VPC to run in. |
| `public_subnet_ids` | `list(string)` | none | yes | Public subnets for the load balancer. |
| `private_subnet_ids` | `list(string)` | none | yes | Private subnets for the tasks. They must route to a NAT gateway (image pull, logs and the upstream). |
| `certificate_arn` | `string` | none | yes | ACM certificate for the public hostname. Required: there is no default. |
| `allowed_cidrs` | `list(string)` | `["0.0.0.0/0"]` | no | Who may reach the public load balancer. Open to the internet by decision (WAF is the protection); narrow it only for the first rollout run. |
| `http_redirect` | `bool` | `false` | no | Open port 80 and redirect it to 443. Off by default: an MCP client misconfigured with http:// would send its bearer token in cleartext before the redirect, so the gateway fails closed instead. |
| `cluster_arn` | `string` | none | yes | ARN of the ECS cluster to run in (the Harbormaster module's cluster). |
| `image` | `string` | none | yes | Gateway image, pinned to a tag or digest. A tag ending in :latest is refused. |
| `cpu_architecture` | `string` | `"X86_64"` | no | Task CPU architecture; it must match the image (X86_64 or ARM64). |
| `cpu` | `number` | `256` | no | Fargate CPU units per task. |
| `memory` | `number` | `512` | no | Fargate memory (MiB) per task. |
| `desired_count` | `number` | `2` | no | Number of gateway tasks. |
| `upstream_url` | `string` | none | yes | HM_MCP_UPSTREAM_URL: Harbormaster's internal address. It must match the internal certificate's name. |
| `public_url` | `string` | none | yes | HM_MCP_PUBLIC_URL: the public hostname of the gateway, for example https://mcp.example.com. Required: there is no default. |
| `authorize_url` | `string` | `null` | no | HM_MCP_AUTHORIZE_URL (OAuth phase): where the gateway sends users to consent. Null leaves it unset. |
| `authorize_via_gateway` | `bool` | `false` | no | HM_MCP_AUTHORIZE_VIA_GATEWAY: redirect-through-gateway fallback for clients that refuse an authorization endpoint on another host. Turn it on only if the claude.ai spike needs it. Requires authorize_url. |
| `max_result_bytes` | `number` | `null` | no | HM_MCP_MAX_RESULT_BYTES: cap on a tool result's size. Null uses the gateway default. |
| `upstream_timeout` | `string` | `null` | no | HM_MCP_UPSTREAM_TIMEOUT: per-request timeout toward Harbormaster, a Go duration such as 20s. Null uses the gateway default. |
| `upstream_ca_pem` | `string` | `null` | no | HM_MCP_UPSTREAM_CA_PEM: PEM of extra CAs that sign the internal load balancer's certificate. A CA certificate is public, so it is passed as a plain environment value (not a secret), but it adds to the task definition's size. Not marked sensitive on purpose: that would mark the whole task definition sensitive and hide its image and environment changes in every plan. Null leaves it unset. |
| `alarm_sns_topic_arns` | `list(string)` | `[]` | no | SNS topics notified by the CloudWatch alarms. Empty creates no alarms. |
| `access_logs_bucket` | `string` | `null` | no | S3 bucket for the public load balancer's access logs. Null turns them off. The bucket and its policy (allowing the regional ELB log delivery) belong to the operator. |
| `access_logs_prefix` | `string` | `"harbormaster-mcp"` | no | Key prefix for the access logs in access_logs_bucket. |
| `deletion_protection` | `bool` | `true` | no | Deletion protection on the public load balancer. Set false before destroying the module. |
| `allowed_origins` | `list(string)` | `[]` | no | HM_MCP_ALLOWED_ORIGINS: browser origins the gateway accepts, joined with commas. |
| `harbormaster_alb_security_group_id` | `string` | none | yes | Security group of Harbormaster's internal load balancer (the egress target). Use the harbormaster module's security_group_ids.alb. |
| `log_retention_days` | `number` | `30` | no | Retention of the gateway log group. |
| `tags` | `map(string)` | `{}` | no | Tags for every resource that takes them. |
| `waf_rate_limit` | `number` | `300` | no | Requests per 5 minutes per source IP before WAF blocks the IP. |
| `waf_common_count_rules` | `list(string)` | `["SizeRestrictions_BODY", "CrossSiteScripting_BODY", "GenericRFI_BODY"]` | no | Rules of the AWS common rule set that only count. Body rules in block mode would refuse a search_logs call whose query looks like an injection. Move a rule out only after a clean week in the WAF log. |
| `waf_oauth_rate_limit` | `number` | `60` | no | Requests per 5 minutes per source IP on /oauth/ paths. Used only when enable_oauth_paths is true. |
| `enable_oauth_paths` | `bool` | `false` | no | Add the stricter per-IP rate rule for /oauth/ paths (OAuth phase). |

## Outputs

| Name | Description |
| --- | --- |
| `alb_dns_name` | DNS name of the public load balancer (point the public hostname at it). |
| `alb_zone_id` | Route 53 hosted zone ID of the load balancer (for alias records). |
| `alb_arn` | ARN of the public load balancer. |
| `task_security_group_id` | Security group of the gateway tasks. Pass it to the harbormaster module's alb_ingress_security_group_ids. |
| `alb_security_group_id` | Security group of the public load balancer. |
| `service_name` | Name of the gateway ECS service. |
| `task_role_arn` | ARN of the gateway task role (it has no policies). |
| `log_group_name` | CloudWatch log group of the gateway. |
| `web_acl_arn` | ARN of the WAF web ACL on the public load balancer. |
| `waf_log_group_name` | CloudWatch log group of the WAF log (blocked and counted requests). |
| `alarm_arns` | ARNs of the CloudWatch alarms by short name (alb-5xx, target-5xx, unhealthy-hosts, waf-blocked). Empty when alarm_sns_topic_arns is empty. |

## Resources the module creates

| Resource | Notes |
| --- | --- |
| Application Load Balancer (public) | HTTPS on 443 with policy `ELBSecurityPolicy-TLS13-1-2-2021-06`; port 80 redirect only when `http_redirect = true`; optional access logs; deletion protection on by default |
| Target group and listeners | Health check on the gateway's `/healthz` |
| ECS Fargate service and task definition | In the existing cluster `cluster_arn`; private subnets, no public IP; deployment circuit breaker with rollback; no `secrets` block |
| Security groups | ALB group (443 from `allowed_cidrs`); task group with no inline rules |
| Security group rules | Task ingress from the ALB group on the gateway port (8481); task egress on 443 to Harbormaster's ALB group; task egress on 443 to `0.0.0.0/0` (image pull, logs, upstream) |
| IAM roles | Task role with no policies; execution role with `AmazonECSTaskExecutionRolePolicy` only |
| CloudWatch log groups | `/ecs/<name>` and `aws-waf-logs-<name>` (retention `log_retention_days`) |
| WAFv2 web ACL, association and logging | See below; the `authorization` and `cookie` headers are redacted from the WAF log |
| CloudWatch alarms | Four alarms, only when `alarm_sns_topic_arns` is not empty |

### WAF rules

| Priority | Rule | Action |
| --- | --- | --- |
| 10 | `rate-per-ip`: `waf_rate_limit` requests per 5 minutes per source IP | Block |
| 15 | `rate-oauth-per-ip`: `waf_oauth_rate_limit` requests per 5 minutes per IP on `/oauth/` paths (only when `enable_oauth_paths = true`) | Block |
| 20 | AWS managed IP reputation list | Block |
| 30 | AWS managed known bad inputs | Block |
| 40 | AWS managed common rule set; the rules in `waf_common_count_rules` count only | Block, with those rules in count mode |

### Alarms

Created only with `alarm_sns_topic_arns` set. Each alarm uses a 5-minute period, treats missing data as not breaching and notifies on alarm and on recovery. The thresholds are fixed.

| Alarm | Metric | Threshold |
| --- | --- | --- |
| `<name>-alb-5xx` | `HTTPCode_ELB_5XX_Count` (Sum) | 5 or more |
| `<name>-target-5xx` | `HTTPCode_Target_5XX_Count` (Sum) | 5 or more |
| `<name>-unhealthy-hosts` | `UnHealthyHostCount` (Maximum) | 1 or more |
| `<name>-waf-blocked` | WAF `BlockedRequests` (Sum, all rules) | 100 or more |

## IAM permissions granted

The gateway holds no AWS permissions and no credentials.

| Role | Permissions |
| --- | --- |
| Task role `<name>-task` | None. No policy is attached |
| Execution role `<name>-execution` | `AmazonECSTaskExecutionRolePolicy` (image pull and log write) only |

Each request carries the caller's own personal token. The gateway forwards it to Harbormaster and never stores or logs it.

The tasks can reach `0.0.0.0/0` on 443 through the NAT gateway. "The gateway only talks to Harbormaster" is a property of its code, not of the network. Delete the `task_to_internet` rule if you later move to VPC endpoints.

## Minimal usage example

This is the shape of `terraform/examples/harbormaster-mcp/main.tf`, with placeholders. It runs Harbormaster and the gateway in one root. Replace every `<...>` value.

```hcl
module "harbormaster" {
  source = "../../modules/harbormaster"

  vpc_id             = "<vpc-id>"
  private_subnet_ids = ["<subnet-a>", "<subnet-b>"]
  certificate_arn    = "<internal-acm-certificate-arn>"
  image              = "<registry>/harbormaster:<version>"
  base_url           = "https://harbormaster.internal.example.com"
  internal           = true
  allowed_cidrs      = ["10.0.0.0/8"]

  alb_ingress_security_group_ids = [module.harbormaster_mcp.task_security_group_id]

  extra_env = {
    HM_API_TOKENS        = "true"
    HM_MCP_PUBLIC_URL    = "https://mcp.example.com"
    HM_MCP_GATEWAY_CIDRS = "<cidr-of-gateway-subnet-a>,<cidr-of-gateway-subnet-b>"
  }
}

data "aws_ecs_cluster" "harbormaster" {
  cluster_name = module.harbormaster.cluster_name
}

module "harbormaster_mcp" {
  source = "../../modules/harbormaster-mcp"

  vpc_id             = "<vpc-id>"
  public_subnet_ids  = ["<public-subnet-a>", "<public-subnet-b>"]
  private_subnet_ids = ["<gateway-subnet-a>", "<gateway-subnet-b>"]
  certificate_arn    = "<public-acm-certificate-arn>"
  cluster_arn        = data.aws_ecs_cluster.harbormaster.arn
  image              = "<registry>/harbormaster-mcp:<version>"
  upstream_url       = "https://harbormaster.internal.example.com"
  public_url         = "https://mcp.example.com"

  harbormaster_alb_security_group_id = module.harbormaster.security_group_ids.alb
}
```

Point your public DNS name at `alb_dns_name` (an alias record using `alb_zone_id`).

## Notes

- **OAuth is off by default.** To let claude.ai and Claude Desktop connect, set `authorize_url` and `enable_oauth_paths = true` on this module, and add `HM_OAUTH = "true"` to the Harbormaster module's `extra_env`. The module does not set `HM_OAUTH`. See the [MCP gateway runbook](../operations/runbooks/mcp-gateway.md) before enabling it.
- **`allowed_cidrs` defaults to the internet.** The gateway is public by design and the WAF is the protection. Narrow it to your own address for the first rollout.
- **`http_redirect` is off by default.** A client misconfigured with `http://` then fails instead of sending its token in cleartext before the redirect.
- **Destroying the module.** Set `deletion_protection = false` and apply first.
- **WAF tuning.** Move a rule out of `waf_common_count_rules` only after a clean week in the WAF log. If a legitimate client is rate limited, raise `waf_rate_limit` rather than removing the rule.
