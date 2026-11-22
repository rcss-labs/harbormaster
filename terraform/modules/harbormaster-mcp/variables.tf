variable "name" {
  description = "Prefix of every resource name."
  type        = string
  default     = "harbormaster-mcp"
}

variable "vpc_id" {
  description = "VPC to run in."
  type        = string
}

variable "public_subnet_ids" {
  description = "Public subnets for the load balancer."
  type        = list(string)
}

variable "private_subnet_ids" {
  description = "Private subnets for the tasks. They must route to a NAT gateway (image pull, logs and the upstream)."
  type        = list(string)
}

variable "certificate_arn" {
  description = "ACM certificate for the public hostname. Required: there is no default."
  type        = string
}

variable "allowed_cidrs" {
  description = "Who may reach the public load balancer. Open to the internet by decision (WAF is the protection); narrow it only for the first rollout run."
  type        = list(string)
  default     = ["0.0.0.0/0"]
}

variable "http_redirect" {
  description = "Redirect port 80 to 443."
  type        = bool
  default     = true
}

variable "cluster_arn" {
  description = "ARN of the ECS cluster to run in (the Harbormaster module's cluster)."
  type        = string
}

variable "image" {
  description = "Gateway image, pinned to a tag or digest. A tag ending in :latest is refused."
  type        = string

  validation {
    condition     = !endswith(var.image, ":latest")
    error_message = "image must not end in :latest; pin a version tag or a digest."
  }
}

variable "cpu_architecture" {
  description = "Task CPU architecture; it must match the image (X86_64 or ARM64)."
  type        = string
  default     = "X86_64"

  validation {
    condition     = contains(["X86_64", "ARM64"], var.cpu_architecture)
    error_message = "cpu_architecture must be X86_64 or ARM64."
  }
}

variable "cpu" {
  description = "Fargate CPU units per task."
  type        = number
  default     = 256
}

variable "memory" {
  description = "Fargate memory (MiB) per task."
  type        = number
  default     = 512
}

variable "desired_count" {
  description = "Number of gateway tasks."
  type        = number
  default     = 2
}

variable "upstream_url" {
  description = "HM_MCP_UPSTREAM_URL: Harbormaster's internal address. It must match the internal certificate's name."
  type        = string

  validation {
    condition     = startswith(var.upstream_url, "https://")
    error_message = "upstream_url must start with https://."
  }
}

variable "public_url" {
  description = "HM_MCP_PUBLIC_URL: the public hostname of the gateway, for example https://mcp.example.com. Required: there is no default."
  type        = string

  validation {
    condition     = can(regex("^https://[^/]+/?$", var.public_url))
    error_message = "public_url must be https:// plus a host, with no path."
  }
}

variable "authorize_url" {
  description = "HM_MCP_AUTHORIZE_URL (OAuth phase): where the gateway sends users to consent. Null leaves it unset."
  type        = string
  default     = null
}

variable "allowed_origins" {
  description = "HM_MCP_ALLOWED_ORIGINS: browser origins the gateway accepts, joined with commas."
  type        = list(string)
  default     = []
}

variable "harbormaster_alb_security_group_id" {
  description = "Security group of Harbormaster's internal load balancer (the egress target). Use the harbormaster module's security_group_ids.alb."
  type        = string
}

variable "log_retention_days" {
  description = "Retention of the gateway log group."
  type        = number
  default     = 30
}

variable "tags" {
  description = "Tags for every resource that takes them."
  type        = map(string)
  default     = {}
}

variable "waf_rate_limit" {
  description = "Requests per 5 minutes per source IP before WAF blocks the IP."
  type        = number
  default     = 300

  validation {
    condition     = var.waf_rate_limit >= 10
    error_message = "waf_rate_limit must be at least 10 (the WAF minimum for rate-based rules is 10)."
  }
}

variable "waf_common_count_rules" {
  description = "Rules of the AWS common rule set that only count. Body rules in block mode would refuse a search_logs call whose query looks like an injection. Move a rule out only after a clean week in the WAF log."
  type        = list(string)
  default     = ["SizeRestrictions_BODY", "CrossSiteScripting_BODY", "GenericRFI_BODY"]
}

variable "waf_oauth_rate_limit" {
  description = "Requests per 5 minutes per source IP on /oauth/ paths. Used only when enable_oauth_paths is true."
  type        = number
  default     = 60

  validation {
    condition     = var.waf_oauth_rate_limit >= 10
    error_message = "waf_oauth_rate_limit must be at least 10."
  }
}

variable "enable_oauth_paths" {
  description = "Add the stricter per-IP rate rule for /oauth/ paths (OAuth phase)."
  type        = bool
  default     = false
}
