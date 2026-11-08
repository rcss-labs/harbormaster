terraform {
  required_version = ">= 1.5.0"
}

provider "aws" {
  region = "eu-west-1"
}

# Placeholder values only. Copy the real task role ARN from the harbormaster example output
# and the external ID from Harbormaster's Connect account form.
module "harbormaster_access" {
  source = "../../modules/harbormaster-access"

  harbormaster_task_role_arn = "arn:aws:iam::123456789012:role/harbormaster-server-task"
  external_id                = "replace-with-the-id-shown-in-harbormaster"
  # scanner_task_role_arn      = "arn:aws:iam::123456789012:role/harbormaster-scanner-task"
}

output "role_arn" {
  description = "Access role ARN to paste into Harbormaster's Connect account form."
  value       = module.harbormaster_access.role_arn
}
