variable "vpc_id" {
  description = "VPC that hosts Harbormaster."
  type        = string
}

variable "private_subnet_ids" {
  description = "Private subnets (at least two AZs) for the ECS service and RDS. Also used by the ALB when internal = true."
  type        = list(string)

  validation {
    condition     = length(var.private_subnet_ids) >= 2
    error_message = "private_subnet_ids must contain at least two subnets in different availability zones."
  }
}

variable "public_subnet_ids" {
  description = "Public subnets for the ALB. Required unless internal = true."
  type        = list(string)
  default     = []
}

variable "internal" {
  description = "Create an internal ALB in private_subnet_ids instead of an internet-facing one."
  type        = bool
  default     = false
}

variable "allowed_cidrs" {
  description = "CIDR blocks allowed to reach the ALB."
  type        = list(string)
  default     = ["0.0.0.0/0"]
}

variable "certificate_arn" {
  description = "ACM certificate ARN for the HTTPS listener."
  type        = string
}

variable "http_redirect" {
  description = "Open port 80 and redirect it to 443."
  type        = bool
  default     = true
}

variable "image" {
  description = "Full container image reference including tag (for example an ECR URI ending in :v1.0.0)."
  type        = string

  validation {
    condition     = !endswith(var.image, ":latest")
    error_message = "image must be pinned to a specific tag; :latest is not allowed."
  }
}

variable "base_url" {
  description = "Public base URL of Harbormaster (becomes HM_BASE_URL)."
  type        = string

  validation {
    condition     = startswith(var.base_url, "https://")
    error_message = "base_url must start with https://."
  }
}

variable "desired_count" {
  description = "Number of Harbormaster server tasks."
  type        = number
  default     = 1
}

variable "cpu" {
  description = "Fargate task CPU units."
  type        = number
  default     = 512
}

variable "memory" {
  description = "Fargate task memory in MiB."
  type        = number
  default     = 1024
}

variable "cpu_architecture" {
  description = "Fargate CPU architecture. The image must be built for the same architecture."
  type        = string
  default     = "X86_64"

  validation {
    condition     = contains(["X86_64", "ARM64"], var.cpu_architecture)
    error_message = "cpu_architecture must be X86_64 or ARM64."
  }
}

variable "access_role_arns" {
  description = "Role ARN patterns the task role may assume (sts:AssumeRole). Tighten to specific account roles in production."
  type        = list(string)
  default     = ["arn:aws:iam::*:role/harbormaster-access"]
}

variable "bootstrap_admin_password_secret_arn" {
  description = "Optional Secrets Manager ARN holding the bootstrap admin password. When set it is injected as an env var and the task role may read exactly this secret."
  type        = string
  default     = null
}

variable "enable_metrics" {
  description = "Create the metrics token secret and inject HM_METRICS_TOKEN."
  type        = bool
  default     = true
}

variable "extra_env" {
  description = "Additional HM_* environment variables (poll intervals, retention, limits)."
  type        = map(string)
  default     = {}

  validation {
    condition     = alltrue([for key in keys(var.extra_env) : can(regex("^HM_[A-Z0-9_]+$", key))])
    error_message = "extra_env keys must match ^HM_[A-Z0-9_]+$."
  }

  validation {
    condition     = length(setintersection(keys(var.extra_env), ["HM_DATABASE_URL", "HM_SESSION_KEY", "HM_METRICS_TOKEN", "HM_FAKE_AWS", "HM_TRUSTED_PROXY"])) == 0
    error_message = "extra_env must not set HM_DATABASE_URL, HM_SESSION_KEY or HM_METRICS_TOKEN (the module injects them from Secrets Manager), HM_FAKE_AWS (demo mode seeds a known-password admin) or HM_TRUSTED_PROXY (the module sets it for the ALB)."
  }
}

variable "db_instance_class" {
  description = "RDS instance class."
  type        = string
  default     = "db.t4g.micro"
}

variable "db_allocated_storage" {
  description = "Initial RDS storage in GiB (gp3)."
  type        = number
  default     = 20
}

variable "db_max_allocated_storage" {
  description = "Maximum RDS storage autoscaling limit in GiB."
  type        = number
  default     = 100
}

variable "db_engine_version" {
  description = "PostgreSQL major version; minor upgrades are applied automatically."
  type        = string
  default     = "16"
}

variable "db_multi_az" {
  description = "Run RDS in multiple availability zones."
  type        = bool
  default     = false
}

variable "db_deletion_protection" {
  description = "Enable RDS deletion protection."
  type        = bool
  default     = true
}

variable "db_kms_key_id" {
  description = "KMS key ARN (arn:aws:kms:<region>:<account>:key/<id>) for RDS storage and the Secrets Manager secrets. Aliases and bare key ids are rejected: the value is used as an IAM Resource. Null uses the AWS-managed keys."
  type        = string
  default     = null

  validation {
    condition     = var.db_kms_key_id == null || can(regex("^arn:aws[a-z-]*:kms:[a-z0-9-]+:[0-9]{12}:key/[A-Za-z0-9-]+$", var.db_kms_key_id))
    error_message = "db_kms_key_id must be a KMS key ARN (arn:aws:kms:<region>:<account>:key/<id>), not an alias or a bare key id."
  }
}

variable "log_retention_days" {
  description = "CloudWatch Logs retention for the server log group."
  type        = number
  default     = 30
}

variable "alb_idle_timeout" {
  description = "ALB idle timeout in seconds; terminal and log-tail WebSockets may idle up to 20 minutes."
  type        = number
  default     = 1800
}

variable "tags" {
  description = "Tags applied to every taggable resource."
  type        = map(string)
  default     = {}
}
