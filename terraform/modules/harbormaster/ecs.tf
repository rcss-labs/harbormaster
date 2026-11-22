locals {
  # Module-set keys win over extra_env on collision (merge: later maps override).
  container_env = merge(
    var.extra_env,
    {
      HM_LISTEN        = ":${local.container_port}"
      HM_BASE_URL      = var.base_url
      HM_TRUSTED_PROXY = "true" # the task is reachable only through the ALB, which appends the client IP
    },
    var.bootstrap_admin_password_secret_arn == null ? {} : {
      HM_BOOTSTRAP_ADMIN_PASSWORD_SECRET_ARN = var.bootstrap_admin_password_secret_arn
    },
    local.scan_server_env,
  )

  container_secrets = merge(
    {
      HM_DATABASE_URL = aws_secretsmanager_secret.database_url.arn
      HM_SESSION_KEY  = aws_secretsmanager_secret.session_key.arn
    },
    var.enable_metrics ? { HM_METRICS_TOKEN = aws_secretsmanager_secret.metrics_token[0].arn } : {},
    var.smtp_password_secret_arn == null ? {} : { HM_SMTP_PASSWORD = var.smtp_password_secret_arn },
    # Not set until a cache exists, so a single-service install without one renders the same task definition as before.
    local.kv_enabled ? { HM_KV_URL = local.kv_url_secret_arn } : {},
  )

  # Map keys iterate in sorted order, so the rendered lists are stable.
  container_definitions = [
    {
      name      = local.server_name
      image     = var.image
      essential = true

      portMappings = [
        { containerPort = local.container_port, protocol = "tcp" }
      ]

      environment = [for key, value in local.container_env : { name = key, value = value }]
      secrets     = [for key, arn in local.container_secrets : { name = key, valueFrom = arn }]

      logConfiguration = {
        logDriver = "awslogs"
        options = {
          "awslogs-group" = aws_cloudwatch_log_group.server.name
          # Region from the log group ARN: data.aws_region .name/.id are deprecated in provider 6.x and .region is 6.x-only.
          "awslogs-region"        = split(":", aws_cloudwatch_log_group.server.arn)[3]
          "awslogs-stream-prefix" = "ecs"
        }
      }

      # ECS ignores the image's HEALTHCHECK; this is the check it uses.
      healthCheck = {
        command     = ["CMD", "/harbormaster", "healthcheck"]
        interval    = 30
        timeout     = 5
        retries     = 3
        startPeriod = 30
      }

      readonlyRootFilesystem = true
    }
  ]
}

resource "aws_ecs_cluster" "this" {
  name = local.name

  setting {
    name  = "containerInsights"
    value = "enabled"
  }

  tags = local.tags
}

# The legacy single service. keep_legacy_service = false removes it, its task definition and its two roles.
resource "aws_ecs_task_definition" "server" {
  count = local.legacy_count

  family                   = local.server_name
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = var.cpu
  memory                   = var.memory
  execution_role_arn       = aws_iam_role.execution[0].arn
  task_role_arn            = aws_iam_role.task[0].arn
  container_definitions    = jsonencode(local.container_definitions)

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = var.cpu_architecture
  }

  tags = local.tags
}

resource "aws_ecs_service" "server" {
  count = local.legacy_count

  name                               = local.server_name
  cluster                            = aws_ecs_cluster.this.id
  task_definition                    = aws_ecs_task_definition.server[0].arn
  desired_count                      = var.desired_count
  launch_type                        = "FARGATE"
  deployment_minimum_healthy_percent = 100
  deployment_maximum_percent         = 200
  health_check_grace_period_seconds  = 60
  propagate_tags                     = "SERVICE"

  deployment_circuit_breaker {
    enable   = true
    rollback = true
  }

  network_configuration {
    subnets          = var.private_subnet_ids
    security_groups  = [aws_security_group.server.id]
    assign_public_ip = false
  }

  load_balancer {
    target_group_arn = aws_lb_target_group.server.arn
    container_name   = local.server_name
    container_port   = local.container_port
  }

  tags = local.tags

  # The task definition references only the secret ARNs, and the database URL value is
  # written after RDS is up (several minutes). Without waiting for the versions, the first
  # tasks fail with "can't find the specified secret value for staging label: AWSCURRENT"
  # and the circuit breaker fails the initial deployment.
  depends_on = [
    aws_lb_listener.https,
    aws_iam_role_policy.execution_secrets,
    aws_iam_role_policy_attachment.execution_managed,
    aws_iam_role_policy.task_assume,
    aws_secretsmanager_secret_version.database_url,
    aws_secretsmanager_secret_version.session_key,
    aws_secretsmanager_secret_version.metrics_token,
    aws_secretsmanager_secret_version.kv_url,
  ]
}

# The legacy service and its task definition gained a count: keep an existing install's state.
moved {
  from = aws_ecs_task_definition.server
  to   = aws_ecs_task_definition.server[0]
}

moved {
  from = aws_ecs_service.server
  to   = aws_ecs_service.server[0]
}
