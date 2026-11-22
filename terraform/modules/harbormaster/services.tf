# The split services: web, poller and coordinator, one image with a different HM_ROLE. They exist when split_services
# is true, next to the legacy single service until keep_legacy_service is set to false (see the upgrade runbook).

locals {
  service_base_env = merge(
    var.extra_env,
    {
      HM_LISTEN   = ":${local.container_port}"
      HM_BASE_URL = var.base_url
    },
  )

  # Module-set keys win over extra_env on collision (merge: later maps override). Secrets come from
  # local.service_secrets (roles.tf), so a task gets only the secrets its job needs.
  service_env = {
    web = merge(
      local.service_base_env,
      {
        HM_ROLE          = "web"
        HM_TRUSTED_PROXY = "true" # web is reachable only through the ALB, which appends the client IP
      },
      var.bootstrap_admin_password_secret_arn == null ? {} : {
        HM_BOOTSTRAP_ADMIN_PASSWORD_SECRET_ARN = var.bootstrap_admin_password_secret_arn
      },
      # Web needs to know scanning is on to show it, not how scans are launched.
      local.scan_enabled ? { HM_SCAN_ENABLED = "true" } : {},
    )
    poller = merge(local.service_base_env, { HM_ROLE = "poller" })
    # The coordinator launches scans, so it gets the launch settings the legacy service gets.
    coordinator = merge(local.service_base_env, { HM_ROLE = "coordinator" }, local.scan_server_env)
  }

  split_container_specs = {
    for role in local.service_roles : role => {
      name          = "${local.name}-${role}"
      environment   = local.service_env[role]
      secrets       = local.service_secrets[role]
      stream_prefix = role
    }
  }

  service_size = {
    web         = { cpu = var.web_cpu, memory = var.web_memory }
    poller      = { cpu = var.poller_cpu, memory = var.poller_memory }
    coordinator = { cpu = var.coordinator_cpu, memory = var.coordinator_memory }
  }
}

resource "aws_ecs_task_definition" "service" {
  for_each = var.split_services ? local.service_roles : toset([])

  family                   = "${local.name}-${each.key}"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = local.service_size[each.key].cpu
  memory                   = local.service_size[each.key].memory
  execution_role_arn       = aws_iam_role.service_execution[each.key].arn
  task_role_arn            = aws_iam_role.service_task[each.key].arn
  container_definitions    = jsonencode(local.container_definitions_by_service[each.key])

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = var.cpu_architecture
  }

  tags = local.tags
}

# Web is the only service behind the ALB. Autoscaling owns its count, so a later apply does not reset it.
resource "aws_ecs_service" "web" {
  count = local.split_count

  name                               = "${local.name}-web"
  cluster                            = aws_ecs_cluster.this.id
  task_definition                    = aws_ecs_task_definition.service["web"].arn
  desired_count                      = var.web_desired_count
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
    security_groups  = [aws_security_group.web.id]
    assign_public_ip = false
  }

  load_balancer {
    target_group_arn = aws_lb_target_group.server.arn
    container_name   = "${local.name}-web"
    container_port   = local.container_port
  }

  tags = local.tags

  lifecycle {
    ignore_changes = [desired_count]
  }

  # The task definition references only secret ARNs and their values are written after the data layer is up: without
  # waiting for the versions the first tasks fail with "can't find the specified secret value for staging label:
  # AWSCURRENT" and the circuit breaker fails the initial deployment (see the legacy service).
  depends_on = [
    aws_lb_listener.https,
    aws_iam_role_policy.service_execution_secrets,
    aws_iam_role_policy_attachment.service_execution_managed,
    aws_iam_role_policy.service_task_assume,
    aws_secretsmanager_secret_version.database_url,
    aws_secretsmanager_secret_version.session_key,
    aws_secretsmanager_secret_version.metrics_token,
    aws_secretsmanager_secret_version.kv_url,
  ]
}

# Autoscaling owns the poller's count too: slices rebalance across however many tasks run.
resource "aws_ecs_service" "poller" {
  count = local.split_count

  name                               = "${local.name}-poller"
  cluster                            = aws_ecs_cluster.this.id
  task_definition                    = aws_ecs_task_definition.service["poller"].arn
  desired_count                      = var.poller_desired_count
  launch_type                        = "FARGATE"
  deployment_minimum_healthy_percent = 100
  deployment_maximum_percent         = 200
  propagate_tags                     = "SERVICE"

  deployment_circuit_breaker {
    enable   = true
    rollback = true
  }

  network_configuration {
    subnets          = var.private_subnet_ids
    security_groups  = [aws_security_group.poller.id]
    assign_public_ip = false
  }

  tags = local.tags

  lifecycle {
    ignore_changes = [desired_count]
  }

  depends_on = [
    aws_iam_role_policy.service_execution_secrets,
    aws_iam_role_policy_attachment.service_execution_managed,
    aws_iam_role_policy.service_task_assume,
    aws_secretsmanager_secret_version.database_url,
    aws_secretsmanager_secret_version.session_key,
    aws_secretsmanager_secret_version.metrics_token,
    aws_secretsmanager_secret_version.kv_url,
  ]
}

# A fixed count: one task holds the coordinator lease, the others wait for it.
resource "aws_ecs_service" "coordinator" {
  count = local.split_count

  name                               = "${local.name}-coordinator"
  cluster                            = aws_ecs_cluster.this.id
  task_definition                    = aws_ecs_task_definition.service["coordinator"].arn
  desired_count                      = var.coordinator_desired_count
  launch_type                        = "FARGATE"
  deployment_minimum_healthy_percent = 100
  deployment_maximum_percent         = 200
  propagate_tags                     = "SERVICE"

  deployment_circuit_breaker {
    enable   = true
    rollback = true
  }

  network_configuration {
    subnets          = var.private_subnet_ids
    security_groups  = [aws_security_group.coordinator.id]
    assign_public_ip = false
  }

  tags = local.tags

  depends_on = [
    aws_iam_role_policy.service_execution_secrets,
    aws_iam_role_policy_attachment.service_execution_managed,
    aws_iam_role_policy.service_task_assume,
    aws_secretsmanager_secret_version.database_url,
    aws_secretsmanager_secret_version.session_key,
    aws_secretsmanager_secret_version.metrics_token,
    aws_secretsmanager_secret_version.kv_url,
  ]
}

# ---- Autoscaling -------------------------------------------------------------------------------------------------

resource "aws_appautoscaling_target" "web" {
  count = local.split_count

  service_namespace  = "ecs"
  scalable_dimension = "ecs:service:DesiredCount"
  resource_id        = "service/${aws_ecs_cluster.this.name}/${aws_ecs_service.web[0].name}"
  min_capacity       = var.web_min_count
  max_capacity       = var.web_max_count
}

# Requests per target per minute. The scale-in cooldown is long because a scale-in closes WebSockets (terminals and
# log tails); the target group is shared with the legacy service while both run, which only makes the signal calmer.
resource "aws_appautoscaling_policy" "web_requests" {
  count = local.split_count

  name               = "${local.name}-web-requests"
  policy_type        = "TargetTrackingScaling"
  service_namespace  = aws_appautoscaling_target.web[0].service_namespace
  scalable_dimension = aws_appautoscaling_target.web[0].scalable_dimension
  resource_id        = aws_appautoscaling_target.web[0].resource_id

  target_tracking_scaling_policy_configuration {
    target_value       = var.web_requests_per_target
    scale_in_cooldown  = 600
    scale_out_cooldown = 60

    predefined_metric_specification {
      predefined_metric_type = "ALBRequestCountPerTarget"
      resource_label         = "${aws_lb.this.arn_suffix}/${aws_lb_target_group.server.arn_suffix}"
    }
  }
}

resource "aws_appautoscaling_policy" "web_cpu" {
  count = local.split_count

  name               = "${local.name}-web-cpu"
  policy_type        = "TargetTrackingScaling"
  service_namespace  = aws_appautoscaling_target.web[0].service_namespace
  scalable_dimension = aws_appautoscaling_target.web[0].scalable_dimension
  resource_id        = aws_appautoscaling_target.web[0].resource_id

  target_tracking_scaling_policy_configuration {
    target_value       = 60
    scale_in_cooldown  = 600
    scale_out_cooldown = 60

    predefined_metric_specification {
      predefined_metric_type = "ECSServiceAverageCPUUtilization"
    }
  }
}

resource "aws_appautoscaling_target" "poller" {
  count = local.split_count

  service_namespace  = "ecs"
  scalable_dimension = "ecs:service:DesiredCount"
  resource_id        = "service/${aws_ecs_cluster.this.name}/${aws_ecs_service.poller[0].name}"
  min_capacity       = var.poller_min_count
  max_capacity       = var.poller_max_count
}

# A scale-in hands a task's slices to the others, and a crash is stale for up to 35 s, so poller scale-in is slower than
# scale-out. Lease load (slices and weight per task) is on /metrics, not in CloudWatch.
resource "aws_appautoscaling_policy" "poller_cpu" {
  count = local.split_count

  name               = "${local.name}-poller-cpu"
  policy_type        = "TargetTrackingScaling"
  service_namespace  = aws_appautoscaling_target.poller[0].service_namespace
  scalable_dimension = aws_appautoscaling_target.poller[0].scalable_dimension
  resource_id        = aws_appautoscaling_target.poller[0].resource_id

  target_tracking_scaling_policy_configuration {
    target_value       = 60
    scale_in_cooldown  = 300
    scale_out_cooldown = 60

    predefined_metric_specification {
      predefined_metric_type = "ECSServiceAverageCPUUtilization"
    }
  }
}
