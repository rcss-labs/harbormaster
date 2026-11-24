locals {
  container_port = 8484
  tags           = var.tags

  # Region from the log group ARN (arn:aws:logs:<region>:...): data.aws_region .name is deprecated in provider 6.x
  # and .region is 6.x-only.
  region = split(":", aws_cloudwatch_log_group.service.arn)[3]

  catalogue_providers = [for entry in var.model_providers : entry.provider]
  uses_bedrock        = contains(local.catalogue_providers, "bedrock")
  key_providers       = [for provider in local.catalogue_providers : provider if provider != "bedrock"]

  catalogue_json = jsonencode(var.model_providers)

  environment = merge(
    {
      HM_AGENT_LISTEN              = ":${local.container_port}"
      HM_AGENT_UPSTREAM_URL        = var.upstream_url
      HM_AGENT_PROVIDERS           = local.catalogue_json
      HM_AGENT_MAX_STEPS           = tostring(var.limits.max_steps)
      HM_AGENT_MAX_RUN_TOKENS      = tostring(var.limits.max_run_tokens)
      HM_AGENT_RUN_TIMEOUT         = var.limits.run_timeout
      HM_AGENT_MAX_CONCURRENT_RUNS = tostring(var.limits.max_concurrent_runs)
      HM_AGENT_MAX_RESULT_BYTES    = tostring(var.limits.max_result_bytes)
    },
    var.upstream_ca_pem == null ? {} : { HM_AGENT_UPSTREAM_CA_PEM = var.upstream_ca_pem },
    local.uses_bedrock ? { HM_AGENT_BEDROCK_REGION = var.bedrock_region } : {},
  )

  # API keys come only through secrets: the execution role fetches them and ECS injects them at start.
  secrets = [
    for provider in sort(keys(var.api_key_secret_arns)) : {
      name      = "HM_AGENT_${upper(provider)}_API_KEY"
      valueFrom = var.api_key_secret_arns[provider]
    }
  ]

  container_definitions = [
    {
      name                   = var.name
      image                  = var.image
      essential              = true
      readonlyRootFilesystem = true
      cpu                    = var.cpu
      memory                 = var.memory

      portMappings = [
        {
          containerPort = local.container_port
          protocol      = "tcp"
        }
      ]

      environment = [for key, value in local.environment : { name = key, value = value }]
      secrets     = local.secrets

      healthCheck = {
        command     = ["CMD", "/harbormaster-agent", "healthcheck"]
        interval    = 15
        timeout     = 5
        retries     = 3
        startPeriod = 10
      }

      logConfiguration = {
        logDriver = "awslogs"
        options = {
          "awslogs-group"         = aws_cloudwatch_log_group.service.name
          "awslogs-region"        = local.region
          "awslogs-stream-prefix" = var.name
        }
      }
    }
  ]
}
