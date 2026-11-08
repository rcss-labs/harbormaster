output "role_arn" {
  description = "ARN of the access role; paste it into Harbormaster's Connect account form."
  value       = aws_iam_role.this.arn
}

output "role_name" {
  description = "Name of the access role."
  value       = aws_iam_role.this.name
}

output "scan_role_arn" {
  description = "ARN of the image scan role; null when scanner_task_role_arn is not set."
  value       = one(aws_iam_role.scan[*].arn)
}
