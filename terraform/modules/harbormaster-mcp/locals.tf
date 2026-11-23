locals {
  container_port = 8481
  tags           = var.tags

  # Region from the log group ARN (arn:aws:logs:<region>:...): data.aws_region .name is deprecated in provider 6.x
  # and .region is 6.x-only.
  region = split(":", aws_cloudwatch_log_group.service.arn)[3]

  environment = merge(
    {
      HM_MCP_LISTEN          = ":${local.container_port}"
      HM_MCP_UPSTREAM_URL    = var.upstream_url
      HM_MCP_PUBLIC_URL      = var.public_url
      HM_MCP_TRUSTED_PROXY   = "true"
      HM_MCP_ALLOWED_ORIGINS = join(",", var.allowed_origins)
    },
    var.authorize_url == null ? {} : { HM_MCP_AUTHORIZE_URL = var.authorize_url },
    var.authorize_via_gateway ? { HM_MCP_AUTHORIZE_VIA_GATEWAY = "true" } : {},
    var.max_result_bytes == null ? {} : { HM_MCP_MAX_RESULT_BYTES = tostring(var.max_result_bytes) },
    var.upstream_timeout == null ? {} : { HM_MCP_UPSTREAM_TIMEOUT = var.upstream_timeout },
    var.upstream_ca_pem == null ? {} : { HM_MCP_UPSTREAM_CA_PEM = var.upstream_ca_pem },
  )

  container_definitions = [
    {
      name                   = var.name
      image                  = var.image
      essential              = true
      readonlyRootFilesystem = true

      portMappings = [
        {
          containerPort = local.container_port
          protocol      = "tcp"
        }
      ]

      # No secrets: the gateway holds no credentials; each caller's own token is forwarded per request.
      environment = [for key, value in local.environment : { name = key, value = value }]

      healthCheck = {
        command     = ["CMD", "/harbormaster-mcp", "healthcheck"]
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
