# MCP gateway

This page shows how to deploy the Harbormaster MCP gateway with the `harbormaster-mcp` Terraform module. It is for the engineer who owns the Harbormaster deployment. To use the gateway from an AI tool, see [AI access with MCP](../guides/ai-access-mcp.md).

The gateway is a small, read-only service that lets AI tools such as Claude Code read Harbormaster. It holds no credentials, database or cache access, or AWS permissions. It is the only public piece: Harbormaster stays on an internal load balancer.

## Before you begin

- Harbormaster is deployed with `internal = true`. This is required. With an internet-facing ALB, personal tokens would be accepted on Harbormaster's own public load balancer and bypass the gateway's WAF and rate limiter. A plan fails otherwise.
- Access tokens are enabled on Harbormaster (`HM_API_TOKENS=true`). See [Access tokens](../guides/access-tokens.md).
- The gateway image is built and pushed to ECR (see [Build the image](build-image.md)), pinned to a tag or digest. `:latest` is rejected, and the architecture must match `cpu_architecture`.
- Private subnets that route to a NAT gateway, and public subnets for the load balancer. Give the gateway its own private subnets where you can: every task inside their CIDRs is trusted to name the client IP.
- A public hostname, an ACM certificate for it, and a DNS record you can point at the gateway's load balancer. The module has no defaults for `public_url` or `certificate_arn`.
- A private DNS name for `upstream_url` that matches the internal load balancer's certificate.

## Steps

1. Configure the `harbormaster` module for the gateway. Pass the gateway's task security group, turn on tokens, and give Harbormaster the gateway's public URL and subnet CIDRs.

    ```hcl
    module "harbormaster" {
      source = "github.com/rcss-labs/harbormaster//terraform/modules/harbormaster?ref=v1.0.0"
      # ... required inputs as in "Deploy with Terraform", with internal = true ...

      alb_ingress_security_group_ids = [module.harbormaster_mcp.task_security_group_id]

      extra_env = {
        HM_API_TOKENS        = "true"
        HM_MCP_PUBLIC_URL    = "https://mcp.example.com"
        HM_MCP_GATEWAY_CIDRS = "<cidr-of-gateway-subnet-1>,<cidr-of-gateway-subnet-2>"
      }
    }
    ```

    `HM_MCP_GATEWAY_CIDRS` lets Harbormaster record the real client IP instead of the gateway's. The IP is used for records and rate limiting, never for authorization. The example derives the CIDRs from the subnets with a `data "aws_subnet"` lookup.

2. Add the gateway module.

    ```hcl
    data "aws_ecs_cluster" "harbormaster" {
      cluster_name = module.harbormaster.cluster_name
    }

    module "harbormaster_mcp" {
      source = "github.com/rcss-labs/harbormaster//terraform/modules/harbormaster-mcp?ref=v1.0.0"

      vpc_id             = "vpc-0123456789abcdef0"
      public_subnet_ids  = ["subnet-0123456789abcdef2", "subnet-0123456789abcdef3"]
      private_subnet_ids = ["subnet-0123456789abcdef4", "subnet-0123456789abcdef5"]
      certificate_arn    = "arn:aws:acm:<region>:<account-id>:certificate/<public-certificate-id>"
      cluster_arn        = data.aws_ecs_cluster.harbormaster.arn
      image              = "<account-id>.dkr.ecr.<region>.amazonaws.com/harbormaster-mcp:v1.0.0"
      upstream_url       = "https://harbormaster.internal.example.com"
      public_url         = "https://mcp.example.com"

      harbormaster_alb_security_group_id = module.harbormaster.security_group_ids.alb
    }
    ```

    The repository's `terraform/examples/harbormaster-mcp/main.tf` is a complete example.

3. Run plan and apply with your usual Terraform workflow.

4. Create a DNS record for the `public_url` hostname that points at the module's `alb_dns_name` output (an alias record with `alb_zone_id` if you use Route 53).

## What the module creates

- A public Application Load Balancer (HTTPS, TLS 1.3 policy) and its security group. The HTTP redirect is off by default, so a client misconfigured with `http://` fails instead of sending its token in cleartext.
- An ECS Fargate service in the Harbormaster cluster, in private subnets with no public IP, and a deployment circuit breaker with rollback.
- A task role with no policies, and an execution role that can only pull the image and write logs.
- Log groups `/ecs/<name>` and `aws-waf-logs-<name>`.
- A WAFv2 web ACL on the load balancer: a per-IP rate rule, AWS IP reputation and known bad inputs rule sets, and the common rule set with the body rules in count mode. The `authorization` and `cookie` headers are redacted from the WAF log.

## Key inputs

| Variable | Default | Description |
| --- | --- | --- |
| `vpc_id`, `public_subnet_ids`, `private_subnet_ids` | required | Network placement. Tasks need a NAT route. |
| `certificate_arn` | required | ACM certificate for the public hostname. |
| `cluster_arn` | required | The Harbormaster module's ECS cluster. |
| `image` | required | Gateway image. Must not end in `:latest`. |
| `upstream_url` | required | Harbormaster's internal `https://` URL. |
| `public_url` | required | The gateway's public `https://` URL, with no path. |
| `harbormaster_alb_security_group_id` | required | The `harbormaster` module's `security_group_ids.alb`. |
| `desired_count` | `2` | Gateway tasks. |
| `waf_rate_limit` | `300` | Requests per 5 minutes per IP. |
| `deletion_protection` | `true` | Load balancer deletion protection. Set `false` and apply before destroying. |
| `alarm_sns_topic_arns` | `[]` | SNS topics for the four alarms. Empty creates none. |

The full table is in the [MCP module reference](../reference/terraform-mcp.md).

## OAuth for claude.ai and Claude Desktop (optional)

OAuth is off in the example. Personal access tokens work without it. To enable OAuth connectors, set `authorize_url` and `enable_oauth_paths = true` on the gateway module, and add `HM_OAUTH = "true"` to the Harbormaster module's `extra_env`. Harbormaster's approval page stays on the private network, so users need the VPN at approval time. See [AI access with MCP](../guides/ai-access-mcp.md) and the [MCP gateway runbook](../operations/runbooks/mcp-gateway.md).

<!-- TODO(docs): the module README says to run an "OAuth spike" (docs/ops/mcp-runbook.md) with a real connector before enabling OAuth; confirm whether that still applies and link the runbook section -->

## Check it worked

- The gateway answers `GET /healthz` at the public URL.
- The ECS service reaches its desired count and the load balancer's targets are healthy.
- A user with a token can connect an MCP client to `https://<public-host>/mcp` (see [AI access with MCP](../guides/ai-access-mcp.md)).

## Next steps

- [AI access with MCP](../guides/ai-access-mcp.md)
- [Access tokens](../guides/access-tokens.md)
- [MCP gateway runbook](../operations/runbooks/mcp-gateway.md)
