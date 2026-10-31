resource "aws_cloudwatch_log_group" "server" {
  name              = "/ecs/${local.name}/${local.server_name}"
  retention_in_days = var.log_retention_days

  tags = local.tags
}
