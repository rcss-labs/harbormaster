terraform {
  required_version = ">= 1.5.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = ">= 5.82, < 7.0"
    }
  }
}

provider "aws" {
  region = "eu-west-1"
}

# Placeholder values only. Replace them with the real network, certificate and image details.
locals {
  vpc_id = "vpc-0123456789abcdef0"
  # Harbormaster's own tasks and load balancer.
  private_subnet_ids = ["subnet-0123456789abcdef0", "subnet-0123456789abcdef1"]
  public_subnet_ids  = ["subnet-0123456789abcdef2", "subnet-0123456789abcdef3"]
  internal_cert_arn  = "arn:aws:acm:eu-west-1:123456789012:certificate/00000000-0000-0000-0000-000000000000"
  public_cert_arn    = "arn:aws:acm:eu-west-1:123456789012:certificate/11111111-1111-1111-1111-111111111111"
  image              = "123456789012.dkr.ecr.eu-west-1.amazonaws.com/harbormaster:v1.0.0"
  mcp_image          = "123456789012.dkr.ecr.eu-west-1.amazonaws.com/harbormaster-mcp:v1.0.0"
  base_url           = "https://harbormaster.internal.example.com"
  mcp_public_url     = "https://mcp.example.com"
}

variable "gateway_private_subnet_ids" {
  description = "Private subnets for the gateway tasks. Use dedicated subnets (not Harbormaster's): every task inside their CIDRs is trusted via HM_MCP_GATEWAY_CIDRS to name a client IP. They must route to a NAT gateway."
  type        = list(string)
  default     = ["subnet-0123456789abcdef4", "subnet-0123456789abcdef5"] # placeholders
}

locals {
  harbormaster_internal = true
}

# The gateway needs Harbormaster on an internal load balancer. With internal = false, HM_API_TOKENS would accept
# personal tokens on Harbormaster's own public ALB, bypassing the gateway's WAF and rate limiter.
resource "terraform_data" "require_internal_harbormaster" {
  lifecycle {
    precondition {
      condition     = local.harbormaster_internal
      error_message = "The harbormaster module must have internal = true when HM_API_TOKENS is on; otherwise tokens are accepted on its public ALB and bypass the gateway's WAF and limiter."
    }
  }
}

# The gateway tasks run in gateway_private_subnet_ids. Harbormaster reads the client IP one hop further left of the
# internal load balancer's entry when that entry is in these ranges.
data "aws_subnet" "gateway" {
  for_each = toset(var.gateway_private_subnet_ids)
  id       = each.value
}

# Harbormaster itself, on an internal load balancer. The gateway's task security group may reach it on 443.
module "harbormaster" {
  source = "../../modules/harbormaster"

  vpc_id             = local.vpc_id
  private_subnet_ids = local.private_subnet_ids
  certificate_arn    = local.internal_cert_arn
  image              = local.image
  base_url           = local.base_url
  internal           = local.harbormaster_internal
  # Keep Harbormaster private: reach it over VPN or from the office. List your own ranges.
  allowed_cidrs = ["10.0.0.0/8"]

  alb_ingress_security_group_ids = [module.harbormaster_mcp.task_security_group_id]

  extra_env = {
    HM_API_TOKENS        = "true"
    HM_MCP_PUBLIC_URL    = local.mcp_public_url
    HM_MCP_GATEWAY_CIDRS = join(",", sort([for subnet in data.aws_subnet.gateway : subnet.cidr_block]))
    # OAuth (claude.ai / Claude Desktop connectors) is off until the real-connector spike has run; see "OAuth spike" in
    # docs/ops/mcp-runbook.md. To enable it later, uncomment this line (needs HM_API_TOKENS and HM_MCP_PUBLIC_URL) together
    # with authorize_url and enable_oauth_paths on the gateway module below.
    # HM_OAUTH = "true"
  }
}

# The module outputs the cluster's name; the gateway module takes its ARN.
data "aws_ecs_cluster" "harbormaster" {
  cluster_name = module.harbormaster.cluster_name
}

# The public gateway. Create the DNS record for local.mcp_public_url pointing at alb_dns_name (alias with alb_zone_id).
module "harbormaster_mcp" {
  source = "../../modules/harbormaster-mcp"

  vpc_id             = local.vpc_id
  public_subnet_ids  = local.public_subnet_ids
  private_subnet_ids = var.gateway_private_subnet_ids
  certificate_arn    = local.public_cert_arn
  cluster_arn        = data.aws_ecs_cluster.harbormaster.arn
  image              = local.mcp_image
  upstream_url       = local.base_url
  public_url         = local.mcp_public_url

  # OAuth for connectors is off in this example (personal tokens only) until the real-connector spike has run; see the
  # OAuth spike section of docs/ops/mcp-runbook.md. To enable it, also uncomment HM_OAUTH on the harbormaster module:
  # the browser is sent to Harbormaster's private URL to approve (VPN only), and /oauth/ paths get a stricter rate rule.
  # authorize_url      = local.base_url
  # enable_oauth_paths = true

  harbormaster_alb_security_group_id = module.harbormaster.security_group_ids.alb
}

output "mcp_alb_dns_name" {
  description = "DNS name of the public MCP load balancer. Point the public hostname at it."
  value       = module.harbormaster_mcp.alb_dns_name
}

output "mcp_alb_zone_id" {
  description = "Hosted zone ID of the public MCP load balancer (for an alias record)."
  value       = module.harbormaster_mcp.alb_zone_id
}

output "mcp_web_acl_arn" {
  description = "ARN of the WAF web ACL on the public MCP load balancer."
  value       = module.harbormaster_mcp.web_acl_arn
}
