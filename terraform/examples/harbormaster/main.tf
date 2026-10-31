terraform {
  required_version = ">= 1.5.0"
}

provider "aws" {
  region = "eu-west-1"
}

# Placeholder values only. Replace them with the real network, certificate and image details.
locals {
  vpc_id             = "vpc-0123456789abcdef0"
  private_subnet_ids = ["subnet-0123456789abcdef0", "subnet-0123456789abcdef1"]
  public_subnet_ids  = ["subnet-0123456789abcdef2", "subnet-0123456789abcdef3"]
  certificate_arn    = "arn:aws:acm:eu-west-1:123456789012:certificate/00000000-0000-0000-0000-000000000000"
  image              = "123456789012.dkr.ecr.eu-west-1.amazonaws.com/harbormaster:v1.0.0"
  base_url           = "https://harbormaster.example.com"
}

module "harbormaster" {
  source = "../../modules/harbormaster"

  vpc_id             = local.vpc_id
  private_subnet_ids = local.private_subnet_ids
  public_subnet_ids  = local.public_subnet_ids
  certificate_arn    = local.certificate_arn
  image              = local.image
  base_url           = local.base_url
}

output "task_role_arn" {
  description = "Task role ARN to pass to the harbormaster-access module."
  value       = module.harbormaster.task_role_arn
}

output "alb_dns_name" {
  description = "DNS name of the Harbormaster load balancer."
  value       = module.harbormaster.alb_dns_name
}
