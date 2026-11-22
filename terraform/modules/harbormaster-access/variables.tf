variable "harbormaster_task_role_arn" {
  description = "ARN of the legacy single Harbormaster ECS task role (output task_role_arn of the harbormaster module) that is allowed to assume the access role. Keep it while the single service runs; set it to null once keep_legacy_service is false in the harbormaster module. At least one of this and web_task_role_arn is required."
  type        = string
  default     = null

  validation {
    condition     = var.harbormaster_task_role_arn == null || can(regex("^arn:aws:iam::[0-9]{12}:role/", var.harbormaster_task_role_arn))
    error_message = "harbormaster_task_role_arn must be null or an IAM role ARN (arn:aws:iam::<12-digit account>:role/...)."
  }
}

variable "web_task_role_arn" {
  description = "ARN of the Harbormaster web task role (output web_task_role_arn of the harbormaster module). It is trusted by the access role next to the legacy role. Null (default) for installs that have not split."
  type        = string
  default     = null

  validation {
    condition     = var.web_task_role_arn == null || can(regex("^arn:aws:iam::[0-9]{12}:role/", var.web_task_role_arn))
    error_message = "web_task_role_arn must be null or an IAM role ARN (arn:aws:iam::<12-digit account>:role/...)."
  }
}

variable "poller_task_role_arn" {
  description = "ARN of the Harbormaster poller task role (output poller_task_role_arn of the harbormaster module). Setting it creates the <role_name>-poller role. Null (default) creates none."
  type        = string
  default     = null

  validation {
    condition     = var.poller_task_role_arn == null || can(regex("^arn:aws:iam::[0-9]{12}:role/", var.poller_task_role_arn))
    error_message = "poller_task_role_arn must be null or an IAM role ARN (arn:aws:iam::<12-digit account>:role/...)."
  }
}

variable "coordinator_task_role_arn" {
  description = "ARN of the Harbormaster coordinator task role (output coordinator_task_role_arn of the harbormaster module). Setting it creates the <role_name>-coordinator role. Null (default) creates none."
  type        = string
  default     = null

  validation {
    condition     = var.coordinator_task_role_arn == null || can(regex("^arn:aws:iam::[0-9]{12}:role/", var.coordinator_task_role_arn))
    error_message = "coordinator_task_role_arn must be null or an IAM role ARN (arn:aws:iam::<12-digit account>:role/...)."
  }
}

variable "scanner_task_role_arn" {
  description = "ARN of the Harbormaster scanner ECS task role that may assume the image scan role. Null (default) creates no scan role."
  type        = string
  default     = null

  validation {
    condition     = var.scanner_task_role_arn == null || can(regex("^arn:aws:iam::[0-9]{12}:role/", var.scanner_task_role_arn))
    error_message = "scanner_task_role_arn must be null or an IAM role ARN (arn:aws:iam::<12-digit account>:role/...)."
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
  description = "Name of the IAM role Harbormaster assumes. The poller and coordinator roles are named <role_name>-poller and <role_name>-coordinator; Harbormaster derives their ARNs by appending the suffix, so keep role_name the same in every account that uses them."
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

variable "scan_role_name" {
  description = "Name of the IAM role the scanner task assumes to pull images from ECR. Only used when scanner_task_role_arn is set."
  type        = string
  default     = "harbormaster-scan"

  validation {
    condition     = can(regex("^[a-z0-9-]+$", var.scan_role_name)) && length(var.scan_role_name) <= 64
    error_message = "scan_role_name must be at most 64 characters of lowercase letters, digits and hyphens."
  }
}

variable "scan_repository_arns" {
  description = "ECR repository ARN patterns the scan role may pull from. Null (default) resolves to arn:aws:ecr:*:<this account>:repository/*."
  type        = list(string)
  default     = null

  validation {
    condition     = var.scan_repository_arns == null ? true : length(var.scan_repository_arns) > 0
    error_message = "scan_repository_arns must be null (the default pattern) or a non-empty list."
  }
}

variable "read_only" {
  description = "When true, the write policy is omitted and Harbormaster can only inspect the account. The coordinator role loses its off-hours write statements too."
  type        = bool
  default     = false
}

variable "tags" {
  description = "Tags applied to the IAM role."
  type        = map(string)
  default     = {}
}
