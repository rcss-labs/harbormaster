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
