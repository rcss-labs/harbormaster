output "agent_url" {
  description = "Set this as HM_AGENT_URL on the harbormaster module (extra_env). Set hostname to a private DNS name that matches certificate_arn and points at alb_dns_name, so the URL carries a name the certificate covers."
  value       = "https://${coalesce(var.hostname, aws_lb.this.dns_name)}"
}

output "alb_dns_name" {
  description = "DNS name of the internal load balancer (point the private hostname at it)."
  value       = aws_lb.this.dns_name
}

output "task_role_arn" {
  description = "ARN of the agent task role (its only policy is Bedrock invocation, when the catalogue lists bedrock)."
  value       = aws_iam_role.task.arn
}

output "task_security_group_id" {
  description = "Security group of the agent tasks. Pass it to the harbormaster module's alb_ingress_security_group_ids so tool calls reach Harbormaster."
  value       = aws_security_group.task.id
}

output "alb_security_group_id" {
  description = "Security group of the agent's internal load balancer."
  value       = aws_security_group.alb.id
}

output "log_group_name" {
  description = "CloudWatch log group of the agent."
  value       = aws_cloudwatch_log_group.service.name
}
