output "repository_url" {
  description = "Full ECR repository URL for use in docker push and Kubernetes image specs"
  value       = aws_ecr_repository.app.repository_url
}

output "repository_arn" {
  description = "ARN of the ECR repository"
  value       = aws_ecr_repository.app.arn
}
