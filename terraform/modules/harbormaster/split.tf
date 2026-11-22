# Settings derived from the split-service and cache variables, and the checks that need more than one variable.

locals {
  # Defaults the design left to plan time: the oldest engine versions ElastiCache offers that Harbormaster supports
  # (ElastiCache documents Valkey 7.2 and Redis OSS 7.1).
  cache_engine_version = coalesce(var.cache_engine_version, var.cache_engine == "valkey" ? "7.2" : "7.1")
  # ElastiCache parameter group families are <engine><major>, for example valkey7 and redis7.
  cache_family = "${var.cache_engine}${split(".", local.cache_engine_version)[0]}"

  cache_create = var.cache_mode == "create"
  kv_enabled   = var.cache_mode != "none"

  legacy_count = var.keep_legacy_service ? 1 : 0
  split_count  = var.split_services ? 1 : 0
}

# Terraform 1.5 variable validation cannot reference other variables, so the cross-variable checks live here. The
# resource is a plain anchor: it creates nothing in AWS, and a failed precondition stops the plan with its message.
resource "terraform_data" "split_checks" {
  lifecycle {
    precondition {
      condition     = var.web_max_count >= var.web_min_count
      error_message = "web_max_count must be at least web_min_count."
    }

    precondition {
      condition     = var.web_desired_count >= var.web_min_count && var.web_desired_count <= var.web_max_count
      error_message = "web_desired_count must be between web_min_count and web_max_count."
    }

    precondition {
      condition     = var.poller_max_count >= var.poller_min_count
      error_message = "poller_max_count must be at least poller_min_count."
    }

    precondition {
      condition     = var.poller_desired_count >= var.poller_min_count && var.poller_desired_count <= var.poller_max_count
      error_message = "poller_desired_count must be between poller_min_count and poller_max_count."
    }

    precondition {
      condition     = !var.split_services || var.cache_mode != "none" || var.allow_split_without_cache
      error_message = "split_services = true needs a cache: set cache_mode to \"create\" or \"external\", or set allow_split_without_cache = true (tests only: limits and live refresh then stay per task)."
    }

    precondition {
      condition     = var.cache_mode != "external" || var.cache_url_secret_arn != null
      error_message = "cache_mode = \"external\" needs cache_url_secret_arn, the Secrets Manager secret that holds the store URL."
    }

    precondition {
      condition     = var.cache_mode == "external" || var.cache_url_secret_arn == null
      error_message = "cache_url_secret_arn is only used with cache_mode = \"external\"; set it to null or change cache_mode."
    }

    precondition {
      condition     = var.keep_legacy_service || var.split_services
      error_message = "keep_legacy_service = false needs split_services = true: the install would have no service at all."
    }
  }
}
