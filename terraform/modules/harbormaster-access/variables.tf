variable "harbormaster_task_role_arn" {
  description = "ARN of the Harbormaster ECS task role (output of the harbormaster module) that is allowed to assume this role."
  type        = string

  validation {
    condition     = can(regex("^arn:aws:iam::[0-9]{12}:role/", var.harbormaster_task_role_arn))
    error_message = "harbormaster_task_role_arn must be an IAM role ARN (arn:aws:iam::<12-digit account>:role/...)."
  }
}

variable "external_id" {
  description = "External ID required in the trust policy. Copy the generated value from Harbormaster (AWS accounts > Connect account)."
  type        = string

  validation {
    condition     = length(var.external_id) >= 2 && length(var.external_id) <= 1224
    error_message = "external_id must be between 2 and 1224 characters."
  }

  validation {
    condition     = can(regex("^[\\w+=,.@:/-]+$", var.external_id))
    error_message = "external_id may only contain letters, digits and the characters _+=,.@:/-"
  }
}

variable "role_name" {
  description = "Name of the IAM role Harbormaster assumes."
  type        = string
  default     = "harbormaster-access"

  validation {
    condition     = can(regex("^[a-z0-9-]+$", var.role_name)) && length(var.role_name) <= 64
    error_message = "role_name must be at most 64 characters of lowercase letters, digits and hyphens."
  }
}

variable "pass_role_arns" {
  description = "Role ARN patterns Harbormaster may pass to ECS tasks. Null (default) resolves to arn:aws:iam::<this account>:role/*-task and arn:aws:iam::<this account>:role/ecs-task-execution."
  type        = list(string)
  default     = null
}

variable "read_only" {
  description = "When true, the write policy is omitted and Harbormaster can only inspect the account."
  type        = bool
  default     = false
}

variable "tags" {
  description = "Tags applied to the IAM role."
  type        = map(string)
  default     = {}
}
