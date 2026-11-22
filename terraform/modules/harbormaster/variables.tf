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

variable "alb_ingress_security_group_ids" {
  description = "Security groups allowed to reach the load balancer on 443 in addition to allowed_cidrs. Set it to the harbormaster-mcp module's task_security_group_id."
  type        = list(string)
  default     = []
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

variable "smtp_password_secret_arn" {
  description = "Optional Secrets Manager ARN holding the SMTP password for email notifications. When set it is injected as HM_SMTP_PASSWORD (a container secret, never plain text) and the execution role may read exactly this secret. The other HM_SMTP_* values go through extra_env."
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
    condition     = length(setintersection(keys(var.extra_env), ["HM_DATABASE_URL", "HM_SESSION_KEY", "HM_METRICS_TOKEN", "HM_SMTP_PASSWORD", "HM_KV_URL", "HM_FAKE_AWS", "HM_TRUSTED_PROXY"])) == 0
    error_message = "extra_env must not set HM_DATABASE_URL, HM_SESSION_KEY, HM_METRICS_TOKEN, HM_SMTP_PASSWORD or HM_KV_URL (the module injects them from Secrets Manager; HM_KV_URL carries the store password), HM_FAKE_AWS (demo mode seeds a known-password admin) or HM_TRUSTED_PROXY (the module sets it for the ALB)."
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

  validation {
    condition     = var.scan_role_arns == null ? true : length(var.scan_role_arns) > 0
    error_message = "scan_role_arns must be null (the default pattern) or a non-empty list."
  }
}

variable "scanner_egress_cidrs" {
  description = "Destinations the scanner may reach on tcp 443 (ECR, S3 and STS). It never contacts the vulnerability data publisher."
  type        = list(string)
  default     = ["0.0.0.0/0"]
}

# ---- Split services (web, poller, coordinator) -------------------------------------------------------------------
# Everything below defaults to today's single service. See the upgrade runbook in README.md.

variable "split_services" {
  description = "Create the web, poller and coordinator ECS services next to (or instead of) the single harbormaster-server service. Needs a cache (cache_mode \"create\" or \"external\")."
  type        = bool
  default     = false
}

variable "keep_legacy_service" {
  description = "Keep the single harbormaster-server service, its task definition and its two roles. Set to false only after the split services are healthy (runbook run 4). Removing the legacy service also removes its task and execution roles; run the access module once more with harbormaster_task_role_arn = null afterwards."
  type        = bool
  default     = true
}

variable "web_desired_count" {
  description = "Initial number of web tasks. Autoscaling owns the count afterwards."
  type        = number
  default     = 2

  validation {
    condition     = var.web_desired_count >= 1
    error_message = "web_desired_count must be at least 1."
  }
}

variable "web_min_count" {
  description = "Minimum number of web tasks (autoscaling)."
  type        = number
  default     = 2

  validation {
    condition     = var.web_min_count >= 1
    error_message = "web_min_count must be at least 1."
  }
}

variable "web_max_count" {
  description = "Maximum number of web tasks (autoscaling). With more than 6, use db.t4g.small or larger (see the README, Database connections)."
  type        = number
  default     = 6

  validation {
    condition     = var.web_max_count >= 1
    error_message = "web_max_count must be at least 1."
  }
}

variable "poller_desired_count" {
  description = "Initial number of poller tasks. Autoscaling owns the count afterwards."
  type        = number
  default     = 2

  validation {
    condition     = var.poller_desired_count >= 1
    error_message = "poller_desired_count must be at least 1."
  }
}

variable "poller_min_count" {
  description = "Minimum number of poller tasks (autoscaling)."
  type        = number
  default     = 2

  validation {
    condition     = var.poller_min_count >= 1
    error_message = "poller_min_count must be at least 1."
  }
}

variable "poller_max_count" {
  description = "Maximum number of poller tasks (autoscaling). With more than 4, use db.t4g.small or larger (see the README, Database connections)."
  type        = number
  default     = 4

  validation {
    condition     = var.poller_max_count >= 1
    error_message = "poller_max_count must be at least 1."
  }
}

variable "coordinator_desired_count" {
  description = "Number of coordinator tasks: one is active and the others stand by. Fixed, not autoscaled."
  type        = number
  default     = 2

  validation {
    condition     = var.coordinator_desired_count >= 1
    error_message = "coordinator_desired_count must be at least 1."
  }
}

variable "web_cpu" {
  description = "Fargate CPU units of a web task."
  type        = number
  default     = 512
}

variable "web_memory" {
  description = "Fargate memory in MiB of a web task."
  type        = number
  default     = 1024
}

variable "poller_cpu" {
  description = "Fargate CPU units of a poller task."
  type        = number
  default     = 512
}

variable "poller_memory" {
  description = "Fargate memory in MiB of a poller task."
  type        = number
  default     = 1024
}

variable "coordinator_cpu" {
  description = "Fargate CPU units of a coordinator task."
  type        = number
  default     = 256
}

variable "coordinator_memory" {
  description = "Fargate memory in MiB of a coordinator task."
  type        = number
  default     = 512
}

variable "web_requests_per_target" {
  description = "Target value of ALBRequestCountPerTarget for web autoscaling: requests per target per minute."
  type        = number
  default     = 1000

  validation {
    condition     = var.web_requests_per_target >= 1
    error_message = "web_requests_per_target must be at least 1."
  }
}

variable "metrics_ingress_security_group_ids" {
  description = "Security groups allowed to scrape /metrics on poller and coordinator tasks (the container port). Web is reached through the ALB; the legacy service is unchanged."
  type        = list(string)
  default     = []
}

# ---- Cache (Valkey or Redis) -------------------------------------------------------------------------------------

variable "cache_mode" {
  description = "\"create\" builds an ElastiCache replication group, \"external\" uses the endpoint in the secret cache_url_secret_arn (an endpoint you run yourself), \"none\" uses no store: shared limits and live page refresh stay per task and split_services cannot be used."
  type        = string
  default     = "none"

  validation {
    condition     = contains(["create", "external", "none"], var.cache_mode)
    error_message = "cache_mode must be create, external or none."
  }
}

variable "cache_engine" {
  description = "Engine of the created cache: valkey or redis. Harbormaster uses a command subset both support (Valkey 7.2 and Redis 7.0 or newer). Only used when cache_mode = \"create\"."
  type        = string
  default     = "valkey"

  validation {
    condition     = contains(["valkey", "redis"], var.cache_engine)
    error_message = "cache_engine must be valkey or redis."
  }
}

variable "cache_engine_version" {
  description = "Engine version of the created cache. Null resolves to 7.2 for valkey and 7.1 for redis."
  type        = string
  default     = null
}

variable "cache_node_type" {
  description = "ElastiCache node type of the created cache."
  type        = string
  default     = "cache.t4g.micro"
}

variable "cache_replicas" {
  description = "Replicas of the created cache. 1 or more adds replicas in other availability zones with Multi-AZ automatic failover."
  type        = number
  default     = 0

  validation {
    condition     = var.cache_replicas >= 0 && var.cache_replicas <= 5
    error_message = "cache_replicas must be between 0 and 5."
  }
}

variable "cache_url_secret_arn" {
  description = "For cache_mode = \"external\": ARN of a Secrets Manager secret whose value is the full store URL (redis://, rediss://, valkey:// or valkeys://, with password). Must be null otherwise."
  type        = string
  default     = null
}

variable "cache_alarm_actions" {
  description = "ARNs (for example an SNS topic) notified when the created cache evicts keys. Empty creates the alarm without actions."
  type        = list(string)
  default     = []
}

variable "allow_split_without_cache" {
  description = "Deprecated, must stay false. The web, poller and coordinator roles refuse to start without a store (HM_KV_URL), so split_services always needs a cache_mode of \"create\" or \"external\"."
  type        = bool
  default     = false

  validation {
    condition     = !var.allow_split_without_cache
    error_message = "allow_split_without_cache is no longer supported: the split roles exit at start without HM_KV_URL. Set cache_mode to \"create\" or \"external\" instead."
  }
}
