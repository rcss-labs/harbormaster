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

  validation {
    condition     = length(setintersection(keys(var.extra_env), ["HM_SCAN_ENABLED", "HM_SCAN_CLUSTER_ARN", "HM_SCAN_TASK_DEFINITION", "HM_SCAN_SUBNETS", "HM_SCAN_SECURITY_GROUP", "HM_SCAN_BUCKET", "HM_SCAN_ROLE_NAME"])) == 0
    error_message = "extra_env must not set HM_SCAN_ENABLED, HM_SCAN_CLUSTER_ARN, HM_SCAN_TASK_DEFINITION, HM_SCAN_SUBNETS, HM_SCAN_SECURITY_GROUP, HM_SCAN_BUCKET or HM_SCAN_ROLE_NAME (the module sets them from enable_image_scanning and the scan_* variables)."
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

variable "enable_image_scanning" {
  description = "Creates the vulnerability scanner (task definition, bucket, roles, security group, log group) and the server's scan permissions, and turns scanning on in the server."
  type        = bool
  default     = false
}

variable "scanner_image" {
  description = "Scanner image pinned by digest, for example <account>.dkr.ecr.<region>.amazonaws.com/harbormaster-scanner@sha256:<64 hex>. Required when enable_image_scanning is true (checked by a precondition on the task definition)."
  type        = string
  default     = null

  validation {
    condition     = var.scanner_image == null || can(regex("@sha256:[a-f0-9]{64}$", var.scanner_image))
    error_message = "scanner_image must be null or end in @sha256:<64 lowercase hex characters>."
  }
}

variable "scanner_cpu" {
  description = "Scanner task CPU units; must be a valid Fargate pair with scanner_memory."
  type        = number
  default     = 1024
}

variable "scanner_memory" {
  description = "Scanner task memory in MiB."
  type        = number
  default     = 2048
}

variable "scanner_ephemeral_storage_gib" {
  description = "Scanner task ephemeral storage in GiB (the /work volume: image layers, Syft and Grype caches, the vulnerability database)."
  type        = number
  default     = 40

  validation {
    condition     = var.scanner_ephemeral_storage_gib >= 21 && var.scanner_ephemeral_storage_gib <= 200
    error_message = "scanner_ephemeral_storage_gib must be between 21 and 200."
  }
}

variable "scan_bucket_name" {
  description = "Name of the scan bucket. Null resolves to harbormaster-scan-<account id>-<region>."
  type        = string
  default     = null
}

variable "scan_role_name" {
  description = "Name of the per-account role the scanner assumes to read ECR (the harbormaster-access module's scan_role_name)."
  type        = string
  default     = "harbormaster-scan"

  validation {
    condition     = can(regex("^[a-z0-9-]+$", var.scan_role_name)) && length(var.scan_role_name) <= 64
    error_message = "scan_role_name must be at most 64 characters of lowercase letters, digits and hyphens."
  }
}

variable "scan_role_arns" {
  description = "Role ARN patterns the scanner task role may assume. Null resolves to arn:aws:iam::*:role/<scan_role_name>. Tighten to explicit role ARNs so the wildcard account is not trusted."
  type        = list(string)
  default     = null
}

variable "scanner_egress_cidrs" {
  description = "Destinations the scanner may reach on tcp 443 (ECR, S3 and STS). It never contacts the vulnerability data publisher."
  type        = list(string)
  default     = ["0.0.0.0/0"]
}
