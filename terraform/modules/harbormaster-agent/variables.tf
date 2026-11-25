variable "name" {
  description = "Prefix of every resource name: lowercase letters, digits and hyphens."
  type        = string
  default     = "harbormaster-agent"

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{0,24}[a-z0-9]$", var.name))
    error_message = "name must be 2 to 26 lowercase letters, digits or hyphens, starting with a letter (load balancer and target group names allow 32 characters)."
  }
}

variable "vpc_id" {
  description = "VPC to run in."
  type        = string
}

variable "private_subnet_ids" {
  description = "Private subnets for the internal load balancer and the tasks. They must route to a NAT gateway (image pull, logs and the model providers)."
  type        = list(string)
}

variable "certificate_arn" {
  description = "ACM certificate for the agent's private hostname. Required: there is no default."
  type        = string
}

variable "cluster_arn" {
  description = "ARN of the ECS cluster to run in (the Harbormaster module's cluster)."
  type        = string
}

variable "image" {
  description = "Agent image, pinned to a tag or digest. An image with neither, or with the tag latest (in any case), is refused."
  type        = string

  validation {
    condition     = can(regex("(@sha256:[0-9a-f]{64}|:[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})$", var.image))
    error_message = "image must end in a tag (repo:1.2.3) or a digest (repo@sha256:...)."
  }

  validation {
    condition     = !can(regex("(?i):latest(@|$)", var.image))
    error_message = "image must not use the latest tag; pin a version tag or a digest."
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
  default     = 512
}

variable "memory" {
  description = "Fargate memory (MiB) per task."
  type        = number
  default     = 1024
}

variable "desired_count" {
  description = "Number of agent tasks."
  type        = number
  default     = 2
}

variable "upstream_url" {
  description = "HM_AGENT_UPSTREAM_URL: Harbormaster's internal address. It must match the internal certificate's name."
  type        = string

  validation {
    condition     = startswith(var.upstream_url, "https://")
    error_message = "upstream_url must start with https://."
  }
}

variable "hostname" {
  description = "Private DNS name of the agent that matches certificate_arn, for example agent.internal.example.com. It only shapes the agent_url output. Null makes agent_url the load balancer's own DNS name, which a certificate for a private name will not match."
  type        = string
  default     = null
}

variable "upstream_ca_pem" {
  description = "HM_AGENT_UPSTREAM_CA_PEM: PEM of extra CAs that sign Harbormaster's internal load balancer certificate. A CA certificate is public, so it is a plain environment value (not a secret). Not marked sensitive on purpose: that would hide the task definition's changes in every plan. Null leaves it unset."
  type        = string
  default     = null
}

variable "harbormaster_alb_security_group_id" {
  description = "Security group of Harbormaster's internal load balancer (the egress target). Use the harbormaster module's security_group_ids.alb."
  type        = string
}

variable "harbormaster_task_security_group_ids" {
  description = "Security groups of the Harbormaster tasks that call the agent: the only callers the agent's load balancer admits on 443. Use the harbormaster module's security_group_ids.web and security_group_ids.coordinator (the chat tasks and the coordinator, which runs investigations), or security_group_ids.server in single-service mode, where that one group covers both."
  type        = list(string)

  validation {
    condition     = length(var.harbormaster_task_security_group_ids) > 0
    error_message = "harbormaster_task_security_group_ids needs at least one security group: the web and coordinator groups (or the server group in single-service mode). Without one, nothing can reach the agent."
  }
}

variable "model_providers" {
  description = "Provider catalogue, serialised as HM_AGENT_PROVIDERS: each provider (anthropic, openai, google or bedrock) with its models. Prices are decimal strings in US dollars per million tokens, for example \"3.00\". At least one provider is required."
  type = list(object({
    provider = string
    models = list(object({
      id                  = string
      input_per_mtok_usd  = string
      output_per_mtok_usd = string
      context_tokens      = number
    }))
  }))

  validation {
    condition     = length(var.model_providers) > 0
    error_message = "model_providers needs at least one provider."
  }

  validation {
    condition     = alltrue([for entry in var.model_providers : contains(["anthropic", "openai", "google", "bedrock"], entry.provider)])
    error_message = "model_providers: provider must be anthropic, openai, google or bedrock."
  }

  validation {
    condition     = length(distinct([for entry in var.model_providers : entry.provider])) == length(var.model_providers)
    error_message = "model_providers lists a provider more than once."
  }

  validation {
    condition = alltrue(flatten([
      for entry in var.model_providers : [
        for model in entry.models : can(regex("^[0-9]+(\\.[0-9]+)?$", model.input_per_mtok_usd)) && can(regex("^[0-9]+(\\.[0-9]+)?$", model.output_per_mtok_usd))
      ]
    ]))
    error_message = "model_providers: input_per_mtok_usd and output_per_mtok_usd must be decimal strings such as \"3.00\"."
  }
}

variable "api_key_secret_arns" {
  description = "Secrets Manager ARN of each provider's API key, by provider: keys limited to anthropic, openai and google. The values reach the task as secrets, never as environment, and the execution role may read only these ARNs. Every non-bedrock provider in the catalogue needs an entry and every entry needs a catalogue provider."
  type        = map(string)
  default     = {}

  validation {
    condition     = alltrue([for provider in keys(var.api_key_secret_arns) : contains(["anthropic", "openai", "google"], provider)])
    error_message = "api_key_secret_arns keys must be anthropic, openai or google."
  }

  validation {
    condition     = alltrue([for arn in values(var.api_key_secret_arns) : startswith(arn, "arn:")])
    error_message = "api_key_secret_arns values must be Secrets Manager ARNs."
  }
}

variable "bedrock_region" {
  description = "HM_AGENT_BEDROCK_REGION: region of the Bedrock endpoint. Required when the catalogue lists bedrock."
  type        = string
  default     = null
}

variable "bedrock_model_arns" {
  description = "Foundation model or inference profile ARNs the task role may invoke (bedrock:InvokeModel and bedrock:InvokeModelWithResponseStream). Required, with at least one entry, when the catalogue lists bedrock."
  type        = list(string)
  default     = []
}

variable "limits" {
  description = "Per-run limits and the task's concurrency: HM_AGENT_MAX_STEPS, HM_AGENT_MAX_RUN_TOKENS, HM_AGENT_RUN_TIMEOUT (a Go duration, at most 5m: the load balancer waits 600 seconds), HM_AGENT_MAX_CONCURRENT_RUNS and HM_AGENT_MAX_RESULT_BYTES."
  type = object({
    max_steps           = optional(number, 20)
    max_run_tokens      = optional(number, 200000)
    run_timeout         = optional(string, "5m")
    max_concurrent_runs = optional(number, 8)
    max_result_bytes    = optional(number, 100000)
  })
  default = {}

  validation {
    condition = alltrue([
      var.limits.max_steps > 0,
      var.limits.max_run_tokens > 0,
      var.limits.max_concurrent_runs > 0,
      var.limits.max_result_bytes > 0,
    ])
    error_message = "limits: max_steps, max_run_tokens, max_concurrent_runs and max_result_bytes must be positive."
  }

  validation {
    condition     = can(regex("^(([1-9]|[1-9][0-9]|[12][0-9]{2}|300)s|[1-5]m|[1-4]m([1-9]|[1-5][0-9])s)$", var.limits.run_timeout))
    error_message = "limits.run_timeout must be at most 5m, written as whole seconds (90s), whole minutes (5m) or both (2m30s)."
  }
}

variable "alarm_sns_topic_arns" {
  description = "SNS topics notified by the CloudWatch alarms. Empty creates no alarms."
  type        = list(string)
  default     = []
}

variable "log_retention_days" {
  description = "Retention of the agent log group."
  type        = number
  default     = 30
}

variable "tags" {
  description = "Tags for every resource that takes them."
  type        = map(string)
  default     = {}
}
