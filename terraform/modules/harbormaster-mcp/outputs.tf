output "alb_dns_name" {
  description = "DNS name of the public load balancer (point the public hostname at it)."
  value       = aws_lb.this.dns_name
}

output "alb_zone_id" {
  description = "Route 53 hosted zone ID of the load balancer (for alias records)."
  value       = aws_lb.this.zone_id
}

output "alb_arn" {
  description = "ARN of the public load balancer."
  value       = aws_lb.this.arn
}

output "task_security_group_id" {
  description = "Security group of the gateway tasks. Pass it to the harbormaster module's alb_ingress_security_group_ids."
  value       = aws_security_group.task.id
}

output "alb_security_group_id" {
  description = "Security group of the public load balancer."
  value       = aws_security_group.alb.id
}

output "service_name" {
  description = "Name of the gateway ECS service."
  value       = aws_ecs_service.service.name
}

output "task_role_arn" {
  description = "ARN of the gateway task role (it has no policies)."
  value       = aws_iam_role.task.arn
}

output "log_group_name" {
  description = "CloudWatch log group of the gateway."
  value       = aws_cloudwatch_log_group.service.name
}
