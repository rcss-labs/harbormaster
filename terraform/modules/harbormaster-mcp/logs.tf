resource "aws_cloudwatch_log_group" "service" {
  name              = "/ecs/${var.name}"
  retention_in_days = var.log_retention_days

  tags = local.tags
}
