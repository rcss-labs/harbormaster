terraform {
  required_version = ">= 1.5.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = ">= 5.60, < 7.0"
    }
  }
}

provider "aws" {
  region = "eu-west-1"
}

# Placeholder values only. Copy the real task role ARNs from the harbormaster example outputs
# and the external ID from Harbormaster's Connect account form. Apply the harbormaster module first: IAM
# rejects a trust policy that names a role that does not exist.
module "harbormaster_access" {
  source = "../../modules/harbormaster-access"

  harbormaster_task_role_arn = "arn:aws:iam::123456789012:role/harbormaster-server-task" # null after the legacy service is removed
  web_task_role_arn          = "arn:aws:iam::123456789012:role/harbormaster-web-task"
  poller_task_role_arn       = "arn:aws:iam::123456789012:role/harbormaster-poller-task"
  coordinator_task_role_arn  = "arn:aws:iam::123456789012:role/harbormaster-coordinator-task"
  external_id                = "replace-with-the-id-shown-in-harbormaster"
  # scanner_task_role_arn      = "arn:aws:iam::123456789012:role/harbormaster-scanner-task"
}

output "role_arn" {
  description = "Access role ARN to paste into Harbormaster's Connect account form."
  value       = module.harbormaster_access.role_arn
}

output "poller_role_arn" {
  description = "Poller access role ARN (Harbormaster derives it from the stored role; nothing to paste)."
  value       = module.harbormaster_access.poller_role_arn
}

output "coordinator_role_arn" {
  description = "Coordinator access role ARN (Harbormaster derives it from the stored role; nothing to paste)."
  value       = module.harbormaster_access.coordinator_role_arn
}
