output "task_role_arn" {
  description = "ARN of the legacy single-service task role (input harbormaster_task_role_arn of the access-role module). Null once keep_legacy_service is false."
  value       = one(aws_iam_role.task[*].arn)
}

output "execution_role_arn" {
  description = "ARN of the legacy single-service ECS task execution role. Null once keep_legacy_service is false."
  value       = one(aws_iam_role.execution[*].arn)
}

output "web_task_role_arn" {
  description = "ARN of the web task role (input web_task_role_arn of the access-role module). Always created."
  value       = aws_iam_role.service_task["web"].arn
}

output "poller_task_role_arn" {
  description = "ARN of the poller task role (input poller_task_role_arn of the access-role module). Always created."
  value       = aws_iam_role.service_task["poller"].arn
}

output "coordinator_task_role_arn" {
  description = "ARN of the coordinator task role (input coordinator_task_role_arn of the access-role module). Always created."
  value       = aws_iam_role.service_task["coordinator"].arn
}

output "cluster_name" {
  description = "ECS cluster name."
  value       = aws_ecs_cluster.this.name
}

output "service_name" {
  description = "Name of the legacy single ECS service. Null once keep_legacy_service is false."
  value       = one(aws_ecs_service.server[*].name)
}

output "alb_dns_name" {
  description = "DNS name of the load balancer."
  value       = aws_lb.this.dns_name
}

output "alb_zone_id" {
  description = "Route 53 hosted zone ID of the load balancer (for alias records)."
  value       = aws_lb.this.zone_id
}

output "alb_arn" {
  description = "ARN of the load balancer."
  value       = aws_lb.this.arn
}

output "db_address" {
  description = "Hostname of the RDS instance."
  value       = aws_db_instance.this.address
}

output "log_group_name" {
  description = "CloudWatch log group of the server."
  value       = aws_cloudwatch_log_group.server.name
}

output "security_group_ids" {
  description = "Security group IDs keyed alb, server and db."
  value = {
    alb    = aws_security_group.alb.id
    server = aws_security_group.server.id
    db     = aws_security_group.db.id
  }
}

output "database_url_secret_arn" {
  description = "ARN of the Secrets Manager secret holding the database URL (the ARN only, not the value)."
  value       = aws_secretsmanager_secret.database_url.arn
}

output "scanner_task_role_arn" {
  description = "ARN of the scanner task role, the principal the access module's scan role trusts. Null when image scanning is off."
  value       = one(aws_iam_role.scanner_task[*].arn)
}

output "scan_bucket_name" {
  description = "Name of the scan bucket. Null when image scanning is off."
  value       = one(aws_s3_bucket.scan[*].bucket)
}
