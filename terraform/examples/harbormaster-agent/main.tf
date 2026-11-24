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

# Placeholder values only. Replace them with the real network, certificate, secret and image details.
locals {
  vpc_id             = "vpc-0123456789abcdef0"
  private_subnet_ids = ["subnet-0123456789abcdef0", "subnet-0123456789abcdef1"]
  certificate_arn    = "arn:aws:acm:eu-west-1:123456789012:certificate/00000000-0000-0000-0000-000000000000"
  agent_certificate  = "arn:aws:acm:eu-west-1:123456789012:certificate/11111111-1111-1111-1111-111111111111"
  image              = "123456789012.dkr.ecr.eu-west-1.amazonaws.com/harbormaster:v1.0.0"
  agent_image        = "123456789012.dkr.ecr.eu-west-1.amazonaws.com/harbormaster-agent:v1.0.0"
  base_url           = "https://harbormaster.internal.example.com"
  agent_hostname     = "agent.internal.example.com"
}

# Harbormaster on an internal load balancer (required for the agent). The agent's task group may reach it on 443.
module "harbormaster" {
  source = "../../modules/harbormaster"

  vpc_id             = local.vpc_id
  private_subnet_ids = local.private_subnet_ids
  certificate_arn    = local.certificate_arn
  image              = local.image
  base_url           = local.base_url
  internal           = true
  allowed_cidrs      = ["10.0.0.0/8"]

  alb_ingress_security_group_ids = [module.harbormaster_agent.task_security_group_id]

  extra_env = {
    HM_AGENT_URL = module.harbormaster_agent.agent_url
    # Only for an agent certificate issued by a private CA: that CA's PEM.
    # HM_AGENT_CA_PEM = file("agent-ca.pem")
  }
}

data "aws_ecs_cluster" "harbormaster" {
  cluster_name = module.harbormaster.cluster_name
}

# The agent. Create a private DNS record for local.agent_hostname pointing at alb_dns_name.
module "harbormaster_agent" {
  source = "../../modules/harbormaster-agent"

  vpc_id             = local.vpc_id
  private_subnet_ids = local.private_subnet_ids
  certificate_arn    = local.agent_certificate
  hostname           = local.agent_hostname
  cluster_arn        = data.aws_ecs_cluster.harbormaster.arn
  image              = local.agent_image
  upstream_url       = local.base_url

  harbormaster_alb_security_group_id   = module.harbormaster.security_group_ids.alb
  harbormaster_task_security_group_ids = [module.harbormaster.security_group_ids.web]

  # Prices and context sizes are the operator's to keep current.
  model_providers = [
    {
      provider = "anthropic"
      models = [
        {
          id                  = "example-model-id"
          input_per_mtok_usd  = "3.00"
          output_per_mtok_usd = "15.00"
          context_tokens      = 200000
        }
      ]
    }
  ]

  # The secret holds the provider's API key as its plain-text value; it belongs to the operator.
  api_key_secret_arns = {
    anthropic = "arn:aws:secretsmanager:eu-west-1:123456789012:secret:harbormaster-agent-anthropic-AbCdEf"
  }
}

output "agent_alb_dns_name" {
  description = "DNS name of the agent's internal load balancer. Point the private hostname at it."
  value       = module.harbormaster_agent.alb_dns_name
}
