output "role_arn" {
  description = "ARN of the access role; paste it into Harbormaster's Connect account form."
  value       = aws_iam_role.this.arn
}

output "role_name" {
  description = "Name of the access role."
  value       = aws_iam_role.this.name
}
