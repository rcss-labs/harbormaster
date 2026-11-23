resource "aws_ecs_task_definition" "service" {
  family                   = var.name
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = var.cpu
  memory                   = var.memory
  execution_role_arn       = aws_iam_role.execution.arn
  task_role_arn            = aws_iam_role.task.arn
  container_definitions    = jsonencode(local.container_definitions)

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = var.cpu_architecture
  }

  tags = local.tags

  # A variable validation cannot read another variable on Terraform 1.5, so the check lives here.
  lifecycle {
    precondition {
      condition     = !var.authorize_via_gateway || var.authorize_url != null
      error_message = "authorize_via_gateway requires authorize_url to be set."
    }
  }
}

resource "aws_ecs_service" "service" {
  name                               = var.name
  cluster                            = var.cluster_arn
  task_definition                    = aws_ecs_task_definition.service.arn
  desired_count                      = var.desired_count
  launch_type                        = "FARGATE"
  deployment_minimum_healthy_percent = 100
  deployment_maximum_percent         = 200
  health_check_grace_period_seconds  = 30
  propagate_tags                     = "SERVICE"

  deployment_circuit_breaker {
    enable   = true
    rollback = true
  }

  network_configuration {
    subnets          = var.private_subnet_ids
    security_groups  = [aws_security_group.task.id]
    assign_public_ip = false
  }

  load_balancer {
    target_group_arn = aws_lb_target_group.service.arn
    container_name   = var.name
    container_port   = local.container_port
  }

  tags = local.tags

  depends_on = [
    aws_lb_listener.https,
    aws_iam_role_policy_attachment.execution_managed,
  ]
}
